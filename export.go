package main

import (
	"bytes"
	csvfmt "encoding/csv"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// CSV (for spreadsheets) and ICS (for calendars and task apps) exports. Both are
// read-only views of the data; the JSON export stays the complete backup format.

var prioNames = []string{"", "low", "medium", "high", "urgent"}

func (db *DB) exportOrder(includeClosed bool) []Node {
	var out []Node
	for _, id := range db.walkOrder() {
		n := db.Nodes[id]
		if !includeClosed && db.isClosed(n) {
			continue
		}
		out = append(out, n)
	}
	return out
}

func fmtMs(ms float64, loc *time.Location) string {
	if ms <= 0 {
		return ""
	}
	return time.UnixMilli(int64(ms)).In(loc).Format("2006-01-02 15:04")
}

// closedAt is the time the entry last entered a closed status ("" if it is open).
func closedAt(n Node, loc *time.Location) string {
	if !closedStatus[n.S("status")] {
		return ""
	}
	h, _ := n["hist"].([]any)
	for i := len(h) - 1; i >= 0; i-- {
		if e, ok := h[i].(map[string]any); ok && closedStatus[str(e["s"])] {
			return fmtMs(num(e["at"]), loc)
		}
	}
	return ""
}

// csvText stops spreadsheets from running text as a formula (=, +, -, @).
func csvText(s string) string {
	if s != "" && strings.ContainsRune("=+-@\t\r", rune(s[0])) {
		return "'" + s
	}
	return s
}

var csvHeader = []string{"id", "parent_id", "path", "name", "type", "status", "priority", "progress_percent", "progress_done", "progress_total", "progress_unit",
	"deadline", "reminder", "repeat", "tags", "people", "location", "url", "estimate_min", "tracked_min", "created", "updated", "closed", "notes"}

func (s *Store) ExportCSV(includeClosed bool) []byte {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var buf bytes.Buffer
	buf.WriteString("\xEF\xBB\xBF") // UTF-8 byte order mark, so Excel reads umlauts correctly
	w := csvfmt.NewWriter(&buf)
	w.UseCRLF = true
	_ = w.Write(csvHeader)
	num0 := func(f float64) string { return strconv.FormatFloat(f, 'f', -1, 64) }
	for _, n := range s.db.exportOrder(includeClosed) {
		p := n.Obj("prog")
		var secs float64
		if t, ok := n["time"].([]any); ok {
			for _, e := range t {
				if m, ok := e.(map[string]any); ok {
					secs += num(m["secs"])
				}
			}
		}
		prio := int(n.N("prio"))
		if prio < 0 || prio >= len(prioNames) {
			prio = 0
		}
		parent := n.S("parent")
		if parent == "root" {
			parent = ""
		}
		recur := map[string]string{"d": "daily", "w": "weekly", "m": "monthly", "y": "yearly"}[n.S("recur")]
		prog := []string{"", "", ""}
		if len(n.Kids()) == 0 || len(s.db.progressKids(n)) == 0 {
			prog = []string{num0(num(p["done"])), num0(num(p["total"])), str(p["unit"])}
		}
		_ = w.Write([]string{
			n.S("id"), parent, csvText(s.db.path(n)), csvText(n.S("name")), n.S("kind"), n.S("status"), prioNames[prio],
			strconv.Itoa(int(s.db.frac(n)*100 + 0.5)), prog[0], prog[1], prog[2],
			n.S("deadline"), n.S("reminder"), recur,
			csvText(strings.Join(strs(n["tags"]), ", ")), csvText(strings.Join(strs(n["people"]), ", ")),
			csvText(n.S("location")), csvText(n.S("url")),
			num0(n.N("est")), num0(round2(secs / 60)),
			fmtMs(n.N("created"), s.loc), fmtMs(n.N("updated"), s.loc), closedAt(n, s.loc), csvText(n.S("notes")),
		})
	}
	w.Flush()
	return buf.Bytes()
}

// ---------------------------------------------------------------- ICS

func icsEscape(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, ";", `\;`)
	s = strings.ReplaceAll(s, ",", `\,`)
	s = strings.ReplaceAll(s, "\r\n", `\n`)
	s = strings.ReplaceAll(s, "\n", `\n`)
	return strings.ReplaceAll(s, "\r", "")
}

// icsLine writes one content line, folded at 75 octets as RFC 5545 requires.
func icsLine(b *bytes.Buffer, line string) {
	for first := true; ; first = false {
		limit := 75
		if !first {
			b.WriteString(" ")
			limit = 74
		}
		if len(line) <= limit {
			b.WriteString(line + "\r\n")
			return
		}
		cut := limit
		for cut > 0 && !utf8.RuneStart(line[cut]) {
			cut--
		}
		b.WriteString(line[:cut] + "\r\n")
		line = line[cut:]
	}
}

// icsWhen formats a date or date-time as a "floating" local value (no time zone,
// so the calendar shows the same wall-clock time as the app).
func icsWhen(v string, loc *time.Location) (prop string, ok bool) {
	t, ok := parseWhen(v, loc)
	if !ok {
		return "", false
	}
	if strings.Contains(v, "T") {
		return ":" + t.Format("20060102T150405"), true
	}
	return ";VALUE=DATE:" + t.Format("20060102"), true
}

func icsUTC(t time.Time) string { return t.UTC().Format("20060102T150405Z") }

var icsRecur = map[string]string{"d": "DAILY", "w": "WEEKLY", "m": "MONTHLY", "y": "YEARLY"}

func icsStatus(n Node) string {
	switch n.S("status") {
	case "progress", "exploring", "active":
		return "IN-PROCESS"
	case "done":
		return "COMPLETED"
	case "cancelled", "rejected", "archived":
		return "CANCELLED"
	}
	return "NEEDS-ACTION"
}

func (s *Store) ExportICS(includeClosed bool) []byte {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var b bytes.Buffer
	stamp := icsUTC(time.Now())
	for _, l := range []string{"BEGIN:VCALENDAR", "VERSION:2.0", "PRODID:-//Tillerz//Todo Mgr " + Version + "//EN", "CALSCALE:GREGORIAN", "X-WR-CALNAME:" + AppName} {
		icsLine(&b, l)
	}
	for _, n := range s.db.exportOrder(includeClosed) {
		id, name, rem := n.S("id"), n.S("name"), n.S("reminder")
		desc := []string{}
		if p := s.db.path(n); p != "" {
			desc = append(desc, p)
		}
		if u := n.S("url"); u != "" {
			desc = append(desc, u)
		}
		if t := strings.TrimSpace(n.S("notes")); t != "" {
			desc = append(desc, "", t)
		}
		common := func(kind string) {
			icsLine(&b, "BEGIN:"+kind)
			icsLine(&b, "UID:"+id+"@todomgr")
			icsLine(&b, "DTSTAMP:"+stamp)
			if u := n.N("updated"); u > 0 {
				icsLine(&b, "LAST-MODIFIED:"+icsUTC(time.UnixMilli(int64(u))))
			}
			icsLine(&b, "SUMMARY:"+icsEscape(name))
			if len(desc) > 0 {
				icsLine(&b, "DESCRIPTION:"+icsEscape(strings.Join(desc, "\n")))
			}
			if u := n.S("url"); u != "" {
				icsLine(&b, "URL:"+u)
			}
			if l := n.S("location"); l != "" {
				icsLine(&b, "LOCATION:"+icsEscape(l))
			}
			if tags := strs(n["tags"]); len(tags) > 0 {
				esc := make([]string, len(tags))
				for i, t := range tags {
					esc[i] = icsEscape(t)
				}
				icsLine(&b, "CATEGORIES:"+strings.Join(esc, ","))
			}
			if r := icsRecur[n.S("recur")]; r != "" {
				icsLine(&b, "RRULE:FREQ="+r)
			}
		}
		alarm := func(trigger, text string) {
			icsLine(&b, "BEGIN:VALARM")
			icsLine(&b, "ACTION:DISPLAY")
			icsLine(&b, "DESCRIPTION:"+icsEscape(text))
			icsLine(&b, "TRIGGER"+trigger)
			icsLine(&b, "END:VALARM")
		}

		if n.S("kind") == "reminder" {
			at, ok := remindAt(rem, s.loc)
			if !ok {
				continue
			}
			common("VEVENT")
			icsLine(&b, "DTSTART:"+at.Format("20060102T150405"))
			if lead := int(n.N("lead")); lead > 0 {
				alarm(fmt.Sprintf(":-PT%dM", lead), name)
			}
			alarm(":PT0S", name)
			icsLine(&b, "END:VEVENT")
			continue
		}

		due, hasDue := icsWhen(n.S("deadline"), s.loc)
		at, hasRem := remindAt(rem, s.loc)
		if !hasDue && !hasRem {
			continue
		}
		common("VTODO")
		if hasDue {
			icsLine(&b, "DUE"+due)
		}
		icsLine(&b, "STATUS:"+icsStatus(n))
		icsLine(&b, "PERCENT-COMPLETE:"+strconv.Itoa(int(s.db.frac(n)*100+0.5)))
		if n.S("status") == "done" {
			for _, e := range reverse(n["hist"]) {
				if str(e["s"]) == "done" {
					icsLine(&b, "COMPLETED:"+icsUTC(time.UnixMilli(int64(num(e["at"])))))
					break
				}
			}
		}
		if p := map[int]int{4: 1, 3: 3, 2: 5, 1: 7}[int(n.N("prio"))]; p > 0 {
			icsLine(&b, "PRIORITY:"+strconv.Itoa(p))
		}
		if hasRem {
			alarm(";VALUE=DATE-TIME:"+icsUTC(at), name)
		}
		icsLine(&b, "END:VTODO")
	}
	icsLine(&b, "END:VCALENDAR")
	return b.Bytes()
}

// reverse returns the entries of a history list, newest first.
func reverse(v any) []map[string]any {
	h, _ := v.([]any)
	out := make([]map[string]any, 0, len(h))
	for i := len(h) - 1; i >= 0; i-- {
		if m, ok := h[i].(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}
