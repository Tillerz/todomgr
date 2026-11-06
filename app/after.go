package main

// "After an event": a todo that belongs to an event, such as a session diary after each game night.
// The todo has after = {id: event, days: N, start: the start of the event occurrence it belongs to}.
// Until that occurrence has ended the todo waits (shown as blocked, not on Today); its deadline is
// the last day of the occurrence + N days. When it is done and the event repeats, it reopens for the
// next occurrence. The page has the same rules (index.html: afterFirst, afterEnd, waits).

import (
	"errors"
	"strings"
	"time"
)

// afterEnd is when the occurrence of event ev that starts at start ends: the event's end moved as
// far as its start moved, or the start itself for an event without an end.
func afterEnd(ev Node, start string, loc *time.Location) (time.Time, bool) {
	if ev.S("end") == "" {
		return remindAt(start, loc)
	}
	tmp := Node{"end": ev.S("end")}
	shiftEnd(tmp, ev.S("reminder"), start)
	return endAt(tmp.S("end"), loc)
}

// afterDeadline: the last day of the occurrence + days, as a date.
func afterDeadline(ev Node, start string, days int, loc *time.Location) string {
	e, ok := afterEnd(ev, start, loc)
	if !ok {
		return ""
	}
	return e.Add(-time.Minute).AddDate(0, 0, days).Format("2006-01-02")
}

// afterFirst picks the occurrence a new "after event" todo belongs to: the last one that started
// (atDone) while its deadline has not passed, else the next one.
func afterFirst(ev Node, days int, now time.Time) string {
	if last := ev.S("atDone"); last != "" {
		if d := afterDeadline(ev, last, days, now.Location()); d >= now.Format("2006-01-02") {
			return last
		}
	}
	return ev.S("reminder")
}

// afterInfo returns the event and the occurrence start of a todo with "after", if both exist.
func (db *DB) afterInfo(n Node) (Node, string, int, bool) {
	a := n.Obj("after")
	if a == nil || n.S("kind") != "todo" {
		return nil, "", 0, false
	}
	ev, ok := db.Nodes[str(a["id"])]
	if !ok || ev.S("kind") != "reminder" || db.inTrash(ev) {
		return nil, "", 0, false
	}
	days, _ := a["days"].(float64)
	return ev, str(a["start"]), int(days), true
}

// waits: the todo belongs to an event occurrence that has not ended yet.
func (db *DB) waits(n Node, now time.Time) bool {
	ev, start, _, ok := db.afterInfo(n)
	if !ok || db.isClosed(n) {
		return false
	}
	e, ok := afterEnd(ev, start, now.Location())
	return ok && now.Before(e)
}

// setAfter validates and applies the "after" field: {id, days} or null to remove it. It sets the
// deadline and removes a repeat rule, because the event's repeat rule takes its place.
func (t *Tx) setAfter(n Node, v any) error {
	if v == nil {
		delete(n, "after")
		return nil
	}
	m, ok := v.(map[string]any)
	if !ok {
		return errors.New("must be {id, days} or null")
	}
	ev, ok := t.db.Nodes[str(m["id"])]
	if !ok || ev.S("kind") != "reminder" || t.db.inTrash(ev) {
		return errors.New("id must be a reminder or event that is not in the trash")
	}
	if ev.S("reminder") == "" {
		return errors.New("the event has no date")
	}
	days, ok := m["days"].(float64)
	if !ok || days < 0 || days > 365 || days != float64(int(days)) {
		return errors.New("days must be a whole number from 0 to 365")
	}
	if n.S("kind") != "todo" {
		return errors.New("only todos can follow an event")
	}
	start := afterFirst(ev, int(days), t.now())
	n["after"] = map[string]any{"id": ev.S("id"), "days": days, "start": start}
	n["deadline"] = afterDeadline(ev, start, int(days), t.loc)
	n["recur"] = ""
	return nil
}

// afterNext reopens a done todo for the next occurrence of its event. false: the event does not
// repeat (or is gone), so the todo stays done.
func (t *Tx) afterNext(n Node) bool {
	ev, start, days, ok := t.db.afterInfo(n)
	r := ev.S("recur")
	if !ok || r == "" || strings.HasSuffix(r, "+") {
		return false
	}
	next := nextOcc(start, r, t.loc)
	if next == start {
		return false
	}
	h, _ := n["hist"].([]any)
	n["status"] = "open"
	n["hist"] = append(h, map[string]any{"s": "open", "at": nowMs()})
	if p := n.Obj("prog"); p != nil {
		p["done"] = 0.0
	}
	delete(n, "remDone")
	n.Obj("after")["start"] = next
	n["deadline"] = afterDeadline(ev, next, days, t.loc)
	return true
}

// afterFollow keeps the todos that follow event ev in step when its date changes: a todo that waits
// for the occurrence that started at oldStart moves with it, and every deadline is worked out again.
func (t *Tx) afterFollow(ev Node, oldStart string) {
	for id, n := range t.db.Nodes {
		a := n.Obj("after")
		if a == nil || str(a["id"]) != ev.S("id") || t.db.isClosed(n) || t.db.inTrash(n) {
			continue
		}
		if str(a["start"]) == oldStart {
			a["start"] = ev.S("reminder")
		}
		days, _ := a["days"].(float64)
		n["deadline"] = afterDeadline(ev, str(a["start"]), int(days), t.loc)
		n["updated"] = nowMs()
		t.touch(id)
	}
}
