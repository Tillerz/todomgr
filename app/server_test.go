package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

// apiT drives the real HTTP handler (security guard, routes, MCP) in memory:
// no port is opened and nothing leaves the process.
type apiT struct {
	t     *testing.T
	h     http.Handler
	token string
	s     *Server
}

func testServer(t *testing.T) *apiT {
	t.Helper()
	dir := t.TempDir()
	cfg, err := LoadConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	st, err := OpenStore(dir, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	s, err := NewServer(st, cfg, 7437, "")
	if err != nil {
		t.Fatal(err)
	}
	old := discordSend
	discordSend = func(hook, content string) error {
		t.Errorf("unexpected Discord call: %s", content)
		return errors.New("no Discord in tests")
	}
	t.Cleanup(func() { discordSend = old })
	return &apiT{t: t, h: s.Handler(), token: cfg.Get().Token, s: s}
}

// do sends a request with the token. body is JSON-encoded unless it is a string.
// hdr are extra header pairs; they replace the defaults (Host, Authorization, Content-Type).
func (a *apiT) do(method, path string, body any, hdr ...string) *httptest.ResponseRecorder {
	a.t.Helper()
	var raw string
	switch b := body.(type) {
	case nil:
	case string:
		raw = b
	default:
		j, _ := json.Marshal(b)
		raw = string(j)
	}
	r := httptest.NewRequest(method, path, strings.NewReader(raw))
	r.Host = "127.0.0.1:7437"
	r.Header.Set("Authorization", "Bearer "+a.token)
	if raw != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	r.RemoteAddr = "127.0.0.1:50000" // this computer; without LAN access nothing else can connect
	for i := 0; i+1 < len(hdr); i += 2 {
		if hdr[i] == "Host" {
			r.Host = hdr[i+1]
		} else if hdr[i] == "RemoteAddr" { // another device, for the LAN access tests
			r.RemoteAddr = hdr[i+1]
		} else {
			r.Header.Set(hdr[i], hdr[i+1])
		}
	}
	w := httptest.NewRecorder()
	a.h.ServeHTTP(w, r)
	return w
}

// call is do + status check + JSON object result.
func (a *apiT) call(want int, method, path string, body any, hdr ...string) map[string]any {
	a.t.Helper()
	w := a.do(method, path, body, hdr...)
	if w.Code != want {
		a.t.Fatalf("%s %s: status %d, want %d: %s", method, path, w.Code, want, strings.TrimSpace(w.Body.String()))
	}
	var m map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &m)
	return m
}

func TestAPIAuthAndGuard(t *testing.T) {
	a := testServer(t)
	cases := []struct {
		name string
		hdr  []string
		want int
	}{
		{"no token", []string{"Authorization", ""}, 401},
		{"wrong token", []string{"Authorization", "Bearer nope"}, 401},
		{"bearer token", nil, 200},
		{"X-Todo-Token", []string{"Authorization", "", "X-Todo-Token", a.token}, 200},
		{"cookie", []string{"Authorization", "", "Cookie", cookieName + "=" + a.token}, 200},
		{"foreign host", []string{"Host", "evil.example:7437"}, 403},
		{"other port", []string{"Host", "localhost:9999"}, 403},
		{"localhost host", []string{"Host", "localhost:7437"}, 200},
		{"foreign origin", []string{"Origin", "http://evil.example"}, 403},
		{"own origin", []string{"Origin", "http://localhost:7437"}, 200},
	}
	for _, c := range cases {
		if w := a.do("GET", "/api/info", nil, c.hdr...); w.Code != c.want {
			t.Errorf("%s: status %d, want %d", c.name, w.Code, c.want)
		}
	}
	if w := a.do("POST", "/api/quick", "text=x", "Content-Type", "text/plain"); w.Code != 415 {
		t.Errorf("non-JSON POST: status %d, want 415", w.Code)
	}
	w := a.do("GET", "/", nil, "Authorization", "")
	if w.Code != 200 || !strings.Contains(w.Header().Get("Set-Cookie"), cookieName+"="+a.token) {
		t.Errorf("index page: status %d, cookie %q", w.Code, w.Header().Get("Set-Cookie"))
	}
	if w := a.do("GET", "/sortable.min.js", nil); w.Code != 200 {
		t.Errorf("embedded static file: status %d", w.Code)
	}
}

// Every licence file that Settings → About links to must be in the embedded web/licenses/.
func TestLicenseFiles(t *testing.T) {
	a := testServer(t)
	page := a.do("GET", "/", nil).Body.String()
	files := regexp.MustCompile(`\['[^']+','[^']+','([a-z]+\.txt)'`).FindAllStringSubmatch(page, -1)
	if len(files) == 0 {
		t.Fatal("no licence list (LIBS) found in index.html")
	}
	for _, f := range files {
		if w := a.do("GET", "/licenses/"+f[1], nil); w.Code != 200 || w.Body.Len() < 100 {
			t.Errorf("/licenses/%s: status %d, %d bytes", f[1], w.Code, w.Body.Len())
		}
	}
}

func TestAPIEntryLifecycle(t *testing.T) {
	a := testServer(t)
	p := a.call(201, "POST", "/api/nodes", map[string]any{"name": "Book", "kind": "project"})
	pid := p["id"].(string)
	n := a.call(201, "POST", "/api/nodes", map[string]any{"name": "Chapter 1", "parent": pid, "tags": []string{"#writing"}})
	id := n["id"].(string)

	if g := a.call(200, "GET", "/api/nodes/"+id, nil); g["name"] != "Chapter 1" {
		t.Errorf("get: name %v", g["name"])
	}
	if g := a.call(200, "PATCH", "/api/nodes/"+id, map[string]any{"name": "Chapter One"}); g["name"] != "Chapter One" {
		t.Errorf("patch: name %v", g["name"])
	}
	if g := a.call(200, "POST", "/api/nodes/"+id+"/status", map[string]any{"status": "progress"}); g["status"] != "progress" {
		t.Errorf("status: %v", g["status"])
	}
	a.call(400, "POST", "/api/nodes/"+id+"/status", map[string]any{"status": "bogus"})
	a.call(200, "POST", "/api/nodes/"+id+"/time", map[string]any{"minutes": 5})
	if l := a.call(200, "GET", "/api/nodes?q=chapter", nil); l["count"] != 1.0 {
		t.Errorf("list q=chapter: count %v", l["count"])
	}
	if l := a.call(200, "GET", "/api/nodes?tag=writing", nil); l["count"] != 1.0 {
		t.Errorf("list tag=writing: count %v", l["count"])
	}
	a.call(200, "POST", "/api/nodes/"+id+"/move", map[string]any{"parent": "root"})
	if l := a.call(200, "GET", "/api/nodes?parent="+pid, nil); l["count"] != 0.0 {
		t.Errorf("after move the project must be empty, count %v", l["count"])
	}

	a.call(200, "DELETE", "/api/nodes/"+id, nil)
	if l := a.call(200, "GET", "/api/nodes?q=chapter", nil); l["count"] != 0.0 {
		t.Errorf("trashed entry still listed")
	}
	if l := a.call(200, "GET", "/api/nodes?q=chapter&trashed=1", nil); l["count"] != 1.0 {
		t.Errorf("trashed entry not in trash list")
	}
	a.call(200, "POST", "/api/nodes/"+id+"/restore", nil)
	if l := a.call(200, "GET", "/api/nodes?q=chapter", nil); l["count"] != 1.0 {
		t.Errorf("restored entry not listed")
	}

	q := a.call(201, "POST", "/api/quick", map[string]any{"text": "Buy milk #shop"})
	if !strings.HasPrefix(fmt.Sprint(q["name"]), "Buy milk") {
		t.Errorf("quick add: name %v", q["name"])
	}
	a.call(404, "GET", "/api/nodes/does-not-exist", nil)
	a.call(400, "POST", "/api/nodes", "{not json")
	for _, path := range []string{"/api/tree", "/api/today", "/api/stats", "/api/state", "/api/info"} {
		a.call(200, "GET", path, nil)
	}
}

func TestAPISyncConflict(t *testing.T) {
	a := testServer(t)
	id := a.call(201, "POST", "/api/nodes", map[string]any{"name": "Shared"})["id"].(string)
	state := a.call(200, "GET", "/api/state", nil)
	base := state["rev"]
	node := state["nodes"].(map[string]any)[id].(map[string]any)
	a.call(200, "PATCH", "/api/nodes/"+id, map[string]any{"notes": "changed by someone else"})
	node["name"] = "stale write"
	a.call(409, "POST", "/api/sync", map[string]any{"base": base, "nodes": map[string]any{id: node}})
	if g := a.call(200, "GET", "/api/nodes/"+id, nil); g["name"] != "Shared" {
		t.Errorf("conflicting sync was applied: name %v", g["name"])
	}
}

func TestAPISettingsBackupsExportImport(t *testing.T) {
	a := testServer(t)
	a.call(201, "POST", "/api/nodes", map[string]any{"name": "Keep me", "deadline": "2026-12-24"})

	a.call(400, "PUT", "/api/settings", map[string]any{"backupIntervalMin": 0})
	if s := a.call(200, "PUT", "/api/settings", map[string]any{"backupIntervalMin": 30}); s["backupIntervalMin"] != 30.0 {
		t.Errorf("settings: backupIntervalMin %v", s["backupIntervalMin"])
	}

	name := a.call(200, "POST", "/api/backups", nil)["name"].(string)
	if l := a.call(200, "GET", "/api/backups", nil); len(l["backups"].([]any)) == 0 {
		t.Error("backup list is empty after a backup")
	}
	a.call(200, "POST", "/api/backups/restore", map[string]any{"name": name})
	a.call(400, "POST", "/api/backups/restore", map[string]any{"name": "../config.json"})

	w := a.do("GET", "/api/export", nil)
	if w.Code != 200 || !json.Valid(w.Body.Bytes()) {
		t.Fatalf("JSON export: status %d", w.Code)
	}
	export := w.Body.String()
	if w := a.do("GET", "/api/export.csv", nil); w.Code != 200 || !strings.Contains(w.Body.String(), "Keep me") {
		t.Errorf("CSV export: status %d", w.Code)
	}
	if w := a.do("GET", "/api/export.ics", nil); w.Code != 200 || !strings.Contains(w.Body.String(), "BEGIN:VCALENDAR") {
		t.Errorf("ICS export: status %d", w.Code)
	}

	a.call(200, "POST", "/api/reset", nil)
	if l := a.call(200, "GET", "/api/nodes?q=keep", nil); l["count"] != 0.0 {
		t.Error("reset kept the entry")
	}
	a.call(200, "POST", "/api/import", export)
	if l := a.call(200, "GET", "/api/nodes?q=keep", nil); l["count"] != 1.0 {
		t.Error("import did not bring the entry back")
	}
	a.call(400, "POST", "/api/import", `{"nodes": "broken"}`)
}

// rpc sends one JSON-RPC request to /mcp and returns the decoded answer.
func (a *apiT) rpc(method string, params any) map[string]any {
	a.t.Helper()
	return a.call(200, "POST", "/mcp", map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
}

// toolText calls an MCP tool and returns its text answer and the isError flag.
func (a *apiT) toolText(name string, args map[string]any) (string, bool) {
	a.t.Helper()
	res, ok := a.rpc("tools/call", map[string]any{"name": name, "arguments": args})["result"].(map[string]any)
	if !ok {
		a.t.Fatalf("tools/call %s: no result", name)
	}
	text := res["content"].([]any)[0].(map[string]any)["text"].(string)
	isErr, _ := res["isError"].(bool)
	return text, isErr
}

func toolNames(m map[string]any) []string {
	var out []string
	for _, t := range m["result"].(map[string]any)["tools"].([]any) {
		out = append(out, t.(map[string]any)["name"].(string))
	}
	return out
}

func TestMCP(t *testing.T) {
	a := testServer(t)
	a.call(503, "POST", "/mcp", map[string]any{"jsonrpc": "2.0", "id": 1, "method": "ping"})
	a.call(200, "PUT", "/api/settings", map[string]any{"mcp": map[string]any{"enabled": true}})

	a.call(401, "POST", "/mcp", map[string]any{"jsonrpc": "2.0", "id": 1, "method": "ping"},
		"Authorization", "", "Cookie", cookieName+"="+a.token)
	a.call(405, "GET", "/mcp", nil)

	init := a.rpc("initialize", map[string]any{"protocolVersion": "2025-03-26"})
	if info := init["result"].(map[string]any)["serverInfo"].(map[string]any); info["name"] != "todomgr" {
		t.Errorf("initialize: serverInfo %v", info)
	}
	if names := strings.Join(toolNames(a.rpc("tools/list", nil)), ","); !strings.Contains(names, "create_entry") {
		t.Errorf("tools/list misses create_entry: %s", names)
	}

	text, isErr := a.toolText("create_entry", map[string]any{"name": "World", "fields": map[string]any{"kind": "project"}})
	if isErr {
		t.Fatalf("create_entry: %s", text)
	}
	var created map[string]any
	_ = json.Unmarshal([]byte(text), &created)
	pid := created["id"].(string)
	a.call(200, "PATCH", "/api/nodes/"+pid, map[string]any{"webhook": "https://discord.com/api/webhooks/1/secret"})
	for _, tool := range []string{"get_entry", "list_entries"} {
		text, _ := a.toolText(tool, map[string]any{"id": pid})
		if strings.Contains(text, "secret") {
			t.Errorf("%s shows the webhook URL", tool)
		}
	}
	if text, isErr := a.toolText("get_entry", map[string]any{"id": "missing"}); !isErr {
		t.Errorf("get_entry of a missing id must be an error: %s", text)
	}

	if e, _ := a.rpc("no/such/method", nil)["error"].(map[string]any); e == nil || e["code"] != -32601.0 {
		t.Errorf("unknown method: %v", e)
	}
	if e, _ := a.rpc("tools/call", map[string]any{"name": "nope"})["error"].(map[string]any); e == nil || e["code"] != -32602.0 {
		t.Errorf("unknown tool: %v", e)
	}
	if w := a.do("POST", "/mcp", `{"jsonrpc":"2.0","method":"notifications/initialized"}`); w.Code != 202 {
		t.Errorf("notification: status %d, want 202", w.Code)
	}
	if w := a.do("POST", "/mcp", `{broken`); !strings.Contains(w.Body.String(), "-32700") {
		t.Errorf("parse error not reported: %s", w.Body.String())
	}
	w := a.do("POST", "/mcp", `[{"jsonrpc":"2.0","id":1,"method":"ping"},{"jsonrpc":"2.0","id":2,"method":"ping"}]`)
	var batch []any
	if json.Unmarshal(w.Body.Bytes(), &batch) != nil || len(batch) != 2 {
		t.Errorf("batch: %s", w.Body.String())
	}

	a.call(200, "PUT", "/api/settings", map[string]any{"mcp": map[string]any{"readOnly": true}})
	if names := strings.Join(toolNames(a.rpc("tools/list", nil)), ","); strings.Contains(names, "create_entry") {
		t.Errorf("read-only tools/list offers write tools: %s", names)
	}
	if _, isErr := a.toolText("create_entry", map[string]any{"name": "Sneaky"}); !isErr {
		t.Error("read-only mode allowed create_entry")
	}
	if _, isErr := a.toolText("list_entries", nil); isErr {
		t.Error("read-only mode blocked list_entries")
	}
}
