package main

import (
	"fmt"
	"log"
	"maps"
	"strings"
	"time"
)

// Reminder scheduler. It runs inside the daemon, so reminders are noticed even
// when no browser window is open. The bookkeeping lives on the entries
// themselves (preDone, atDone, remDone, snoozeUntil), so it survives restarts.
//
// When something fires, the entry gets  alert = {when, at}.  Every open UI shows
// it as a pop-up and clears it with POST /api/alerts/ack; if no UI is open, it
// simply waits until one is. when is one of:
//
//	soon     the early warning ("starts in 15 min")
//	now      it is time
//	task     the reminder time of a normal todo/idea/project
//	snoozed  a snooze ran out

const snoozeMinutes = 10

// remindAt is the moment a reminder value means (a bare date means 09:00).
func remindAt(rem string, loc *time.Location) (time.Time, bool) {
	t, ok := parseWhen(rem, loc)
	if ok && !strings.Contains(rem, "T") {
		t = t.Add(9 * time.Hour)
	}
	return t, ok
}

// endAt is when an event ends; a date-only end lasts to the end of that day.
func endAt(end string, loc *time.Location) (time.Time, bool) {
	t, ok := parseWhen(end, loc)
	if ok && !strings.Contains(end, "T") {
		t = t.AddDate(0, 0, 1)
	}
	return t, ok
}

// shiftEnd moves the end of an event by as much as its start moved (in wall-clock
// time, so a move across a daylight saving change keeps the clock times).
func shiftEnd(n Node, oldStart, newStart string) {
	end := n.S("end")
	a, ok1 := parseWhen(oldStart, time.UTC)
	b, ok2 := parseWhen(newStart, time.UTC)
	e, ok3 := parseWhen(end, time.UTC)
	if end == "" || !ok1 || !ok2 || !ok3 || a.Equal(b) {
		return
	}
	layout := "2006-01-02"
	if strings.Contains(end, "T") {
		layout = "2006-01-02T15:04"
	}
	n["end"] = e.Add(b.Sub(a)).Format(layout)
}

func setAlert(n Node, when string, at float64) {
	n["alert"] = map[string]any{"when": when, "at": at}
}

// stepReminder advances the reminder state of one entry to the time `now`.
// changed reports whether the entry was modified; announce whether a
// reminder-type entry reached its time (so it may be posted to Discord).
func stepReminder(n Node, now time.Time, loc *time.Location) (changed, announce bool) {
	t := float64(now.UnixMilli())
	if su := n.N("snoozeUntil"); su > 0 && t >= su {
		delete(n, "snoozeUntil")
		setAlert(n, "snoozed", t)
		changed = true
	}
	rem := n.S("reminder")
	if rem == "" {
		return
	}
	at, ok := remindAt(rem, loc)
	if !ok {
		return
	}
	atMs := float64(at.UnixMilli())

	if n.S("kind") != "reminder" {
		if done, _ := n["remDone"].(bool); !done && !closedStatus[n.S("status")] && t >= atMs {
			n["remDone"] = true
			setAlert(n, "task", atMs)
			changed = true
		}
		return
	}

	lead := n.N("lead") * 60000
	if lead > 0 && n.S("preDone") != rem && t >= atMs-lead && t < atMs {
		n["preDone"] = rem
		setAlert(n, "soon", atMs)
		changed = true
	}
	if t >= atMs && n.S("atDone") != rem {
		n["atDone"], n["preDone"] = rem, rem
		// a reminder found more than 2 minutes late (the app was off) still pops up,
		// shown as "missed" by the UI, but is not posted to Discord
		announce = t-atMs <= 2*60000
		setAlert(n, "now", atMs)
		changed = true
		if r := n.S("recur"); r != "" {
			for g := 0; g < 2000; g++ {
				nt, ok := remindAt(n.S("reminder"), loc)
				if !ok || float64(nt.UnixMilli()) > t {
					break
				}
				next := nextOcc(n.S("reminder"), r, loc)
				shiftEnd(n, n.S("reminder"), next)
				n["reminder"] = next
			}
		}
	}
	return
}

func (s *Store) reminderLoop() {
	tk := time.NewTicker(5 * time.Second)
	defer tk.Stop()
	for {
		s.checkReminders(s.Now())
		select {
		case <-s.done:
			return
		case <-tk.C:
		}
	}
}

// checkReminders fires everything that is due at `now`.
func (s *Store) checkReminders(now time.Time) {
	// cheap read-only pass first: most ticks have nothing to do and must not write
	s.mu.RLock()
	var due []string
	for id, n := range s.db.Nodes {
		if id == "root" || n["trashed"] != nil || (n.S("reminder") == "" && n.N("snoozeUntil") == 0) {
			continue
		}
		if ch, _ := stepReminder(maps.Clone(n), now, s.loc); ch {
			due = append(due, id)
		}
	}
	s.mu.RUnlock()
	if len(due) == 0 {
		return
	}
	var post []string
	_, err := s.Mutate("system", "", func(tx *Tx) error {
		for _, id := range due {
			n := tx.db.Nodes[id]
			if n == nil {
				continue
			}
			ch, ann := stepReminder(n, now, s.loc)
			if ch {
				tx.touch(id)
			}
			if ann && n.Bool("discord") {
				post = append(post, id)
			}
		}
		return nil
	})
	if err != nil {
		log.Printf("reminders: %v", err)
		return
	}
	for _, id := range post {
		go func() {
			if err := s.announceReminder(id); err != nil {
				log.Printf("reminder %s: could not post to Discord: %v", id, err)
			}
		}()
	}
}

// AckAlert clears the pending pop-up of an entry ("ok") or postpones it ("snooze").
func (s *Store) AckAlert(id, action string) error {
	if action != "ok" && action != "snooze" {
		return fmt.Errorf("action must be ok or snooze")
	}
	s.mu.RLock()
	n, found := s.db.Nodes[id]
	has := found && n["alert"] != nil
	s.mu.RUnlock()
	if !found || id == "root" {
		return fmt.Errorf("entry %q not found", id)
	}
	if !has {
		return nil // already handled in another window
	}
	_, err := s.Mutate("ui", "", func(tx *Tx) error {
		n := tx.db.Nodes[id]
		delete(n, "alert")
		if action == "snooze" {
			n["snoozeUntil"] = float64(time.Now().Add(snoozeMinutes * time.Minute).UnixMilli())
		}
		tx.touch(id)
		return nil
	})
	return err
}
