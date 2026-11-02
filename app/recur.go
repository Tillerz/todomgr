package main

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Repeat rules (the "recur" field):
//
//	d w m y          every day / week / month / year
//	2w 4w 3d 6m 2y   every N days / weeks / months / years (N = 1–999)
//	m:1fr,3fr,-1fr   monthly on the 1st and 3rd Friday and the last Friday
//	                 (1–5 or -1 for the last; mo tu we th fr sa su); a month
//	                 without a 5th Friday has no date for "5fr"
//	3d+ 2w+ m:1fr+   "+" at the end: count from the day the entry is done,
//	                 not from its old date (see afterDone)
//
// web/index.html has the same rules in JavaScript (nextOcc, recurText).

var (
	reRecur   = regexp.MustCompile(`^(?:([1-9]\d{0,2})?([dwmy])|m:((?:-1|[1-5])(?:mo|tu|we|th|fr|sa|su)(?:,(?:-1|[1-5])(?:mo|tu|we|th|fr|sa|su))*))(\+)?$`)
	recurDays = map[string]time.Weekday{"su": time.Sunday, "mo": time.Monday, "tu": time.Tuesday, "we": time.Wednesday, "th": time.Thursday, "fr": time.Friday, "sa": time.Saturday}
)

// normRecur turns user input (a rule, or daily / weekly / monthly / yearly) into a rule;
// ok is false for anything else.
func normRecur(v string) (string, bool) {
	v = strings.ToLower(strings.TrimSpace(v))
	base, plus := strings.CutSuffix(v, "+")
	if w, ok := map[string]string{"daily": "d", "weekly": "w", "monthly": "m", "yearly": "y", "1d": "d", "1w": "w", "1m": "m", "1y": "y"}[base]; ok {
		v = w
		if plus {
			v += "+"
		}
	}
	return v, v == "" || reRecur.MatchString(v)
}

// nthDay is the day of the month of the n-th weekday (n = -1: the last one), 0 if the month has none.
func nthDay(y int, mo time.Month, n int, wd time.Weekday) int {
	days := time.Date(y, mo+1, 0, 0, 0, 0, 0, time.UTC).Day()
	if n < 0 {
		lw := time.Date(y, mo, days, 0, 0, 0, 0, time.UTC).Weekday()
		return days - (int(lw)-int(wd)+7)%7
	}
	first := time.Date(y, mo, 1, 0, 0, 0, 0, time.UTC).Weekday()
	if day := 1 + (int(wd)-int(first)+7)%7 + (n-1)*7; day <= days {
		return day
	}
	return 0
}

// nextOcc returns the next date of a repeating entry after s (same layout as s:
// YYYY-MM-DD or YYYY-MM-DDTHH:MM). An unknown rule returns s unchanged.
func nextOcc(s, r string, loc *time.Location) string {
	layout := "2006-01-02"
	if strings.Contains(s, "T") {
		layout = "2006-01-02T15:04"
	}
	d, err := time.ParseInLocation(layout, s, loc)
	m := reRecur.FindStringSubmatch(r)
	if err != nil || m == nil {
		return s
	}
	if m[3] != "" {
		// a match comes within 14 months: every month has a 1st–4th and a last weekday,
		// and a 5th weekday comes several times a year
		for i := 0; i < 14; i++ {
			first := time.Date(d.Year(), d.Month()+time.Month(i), 1, 0, 0, 0, 0, loc)
			best := 0
			for _, rule := range strings.Split(m[3], ",") {
				n, _ := strconv.Atoi(rule[:len(rule)-2])
				day := nthDay(first.Year(), first.Month(), n, recurDays[rule[len(rule)-2:]])
				c := time.Date(first.Year(), first.Month(), day, d.Hour(), d.Minute(), 0, 0, loc)
				if day > 0 && c.After(d) && (best == 0 || day < best) {
					best = day
				}
			}
			if best > 0 {
				return time.Date(first.Year(), first.Month(), best, d.Hour(), d.Minute(), 0, 0, loc).Format(layout)
			}
		}
		return s
	}
	n := 1
	if m[1] != "" {
		n, _ = strconv.Atoi(m[1])
	}
	switch m[2] {
	case "d":
		d = d.AddDate(0, 0, n)
	case "w":
		d = d.AddDate(0, 0, 7*n)
	case "m":
		d = d.AddDate(0, n, 0)
	default:
		d = d.AddDate(n, 0, 0)
	}
	return d.Format(layout)
}

// afterDone gives the new deadline and reminder of an entry with a "+" rule that is done today
// (YYYY-MM-DD): the deadline, else the reminder, goes to the next date after today, at the same
// time of day, and the other date moves by as many days. Without dates, the deadline becomes the
// next date after today.
func afterDone(deadline, reminder, r, today string, loc *time.Location) (string, string) {
	a := deadline
	if a == "" {
		a = reminder
	}
	if a == "" {
		return nextOcc(today, r, loc), ""
	}
	day := func(s string) time.Time { d, _ := time.Parse("2006-01-02", s[:10]); return d }
	days := int(day(nextOcc(today+a[10:], r, loc)).Sub(day(a)).Hours() / 24)
	shift := func(s string) string {
		if len(s) < 10 {
			return s
		}
		return day(s).AddDate(0, 0, days).Format("2006-01-02") + s[10:]
	}
	return shift(deadline), shift(reminder)
}

var recurFreq = map[string]string{"d": "DAILY", "w": "WEEKLY", "m": "MONTHLY", "y": "YEARLY"}

// recurRRULE is the calendar (RFC 5545) form of a rule, "" if there is none.
func recurRRULE(r string) string {
	m := reRecur.FindStringSubmatch(r)
	switch {
	case m == nil:
		return ""
	case m[3] != "":
		return "FREQ=MONTHLY;BYDAY=" + strings.ToUpper(m[3])
	case m[1] != "" && m[1] != "1":
		return "FREQ=" + recurFreq[m[2]] + ";INTERVAL=" + m[1]
	}
	return "FREQ=" + recurFreq[m[2]]
}

// recurCSV is the rule as written into the CSV export: daily … yearly, or the rule itself.
func recurCSV(r string) string {
	if w, ok := map[string]string{"d": "daily", "w": "weekly", "m": "monthly", "y": "yearly"}[r]; ok {
		return w
	}
	return r
}
