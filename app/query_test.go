package main

import (
	"fmt"
	"strings"
	"testing"
)

// the outline shows all levels unless a depth limits it
func TestTreeAllLevels(t *testing.T) {
	a := testServer(t)
	parent, last := "", ""
	for i := 1; i <= 6; i++ { // six levels deep: a > b > c > d > e > f
		f := map[string]any{"name": fmt.Sprintf("level %d", i)}
		if parent != "" {
			f["parent"] = parent
		}
		last = a.call(201, "POST", "/api/nodes", f)["id"].(string)
		parent = last
	}
	var find func(m map[string]any, id string) bool
	find = func(m map[string]any, id string) bool {
		if m["id"] == id {
			return true
		}
		for _, k := range anyList(m["items"]) {
			if find(k.(map[string]any), id) {
				return true
			}
		}
		return false
	}
	for _, d := range []int{0, -1} { // not given, 0 and -1 all mean all levels
		tr, err := a.s.st.Tree("", d)
		if err != nil || !find(tr, last) {
			t.Errorf("depth %d: the deepest entry is missing (%v)", d, err)
		}
	}
	if !find(a.call(200, "GET", "/api/tree", nil), last) {
		t.Error("GET /api/tree without depth must show all levels")
	}
	if tr, _ := a.s.st.Tree("", 2); find(tr, last) {
		t.Error("depth 2 must cut the tree")
	}
}

// a search word that is an entry id finds that entry (a part of an id does not)
func TestSearchFindsIDs(t *testing.T) {
	a := testServer(t)
	id := a.call(201, "POST", "/api/nodes", map[string]any{"name": "Write the report"})["id"].(string)
	a.call(201, "POST", "/api/nodes", map[string]any{"name": "Other thing"})
	count := func(q string) int { return len(a.s.st.List(Filter{Q: q})) }
	if count(id) != 1 || count("REPORT "+id) != 1 || count("other "+id) != 0 {
		t.Errorf("search by id: %d, with a word %d, wrong word %d", count(id), count("REPORT "+id), count("other "+id))
	}
	if part := id[:3]; !strings.Contains("write the report other thing", part) && count(part) != 0 { // a part of an id finds nothing
		t.Errorf("a part of an id found %d entries", count(part))
	}
}
