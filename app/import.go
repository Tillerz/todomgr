package main

import (
	csvfmt "encoding/csv"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// CSV and calendar (ICS) import. Both only add entries: everything goes below a new
// top-level project "Import: <file>, <date>", so nothing that exists is changed.
// An entry that is already there (see knownEntry) is skipped, so a file can be imported again.
// Past one-time events are skipped, and values that have no field here go into the
// notes, so nothing is dropped silently. The JSON import (Store.Import) replaces all.

// impItem is one entry to create. key and parent are ids from the file (CSV id /
// parent_id, ICS UID / RELATED-TO) and rebuild the tree.
type impItem struct {
	key, parent, name string
	f                 map[string]any
	extra             []string // lines added to the notes
}

func (it *impItem) note(format string, a ...any) {
	it.extra = append(it.extra, fmt.Sprintf(format, a...))
}

type impResult struct {
	items   []*impItem
	skipped int
}

func withExtra(notes string, extra []string) string {
	notes = strings.TrimSpace(notes)
	if len(extra) == 0 {
		return notes
	}
	if notes != "" {
		notes += "\n\n"
	}
	return notes + strings.Join(extra, "\n")
}

// ImportEntries adds the entries of a CSV or ICS file below a new project and
// returns its id and name plus the number of entries added and skipped.
func (s *Store) ImportEntries(actor, format, file string, data []byte) (map[string]any, error) {
	format = strings.ToLower(strings.TrimPrefix(format, "."))
	if format == "" {
		format = strings.ToLower(strings.TrimPrefix(filepath.Ext(file), "."))
	}
	if !utf8.Valid(data) {
		data = []byte(fromWindows1252(data))
	}
	now := s.Now()
	var r impResult
	var err error
	switch format {
	case "csv":
		r, err = parseCSVImport(data, s.loc, now)
	case "ics", "ical", "icalendar":
		r, err = parseICSImport(data, s.loc, now)
	default:
		return nil, fmt.Errorf("unknown import format %q (use csv or ics)", format)
	}
	if err != nil {
		return nil, err
	}
	if len(r.items) == 0 {
		return nil, fmt.Errorf("nothing to import (%d skipped)", r.skipped)
	}
	name := filepath.Base(strings.TrimSpace(file))
	if name == "" || name == "." || name == "/" {
		name = strings.ToUpper(format) + " file"
	}
	title := "Import: " + name + ", " + now.Format("2006-01-02 15:04")
	res := map[string]any{"name": title, "skipped": r.skipped}
	_, err = s.Mutate(actor, "", func(tx *Tx) error {
		known := 0
		for _, it := range r.items {
			if tx.db.knownEntry(it) != "" {
				known++
			}
		}
		if known == len(r.items) {
			return fmt.Errorf("nothing new to import: all %d entries are already there", known)
		}
		p, err := tx.create(title, "", 0, map[string]any{"kind": "project", "status": "active"})
		if err != nil {
			return err
		}
		pid := p.S("id")
		byKey := map[string]*impItem{}
		for _, it := range r.items {
			if _, dup := byKey[it.key]; it.key != "" && !dup {
				byKey[it.key] = it
			}
		}
		made, busy := map[*impItem]string{}, map[*impItem]bool{}
		added, partly, dups := 0, 0, 0
		var add func(it *impItem) error
		add = func(it *impItem) error {
			if _, ok := made[it]; ok {
				return nil
			}
			if id := tx.db.knownEntry(it); id != "" { // its sub-entries from the file go below the one that is there
				made[it] = id
				dups++
				return nil
			}
			busy[it] = true
			defer delete(busy, it)
			parent := pid
			if pi := byKey[it.parent]; pi != nil && !busy[pi] { // a busy parent means a cycle: keep it at the top
				if err := add(pi); err != nil {
					return err
				}
				parent = made[pi]
			}
			f := copyMap(it.f)
			notes := withExtra(str(f["notes"]), it.extra)
			f["notes"] = notes
			n, err := tx.create(it.name, parent, -1, f)
			if err != nil { // a value the app does not take: keep the entry, with the reason in its notes
				n, err = tx.create(it.name, parent, -1, map[string]any{"notes": withExtra(notes, []string{"Not all fields could be imported: " + err.Error()})})
				if err != nil {
					return err
				}
				partly++
			}
			made[it] = n.S("id")
			added++
			return nil
		}
		for _, it := range r.items {
			if err := add(it); err != nil {
				return err
			}
		}
		res["id"], res["added"], res["partly"], res["duplicates"] = pid, added, partly, dups
		return nil
	})
	if err != nil {
		return nil, err
	}
	return res, nil
}

// knownEntry returns the id of the entry an item to import is already there as, or "". An event
// exported by this app carries the entry id in its UID (<id>@todomgr), which is matched first,
// so a renamed or moved entry is still recognised; everything else is matched by sameEntry.
func (db *DB) knownEntry(it *impItem) string {
	if id, ok := strings.CutSuffix(it.key, "@todomgr"); ok {
		if n, ok := db.Nodes[id]; ok && id != "root" && !db.inTrash(n) {
			return id
		}
	}
	return db.sameEntry(it.name, it.f)
}

// sameEntry returns the id of a live entry with the same kind, name and dates as an entry
// to import, or "". Entries without any date never match: two todos "Buy milk" can both be meant.
func (db *DB) sameEntry(name string, f map[string]any) string {
	kind, rem, end, dl := str(f["kind"]), str(f["reminder"]), str(f["end"]), str(f["deadline"])
	if kind == "" {
		kind = "todo"
	}
	if rem == "" && dl == "" {
		return ""
	}
	name = strings.TrimSpace(name)
	for id, n := range db.Nodes {
		if n.S("kind") == kind && n.S("reminder") == rem && n.S("end") == end && n.S("deadline") == dl &&
			strings.TrimSpace(n.S("name")) == name && !db.inTrash(n) {
			return id
		}
	}
	return ""
}

// cp1252 holds the characters of Windows-1252 bytes 0x80–0x9F; the other bytes are Latin-1.
var cp1252 = []rune("€\u0081‚ƒ„…†‡ˆ‰Š‹Œ\u008dŽ\u008f\u0090‘’“”•–—˜™š›œ\u009džŸ")

// fromWindows1252 decodes text that is not UTF-8, such as a CSV file saved by German Excel.
func fromWindows1252(b []byte) string {
	r := make([]rune, len(b))
	for i, c := range b {
		if c >= 0x80 && c < 0xA0 {
			r[i] = cp1252[c-0x80]
		} else {
			r[i] = rune(c)
		}
	}
	return string(r)
}

// ---------------------------------------------------------------- dates

func whenStr(t time.Time, dateOnly bool) string {
	if dateOnly {
		return t.Format("2006-01-02")
	}
	return t.Format("2006-01-02T15:04")
}

// isPast: a date is past from the next day on, a date-time from that minute on.
func isPast(when string, loc *time.Location, now time.Time) bool {
	t, ok := parseWhen(when, loc)
	if !ok {
		return false
	}
	if !strings.Contains(when, "T") {
		return t.Before(startOfDay(now))
	}
	return t.Before(now)
}

// ---------------------------------------------------------------- ICS

type icsProp struct {
	params map[string]string
	value  string
}

type icsComp struct {
	name  string
	props map[string][]icsProp
	subs  []*icsComp
}

func (c *icsComp) get(k string) *icsProp {
	if p := c.props[k]; len(p) > 0 {
		return &p[0]
	}
	return nil
}

func (c *icsComp) text(k string) string {
	if p := c.get(k); p != nil {
		return strings.TrimSpace(icsUnescape(p.value))
	}
	return ""
}

// parseICS reads the component tree: lines are unfolded, BEGIN/END nest.
func parseICS(data []byte) (*icsComp, error) {
	s := strings.TrimPrefix(string(data), "\xEF\xBB\xBF")
	s = strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\r", "\n")
	var lines []string
	for _, l := range strings.Split(s, "\n") {
		if (strings.HasPrefix(l, " ") || strings.HasPrefix(l, "\t")) && len(lines) > 0 {
			lines[len(lines)-1] += l[1:]
		} else if strings.TrimSpace(l) != "" {
			lines = append(lines, l)
		}
	}
	root := &icsComp{name: "ROOT", props: map[string][]icsProp{}}
	stack := []*icsComp{root}
	for _, l := range lines {
		name, params, value := icsSplit(l)
		cur := stack[len(stack)-1]
		switch name {
		case "BEGIN":
			c := &icsComp{name: strings.ToUpper(strings.TrimSpace(value)), props: map[string][]icsProp{}}
			cur.subs = append(cur.subs, c)
			stack = append(stack, c)
		case "END":
			if len(stack) > 1 {
				stack = stack[:len(stack)-1]
			}
		default:
			cur.props[name] = append(cur.props[name], icsProp{params, value})
		}
	}
	if len(root.subs) == 0 {
		return nil, errors.New("not a calendar file (no BEGIN:VCALENDAR)")
	}
	return root, nil
}

// icsSplit splits "NAME;KEY=val;KEY=\"quoted\":value".
func icsSplit(l string) (name string, params map[string]string, value string) {
	params = map[string]string{}
	i := strings.IndexAny(l, ";:")
	if i < 0 {
		return strings.ToUpper(strings.TrimSpace(l)), params, ""
	}
	name, rest := strings.ToUpper(strings.TrimSpace(l[:i])), l[i:]
	for strings.HasPrefix(rest, ";") {
		rest = rest[1:]
		eq := strings.IndexByte(rest, '=')
		if eq < 0 {
			break
		}
		k, v := strings.ToUpper(rest[:eq]), ""
		rest = rest[eq+1:]
		if strings.HasPrefix(rest, `"`) {
			if end := strings.IndexByte(rest[1:], '"'); end >= 0 {
				v, rest = rest[1:end+1], rest[end+2:]
			} else {
				v, rest = rest[1:], ""
			}
			if j := strings.IndexAny(rest, ";:"); j > 0 {
				rest = rest[j:]
			}
		} else if end := strings.IndexAny(rest, ";:"); end >= 0 {
			v, rest = rest[:end], rest[end:]
		} else {
			v, rest = rest, ""
		}
		params[k] = v
	}
	return name, params, strings.TrimPrefix(rest, ":")
}

func icsUnescape(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			i++
			if s[i] == 'n' || s[i] == 'N' {
				b.WriteByte('\n')
			} else {
				b.WriteByte(s[i])
			}
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// icsList splits a list value (CATEGORIES) at commas that are not escaped.
func icsList(v string) []string {
	var out []string
	start := 0
	for i := 0; i <= len(v); i++ {
		if i == len(v) || (v[i] == ',' && (i == 0 || v[i-1] != '\\')) {
			if s := strings.TrimSpace(icsUnescape(v[start:i])); s != "" {
				out = append(out, s)
			}
			start = i + 1
		}
	}
	return out
}

// icsTime reads a DATE or DATE-TIME value into the app's time zone. UTC ("Z") and
// TZID times are converted; an unknown TZID or a floating time counts as local.
func icsTime(p *icsProp, loc *time.Location) (t time.Time, dateOnly, ok bool) {
	v := strings.TrimSpace(p.value)
	if len(v) == 8 || strings.EqualFold(p.params["VALUE"], "DATE") {
		t, err := time.ParseInLocation("20060102", v[:min(8, len(v))], loc)
		return t, true, err == nil
	}
	zl := loc
	if strings.HasSuffix(v, "Z") {
		zl, v = time.UTC, strings.TrimSuffix(v, "Z")
	} else if tz := p.params["TZID"]; tz != "" {
		if l, err := time.LoadLocation(strings.TrimPrefix(tz, "/")); err == nil {
			zl = l
		}
	}
	t, err := time.ParseInLocation("20060102T150405", v, zl)
	if err != nil {
		t, err = time.ParseInLocation("20060102T1504", v, zl)
	}
	return t.In(loc), false, err == nil
}

var reICSDur = regexp.MustCompile(`^([+-])?P(?:(\d+)W)?(?:(\d+)D)?(?:T(?:(\d+)H)?(?:(\d+)M)?(?:(\d+)S)?)?$`)

func icsDur(v string) (time.Duration, bool) {
	m := reICSDur.FindStringSubmatch(strings.ToUpper(strings.TrimSpace(v)))
	if m == nil {
		return 0, false
	}
	n := func(s string) time.Duration { x, _ := strconv.Atoi(s); return time.Duration(x) }
	d := n(m[2])*7*24*time.Hour + n(m[3])*24*time.Hour + n(m[4])*time.Hour + n(m[5])*time.Minute + n(m[6])*time.Second
	if m[1] == "-" {
		d = -d
	}
	return d, true
}

// icsRule maps a calendar repeat rule to a rule of the app (see recur.go). exact is false
// when it has parts the app cannot repeat (a count, several weekdays a week…); until is
// zero if the rule is open-ended.
func icsRule(v string, loc *time.Location) (recur string, exact bool, until time.Time) {
	parts := map[string]string{}
	for _, kv := range strings.Split(v, ";") {
		if k, x, ok := strings.Cut(kv, "="); ok {
			parts[strings.ToUpper(strings.TrimSpace(k))] = strings.TrimSpace(x)
		}
	}
	freq := map[string]string{"DAILY": "d", "WEEKLY": "w", "MONTHLY": "m", "YEARLY": "y"}[strings.ToUpper(parts["FREQ"])]
	if freq == "" {
		return "", false, until
	}
	exact = true
	for k, x := range parts {
		switch k {
		case "FREQ", "WKST", "INTERVAL", "BYDAY":
		case "UNTIL":
			if t, dateOnly, ok := icsTime(&icsProp{params: map[string]string{}, value: x}, loc); ok {
				if dateOnly {
					t = t.AddDate(0, 0, 1)
				}
				until = t
			}
		default:
			exact = false
		}
	}
	n := 1
	if x := parts["INTERVAL"]; x != "" {
		if i, err := strconv.Atoi(x); err == nil && i >= 1 && i <= 999 {
			n = i
		} else {
			exact = false
		}
	}
	recur = freq
	if n > 1 {
		recur = strconv.Itoa(n) + freq
	}
	if by := strings.ReplaceAll(strings.ToLower(parts["BYDAY"]), "+", ""); by != "" {
		switch {
		case freq == "m" && n == 1 && reRecur.MatchString("m:"+by): // 1FR,3FR,-1FR
			recur = "m:" + by
		case freq == "w" && !strings.Contains(by, ","): // weekly on one day is the day of the start date
		default:
			exact = false
		}
	}
	return
}

// icsPrio maps PRIORITY 1 (highest) … 9 (lowest) to urgent … low; 0 is none.
func icsPrio(v string) float64 {
	p, _ := strconv.Atoi(strings.TrimSpace(v))
	switch {
	case p <= 0:
		return 0
	case p <= 2:
		return 4
	case p <= 4:
		return 3
	case p == 5:
		return 2
	}
	return 1
}

// icsCommon reads the fields events and tasks share.
func icsCommon(c *icsComp) *impItem {
	it := &impItem{name: c.text("SUMMARY"), f: map[string]any{}, key: c.text("UID")}
	if it.name == "" {
		it.name = "(no title)"
	}
	if r := c.get("RELATED-TO"); r != nil {
		if rt := strings.ToUpper(r.params["RELTYPE"]); rt == "" || rt == "PARENT" {
			it.parent = strings.TrimSpace(icsUnescape(r.value))
		}
	}
	if v := c.text("DESCRIPTION"); v != "" {
		it.f["notes"] = v
	}
	if v := c.text("LOCATION"); v != "" {
		it.f["location"] = v
	}
	if v := c.text("URL"); v != "" {
		if u := normURL(v); u != "" {
			it.f["url"] = u
		} else {
			it.note("Link: %s", v)
		}
	}
	var tags []any
	for _, p := range c.props["CATEGORIES"] {
		for _, t := range icsList(p.value) {
			tags = append(tags, t)
		}
	}
	if len(tags) > 0 {
		it.f["tags"] = tags
	}
	if p := icsPrio(c.text("PRIORITY")); p > 0 {
		it.f["prio"] = p
	}
	return it
}

// icsEvent turns an event into a reminder. ok is false for events that are skipped:
// cancelled, without a start, or over (past and not repeating, or the repeat ended).
func icsEvent(c *icsComp, loc *time.Location, now time.Time) (*impItem, bool) {
	sp := c.get("DTSTART")
	if strings.EqualFold(c.text("STATUS"), "CANCELLED") || sp == nil {
		return nil, false
	}
	start, dateOnly, ok := icsTime(sp, loc)
	if !ok {
		return nil, false
	}
	it := icsCommon(c)
	it.f["kind"] = "reminder"
	when, end := whenStr(start, dateOnly), ""
	// the end: DTEND (for all-day events the day after the last day) or DURATION
	if p := c.get("DTEND"); p != nil {
		if t, d, ok := icsTime(p, loc); ok {
			if d {
				t = t.AddDate(0, 0, -1)
			}
			end = whenStr(t, d)
		}
	} else if d, ok := icsDur(c.text("DURATION")); ok && d > 0 {
		if dateOnly {
			end = whenStr(start.Add(d).AddDate(0, 0, -1), true)
		} else {
			end = whenStr(start.Add(d), false)
		}
	}
	if end == when { // a one-day all-day event
		end = ""
	}
	if strings.EqualFold(c.text("TRANSP"), "TRANSPARENT") { // "free" in the calendar: does not block time
		it.f["free"] = true
	}
	over := func() bool { // the event (or this occurrence) has ended
		if end != "" {
			return isPast(end, loc, now)
		}
		return isPast(when, loc, now)
	}
	if r := c.get("RRULE"); r != nil {
		recur, exact, until := icsRule(r.value, loc)
		if !until.IsZero() && until.Before(now) || recur == "" && over() {
			return nil, false
		}
		if recur != "" {
			it.f["recur"] = recur
			for i := 0; i < 100000 && over(); i++ { // move on to the first occurrence that has not ended
				next, ev := nextOcc(when, recur, loc), Node{"end": end}
				shiftEnd(ev, when, next)
				when, end = next, ev.S("end")
			}
		}
		if !exact {
			it.note("Repeat rule from the calendar: %s", r.value)
		}
	} else if over() {
		return nil, false
	}
	it.f["reminder"] = when
	if end != "" {
		it.f["end"] = end
	}
	lead := 0.0 // no alarm in the calendar: no early warning either
	for _, a := range c.subs {
		if p := a.get("TRIGGER"); a.name == "VALARM" && p != nil && !strings.EqualFold(p.params["VALUE"], "DATE-TIME") && !strings.EqualFold(p.params["RELATED"], "END") {
			if d, ok := icsDur(p.value); ok && d <= 0 {
				lead = float64(-d / time.Minute)
				break
			}
		}
	}
	it.f["lead"] = lead
	return it, true
}

var icsStatusMap = map[string]string{"NEEDS-ACTION": "open", "IN-PROCESS": "progress", "COMPLETED": "done", "CANCELLED": "cancelled"}

// icsTodo turns a task into a todo. A past alarm goes into the notes, so it does not
// pop up as missed right after the import.
func icsTodo(c *icsComp, loc *time.Location, now time.Time) *impItem {
	it := icsCommon(c)
	it.f["kind"] = "todo"
	st := icsStatusMap[strings.ToUpper(c.text("STATUS"))]
	if st == "" && c.get("COMPLETED") != nil {
		st = "done"
	}
	if st != "" {
		it.f["status"] = st
	}
	var due, start time.Time
	if p := c.get("DUE"); p != nil {
		if t, d, ok := icsTime(p, loc); ok {
			it.f["deadline"], due = whenStr(t, d), t
		}
	}
	if p := c.get("DTSTART"); p != nil {
		if t, d, ok := icsTime(p, loc); ok {
			it.note("Start: %s", whenStr(t, d))
			start = t
		}
	}
	if x, err := strconv.Atoi(c.text("PERCENT-COMPLETE")); err == nil && x > 0 {
		it.f["prog"] = map[string]any{"mode": "percent", "done": float64(min(x, 100))}
	}
	if r := c.get("RRULE"); r != nil {
		recur, exact, _ := icsRule(r.value, loc)
		if recur != "" {
			it.f["recur"] = recur
		}
		if !exact {
			it.note("Repeat rule from the calendar: %s", r.value)
		}
	}
	for _, a := range c.subs {
		p := a.get("TRIGGER")
		if a.name != "VALARM" || p == nil {
			continue
		}
		var at time.Time
		if strings.EqualFold(p.params["VALUE"], "DATE-TIME") {
			at, _, _ = icsTime(p, loc)
		} else if d, ok := icsDur(p.value); ok {
			base := start
			if strings.EqualFold(p.params["RELATED"], "END") || base.IsZero() {
				base = due
			}
			if !base.IsZero() {
				at = base.Add(d)
			}
		}
		if at.IsZero() {
			continue
		}
		if w := whenStr(at, false); closedStatus[st] || isPast(w, loc, now) {
			it.note("Alarm: %s", w)
		} else {
			it.f["reminder"] = w
		}
		break
	}
	return it
}

func parseICSImport(data []byte, loc *time.Location, now time.Time) (impResult, error) {
	root, err := parseICS(data)
	if err != nil {
		return impResult{}, err
	}
	var r impResult
	var walk func(c *icsComp)
	walk = func(c *icsComp) {
		for _, x := range c.subs {
			switch {
			case x.name == "VCALENDAR":
				walk(x)
			case (x.name == "VTODO" || x.name == "VEVENT") && x.get("RECURRENCE-ID") != nil:
				r.skipped++ // a changed single date of a repeating series: the series itself is imported
			case x.name == "VTODO":
				r.items = append(r.items, icsTodo(x, loc, now))
			case x.name == "VEVENT":
				if it, ok := icsEvent(x, loc, now); ok {
					r.items = append(r.items, it)
				} else {
					r.skipped++
				}
			}
		}
	}
	walk(root)
	return r, nil
}

// ---------------------------------------------------------------- CSV

// csvAliases maps column names of this app's CSV export and of other tools to fields.
var csvAliases = map[string][]string{
	"name":     {"name", "title", "task", "task_name", "summary", "subject", "content", "todo", "item"},
	"notes":    {"notes", "note", "description", "details", "comment", "comments", "body"},
	"deadline": {"deadline", "due", "due_date", "duedate", "date"},
	"end":      {"end", "end_date", "ends", "end_time"},
	"reminder": {"reminder", "remind", "reminder_date", "alarm"},
	"tags":     {"tags", "tag", "labels", "label", "categories", "category"},
	"people":   {"people", "person", "assignee", "assignees", "assigned_to", "owner", "responsible"},
	"priority": {"priority", "prio", "importance"},
	"status":   {"status", "state", "completed", "complete", "done"},
	"type":     {"type", "kind"},
	"location": {"location", "place"},
	"url":      {"url", "link", "website"},
	"repeat":   {"repeat", "recurrence", "recur"},
	"estimate": {"estimate_min", "estimate", "estimate_minutes"},
	"tracked":  {"tracked_min"},
	"percent":  {"progress_percent", "progress", "percent_complete"},
	"done":     {"progress_done"},
	"total":    {"progress_total"},
	"unit":     {"progress_unit"},
	"id":       {"id"},
	"parent":   {"parent_id", "parent"},
}

func csvKey(h string) string {
	h = strings.NewReplacer(" ", "_", "-", "_", "/", "_", ".", "_").Replace(strings.ToLower(strings.TrimSpace(h)))
	for k, al := range csvAliases {
		for _, a := range al {
			if a == h {
				return k
			}
		}
	}
	return ""
}

// csvUntext undoes csvText: the ' that the export puts before =, +, -, @.
func csvUntext(s string) string {
	if len(s) > 1 && s[0] == '\'' && strings.ContainsRune("=+-@\t\r", rune(s[1])) {
		return s[1:]
	}
	return s
}

var csvStatusAlias = map[string]string{"completed": "done", "complete": "done", "finished": "done", "closed": "done",
	"yes": "done", "true": "done", "x": "done", "1": "done", "in progress": "progress", "in_progress": "progress",
	"started": "progress", "doing": "progress", "to do": "open", "todo": "open", "not started": "open", "new": "open",
	"no": "open", "false": "open", "0": "open", "blocked": "waiting", "on hold": "waiting", "someday": "later",
	"canceled": "cancelled", "postponed": "later"}

func csvStatus(kind, v string) string {
	s := strings.ToLower(strings.TrimSpace(v))
	if validStatus(kind, s) {
		return s
	}
	if a := csvStatusAlias[s]; validStatus(kind, a) {
		return a
	}
	return ""
}

func csvPrio(v string) (float64, bool) {
	s := strings.ToLower(strings.TrimSpace(v))
	if p, ok := map[string]float64{"none": 0, "low": 1, "medium": 2, "normal": 2, "high": 3, "urgent": 4, "highest": 4, "critical": 4}[s]; ok {
		return p, true
	}
	if x, err := strconv.Atoi(s); err == nil && x >= 0 && x <= 4 {
		return float64(x), true
	}
	return 0, false
}

var csvTimeLayouts = []string{"2006-01-02T15:04:05", "2006-01-02T15:04", "2006-01-02 15:04:05", "2006-01-02 15:04", "2.1.2006 15:04:05", "2.1.2006 15:04"}

// csvWhen reads 2026-11-05, 05.11.2026 (with an optional time) and RFC 3339 times.
func csvWhen(v string, loc *time.Location) (string, bool) {
	v = strings.TrimSpace(v)
	if t, err := time.Parse(time.RFC3339, v); err == nil {
		return t.In(loc).Format("2006-01-02T15:04"), true
	}
	for _, l := range []string{"2006-01-02", "2.1.2006"} {
		if t, err := time.ParseInLocation(l, v, loc); err == nil {
			return t.Format("2006-01-02"), true
		}
	}
	for _, l := range csvTimeLayouts {
		if t, err := time.ParseInLocation(l, v, loc); err == nil {
			return t.Format("2006-01-02T15:04"), true
		}
	}
	return "", false
}

func csvList(v string) []any {
	var out []any
	for _, s := range strings.FieldsFunc(v, func(r rune) bool { return r == ',' || r == ';' }) {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func csvNum(v string) (float64, bool) {
	x, err := strconv.ParseFloat(strings.ReplaceAll(strings.TrimSpace(v), ",", "."), 64)
	return x, err == nil && x >= 0
}

func parseCSVImport(data []byte, loc *time.Location, now time.Time) (impResult, error) {
	s := strings.TrimPrefix(string(data), "\xEF\xBB\xBF")
	first := s
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		first = s[:i]
	}
	comma, best := ',', strings.Count(first, ",")
	for _, c := range []rune{';', '\t'} { // German Excel saves with semicolons
		if n := strings.Count(first, string(c)); n > best {
			comma, best = c, n
		}
	}
	rd := csvfmt.NewReader(strings.NewReader(s))
	rd.Comma, rd.FieldsPerRecord, rd.LazyQuotes = comma, -1, true
	rows, err := rd.ReadAll()
	if err != nil {
		return impResult{}, fmt.Errorf("not a valid CSV file: %w", err)
	}
	if len(rows) < 2 {
		return impResult{}, errors.New("the CSV file has no rows below the header")
	}
	col := map[string]int{}
	for i, h := range rows[0] {
		if k := csvKey(h); k != "" {
			if _, dup := col[k]; !dup {
				col[k] = i
			}
		}
	}
	if _, ok := col["name"]; !ok {
		return impResult{}, errors.New("no name column: the first row needs a column such as name, title or task")
	}

	var r impResult
	for _, row := range rows[1:] {
		get := func(k string) string {
			if i, ok := col[k]; ok && i < len(row) {
				return csvUntext(strings.TrimSpace(row[i]))
			}
			return ""
		}
		if strings.TrimSpace(strings.Join(row, "")) == "" {
			continue
		}
		it := &impItem{name: get("name"), f: map[string]any{}, key: get("id"), parent: get("parent")}
		if it.name == "" {
			r.skipped++
			continue
		}
		raw := get("type")
		kind := map[string]string{"todo": "todo", "task": "todo", "idea": "idea", "project": "project", "reminder": "reminder", "event": "reminder"}[strings.ToLower(raw)]
		if kind == "" {
			kind = "todo"
			if raw != "" {
				it.note("Type: %s", raw)
			}
		}
		it.f["kind"] = kind
		if v := get("status"); v != "" && kind != "reminder" {
			if st := csvStatus(kind, v); st != "" {
				it.f["status"] = st
			} else {
				it.note("Status: %s", v)
			}
		}
		if v := get("priority"); v != "" {
			if p, ok := csvPrio(v); ok {
				it.f["prio"] = p
			} else {
				it.note("Priority: %s", v)
			}
		}
		for _, k := range []string{"notes", "location"} {
			if v := get(k); v != "" {
				it.f[k] = v
			}
		}
		if v := get("url"); v != "" {
			if u := normURL(v); u != "" {
				it.f["url"] = u
			} else {
				it.note("Link: %s", v)
			}
		}
		for _, k := range []string{"tags", "people"} {
			if l := csvList(get(k)); len(l) > 0 {
				it.f[k] = l
			}
		}
		if v := get("repeat"); v != "" {
			if rc, ok := normRecur(v); ok && rc != "" {
				it.f["recur"] = rc
			} else {
				it.note("Repeat: %s", v)
			}
		}
		if v := get("estimate"); v != "" {
			if x, ok := csvNum(v); ok {
				it.f["est"] = x
			} else {
				it.note("Estimate: %s", v)
			}
		}
		if x, ok := csvNum(get("tracked")); ok && x > 0 {
			it.note("Time tracked before the import: %s min", get("tracked"))
		}
		if v := get("deadline"); v != "" {
			if w, ok := csvWhen(v, loc); ok {
				it.f["deadline"] = w
			} else {
				it.note("Due: %s", v)
			}
		}
		end := get("end")
		if v := end; v != "" && kind != "reminder" && get("deadline") == "" { // other tools: "end date" of a task
			if w, ok := csvWhen(v, loc); ok {
				it.f["deadline"] = w
			} else {
				it.note("End: %s", v)
			}
			end = ""
		}
		if v := get("reminder"); v != "" {
			w, ok := csvWhen(v, loc)
			rc, _ := it.f["recur"].(string)
			switch {
			case !ok:
				it.note("Reminder: %s", v)
			case kind == "reminder":
				e, eok := csvWhen(end, loc)
				last := w
				if eok {
					last = e
				}
				if isPast(last, loc, now) && rc == "" {
					r.skipped++ // a past one-time event
					continue
				}
				for i := 0; i < 100000 && isPast(last, loc, now); i++ { // first occurrence that has not ended
					next, ev := nextOcc(w, rc, loc), Node{"end": e}
					shiftEnd(ev, w, next)
					w, e = next, ev.S("end")
					last = w
					if eok {
						last = e
					}
				}
				it.f["reminder"] = w
				if eok {
					it.f["end"] = e
				}
			case isPast(w, loc, now):
				it.note("Reminder: %s", w)
			default:
				it.f["reminder"] = w
			}
		}
		if _, used := it.f["end"]; end != "" && !used {
			it.note("End: %s", end)
		}
		if d, ok := csvNum(get("done")); ok {
			t, _ := csvNum(get("total"))
			if u := get("unit"); u != "" || (t > 0 && t != 100) {
				it.f["prog"] = map[string]any{"mode": "units", "done": d, "total": max(t, 1), "unit": u}
			} else if d > 0 {
				it.f["prog"] = map[string]any{"mode": "percent", "done": d}
			}
		} else if p, ok := csvNum(strings.TrimSuffix(get("percent"), "%")); ok && p > 0 {
			it.f["prog"] = map[string]any{"mode": "percent", "done": min(p, 100)}
		}
		r.items = append(r.items, it)
	}
	return r, nil
}
