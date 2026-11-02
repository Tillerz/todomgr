package main

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Tx is one atomic change. Everything done through it is validated, saved and
// announced together by Store.Mutate; if anything fails nothing is kept.
type Tx struct {
	db      *DB
	actor   string
	loc     *time.Location
	touched map[string]bool
	acts    []map[string]any
}

func (t *Tx) touch(ids ...string) {
	for _, id := range ids {
		if id != "" {
			t.touched[id] = true
		}
	}
}

func (t *Tx) log(typ, id string, extra map[string]any) {
	e := map[string]any{"t": nowMs(), "id": id, "type": typ, "by": t.actor}
	for k, v := range extra {
		e[k] = v
	}
	t.acts = append(t.acts, e)
}

func (t *Tx) now() time.Time { return time.Now().In(t.loc) }

var (
	reColor = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)
	// Discord webhook URLs only (https, discord.com / discordapp.com, optional ptb./canary.).
	reHook = regexp.MustCompile(`^https://((ptb|canary)\.)?discord(app)?\.com/api(/v\d+)?/webhooks/\d+/[\w-]+$`)
	reHTTP = regexp.MustCompile(`^https?://[^\s]+$`)
)

// normURL returns a valid http(s) URL or "" (bare "example.com" gets https://).
func normURL(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return ""
	}
	if !regexp.MustCompile(`(?i)^[a-z][a-z0-9+.-]*:`).MatchString(v) {
		v = "https://" + v
	}
	if reHTTP.MatchString(v) {
		return v
	}
	return ""
}

var fieldOrder = []string{"kind", "name", "color", "prio", "tags", "people", "location", "deadline", "reminder", "end",
	"deps", "notes", "notesFormat", "links", "url", "webhook", "prog", "recur", "est", "lead", "discord", "collapsed", "status"}

func (t *Tx) applyFields(n Node, f map[string]any, creating bool) error {
	for k := range f {
		known := false
		for _, o := range fieldOrder {
			if o == k {
				known = true
				break
			}
		}
		if !known {
			return fmt.Errorf("unknown field %q", k)
		}
	}
	for _, k := range fieldOrder {
		v, ok := f[k]
		if !ok {
			continue
		}
		if err := t.setField(n, k, v, creating); err != nil {
			return fmt.Errorf("%s: %w", k, err)
		}
	}
	n["updated"] = nowMs()
	return nil
}

func cleanList(v any, strip string) ([]any, error) {
	var in []string
	switch x := v.(type) {
	case string:
		for _, p := range strings.Split(x, ",") {
			in = append(in, p)
		}
	case []any, []string:
		in = strs(x)
		if arr, ok := x.([]any); ok && len(arr) != len(in) {
			return nil, errors.New("must be a list of strings")
		}
	case nil:
	default:
		return nil, errors.New("must be a list of strings")
	}
	out := []any{}
	seen := map[string]bool{}
	for _, s := range in {
		s = strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(s), strip))
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out, nil
}

func (t *Tx) setField(n Node, k string, v any, creating bool) error {
	id := n.S("id")
	switch k {
	case "name":
		s, ok := v.(string)
		if !ok || strings.TrimSpace(s) == "" {
			return errors.New("a name is required")
		}
		n["name"] = strings.TrimSpace(s)
	case "kind":
		s, _ := v.(string)
		if _, ok := kindStatuses[s]; !ok {
			return errors.New("must be todo, idea, project or reminder")
		}
		t.setKind(n, s)
	case "color":
		s, ok := v.(string)
		if !ok || (s != "" && !reColor.MatchString(s)) {
			return errors.New("must be empty or #rrggbb")
		}
		n["color"] = s
	case "prio":
		p := num(v)
		if p < 0 || p > 4 || p != float64(int(p)) {
			return errors.New("must be 0 (none) to 4 (urgent)")
		}
		n["prio"] = p
	case "tags":
		l, err := cleanList(v, "#")
		if err != nil {
			return err
		}
		n["tags"] = l
	case "people":
		l, err := cleanList(v, "@")
		if err != nil {
			return err
		}
		n["people"] = l
	case "location", "notes":
		s, ok := v.(string)
		if !ok {
			return errors.New("must be text")
		}
		n[k] = s
	case "end": // the end of an event: not before its start; a date-only end lasts to the end of that day
		s, ok := v.(string)
		if !ok || !validWhen(s, t.loc) {
			return errors.New("must be empty, YYYY-MM-DD or YYYY-MM-DDTHH:MM")
		}
		if start, ok := parseWhen(n.S("reminder"), t.loc); ok && s != "" {
			if e, _ := endAt(s, t.loc); e.Before(start) {
				return errors.New("the end must not be before the start")
			}
		}
		n["end"] = s
	case "notesFormat":
		s, _ := v.(string)
		if s != "markdown" && s != "bbcode" {
			return errors.New("must be markdown or bbcode")
		}
		n[k] = s
	case "deadline", "reminder":
		s, ok := v.(string)
		if !ok || !validWhen(s, t.loc) {
			return errors.New("must be empty, YYYY-MM-DD or YYYY-MM-DDTHH:MM")
		}
		old := n.S(k)
		n[k] = s
		if k == "reminder" { // a new time re-arms the reminder, and an event keeps its length
			shiftEnd(n, old, s)
			delete(n, "alert")
			delete(n, "snoozeUntil")
			delete(n, "remDone")
		}
	case "deps":
		l, err := cleanList(v, "")
		if err != nil {
			return err
		}
		for _, d := range strs(l) {
			x, ok := t.db.Nodes[d]
			if !ok || d == id || d == "root" || x["trashed"] != nil {
				return fmt.Errorf("unknown entry %q", d)
			}
			if t.db.dependsOn(x, id, nil) {
				return fmt.Errorf("%q already depends on this entry (dependency cycle)", d)
			}
		}
		n["deps"] = l
	case "links":
		arr, ok := v.([]any)
		if !ok && v != nil {
			return errors.New("must be a list of {title, uri}")
		}
		out := []any{}
		for _, e := range arr {
			m, ok := e.(map[string]any)
			if !ok || strings.TrimSpace(str(m["uri"])) == "" {
				return errors.New("each link needs a uri")
			}
			title := strings.TrimSpace(str(m["title"]))
			if title == "" {
				title = str(m["uri"])
			}
			out = append(out, map[string]any{"title": title, "uri": strings.TrimSpace(str(m["uri"]))})
		}
		n["links"] = out
	case "url":
		s, ok := v.(string)
		if !ok {
			return errors.New("must be text")
		}
		u := normURL(s)
		if strings.TrimSpace(s) != "" && u == "" {
			return errors.New("must be an http(s) URL")
		}
		n["url"] = u
	case "webhook":
		s, ok := v.(string)
		s = strings.TrimSpace(s)
		if !ok || (s != "" && !reHook.MatchString(s)) {
			return errors.New("must be a Discord webhook URL")
		}
		n["webhook"] = s
	case "prog":
		m, ok := v.(map[string]any)
		if !ok {
			return errors.New("must be {mode, done, total, unit}")
		}
		cur := n.Obj("prog")
		if cur == nil {
			cur = map[string]any{"mode": "percent", "done": 0.0, "total": 100.0, "unit": ""}
		}
		out := map[string]any{"mode": cur["mode"], "done": cur["done"], "total": cur["total"], "unit": cur["unit"]}
		if x, ok := m["mode"]; ok {
			if s := str(x); s == "percent" || s == "units" {
				out["mode"] = s
			} else {
				return errors.New("mode must be percent or units")
			}
		}
		if x, ok := m["total"]; ok {
			out["total"] = max(1.0, num(x))
		}
		if x, ok := m["done"]; ok {
			out["done"] = max(0.0, num(x))
		}
		if x, ok := m["unit"]; ok {
			out["unit"] = str(x)
		}
		if out["mode"] == "percent" {
			out["total"] = 100.0
		}
		if num(out["done"]) > num(out["total"]) {
			out["done"] = out["total"]
		}
		n["prog"] = out
	case "recur":
		s, _ := v.(string)
		r, ok := normRecur(s)
		if !ok {
			return errors.New("must be empty, d/w/m/y, every N (2w, 3d, 6m) or monthly on weekdays (m:1fr,3fr,-1fr)")
		}
		n["recur"] = r
	case "est", "lead":
		x := num(v)
		if x < 0 {
			return errors.New("must be >= 0")
		}
		n[k] = x
	case "collapsed", "discord":
		b, ok := v.(bool)
		if !ok {
			return errors.New("must be true or false")
		}
		n[k] = b
	case "status":
		s, _ := v.(string)
		if creating {
			if !validStatus(n.S("kind"), s) {
				return fmt.Errorf("%q is not a status of a %s", s, n.S("kind"))
			}
			n["status"] = s
			n["hist"] = []any{map[string]any{"s": s, "at": nowMs()}}
		} else if err := t.setStatus(n, s); err != nil {
			return err
		}
	}
	return nil
}

func (t *Tx) setKind(n Node, kind string) {
	if n.S("kind") == kind && n.S("status") != "" {
		return
	}
	n["kind"] = kind
	if !validStatus(kind, n.S("status")) {
		s := kindStatuses[kind][0]
		n["status"] = s
		h, _ := n["hist"].([]any)
		n["hist"] = append(h, map[string]any{"s": s, "at": nowMs()})
	}
	if kind == "reminder" {
		if n.S("reminder") == "" {
			n["reminder"] = defaultRemTime(t.now())
		}
		if _, ok := n["lead"]; !ok {
			n["lead"] = 15.0
		}
	}
}

func (t *Tx) setStatus(n Node, s string) error {
	kind := n.S("kind")
	if kind == "reminder" {
		return errors.New("reminders have no status")
	}
	if !validStatus(kind, s) {
		return fmt.Errorf("%q is not a status of a %s (use: %s)", s, kind, strings.Join(kindStatuses[kind], ", "))
	}
	if n.S("status") == s {
		return nil
	}
	n["status"] = s
	h, _ := n["hist"].([]any)
	n["hist"] = append(h, map[string]any{"s": s, "at": nowMs()})
	n["updated"] = nowMs()
	typ := "status"
	if s == "done" {
		typ = "close"
	}
	t.log(typ, n.S("id"), map[string]any{"s": s})
	t.touch(n.S("id"))
	if s == "done" {
		p := n.Obj("prog")
		if p != nil {
			p["done"] = p["total"]
		}
		if n.S("recur") != "" && len(t.db.progressKids(n)) == 0 {
			t.recurNext(n)
		}
	}
	return nil
}

func (t *Tx) recurNext(n Node) {
	h, _ := n["hist"].([]any)
	n["status"] = "open"
	n["hist"] = append(h, map[string]any{"s": "open", "at": nowMs()})
	if p := n.Obj("prog"); p != nil {
		p["done"] = 0.0
	}
	delete(n, "remDone")
	for _, k := range []string{"deadline", "reminder"} {
		if s := n.S(k); s != "" {
			n[k] = nextOcc(s, n.S("recur"), t.loc)
		}
	}
}

func (t *Tx) create(name, parent string, index int, f map[string]any) (Node, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, errors.New("a name is required")
	}
	if parent == "" {
		parent = "root"
	}
	p, ok := t.db.Nodes[parent]
	if !ok || p["trashed"] != nil {
		return nil, fmt.Errorf("parent %q not found", parent)
	}
	id := newID(t.db)
	n := defaultNode(id, name)
	t.db.Nodes[id] = n
	f = copyMap(f)
	delete(f, "name")
	// status must be applied after kind, which fieldOrder guarantees
	if err := t.applyFields(n, f, true); err != nil {
		delete(t.db.Nodes, id) // not attached yet: drop it, so the caller may go on (the import does)
		return nil, err
	}
	if _, has := f["kind"]; has {
		if _, hasStatus := f["status"]; !hasStatus {
			s := kindStatuses[n.S("kind")][0]
			n["status"] = s
			n["hist"] = []any{map[string]any{"s": s, "at": nowMs()}}
		}
	}
	t.db.attach(n, parent, index)
	p["collapsed"] = false
	t.touch(id, parent)
	t.log("create", id, nil)
	return n, nil
}

func copyMap(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func (t *Tx) get(id string) (Node, error) {
	n, ok := t.db.Nodes[id]
	if !ok || id == "root" {
		return nil, fmt.Errorf("entry %q not found", id)
	}
	return n, nil
}

func (t *Tx) patch(id string, f map[string]any) (Node, error) {
	n, err := t.get(id)
	if err != nil {
		return nil, err
	}
	if n["trashed"] != nil {
		return nil, errors.New("entry is in the trash; restore it first")
	}
	if len(f) == 0 {
		return nil, errors.New("nothing to change")
	}
	// reminders: a status change is an error, everything else fine
	if err := t.applyFields(n, f, false); err != nil {
		return nil, err
	}
	t.touch(id)
	t.log("edit", id, nil)
	return n, nil
}

func (t *Tx) move(id, parent string, index int) (Node, error) {
	n, err := t.get(id)
	if err != nil {
		return nil, err
	}
	if parent == "" {
		parent = "root"
	}
	p, ok := t.db.Nodes[parent]
	if !ok || p["trashed"] != nil {
		return nil, fmt.Errorf("target %q not found", parent)
	}
	if n["trashed"] != nil {
		return nil, errors.New("entry is in the trash")
	}
	if t.db.inSubtree(parent, id) {
		return nil, errors.New("cannot move an entry into itself or its own sub-items")
	}
	old := n.S("parent")
	t.db.detach(n)
	t.db.attach(n, parent, index)
	p["collapsed"] = false
	n["updated"] = nowMs()
	t.touch(id, old, parent)
	t.log("move", id, nil)
	return n, nil
}

func (t *Tx) trash(id string) error {
	n, err := t.get(id)
	if err != nil {
		return err
	}
	if n["trashed"] != nil {
		return errors.New("already in the trash")
	}
	old := n.S("parent")
	n["trashed"] = nowMs()
	n["tparent"] = old
	t.db.detach(n)
	n["parent"] = nil
	t.touch(id, old)
	t.log("trash", id, nil)
	return nil
}

func (t *Tx) restore(id string) (Node, error) {
	n, err := t.get(id)
	if err != nil {
		return nil, err
	}
	if n["trashed"] == nil {
		return nil, errors.New("not in the trash")
	}
	pid := n.S("tparent")
	if p, ok := t.db.Nodes[pid]; !ok || p["trashed"] != nil {
		pid = "root"
	}
	delete(n, "trashed")
	delete(n, "tparent")
	t.db.attach(n, pid, -1)
	n["updated"] = nowMs()
	t.touch(id, pid)
	t.log("restore", id, nil)
	return n, nil
}

func (t *Tx) logTime(id string, secs float64) (Node, error) {
	n, err := t.get(id)
	if err != nil {
		return nil, err
	}
	if secs <= 0 || secs > 24*3600 {
		return nil, errors.New("duration must be between 1 second and 24 hours")
	}
	arr, _ := n["time"].([]any)
	n["time"] = append(arr, map[string]any{"at": nowMs(), "secs": secs})
	n["updated"] = nowMs()
	t.touch(id)
	t.log("time", id, map[string]any{"secs": secs})
	return n, nil
}

// ---------------------------------------------------------------- quick add

var (
	reQuickTok  = regexp.MustCompile(`(^|\s)(due:|at:|repeat:|[#@!~])(\S+)`)
	reQuickRem  = regexp.MustCompile(`(?i)^r:\s*`)
	reQuickIdea = regexp.MustCompile(`^\?\s*`)
	reHM        = regexp.MustCompile(`^\d{1,2}:\d{2}$`)
	reEst       = regexp.MustCompile(`^(\d+)(m|h)?$`)
)

func parseWhenWord(v string, now time.Time) string {
	v = strings.ToLower(v)
	day := func(n int) string { return now.AddDate(0, 0, n).Format("2006-01-02") }
	switch v {
	case "today":
		return day(0)
	case "tomorrow", "tom":
		return day(1)
	}
	if m := regexp.MustCompile(`^\+(\d+)([dw])$`).FindStringSubmatch(v); m != nil {
		n := 0
		fmt.Sscanf(m[1], "%d", &n)
		if m[2] == "w" {
			n *= 7
		}
		return day(n)
	}
	if len(v) >= 3 {
		for i, d := range []string{"sun", "mon", "tue", "wed", "thu", "fri", "sat"} {
			if strings.HasPrefix(v, d) {
				diff := (i - int(now.Weekday()) + 7) % 7
				if diff == 0 {
					diff = 7
				}
				return day(diff)
			}
		}
	}
	if regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`).MatchString(v) {
		return v
	}
	if regexp.MustCompile(`^\d{2}-\d{2}$`).MatchString(v) {
		return fmt.Sprintf("%d-%s", now.Year(), v)
	}
	return ""
}

// parseQuick understands the same syntax as the UI's quick-add field:
//
//	[r: | ?] Name #tag @person !high due:fri at:19:00 repeat:weekly ~30m
func parseQuick(text string, now time.Time) (string, map[string]any) {
	f := map[string]any{}
	var tags, people []string
	var date, tm string
	if reQuickRem.MatchString(text) {
		f["kind"], f["lead"] = "reminder", 15.0
		text = reQuickRem.ReplaceAllString(text, "")
	} else if reQuickIdea.MatchString(text) {
		f["kind"] = "idea"
		text = reQuickIdea.ReplaceAllString(text, "")
	}
	prio := map[string]float64{"low": 1, "l": 1, "med": 2, "medium": 2, "m": 2, "high": 3, "h": 3, "urgent": 4, "u": 4, "1": 1, "2": 2, "3": 3, "4": 4}
	var sb strings.Builder
	last := 0
	for _, m := range reQuickTok.FindAllStringSubmatchIndex(text, -1) {
		sp, k, v := text[m[2]:m[3]], text[m[4]:m[5]], text[m[6]:m[7]]
		keep := false
		switch k {
		case "#":
			tags = append(tags, v)
		case "@":
			people = append(people, v)
		case "!":
			if p, ok := prio[strings.ToLower(v)]; ok {
				f["prio"] = p
			} else {
				keep = true
			}
		case "~":
			if mm := reEst.FindStringSubmatch(v); mm != nil {
				n := 0.0
				fmt.Sscanf(mm[1], "%f", &n)
				if mm[2] == "h" {
					n *= 60
				}
				f["est"] = n
			} else {
				keep = true
			}
		case "due:":
			if w := parseWhenWord(v, now); w != "" {
				date = w
			} else {
				keep = true
			}
		case "at:":
			if reHM.MatchString(v) {
				if len(v) == 4 {
					v = "0" + v
				}
				tm = v
			} else {
				keep = true
			}
		case "repeat:":
			if r, ok := normRecur(v); ok && r != "" {
				f["recur"] = r
			} else {
				keep = true
			}
		}
		sb.WriteString(text[last:m[0]])
		if keep {
			sb.WriteString(text[m[0]:m[1]])
		} else {
			sb.WriteString(sp)
		}
		last = m[1]
	}
	sb.WriteString(text[last:])
	name := strings.Join(strings.Fields(sb.String()), " ")
	if len(tags) > 0 {
		f["tags"] = toAny(tags)
	}
	if len(people) > 0 {
		f["people"] = toAny(people)
	}
	if f["kind"] == "reminder" {
		switch {
		case date == "" && tm == "":
			f["reminder"] = defaultRemTime(now)
		default:
			if date == "" {
				date = now.Format("2006-01-02")
				if d, ok := parseWhen(date+"T"+tm, now.Location()); ok && !d.After(now) {
					date = now.AddDate(0, 0, 1).Format("2006-01-02")
				}
			}
			if tm != "" {
				f["reminder"] = date + "T" + tm
			} else {
				f["reminder"] = date
			}
		}
	} else if date != "" {
		if tm != "" {
			f["deadline"] = date + "T" + tm
		} else {
			f["deadline"] = date
		}
	}
	return name, f
}
