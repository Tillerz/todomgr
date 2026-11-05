package main

import (
	"strings"
	"testing"
)

// calendar subscription: off by default, only the key opens it, a new key ends the old link
func TestCalendarFeed(t *testing.T) {
	a := testServer(t)
	mustCreate(t, a.s.st, "Stream", "", map[string]any{"kind": "reminder", "reminder": "2030-12-05T19:00", "end": "2030-12-05T21:00"})
	get := func(path string, hdr ...string) (int, string) {
		w := a.do("GET", path, nil, append([]string{"Authorization", ""}, hdr...)...)
		return w.Code, w.Body.String()
	}
	if c := a.call(200, "GET", "/api/settings", nil)["cal"].(map[string]any); c["enabled"] != false {
		t.Fatalf("the feed must be off by default: %v", c)
	}
	if code, _ := get("/cal/x.ics"); code != 404 {
		t.Errorf("feed while off: status %d", code)
	}
	c := a.call(200, "PUT", "/api/settings", map[string]any{"cal": map[string]any{"enabled": true}})["cal"].(map[string]any)
	l := c["links"].([]any)[0].(map[string]any)
	url, webcal := str(l["url"]), str(l["webcal"])
	if !strings.HasPrefix(url, "http://localhost:7437/cal/") || webcal != "webcal"+strings.TrimPrefix(url, "http") {
		t.Fatalf("links %v", l)
	}
	path := strings.TrimPrefix(url, "http://localhost:7437")
	code, body := get(path)
	if code != 200 || !strings.Contains(body, "SUMMARY:Stream") || !strings.Contains(body, "REFRESH-INTERVAL") {
		t.Fatalf("feed: status %d\n%s", code, body)
	}
	if code, _ := get("/cal/wrong.ics"); code != 404 {
		t.Errorf("wrong key: status %d", code)
	}
	if code, _ := get(path, "Host", "evil.example:7437"); code != 403 {
		t.Errorf("feed with a foreign host: status %d", code)
	}
	c = a.call(200, "PUT", "/api/settings", map[string]any{"cal": map[string]any{"renew": true}})["cal"].(map[string]any)
	if code, _ := get(path); code != 404 {
		t.Error("the old link must stop working after a new key")
	}
	if p2 := strings.TrimPrefix(str(c["links"].([]any)[0].(map[string]any)["url"]), "http://localhost:7437"); p2 == path {
		t.Error("renew must make a new key")
	} else if code, _ := get(p2); code != 200 {
		t.Errorf("new link: status %d", code)
	}
	a.call(200, "PUT", "/api/settings", map[string]any{"cal": map[string]any{"enabled": false}})
	if a.s.cfg.Get().CalKey != "" {
		t.Error("switching off must drop the key")
	}
}

// an event exported by the app is recognised by its UID on import, also after a rename
func TestImportMatchesOwnUIDs(t *testing.T) {
	st, _ := testStore(t)
	id := mustCreate(t, st, "Stream", "", map[string]any{"kind": "reminder", "reminder": "2030-12-05T19:00"})
	ics := st.ExportICS(false)
	if _, err := st.Patch("test", id, map[string]any{"name": "Stream (renamed)", "reminder": "2030-12-06T19:00"}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ImportEntries("test", "ics", "own.ics", ics); err == nil || !strings.Contains(err.Error(), "already there") {
		t.Fatalf("own event after a rename: %v", err)
	}
	if err := st.Trash("test", id); err != nil {
		t.Fatal(err)
	}
	if res, err := st.ImportEntries("test", "ics", "own.ics", ics); err != nil || res["added"] != 1 {
		t.Fatalf("an entry in the trash does not count: %v %v", res, err)
	}
}
