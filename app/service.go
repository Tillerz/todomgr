package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// ---------------------------------------------------------------- write operations
// Used by the REST API, the CLI (through REST) and the MCP server.

func (s *Store) Create(actor, name, parent string, index int, f map[string]any) (map[string]any, error) {
	var out map[string]any
	_, err := s.Mutate(actor, "", func(tx *Tx) error {
		n, err := tx.create(name, parent, index, f)
		if err == nil {
			out = tx.db.fullView(n, tx.now())
		}
		return err
	})
	return out, err
}

func (s *Store) Quick(actor, text, parent string) (map[string]any, error) {
	name, f := parseQuick(text, s.Now())
	if name == "" {
		return nil, errors.New("nothing to add: the text has no name")
	}
	if parent == "" && f["kind"] == "project" {
		parent = "root" // projects group other entries, so they start at the top level
	} else if parent == "" {
		var err error
		if parent, err = s.inboxID(actor); err != nil {
			return nil, err
		}
	}
	return s.Create(actor, name, parent, -1, f)
}

// inboxID finds (or creates) the top-level "Inbox" entry.
func (s *Store) inboxID(actor string) (string, error) {
	s.mu.RLock()
	for _, c := range s.db.Nodes["root"].Kids() {
		if n := s.db.Nodes[c]; n.S("name") == "Inbox" && n["trashed"] == nil {
			s.mu.RUnlock()
			return c, nil
		}
	}
	s.mu.RUnlock()
	n, err := s.Create(actor, "Inbox", "root", 0, map[string]any{"ongoing": true})
	if err != nil {
		return "", err
	}
	return str(n["id"]), nil
}

func (s *Store) Patch(actor, id string, f map[string]any) (map[string]any, error) {
	var out map[string]any
	_, err := s.Mutate(actor, "", func(tx *Tx) error {
		n, err := tx.patch(id, f)
		if err == nil {
			out = tx.db.fullView(n, tx.now())
		}
		return err
	})
	return out, err
}

func (s *Store) SetStatus(actor, id, status string) (map[string]any, error) {
	var out map[string]any
	_, err := s.Mutate(actor, "", func(tx *Tx) error {
		n, err := tx.get(id)
		if err != nil {
			return err
		}
		if tx.db.inTrash(n) {
			return errors.New("entry is in the trash; restore it first")
		}
		if err := tx.setStatus(n, status); err != nil {
			return err
		}
		out = tx.db.fullView(n, tx.now())
		return nil
	})
	return out, err
}

func (s *Store) Move(actor, id, parent string, index int) (map[string]any, error) {
	var out map[string]any
	_, err := s.Mutate(actor, "", func(tx *Tx) error {
		n, err := tx.move(id, parent, index)
		if err == nil {
			out = tx.db.fullView(n, tx.now())
		}
		return err
	})
	return out, err
}

func (s *Store) Trash(actor, id string) error {
	_, err := s.Mutate(actor, "", func(tx *Tx) error { return tx.trash(id) })
	return err
}

func (s *Store) Restore(actor, id string) (map[string]any, error) {
	var out map[string]any
	_, err := s.Mutate(actor, "", func(tx *Tx) error {
		n, err := tx.restore(id)
		if err == nil {
			out = tx.db.fullView(n, tx.now())
		}
		return err
	})
	return out, err
}

func (s *Store) LogTime(actor, id string, secs float64) (map[string]any, error) {
	var out map[string]any
	_, err := s.Mutate(actor, "", func(tx *Tx) error {
		n, err := tx.logTime(id, secs)
		if err == nil {
			out = tx.db.fullView(n, tx.now())
		}
		return err
	})
	return out, err
}

// ---------------------------------------------------------------- UI sync

type SyncReq struct {
	Base     int64                      `json:"base"`
	Client   string                     `json:"client"`
	Nodes    map[string]json.RawMessage `json:"nodes"`
	Activity []map[string]any           `json:"activity"`
	TimerSet bool                       `json:"timerSet"`
	Timer    any                        `json:"timer"`
	PrefsSet bool                       `json:"prefsSet"`
	Prefs    map[string]any             `json:"prefs"`
}

// Sync applies the changes of one UI at once. Only the entries the UI actually
// changed are sent; entries changed by somebody else since the UI's base
// revision cause a conflict (nothing is applied) and the UI refreshes.
func (s *Store) Sync(r SyncReq) (int64, error) {
	return s.Mutate("ui", r.Client, func(tx *Tx) error {
		if r.Base < s.floorRev {
			return &conflictError{ids: []string{"*"}}
		}
		var conflicts []string
		for id := range r.Nodes {
			if s.nodeRev[id] > r.Base {
				conflicts = append(conflicts, id)
			}
		}
		if len(conflicts) > 0 {
			sort.Strings(conflicts)
			return &conflictError{ids: conflicts}
		}
		for id, raw := range r.Nodes {
			if id == "root" && string(raw) == "null" {
				return errors.New("the root cannot be deleted")
			}
			if string(raw) == "null" {
				delete(tx.db.Nodes, id)
				tx.touch(id)
				continue
			}
			var n Node
			if err := json.Unmarshal(raw, &n); err != nil || n == nil {
				return fmt.Errorf("entry %s is not valid", id)
			}
			if n.S("id") != id {
				return fmt.Errorf("entry %s has a mismatching id", id)
			}
			if id != "root" {
				fillDefaults(n, id)
				if _, ok := kindStatuses[n.S("kind")]; !ok {
					return fmt.Errorf("entry %s has an unknown kind", id)
				}
			}
			tx.db.Nodes[id] = n
			tx.touch(id)
		}
		for _, a := range r.Activity {
			if _, ok := a["t"].(float64); !ok || str(a["type"]) == "" {
				continue
			}
			if str(a["by"]) == "" {
				a["by"] = "ui"
			}
			tx.acts = append(tx.acts, a)
		}
		if r.TimerSet {
			tx.db.Timer = r.Timer
		}
		if r.PrefsSet && r.Prefs != nil {
			tx.db.Prefs = r.Prefs
		}
		return nil
	})
}

// State returns everything the UI needs at start-up (and after a refresh).
func (s *Store) State() map[string]any {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return map[string]any{
		"rev": s.db.Rev, "schema": s.db.Schema, "nodes": s.db.Nodes, "timer": s.db.Timer,
		"prefs": s.db.Prefs, "activity": s.activity, "recovered": s.Recovered,
	}
}

// ---------------------------------------------------------------- queries

func (db *DB) fullView(n Node, now time.Time) map[string]any {
	v := deepCopy(map[string]any(n)).(map[string]any)
	for k, x := range db.view(n, now) {
		if _, has := v[k]; !has || k == "path" || k == "progress" || k == "blocked" {
			v[k] = x
		}
	}
	v["childIds"] = v["children"]
	v["children"] = len(n.Kids())
	return v
}

func (s *Store) Get(id string) (map[string]any, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	n, ok := s.db.Nodes[id]
	if !ok || id == "root" {
		return nil, fmt.Errorf("entry %q not found", id)
	}
	return s.db.fullView(n, s.Now()), nil
}

type Filter struct {
	Q, Tag, Person, Parent, Project string
	Statuses, Kinds                 []string
	Open, Overdue, Trashed          bool
	Limit                           int
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if strings.EqualFold(x, v) {
			return true
		}
	}
	return false
}

func (s *Store) List(f Filter) []map[string]any {
	s.mu.RLock()
	defer s.mu.RUnlock()
	now := s.Now()
	if f.Limit <= 0 {
		f.Limit = 100
	}
	f.Limit = min(f.Limit, 1000)
	words := strings.Fields(strings.ToLower(f.Q))
	var ids []string
	if f.Trashed {
		for id, n := range s.db.Nodes {
			if n["trashed"] != nil {
				ids = append(ids, id)
			}
		}
		sort.Strings(ids)
	} else {
		ids = s.db.walkOrder()
	}
	out := []map[string]any{}
	for _, id := range ids {
		n := s.db.Nodes[id]
		if f.Parent != "" && (id == f.Parent || !s.db.inSubtree(id, f.Parent)) {
			continue
		}
		if len(f.Statuses) > 0 && !contains(f.Statuses, n.S("status")) {
			continue
		}
		if len(f.Kinds) > 0 && !contains(f.Kinds, n.S("kind")) {
			continue
		}
		if f.Open && (s.db.isClosed(n) || n.S("status") == "later" || n.S("kind") == "reminder") {
			continue
		}
		if f.Overdue && s.db.dueState(n, now) != "over" {
			continue
		}
		if f.Tag != "" && !contains(strs(n["tags"]), strings.TrimPrefix(f.Tag, "#")) {
			continue
		}
		if f.Person != "" && !contains(strs(n["people"]), strings.TrimPrefix(f.Person, "@")) {
			continue
		}
		if f.Project != "" {
			found := false
			for x, ok := s.db.Nodes[n.S("parent")]; ok && x.S("id") != "root"; x, ok = s.db.Nodes[x.S("parent")] {
				if x.S("kind") == "project" && (x.S("id") == f.Project || strings.EqualFold(x.S("name"), f.Project)) {
					found = true
					break
				}
			}
			if !found {
				continue
			}
		}
		if len(words) > 0 {
			hay := strings.ToLower(n.S("name") + " " + n.S("notes") + " " + n.S("location") + " " + strings.Join(strs(n["tags"]), " ") + " " + strings.Join(strs(n["people"]), " "))
			ok := true
			for _, w := range words {
				if !strings.Contains(hay, w) {
					ok = false
					break
				}
			}
			if !ok {
				continue
			}
		}
		out = append(out, s.db.view(n, now))
		if len(out) >= f.Limit {
			break
		}
	}
	return out
}

func (s *Store) Tree(root string, depth int) (map[string]any, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if root == "" {
		root = "root"
	}
	if _, ok := s.db.Nodes[root]; !ok {
		return nil, fmt.Errorf("entry %q not found", root)
	}
	if depth == 0 {
		depth = 3
	}
	return s.db.tree(root, depth, s.Now()), nil
}

// Today summarises what needs attention now.
func (s *Store) Today() map[string]any {
	s.mu.RLock()
	defer s.mu.RUnlock()
	now := s.Now()
	res := map[string][]map[string]any{"overdue": {}, "dueToday": {}, "inProgress": {}, "upcomingReminders": {}, "nextUp": {}}
	weekEnd := startOfDay(now).AddDate(0, 0, 7)
	for _, id := range s.db.walkOrder() {
		n := s.db.Nodes[id]
		switch n.S("kind") {
		case "reminder":
			// an event shows on every day it runs: it starts before the week ends and has not ended yet
			d, ok := parseWhen(n.S("reminder"), s.loc)
			last := d
			if e, ok := endAt(n.S("end"), s.loc); ok {
				last = e.Add(-time.Minute)
			}
			if ok && !last.Before(startOfDay(now)) && d.Before(weekEnd) {
				res["upcomingReminders"] = append(res["upcomingReminders"], s.db.view(n, now))
			}
			continue
		case "todo":
		default:
			continue
		}
		if s.db.isClosed(n) || n.S("status") == "later" || s.db.waits(n, now) {
			continue
		}
		switch s.db.dueState(n, now) {
		case "over":
			res["overdue"] = append(res["overdue"], s.db.view(n, now))
			continue
		case "today":
			res["dueToday"] = append(res["dueToday"], s.db.view(n, now))
			continue
		}
		if n.S("status") == "progress" {
			res["inProgress"] = append(res["inProgress"], s.db.view(n, now))
		} else if n.N("prio") >= 3 && !s.db.blocked(n) && len(n.Kids()) == 0 {
			res["nextUp"] = append(res["nextUp"], s.db.view(n, now))
		}
	}
	out := map[string]any{"date": now.Format("2006-01-02")}
	for k, v := range res {
		out[k] = v
	}
	return out
}

// Stats: key figures plus per-day counts for the last `days` days.
func (s *Store) Stats(days int) map[string]any {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if days <= 0 || days > 730 {
		days = 30
	}
	now := s.Now()
	today := startOfDay(now)
	weekStart := today.AddDate(0, 0, -((int(today.Weekday()) + 6) % 7))
	type day struct {
		closed, created, activity int
		timeSecs                  float64
	}
	byDay := map[string]*day{}
	closedDays := map[string]bool{}
	var closedWeek int
	var timeWeek float64
	for _, a := range s.activity {
		t := time.UnixMilli(int64(num(a["t"]))).In(s.loc)
		k := t.Format("2006-01-02")
		d := byDay[k]
		if d == nil {
			d = &day{}
			byDay[k] = d
		}
		typ := str(a["type"])
		switch typ {
		case "close":
			d.closed++
			closedDays[k] = true
			if !t.Before(weekStart) {
				closedWeek++
			}
		case "create":
			d.created++
		case "time":
			d.timeSecs += num(a["secs"])
			if !t.Before(weekStart) {
				timeWeek += num(a["secs"])
			}
		}
		if typ != "time" {
			d.activity++
		}
	}
	streak := 0
	for d := today; ; d = d.AddDate(0, 0, -1) {
		if closedDays[d.Format("2006-01-02")] {
			streak++
		} else if d.Equal(today) {
			continue
		} else {
			break
		}
		if streak > 3650 {
			break
		}
	}
	open, overdue, later := 0, 0, 0
	for _, id := range s.db.walkOrder() {
		n := s.db.Nodes[id]
		if s.db.dueState(n, now) == "over" {
			overdue++
		}
		if n.S("kind") == "project" || n.S("kind") == "reminder" || s.db.isClosed(n) {
			continue
		}
		if n.S("status") == "later" {
			later++
		} else {
			open++
		}
	}
	rows := make([]map[string]any, 0, days)
	for i := days - 1; i >= 0; i-- {
		k := today.AddDate(0, 0, -i).Format("2006-01-02")
		d := byDay[k]
		if d == nil {
			d = &day{}
		}
		rows = append(rows, map[string]any{"date": k, "closed": d.closed, "created": d.created, "activity": d.activity, "timeMinutes": int(d.timeSecs/60 + 0.5)})
	}
	return map[string]any{
		"open": open, "later": later, "overdue": overdue, "closedThisWeek": closedWeek,
		"trackedThisWeekMinutes": int(timeWeek/60 + 0.5), "closingStreakDays": streak, "days": rows,
	}
}
