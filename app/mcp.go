package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// MCP (Model Context Protocol) over HTTP, JSON-RPC 2.0. It is served by the
// same daemon as the UI, only on 127.0.0.1, only when switched on in Settings,
// and only with the token. "todo mcp" bridges stdio clients to this endpoint.

type rpcReq struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

const mcpInstructions = `Tillerz Todo Mgr: a tree of entries. Every entry is a todo, idea, project or reminder and can contain sub-entries.
Statuses — todo: open, progress, waiting, later (postponed, no plan), done, cancelled; idea: raw, exploring, parked, rejected;
project: planning, active, paused, done, archived; reminders have no status. Dates are YYYY-MM-DD or YYYY-MM-DDTHH:MM (local time).
Use create_entry with "text" for quick syntax ("Call Anna #work @anna !high due:fri at:14:00 ~30m"; start with "i:" or "?" for an idea, "p:" for a top-level project, "r:" for a reminder).
There is no hard delete: trash_entry moves to the trash and restore_entry brings it back.`

func obj(props map[string]any, required ...string) map[string]any {
	s := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		s["required"] = required
	}
	return s
}

var (
	tStr  = map[string]any{"type": "string"}
	tInt  = map[string]any{"type": "integer"}
	tBool = map[string]any{"type": "boolean"}
	tObj  = map[string]any{"type": "object"}
)

func withDesc(t map[string]any, d string) map[string]any {
	c := map[string]any{}
	for k, v := range t {
		c[k] = v
	}
	c["description"] = d
	return c
}

type mcpTool struct {
	Name, Desc string
	Schema     map[string]any
	Write      bool
	Run        func(s *Store, a map[string]any) (any, error)
}

func argStr(a map[string]any, k string) string { s, _ := a[k].(string); return s }
func argInt(a map[string]any, k string, def int) int {
	if v, ok := a[k]; ok {
		return int(num(v))
	}
	return def
}
func argBool(a map[string]any, k string) bool { b, _ := a[k].(bool); return b }

// redactSecrets hides Discord webhook URLs (they are passwords) from AI tools.
func redactSecrets(v any) any {
	b, err := json.Marshal(v)
	if err != nil {
		return v
	}
	var generic any
	if json.Unmarshal(b, &generic) != nil {
		return v
	}
	return redactWalk(generic)
}

func redactWalk(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, e := range x {
			if s, ok := e.(string); ok && k == "webhook" && s != "" {
				out[k] = "(set)"
			} else {
				out[k] = redactWalk(e)
			}
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = redactWalk(e)
		}
		return out
	}
	return v
}
func argMap(a map[string]any, k string) map[string]any {
	m, _ := a[k].(map[string]any)
	if m == nil {
		return map[string]any{}
	}
	return m
}
func csv(v string) []string {
	var out []string
	for _, p := range strings.Split(v, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

var mcpTools = []mcpTool{
	{Name: "list_entries", Desc: "List entries in tree order. Filters combine with AND. Returns compact entries (id, name, kind, status, deadline, tags, path, progress…).",
		Schema: obj(map[string]any{
			"query":  withDesc(tStr, "words that must all appear in name, notes, location, tags or people"),
			"status": withDesc(tStr, "comma separated statuses, e.g. open,progress"), "kind": withDesc(tStr, "comma separated: todo, idea, project, reminder"),
			"tag": tStr, "person": tStr, "project": withDesc(tStr, "project name or id the entry lives in"), "parent": withDesc(tStr, "only entries below this id"),
			"open_only": withDesc(tBool, "hide closed, postponed (later) entries and reminders"), "overdue_only": tBool, "include_trashed": tBool, "limit": tInt}),
		Run: func(s *Store, a map[string]any) (any, error) {
			items := s.List(Filter{Q: argStr(a, "query"), Statuses: csv(argStr(a, "status")), Kinds: csv(argStr(a, "kind")), Tag: argStr(a, "tag"), Person: argStr(a, "person"),
				Project: argStr(a, "project"), Parent: argStr(a, "parent"), Open: argBool(a, "open_only"), Overdue: argBool(a, "overdue_only"), Trashed: argBool(a, "include_trashed"), Limit: argInt(a, "limit", 50)})
			return map[string]any{"count": len(items), "items": items}, nil
		}},
	{Name: "get_entry", Desc: "Get all details of one entry (notes, links, dependencies, history, time log…).",
		Schema: obj(map[string]any{"id": tStr}, "id"),
		Run:    func(s *Store, a map[string]any) (any, error) { return s.Get(argStr(a, "id")) }},
	{Name: "get_tree", Desc: "Outline of the tree below an entry (default: everything, depth 3).",
		Schema: obj(map[string]any{"id": withDesc(tStr, "start entry, default top level"), "depth": tInt}),
		Run:    func(s *Store, a map[string]any) (any, error) { return s.Tree(argStr(a, "id"), argInt(a, "depth", 3)) }},
	{Name: "today", Desc: "What needs attention now: overdue, due today, in progress, upcoming reminders, high-priority next-up.",
		Schema: obj(map[string]any{}),
		Run:    func(s *Store, a map[string]any) (any, error) { return s.Today(), nil }},
	{Name: "stats", Desc: "Key figures and per-day counts (closed, created, activity, minutes tracked).",
		Schema: obj(map[string]any{"days": tInt}),
		Run:    func(s *Store, a map[string]any) (any, error) { return s.Stats(argInt(a, "days", 30)), nil }},
	{Name: "create_entry", Write: true, Desc: "Create an entry. Either give \"text\" in quick syntax (goes to the Inbox unless parent is given) or \"name\" plus optional \"fields\" " +
		"(kind, status, notes, notesFormat markdown|bbcode, deadline, reminder, end (end of an event), tags[], people[], prio 0-4, url, location, color, icon (a project's icon in the tree, e.g. star, book, code, map), recur (d/w/m/y, every N: 2w 3d 6m, monthly on weekdays: m:1fr,3fr,-1fr; a + at the end counts from the day it is done: 3d+), est minutes, lead minutes, deps[], prog{mode,done,total,unit}).",
		Schema: obj(map[string]any{"text": tStr, "name": tStr, "parent": withDesc(tStr, "parent id (default: Inbox for text, top level for name)"), "index": tInt, "fields": tObj}),
		Run: func(s *Store, a map[string]any) (any, error) {
			if t := argStr(a, "text"); t != "" {
				return s.Quick("mcp", t, argStr(a, "parent"))
			}
			return s.Create("mcp", argStr(a, "name"), argStr(a, "parent"), argInt(a, "index", -1), argMap(a, "fields"))
		}},
	{Name: "update_entry", Write: true, Desc: "Change fields of an entry (same field names as create_entry, plus name and status).",
		Schema: obj(map[string]any{"id": tStr, "fields": tObj}, "id", "fields"),
		Run: func(s *Store, a map[string]any) (any, error) {
			return s.Patch("mcp", argStr(a, "id"), argMap(a, "fields"))
		}},
	{Name: "set_status", Write: true, Desc: "Set the status of an entry (marking a recurring todo done reopens it with the next date).",
		Schema: obj(map[string]any{"id": tStr, "status": tStr}, "id", "status"),
		Run: func(s *Store, a map[string]any) (any, error) {
			return s.SetStatus("mcp", argStr(a, "id"), argStr(a, "status"))
		}},
	{Name: "move_entry", Write: true, Desc: "Move an entry (with its sub-entries) below another parent (\"root\" = top level).",
		Schema: obj(map[string]any{"id": tStr, "parent": tStr, "index": tInt}, "id", "parent"),
		Run: func(s *Store, a map[string]any) (any, error) {
			return s.Move("mcp", argStr(a, "id"), argStr(a, "parent"), argInt(a, "index", -1))
		}},
	{Name: "trash_entry", Write: true, Desc: "Move an entry (and everything below it) to the trash. Reversible with restore_entry.",
		Schema: obj(map[string]any{"id": tStr}, "id"),
		Run: func(s *Store, a map[string]any) (any, error) {
			return map[string]any{"ok": true}, s.Trash("mcp", argStr(a, "id"))
		}},
	{Name: "restore_entry", Write: true, Desc: "Bring an entry back from the trash.",
		Schema: obj(map[string]any{"id": tStr}, "id"),
		Run:    func(s *Store, a map[string]any) (any, error) { return s.Restore("mcp", argStr(a, "id")) }},
	{Name: "log_time", Write: true, Desc: "Add tracked time to an entry.",
		Schema: obj(map[string]any{"id": tStr, "minutes": map[string]any{"type": "number"}}, "id", "minutes"),
		Run: func(s *Store, a map[string]any) (any, error) {
			return s.LogTime("mcp", argStr(a, "id"), num(a["minutes"])*60)
		}},
}

func rpcErr(id json.RawMessage, code int, msg string) map[string]any {
	return map[string]any{"jsonrpc": "2.0", "id": rawID(id), "error": map[string]any{"code": code, "message": msg}}
}
func rawID(id json.RawMessage) any {
	if len(id) == 0 {
		return nil
	}
	return id
}

func (s *Server) mcpHandle(req rpcReq) map[string]any {
	ok := func(res any) map[string]any {
		return map[string]any{"jsonrpc": "2.0", "id": rawID(req.ID), "result": res}
	}
	switch req.Method {
	case "initialize":
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		_ = json.Unmarshal(req.Params, &p)
		if p.ProtocolVersion == "" {
			p.ProtocolVersion = "2025-03-26"
		}
		return ok(map[string]any{"protocolVersion": p.ProtocolVersion, "capabilities": map[string]any{"tools": map[string]any{}},
			"serverInfo": map[string]any{"name": "todomgr", "version": Version}, "instructions": mcpInstructions})
	case "ping":
		return ok(map[string]any{})
	case "tools/list":
		ro := s.cfg.Get().MCPReadOnly
		var list []any
		for _, t := range mcpTools {
			if t.Write && ro {
				continue
			}
			list = append(list, map[string]any{"name": t.Name, "description": t.Desc, "inputSchema": t.Schema})
		}
		return ok(map[string]any{"tools": list})
	case "tools/call":
		var p struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return rpcErr(req.ID, -32602, "invalid params")
		}
		if p.Arguments == nil {
			p.Arguments = map[string]any{}
		}
		for _, t := range mcpTools {
			if t.Name != p.Name {
				continue
			}
			if t.Write && s.cfg.Get().MCPReadOnly {
				return ok(map[string]any{"isError": true, "content": []any{map[string]any{"type": "text", "text": "MCP is in read-only mode (change it in Settings)"}}})
			}
			res, err := t.Run(s.st, p.Arguments)
			if err != nil {
				return ok(map[string]any{"isError": true, "content": []any{map[string]any{"type": "text", "text": err.Error()}}})
			}
			b, _ := json.MarshalIndent(redactSecrets(res), "", " ")
			return ok(map[string]any{"content": []any{map[string]any{"type": "text", "text": string(b)}}})
		}
		return rpcErr(req.ID, -32602, "unknown tool "+p.Name)
	}
	if strings.HasPrefix(req.Method, "notifications/") || len(req.ID) == 0 {
		return nil
	}
	return rpcErr(req.ID, -32601, "method not found: "+req.Method)
}

func (s *Server) hMCP(w http.ResponseWriter, r *http.Request) {
	if !s.cfg.Get().MCPEnabled {
		jerr(w, http.StatusServiceUnavailable, errors.New("the MCP server is switched off — enable it in Settings"))
		return
	}
	// header token only (never the UI cookie)
	if r.Header.Get("Authorization") == "" && r.Header.Get("X-Todo-Token") == "" || !s.tokenOK(r) {
		w.Header().Set("WWW-Authenticate", "Bearer")
		jerr(w, http.StatusUnauthorized, errors.New("missing or wrong token (Authorization: Bearer <token from Settings>)"))
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 4<<20))
	if err != nil {
		jerr(w, 400, err)
		return
	}
	body = bytes.TrimSpace(body)
	if len(body) > 0 && body[0] == '[' {
		var batch []rpcReq
		if err := json.Unmarshal(body, &batch); err != nil {
			jout(w, 200, rpcErr(nil, -32700, "parse error"))
			return
		}
		var out []any
		for _, q := range batch {
			if resp := s.mcpHandle(q); resp != nil {
				out = append(out, resp)
			}
		}
		if len(out) == 0 {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		jout(w, 200, out)
		return
	}
	var q rpcReq
	if err := json.Unmarshal(body, &q); err != nil {
		jout(w, 200, rpcErr(nil, -32700, "parse error"))
		return
	}
	resp := s.mcpHandle(q)
	if resp == nil {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	jout(w, 200, resp)
}

// runMCPStdio is "todo mcp": a stdio bridge for MCP clients that cannot speak
// HTTP. It forwards every JSON-RPC line to the running daemon and refuses to
// start while the MCP server is switched off.
func runMCPStdio(args []string) error {
	c, err := newClient(args)
	if err != nil {
		return err
	}
	var info struct{ MCP struct{ Enabled bool } }
	if err := c.get("/api/info", &info); err != nil {
		return err
	}
	if !info.MCP.Enabled {
		return errors.New("the MCP server is switched off. Enable it in the app under Settings → MCP server")
	}
	in := bufio.NewReaderSize(os.Stdin, 1<<20)
	out := bufio.NewWriter(os.Stdout)
	for {
		line, err := in.ReadBytes('\n')
		line = bytes.TrimSpace(line)
		if len(line) > 0 {
			req, _ := http.NewRequest("POST", c.base+"/mcp", bytes.NewReader(line))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", "Bearer "+c.token)
			resp, herr := (&http.Client{Timeout: 60 * time.Second}).Do(req)
			if herr != nil {
				fmt.Fprintln(os.Stderr, "todo mcp:", herr)
			} else {
				b, _ := io.ReadAll(resp.Body)
				resp.Body.Close()
				if resp.StatusCode == 200 && len(bytes.TrimSpace(b)) > 0 {
					out.Write(bytes.TrimSpace(b))
					out.WriteByte('\n')
					out.Flush()
				} else if resp.StatusCode >= 400 {
					fmt.Fprintf(os.Stderr, "todo mcp: server answered %d: %s\n", resp.StatusCode, bytes.TrimSpace(b))
				}
			}
		}
		if err != nil {
			return nil
		}
	}
}
