package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Node is one entry (todo, idea, project or reminder). It is a generic map on
// purpose: fields the server does not know about (added by newer UIs) survive a
// round trip unchanged.
type Node map[string]any

func num(v any) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case int:
		return float64(x)
	case int64:
		return float64(x)
	case json.Number:
		f, _ := x.Float64()
		return f
	}
	return 0
}
func str(v any) string { s, _ := v.(string); return s }
func strs(v any) []string {
	switch x := v.(type) {
	case []string:
		return append([]string(nil), x...)
	case []any:
		out := make([]string, 0, len(x))
		for _, e := range x {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}
func toAny(ss []string) []any {
	out := make([]any, 0, len(ss))
	for _, s := range ss {
		out = append(out, s)
	}
	return out
}
func nowMs() float64 { return float64(time.Now().UnixMilli()) }

func (n Node) S(k string) string           { return str(n[k]) }
func (n Node) N(k string) float64          { return num(n[k]) }
func (n Node) Bool(k string) bool          { b, _ := n[k].(bool); return b }
func (n Node) Kids() []string              { return strs(n["children"]) }
func (n Node) Obj(k string) map[string]any { m, _ := n[k].(map[string]any); return m }

// DB is the persisted document (data/database.json).
type DB struct {
	Schema  int             `json:"schema"`
	App     string          `json:"app"`
	Rev     int64           `json:"rev"`
	SavedAt string          `json:"savedAt"`
	Nodes   map[string]Node `json:"nodes"`
	Timer   any             `json:"timer"`
	Prefs   map[string]any  `json:"prefs"`
}

var kindStatuses = map[string][]string{
	"todo":     {"open", "progress", "waiting", "later", "done", "cancelled"},
	"idea":     {"raw", "exploring", "parked", "rejected"},
	"project":  {"planning", "active", "paused", "done", "archived"},
	"reminder": {"set"},
}
var closedStatus = map[string]bool{"done": true, "cancelled": true, "rejected": true, "archived": true}
var reopenStatus = map[string]string{"todo": "open", "idea": "raw", "project": "active", "reminder": "set"}

func validStatus(kind, s string) bool {
	for _, x := range kindStatuses[kind] {
		if x == s {
			return true
		}
	}
	return false
}

func defaultNode(id, name string) Node {
	t := nowMs()
	return Node{
		"id": id, "name": name, "kind": "todo", "status": "open", "color": "", "icon": "", "prio": 0.0,
		"tags": []any{}, "people": []any{}, "location": "", "deadline": "", "reminder": "", "end": "",
		"deps": []any{}, "notes": "", "notesFormat": "markdown", "links": []any{},
		"prog":  map[string]any{"mode": "percent", "done": 0.0, "total": 100.0, "unit": ""},
		"recur": "", "est": 0.0, "time": []any{}, "url": "", "webhook": "", "collapsed": false,
		"created": t, "updated": t, "parent": nil, "children": []any{},
		"hist": []any{map[string]any{"s": "open", "at": t}},
	}
}

const idAlphabet = "abcdefghijklmnopqrstuvwxyz0123456789"

func newID(db *DB) string {
	for {
		b := make([]byte, 6)
		for i := range b {
			b[i] = idAlphabet[rand.IntN(len(idAlphabet))]
		}
		id := string(b)
		if _, taken := db.Nodes[id]; !taken && id != "root" {
			return id
		}
	}
}

func deepCopy(v any) any {
	switch x := v.(type) {
	case map[string]any:
		m := make(map[string]any, len(x))
		for k, e := range x {
			m[k] = deepCopy(e)
		}
		return m
	case Node:
		return Node(deepCopy(map[string]any(x)).(map[string]any))
	case []any:
		s := make([]any, len(x))
		for i, e := range x {
			s[i] = deepCopy(e)
		}
		return s
	case []string:
		return append([]string(nil), x...)
	}
	return v
}
func cloneNode(n Node) Node { return deepCopy(n).(Node) }

// ---------------------------------------------------------------- tree helpers

func (db *DB) detach(n Node) {
	p, ok := db.Nodes[n.S("parent")]
	if !ok {
		return
	}
	id := n.S("id")
	kids := p.Kids()
	for i, k := range kids {
		if k == id {
			kids = append(kids[:i], kids[i+1:]...)
			break
		}
	}
	p["children"] = toAny(kids)
}

func (db *DB) attach(n Node, pid string, idx int) {
	p := db.Nodes[pid]
	kids := p.Kids()
	if idx < 0 || idx > len(kids) {
		idx = len(kids)
	}
	kids = append(kids, "")
	copy(kids[idx+1:], kids[idx:])
	kids[idx] = n.S("id")
	p["children"] = toAny(kids)
	n["parent"] = pid
}

// inSubtree reports whether id is anc itself or lies below it.
func (db *DB) inSubtree(id, anc string) bool {
	for i := 0; i < 10000; i++ {
		n, ok := db.Nodes[id]
		if !ok {
			return false
		}
		if id == anc {
			return true
		}
		id = n.S("parent")
		if id == "" {
			return false
		}
	}
	return false
}

func (db *DB) path(n Node) string {
	var parts []string
	for x, ok := db.Nodes[n.S("parent")]; ok && x.S("id") != "root"; x, ok = db.Nodes[x.S("parent")] {
		parts = append([]string{x.S("name")}, parts...)
		if len(parts) > 100 {
			break
		}
	}
	return strings.Join(parts, " › ")
}

func (db *DB) isClosed(n Node) bool { return closedStatus[n.S("status")] }

func (db *DB) blocked(n Node) bool {
	for _, d := range strs(n["deps"]) {
		if x, ok := db.Nodes[d]; ok && x["trashed"] == nil && x.S("status") != "done" && x.S("status") != "cancelled" {
			return true
		}
	}
	return false
}

func (db *DB) dependsOn(x Node, id string, seen map[string]bool) bool {
	if seen == nil {
		seen = map[string]bool{}
	}
	xid := x.S("id")
	if seen[xid] {
		return false
	}
	seen[xid] = true
	for _, d := range strs(x["deps"]) {
		if d == id {
			return true
		}
		if y, ok := db.Nodes[d]; ok && db.dependsOn(y, id, seen) {
			return true
		}
	}
	return false
}

func (db *DB) progressKids(n Node) []Node {
	var out []Node
	for _, c := range n.Kids() {
		x, ok := db.Nodes[c]
		if !ok || x["trashed"] != nil || x.S("kind") == "reminder" {
			continue
		}
		switch x.S("status") {
		case "cancelled", "rejected", "archived":
			continue
		}
		out = append(out, x)
	}
	return out
}

func (db *DB) frac(n Node) float64 {
	if k := db.progressKids(n); len(k) > 0 {
		sum := 0.0
		for _, c := range k {
			sum += db.frac(c)
		}
		return sum / float64(len(k))
	}
	if n.S("status") == "done" {
		return 1
	}
	p := n.Obj("prog")
	total := num(p["total"])
	if total <= 0 {
		total = 1
	}
	return min(1, num(p["done"])/total)
}

// validateDB checks structural integrity. It runs after every change; a change
// that would corrupt the tree is rejected and rolled back.
func validateDB(db *DB) error {
	if db.Nodes == nil {
		return errors.New("no nodes")
	}
	if _, ok := db.Nodes["root"]; !ok {
		return errors.New("root node missing")
	}
	seen := map[string]bool{}
	var walk func(id string, depth int) error
	walk = func(id string, depth int) error {
		if depth > 500 {
			return errors.New("tree too deep")
		}
		n, ok := db.Nodes[id]
		if !ok {
			return fmt.Errorf("missing node %s", id)
		}
		if seen[id] {
			return fmt.Errorf("node %s is referenced twice (cycle or duplicate)", id)
		}
		seen[id] = true
		for _, c := range n.Kids() {
			cn, ok := db.Nodes[c]
			if !ok {
				return fmt.Errorf("node %s lists missing child %s", id, c)
			}
			if cn.S("parent") != id {
				return fmt.Errorf("child %s has parent %q, expected %s", c, cn.S("parent"), id)
			}
			if err := walk(c, depth+1); err != nil {
				return err
			}
		}
		return nil
	}
	if err := walk("root", 0); err != nil {
		return err
	}
	for id, n := range db.Nodes {
		if id != n.S("id") {
			return fmt.Errorf("node key %s does not match its id %q", id, n.S("id"))
		}
		if n["trashed"] != nil && !seen[id] {
			if err := walk(id, 0); err != nil {
				return err
			}
		}
	}
	for id := range db.Nodes {
		if !seen[id] {
			return fmt.Errorf("node %s is not reachable from the tree", id)
		}
	}
	return nil
}

// ---------------------------------------------------------------- dates

var reWhen = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}(T\d{2}:\d{2})?$`)

func validWhen(s string, loc *time.Location) bool {
	if s == "" {
		return true
	}
	if !reWhen.MatchString(s) {
		return false
	}
	_, ok := parseWhen(s, loc)
	return ok
}

func parseWhen(s string, loc *time.Location) (time.Time, bool) {
	layout := "2006-01-02"
	if strings.Contains(s, "T") {
		layout = "2006-01-02T15:04"
	}
	t, err := time.ParseInLocation(layout, s, loc)
	return t, err == nil
}

func startOfDay(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, t.Location())
}

func defaultRemTime(now time.Time) string {
	return now.Add(time.Hour).Truncate(time.Hour).Format("2006-01-02T15:04")
}

// dueState: "" (no deadline / closed), "over", "today" or "soon".
func (db *DB) dueState(n Node, now time.Time) string {
	dl := n.S("deadline")
	if dl == "" || db.isClosed(n) {
		return ""
	}
	d, ok := parseWhen(dl, now.Location())
	if !ok {
		return ""
	}
	if strings.Contains(dl, "T") {
		if d.Before(now) {
			return "over"
		}
	} else if d.Before(startOfDay(now)) {
		return "over"
	}
	if d.Format("2006-01-02") == now.Format("2006-01-02") {
		return "today"
	}
	return "soon"
}

// ---------------------------------------------------------------- views

func round2(f float64) float64 { return float64(int(f*100+0.5)) / 100 }

// view is the compact representation used by list endpoints, the CLI and MCP.
func (db *DB) view(n Node, now time.Time) map[string]any {
	v := map[string]any{
		"id": n.S("id"), "name": n.S("name"), "kind": n.S("kind"), "status": n.S("status"),
		"prio": n["prio"], "parent": n["parent"], "path": db.path(n),
		"progress": round2(db.frac(n)), "blocked": db.blocked(n), "children": len(n.Kids()),
	}
	for _, k := range []string{"deadline", "reminder", "end", "url", "location", "color", "icon", "recur"} {
		if s := n.S(k); s != "" {
			v[k] = s
		}
	}
	if t := strs(n["tags"]); len(t) > 0 {
		v["tags"] = t
	}
	if p := strs(n["people"]); len(p) > 0 {
		v["people"] = p
	}
	if ds := db.dueState(n, now); ds == "over" || ds == "today" {
		v["due"] = ds
	}
	if n["trashed"] != nil {
		v["trashed"] = true
	}
	return v
}

func (db *DB) tree(id string, depth int, now time.Time) map[string]any {
	n := db.Nodes[id]
	v := map[string]any{"id": id, "name": n.S("name"), "kind": n.S("kind"), "status": n.S("status"), "progress": round2(db.frac(n))}
	if id == "root" {
		v["name"] = "(top level)"
	}
	if d := n.S("deadline"); d != "" {
		v["deadline"] = d
	}
	if depth != 0 {
		var kids []any
		for _, c := range n.Kids() {
			if x, ok := db.Nodes[c]; ok && x["trashed"] == nil {
				kids = append(kids, db.tree(c, depth-1, now))
			}
		}
		if kids != nil {
			v["items"] = kids
		}
	} else if len(n.Kids()) > 0 {
		v["children"] = len(n.Kids())
	}
	return v
}

// walkOrder returns all live node ids in tree (depth-first) order.
func (db *DB) walkOrder() []string {
	var out []string
	var walk func(id string)
	walk = func(id string) {
		for _, c := range db.Nodes[id].Kids() {
			if x, ok := db.Nodes[c]; ok && x["trashed"] == nil {
				out = append(out, c)
				walk(c)
			}
		}
	}
	walk("root")
	return out
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
