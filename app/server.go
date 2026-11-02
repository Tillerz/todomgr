package main

import (
	"bytes"
	"context"
	"crypto/subtle"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

//go:embed web
var embeddedWeb embed.FS

const cookieName = "todomgr_token"

type Server struct {
	st      *Store
	cfg     *Config
	port    int
	ui      fs.FS
	started time.Time
	http    *http.Server
}

func NewServer(st *Store, cfg *Config, port int, uiDir string) (*Server, error) {
	var ui fs.FS
	if uiDir != "" {
		if _, err := os.Stat(uiDir); err != nil {
			return nil, fmt.Errorf("--ui folder: %w", err)
		}
		ui = os.DirFS(uiDir)
	} else {
		sub, err := fs.Sub(embeddedWeb, "web")
		if err != nil {
			return nil, err
		}
		ui = sub
	}
	return &Server{st: st, cfg: cfg, port: port, ui: ui, started: time.Now()}, nil
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.serveIndex)
	mux.Handle("GET /", http.FileServerFS(s.ui))

	api := func(pattern string, h http.HandlerFunc) { mux.HandleFunc(pattern, s.auth(h)) }
	api("GET /api/info", s.hInfo)
	api("GET /api/state", s.hState)
	api("POST /api/sync", s.hSync)
	api("GET /api/events", s.hEvents)
	api("GET /api/settings", s.hSettingsGet)
	api("PUT /api/settings", s.hSettingsPut)
	api("GET /api/backups", s.hBackups)
	api("POST /api/backups", s.hBackupNow)
	api("POST /api/backups/restore", s.hRestore)
	api("GET /api/export", s.hExport)
	api("GET /api/export.csv", s.hExportCSV)
	api("GET /api/export.ics", s.hExportICS)
	api("POST /api/import", s.hImport)
	api("POST /api/import/entries", s.hImportEntries)
	api("POST /api/reset", s.hReset)
	api("POST /api/discord", s.hDiscord)
	api("POST /api/alerts/ack", s.hAck)

	api("GET /api/nodes", s.hList)
	api("POST /api/nodes", s.hCreate)
	api("GET /api/nodes/{id}", s.hGet)
	api("PATCH /api/nodes/{id}", s.hPatch)
	api("DELETE /api/nodes/{id}", s.hTrash)
	api("POST /api/nodes/{id}/status", s.hStatus)
	api("POST /api/nodes/{id}/move", s.hMove)
	api("POST /api/nodes/{id}/restore", s.hRestoreNode)
	api("POST /api/nodes/{id}/time", s.hTime)
	api("POST /api/quick", s.hQuick)
	api("GET /api/tree", s.hTree)
	api("GET /api/today", s.hToday)
	api("GET /api/stats", s.hStats)

	mux.HandleFunc("POST /mcp", s.hMCP)
	mux.HandleFunc("GET /mcp", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Allow", "POST")
		http.Error(w, "this MCP endpoint only accepts POST (no server-initiated stream)", http.StatusMethodNotAllowed)
	})
	return s.guard(mux)
}

// ---------------------------------------------------------------- security

func (s *Server) hostOK(h string) bool {
	host, port, err := net.SplitHostPort(h)
	if err != nil {
		host, port = strings.Trim(h, "[]"), ""
	}
	if host != "localhost" && host != "127.0.0.1" && host != "::1" {
		return false
	}
	return port == "" || port == strconv.Itoa(s.port)
}

// guard rejects every request that is not addressed to the local machine. The
// server only listens on 127.0.0.1, and additionally validates the Host and
// Origin headers so that a web page cannot reach it via DNS rebinding.
func (s *Server) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.hostOK(r.Host) {
			http.Error(w, "forbidden host", http.StatusForbidden)
			return
		}
		if o := r.Header.Get("Origin"); o != "" {
			u, err := url.Parse(o)
			if err != nil || !s.hostOK(u.Host) {
				http.Error(w, "forbidden origin", http.StatusForbidden)
				return
			}
		}
		if r.Method == http.MethodPost || r.Method == http.MethodPut || r.Method == http.MethodPatch {
			if ct := r.Header.Get("Content-Type"); r.ContentLength != 0 && !strings.HasPrefix(ct, "application/json") {
				http.Error(w, "Content-Type must be application/json", http.StatusUnsupportedMediaType)
				return
			}
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
}

func (s *Server) tokenOK(r *http.Request) bool {
	want := []byte(s.cfg.Get().Token)
	var got string
	if c, err := r.Cookie(cookieName); err == nil {
		got = c.Value
	}
	if a := r.Header.Get("Authorization"); strings.HasPrefix(a, "Bearer ") {
		got = strings.TrimPrefix(a, "Bearer ")
	}
	if t := r.Header.Get("X-Todo-Token"); t != "" {
		got = t
	}
	return got != "" && subtle.ConstantTimeCompare([]byte(got), want) == 1
}

func (s *Server) auth(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.tokenOK(r) {
			jerr(w, http.StatusUnauthorized, errors.New("missing or wrong token (the UI gets it as a cookie; CLI/MCP read it from data/config.json)"))
			return
		}
		h(w, r)
	}
}

// ---------------------------------------------------------------- helpers

func jout(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func jerr(w http.ResponseWriter, code int, err error) {
	jout(w, code, map[string]any{"error": err.Error()})
}

func fail(w http.ResponseWriter, err error) {
	var ce *conflictError
	switch {
	case errors.As(err, &ce):
		jout(w, http.StatusConflict, map[string]any{"error": err.Error(), "conflicts": ce.ids})
	case strings.Contains(err.Error(), "not found"):
		jerr(w, http.StatusNotFound, err)
	case strings.HasPrefix(err.Error(), "saving failed"):
		jerr(w, http.StatusInternalServerError, err)
	default:
		jerr(w, http.StatusBadRequest, err)
	}
}

func readBody(r *http.Request, v any, limit int64) error {
	body, err := io.ReadAll(io.LimitReader(r.Body, limit+1))
	if err != nil {
		return err
	}
	if int64(len(body)) > limit {
		return errors.New("request too large")
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return nil
	}
	if err := json.Unmarshal(body, v); err != nil {
		return fmt.Errorf("invalid JSON: %w", err)
	}
	return nil
}

func actorOf(r *http.Request) string {
	switch a := r.Header.Get("X-Actor"); a {
	case "cli", "mcp", "ui":
		return a
	}
	return "api"
}

// ---------------------------------------------------------------- static / info

func (s *Server) serveIndex(w http.ResponseWriter, r *http.Request) {
	b, err := fs.ReadFile(s.ui, "index.html")
	if err != nil {
		http.Error(w, "index.html missing", http.StatusInternalServerError)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: s.cfg.Get().Token, Path: "/", HttpOnly: true,
		SameSite: http.SameSiteStrictMode, MaxAge: 10 * 365 * 24 * 3600})
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(b)
}

func (s *Server) info() map[string]any {
	c := s.cfg.Get()
	return map[string]any{
		"name": AppName, "version": Version, "schema": schemaVersion, "dataDir": s.st.dir, "port": s.port,
		"timezone": s.st.tzName(), "startedAt": s.started.Format(time.RFC3339),
		"mcp": map[string]any{"enabled": c.MCPEnabled, "readOnly": c.MCPReadOnly},
	}
}

func (s *Server) hInfo(w http.ResponseWriter, r *http.Request) { jout(w, 200, s.info()) }

func (s *Server) hState(w http.ResponseWriter, r *http.Request) {
	st := s.st.State()
	st["info"] = s.info()
	jout(w, 200, st)
}

func (s *Server) hSync(w http.ResponseWriter, r *http.Request) {
	var req SyncReq
	if err := readBody(r, &req, 64<<20); err != nil {
		fail(w, err)
		return
	}
	if req.Client == "" {
		req.Client = r.Header.Get("X-Client")
	}
	rev, err := s.st.Sync(req)
	if err != nil {
		var ce *conflictError
		if errors.As(err, &ce) {
			s.st.mu.RLock()
			cur := s.st.db.Rev
			s.st.mu.RUnlock()
			jout(w, http.StatusConflict, map[string]any{"error": err.Error(), "conflicts": ce.ids, "rev": cur})
			return
		}
		fail(w, err)
		return
	}
	jout(w, 200, map[string]any{"rev": rev})
}

func (s *Server) hEvents(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		jerr(w, 500, errors.New("streaming not supported"))
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	ch := s.st.hub.Subscribe()
	defer s.st.hub.Unsubscribe(ch)
	s.st.mu.RLock()
	rev := s.st.db.Rev
	s.st.mu.RUnlock()
	fmt.Fprintf(w, "event: hello\ndata: {\"rev\":%d}\n\n", rev)
	fl.Flush()
	tick := time.NewTicker(25 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case m := <-ch:
			_, _ = w.Write(m)
			fl.Flush()
		case <-tick.C:
			_, _ = w.Write([]byte(": ping\n\n"))
			fl.Flush()
		}
	}
}

// ---------------------------------------------------------------- settings / backups

func (s *Server) settings() map[string]any {
	c := s.cfg.Get()
	return map[string]any{
		"version": Version, "schema": schemaVersion, "dataDir": s.st.dir, "port": s.port, "timezone": s.st.tzName(),
		"backupIntervalMin": c.BackupIntervalMin,
		"mcp": map[string]any{
			"enabled": c.MCPEnabled, "readOnly": c.MCPReadOnly,
			"url": fmt.Sprintf("http://127.0.0.1:%d/mcp", s.port), "token": c.Token,
		},
	}
}

func (s *Server) hSettingsGet(w http.ResponseWriter, r *http.Request) { jout(w, 200, s.settings()) }

func (s *Server) hSettingsPut(w http.ResponseWriter, r *http.Request) {
	var req struct {
		MCP *struct {
			Enabled  *bool `json:"enabled"`
			ReadOnly *bool `json:"readOnly"`
		} `json:"mcp"`
		BackupIntervalMin *int `json:"backupIntervalMin"`
	}
	if err := readBody(r, &req, 1<<16); err != nil {
		fail(w, err)
		return
	}
	if req.BackupIntervalMin != nil && (*req.BackupIntervalMin < 1 || *req.BackupIntervalMin > 1440) {
		jerr(w, 400, errors.New("backupIntervalMin must be between 1 and 1440"))
		return
	}
	err := s.cfg.Update(func(d *ConfigData) {
		if req.MCP != nil {
			if req.MCP.Enabled != nil {
				d.MCPEnabled = *req.MCP.Enabled
			}
			if req.MCP.ReadOnly != nil {
				d.MCPReadOnly = *req.MCP.ReadOnly
			}
		}
		if req.BackupIntervalMin != nil {
			d.BackupIntervalMin = *req.BackupIntervalMin
		}
	})
	if err != nil {
		jerr(w, 500, err)
		return
	}
	jout(w, 200, s.settings())
}

func (s *Server) hBackups(w http.ResponseWriter, r *http.Request) {
	l, err := s.st.ListBackups()
	if err != nil {
		jerr(w, 500, err)
		return
	}
	if l == nil {
		l = []BackupInfo{}
	}
	jout(w, 200, map[string]any{"backups": l})
}

func (s *Server) hBackupNow(w http.ResponseWriter, r *http.Request) {
	name, err := s.st.BackupNow()
	if err != nil {
		fail(w, err)
		return
	}
	jout(w, 200, map[string]any{"name": name})
}

func (s *Server) hRestore(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
	}
	if err := readBody(r, &req, 1<<16); err != nil {
		fail(w, err)
		return
	}
	if err := s.st.RestoreBackup(req.Name); err != nil {
		fail(w, err)
		return
	}
	jout(w, 200, map[string]any{"ok": true})
}

func (s *Server) hExport(w http.ResponseWriter, r *http.Request) {
	b, err := s.st.Export()
	if err != nil {
		jerr(w, 500, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", `attachment; filename="todomgr-`+time.Now().Format("2006-01-02")+`.json"`)
	_, _ = w.Write(b)
}

// closedParam: ?closed=1 includes finished entries, ?closed=0 leaves them out.
func closedParam(r *http.Request, def bool) bool {
	switch r.URL.Query().Get("closed") {
	case "1", "true":
		return true
	case "0", "false":
		return false
	}
	return def
}

func (s *Server) download(w http.ResponseWriter, ctype, ext string, body []byte) {
	w.Header().Set("Content-Type", ctype)
	w.Header().Set("Content-Disposition", `attachment; filename="todomgr-`+time.Now().Format("2006-01-02")+ext+`"`)
	_, _ = w.Write(body)
}

// CSV lists everything by default, the calendar only what is still open.
func (s *Server) hExportCSV(w http.ResponseWriter, r *http.Request) {
	s.download(w, "text/csv; charset=utf-8", ".csv", s.st.ExportCSV(closedParam(r, true)))
}

func (s *Server) hExportICS(w http.ResponseWriter, r *http.Request) {
	s.download(w, "text/calendar; charset=utf-8", ".ics", s.st.ExportICS(closedParam(r, false)))
}

func (s *Server) hImport(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 128<<20))
	if err != nil {
		fail(w, err)
		return
	}
	if err := s.st.Import(body); err != nil {
		fail(w, err)
		return
	}
	jout(w, 200, map[string]any{"ok": true})
}

// hImportEntries adds the entries of a CSV or ICS file: {format: "csv"|"ics", name, data}.
func (s *Server) hImportEntries(w http.ResponseWriter, r *http.Request) {
	var req struct{ Format, Name, Data string }
	if err := readBody(r, &req, 64<<20); err != nil {
		fail(w, err)
		return
	}
	res, err := s.st.ImportEntries(actorOf(r), req.Format, req.Name, []byte(req.Data))
	if err != nil {
		fail(w, err)
		return
	}
	jout(w, 200, res)
}

func (s *Server) hReset(w http.ResponseWriter, r *http.Request) {
	if err := s.st.Reset(); err != nil {
		fail(w, err)
		return
	}
	jout(w, 200, map[string]any{"ok": true})
}

// hDiscord posts a message to the Discord channel of the project of an entry.
// It is a UI/API feature only; MCP has no tool for it on purpose.
func (s *Server) hDiscord(w http.ResponseWriter, r *http.Request) {
	var req struct{ ID, Title, URL string }
	if err := readBody(r, &req, 1<<16); err != nil {
		fail(w, err)
		return
	}
	if err := s.st.Announce(req.ID, req.Title, req.URL); err != nil {
		fail(w, err)
		return
	}
	jout(w, 200, map[string]any{"ok": true})
}

func (s *Server) hAck(w http.ResponseWriter, r *http.Request) {
	var req struct{ ID, Action string }
	if err := readBody(r, &req, 1<<16); err != nil {
		fail(w, err)
		return
	}
	if err := s.st.AckAlert(req.ID, req.Action); err != nil {
		fail(w, err)
		return
	}
	jout(w, 200, map[string]any{"ok": true})
}

// ---------------------------------------------------------------- REST: entries

func (s *Server) hList(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	split := func(k string) []string {
		var out []string
		for _, p := range strings.Split(q.Get(k), ",") {
			if p = strings.TrimSpace(p); p != "" {
				out = append(out, p)
			}
		}
		return out
	}
	lim, _ := strconv.Atoi(q.Get("limit"))
	items := s.st.List(Filter{Q: q.Get("q"), Tag: q.Get("tag"), Person: q.Get("person"), Parent: q.Get("parent"), Project: q.Get("project"),
		Statuses: split("status"), Kinds: split("kind"), Open: q.Get("open") == "1", Overdue: q.Get("overdue") == "1", Trashed: q.Get("trashed") == "1", Limit: lim})
	jout(w, 200, map[string]any{"count": len(items), "items": items})
}

func (s *Server) hGet(w http.ResponseWriter, r *http.Request) {
	v, err := s.st.Get(r.PathValue("id"))
	if err != nil {
		fail(w, err)
		return
	}
	jout(w, 200, v)
}

func (s *Server) hCreate(w http.ResponseWriter, r *http.Request) {
	var f map[string]any
	if err := readBody(r, &f, 1<<20); err != nil {
		fail(w, err)
		return
	}
	name, _ := f["name"].(string)
	parent, _ := f["parent"].(string)
	index := -1
	if x, ok := f["index"]; ok {
		index = int(num(x))
	}
	delete(f, "parent")
	delete(f, "index")
	v, err := s.st.Create(actorOf(r), name, parent, index, f)
	if err != nil {
		fail(w, err)
		return
	}
	jout(w, 201, v)
}

func (s *Server) hQuick(w http.ResponseWriter, r *http.Request) {
	var req struct{ Text, Parent string }
	if err := readBody(r, &req, 1<<16); err != nil {
		fail(w, err)
		return
	}
	v, err := s.st.Quick(actorOf(r), req.Text, req.Parent)
	if err != nil {
		fail(w, err)
		return
	}
	jout(w, 201, v)
}

func (s *Server) hPatch(w http.ResponseWriter, r *http.Request) {
	var f map[string]any
	if err := readBody(r, &f, 1<<20); err != nil {
		fail(w, err)
		return
	}
	v, err := s.st.Patch(actorOf(r), r.PathValue("id"), f)
	if err != nil {
		fail(w, err)
		return
	}
	jout(w, 200, v)
}

func (s *Server) hTrash(w http.ResponseWriter, r *http.Request) {
	if err := s.st.Trash(actorOf(r), r.PathValue("id")); err != nil {
		fail(w, err)
		return
	}
	jout(w, 200, map[string]any{"ok": true})
}

func (s *Server) hRestoreNode(w http.ResponseWriter, r *http.Request) {
	v, err := s.st.Restore(actorOf(r), r.PathValue("id"))
	if err != nil {
		fail(w, err)
		return
	}
	jout(w, 200, v)
}

func (s *Server) hStatus(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Status string `json:"status"`
	}
	if err := readBody(r, &req, 1<<16); err != nil {
		fail(w, err)
		return
	}
	v, err := s.st.SetStatus(actorOf(r), r.PathValue("id"), req.Status)
	if err != nil {
		fail(w, err)
		return
	}
	jout(w, 200, v)
}

func (s *Server) hMove(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Parent string `json:"parent"`
		Index  *int   `json:"index"`
	}
	if err := readBody(r, &req, 1<<16); err != nil {
		fail(w, err)
		return
	}
	idx := -1
	if req.Index != nil {
		idx = *req.Index
	}
	v, err := s.st.Move(actorOf(r), r.PathValue("id"), req.Parent, idx)
	if err != nil {
		fail(w, err)
		return
	}
	jout(w, 200, v)
}

func (s *Server) hTime(w http.ResponseWriter, r *http.Request) {
	var req struct{ Minutes, Seconds float64 }
	if err := readBody(r, &req, 1<<16); err != nil {
		fail(w, err)
		return
	}
	secs := req.Seconds + req.Minutes*60
	v, err := s.st.LogTime(actorOf(r), r.PathValue("id"), secs)
	if err != nil {
		fail(w, err)
		return
	}
	jout(w, 200, v)
}

func (s *Server) hTree(w http.ResponseWriter, r *http.Request) {
	d, _ := strconv.Atoi(r.URL.Query().Get("depth"))
	v, err := s.st.Tree(r.URL.Query().Get("root"), d)
	if err != nil {
		fail(w, err)
		return
	}
	jout(w, 200, v)
}

func (s *Server) hToday(w http.ResponseWriter, r *http.Request) { jout(w, 200, s.st.Today()) }

func (s *Server) hStats(w http.ResponseWriter, r *http.Request) {
	d, _ := strconv.Atoi(r.URL.Query().Get("days"))
	jout(w, 200, s.st.Stats(d))
}

// ---------------------------------------------------------------- lifecycle

func (s *Server) Serve(ln net.Listener) error {
	s.http = &http.Server{Handler: s.Handler(), ReadHeaderTimeout: 10 * time.Second}
	err := s.http.Serve(ln)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func (s *Server) Shutdown() {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if s.http != nil {
		_ = s.http.Shutdown(ctx)
	}
}
