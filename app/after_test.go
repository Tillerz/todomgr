package main

import (
	"testing"
	"time"
)

func TestAfterDeadline(t *testing.T) {
	loc := time.Local
	for _, c := range []struct {
		ev          Node
		start, want string
		days        int
	}{
		{Node{"reminder": "2026-11-04T18:00", "end": "2026-11-04T23:00"}, "2026-11-18T18:00", "2026-11-26", 8},
		{Node{"reminder": "2026-11-04", "end": "2026-11-06"}, "2026-11-04", "2026-11-08", 2}, // 3-day event, date only
		{Node{"reminder": "2026-11-04T18:00"}, "2026-11-04T18:00", "2026-11-12", 8},          // no end
		{Node{"reminder": "2026-11-04T22:00", "end": "2026-11-05T02:00"}, "2026-11-04T22:00", "2026-11-05", 0},
	} {
		if got := afterDeadline(c.ev, c.start, c.days, loc); got != c.want {
			t.Errorf("%v from %s +%d: %s, want %s", c.ev, c.start, c.days, got, c.want)
		}
	}
}

func TestAfterEvent(t *testing.T) {
	a := testServer(t)
	now := time.Now()
	day := func(d int) string { return now.AddDate(0, 0, d).Format("2006-01-02") }
	// a weekly game night that was 2 days ago; firing it moves it on to next week and keeps atDone
	ev := a.call(201, "POST", "/api/nodes", map[string]any{"name": "Game night", "kind": "reminder",
		"reminder": day(-2) + "T18:00", "end": day(-2) + "T23:00", "recur": "w"})["id"].(string)
	a.s.st.checkReminders(now)
	if g := a.call(200, "GET", "/api/nodes/"+ev, nil); g["reminder"] != day(5)+"T18:00" || g["atDone"] != day(-2)+"T18:00" {
		t.Fatalf("event after firing: reminder %v, atDone %v", g["reminder"], g["atDone"])
	}
	id := a.call(201, "POST", "/api/nodes", map[string]any{"name": "Session diary", "prio": 3, "recur": "w+"})["id"].(string)

	// the last game night is 2 days ago and its 8 days have not passed: the diary is due now
	n := a.call(200, "PATCH", "/api/nodes/"+id, map[string]any{"after": map[string]any{"id": ev, "days": 8}})
	if af, _ := n["after"].(map[string]any); af["start"] != day(-2)+"T18:00" || n["deadline"] != day(6) || n["recur"] != "" || n["blocked"] == true {
		t.Fatalf("after set: after %v, deadline %v (want %s), recur %q, blocked %v", n["after"], n["deadline"], day(6), n["recur"], n["blocked"])
	}
	if !inToday(a, id) {
		t.Error("an open diary of a past game night should be on Today (next up)")
	}

	// done: it waits for next week's game night, due 8 days after it
	n = a.call(200, "POST", "/api/nodes/"+id+"/status", map[string]any{"status": "done"})
	if af, _ := n["after"].(map[string]any); n["status"] != "open" || af["start"] != day(5)+"T18:00" || n["deadline"] != day(13) || n["blocked"] != true {
		t.Fatalf("after done: status %v, after %v, deadline %v (want %s), blocked %v", n["status"], n["after"], n["deadline"], day(13), n["blocked"])
	}
	if inToday(a, id) {
		t.Error("a diary that waits for its game night must not be on Today")
	}

	// only 1 day after the game night: that one has passed, so it belongs to next week's
	n = a.call(200, "PATCH", "/api/nodes/"+id, map[string]any{"after": map[string]any{"id": ev, "days": 1}})
	if af, _ := n["after"].(map[string]any); af["start"] != day(5)+"T18:00" || n["deadline"] != day(6) {
		t.Errorf("1 day: after %v, deadline %v", n["after"], n["deadline"])
	}

	// a repeat rule of its own replaces it; null removes it
	if n = a.call(200, "PATCH", "/api/nodes/"+id, map[string]any{"recur": "m"}); n["after"] != nil {
		t.Errorf("recur did not remove after: %v", n["after"])
	}
	a.call(200, "PATCH", "/api/nodes/"+id, map[string]any{"after": map[string]any{"id": ev, "days": 8}})
	if n = a.call(200, "PATCH", "/api/nodes/"+id, map[string]any{"after": nil}); n["after"] != nil {
		t.Errorf("null did not remove after: %v", n["after"])
	}

	// an event that does not repeat: done stays done
	once := a.call(201, "POST", "/api/nodes", map[string]any{"name": "Convention", "kind": "reminder", "reminder": day(-1)})["id"].(string)
	a.call(200, "PATCH", "/api/nodes/"+id, map[string]any{"after": map[string]any{"id": once, "days": 3}})
	if n = a.call(200, "POST", "/api/nodes/"+id+"/status", map[string]any{"status": "done"}); n["status"] != "done" {
		t.Errorf("after a single event: status %v, want done", n["status"])
	}

	// invalid values
	a.call(400, "PATCH", "/api/nodes/"+id, map[string]any{"after": map[string]any{"id": id, "days": 8}})
	a.call(400, "PATCH", "/api/nodes/"+id, map[string]any{"after": map[string]any{"id": ev, "days": -1}})
	a.call(400, "PATCH", "/api/nodes/"+id, map[string]any{"after": map[string]any{"id": ev, "days": 1.5}})
	a.call(400, "PATCH", "/api/nodes/"+ev, map[string]any{"after": map[string]any{"id": once, "days": 1}})
}

func inToday(a *apiT, id string) bool {
	for _, l := range a.call(200, "GET", "/api/today", nil) {
		arr, _ := l.([]any)
		for _, x := range arr {
			if m, _ := x.(map[string]any); m["id"] == id {
				return true
			}
		}
	}
	return false
}

// moving the event moves the todos that wait for it; a todo that becomes an event loses its deadline
func TestAfterFollowAndKind(t *testing.T) {
	a := testServer(t)
	now := time.Now()
	day := func(d int) string { return now.AddDate(0, 0, d).Format("2006-01-02") }
	ev := a.call(201, "POST", "/api/nodes", map[string]any{"name": "Game night", "kind": "reminder",
		"reminder": day(5) + "T18:00", "end": day(5) + "T23:00", "recur": "w"})["id"].(string)
	id := a.call(201, "POST", "/api/nodes", map[string]any{"name": "Session diary"})["id"].(string)
	a.call(200, "PATCH", "/api/nodes/"+id, map[string]any{"after": map[string]any{"id": ev, "days": 8}})
	a.call(200, "PATCH", "/api/nodes/"+ev, map[string]any{"reminder": day(6) + "T19:00", "end": day(7) + "T01:00"})
	n := a.call(200, "GET", "/api/nodes/"+id, nil)
	if af, _ := n["after"].(map[string]any); af["start"] != day(6)+"T19:00" || n["deadline"] != day(15) {
		t.Errorf("after the event moved: after %v, deadline %v (want %s)", n["after"], n["deadline"], day(15))
	}
	td := a.call(201, "POST", "/api/nodes", map[string]any{"name": "Stream", "deadline": day(3) + "T20:00"})["id"].(string)
	if n = a.call(200, "PATCH", "/api/nodes/"+td, map[string]any{"kind": "reminder"}); n["reminder"] != day(3)+"T20:00" || n["deadline"] != "" {
		t.Errorf("todo → event: reminder %v, deadline %v", n["reminder"], n["deadline"])
	}
}
