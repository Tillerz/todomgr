package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// Tests for the fixes of the code review of 2026-11-12. (The repeat rule at the end of a month is in
// testdata/recur_cases.json, shared with the page.)

// "at:" without "due:" on a todo: today at that time, or tomorrow when the time has passed.
func TestQuickTimeAlone(t *testing.T) {
	now := time.Date(2026, 11, 1, 10, 0, 0, 0, time.Local)
	name, f := parseQuick("Call Anna at:14:00", now)
	if name != "Call Anna" || f["deadline"] != "2026-11-01T14:00" {
		t.Errorf("later today: %q %v", name, f)
	}
	if _, f := parseQuick("Call Anna at:9:00", now); f["deadline"] != "2026-11-02T09:00" {
		t.Errorf("a time that has passed must mean tomorrow: %v", f)
	}
	if _, f := parseQuick("Call Anna", now); f["deadline"] != nil {
		t.Errorf("no time, no deadline: %v", f)
	}
}

// The page's sync saves entries as they are, so a webhook is checked again before it is used.
func TestAnnounceChecksStoredWebhook(t *testing.T) {
	st, _ := testStore(t)
	sent := 0
	old := discordSend
	discordSend = func(hook, content string) error { sent++; return nil }
	defer func() { discordSend = old }()

	pid := mustCreate(t, st, "World", "", map[string]any{"kind": "project"})
	n := nodeOf(st, pid)
	n["webhook"] = "http://127.0.0.1:9/api/webhooks/1/x"
	raw, _ := json.Marshal(n)
	if _, err := st.Sync(SyncReq{Base: st.db.Rev, Nodes: map[string]json.RawMessage{pid: raw}}); err != nil {
		t.Fatal(err)
	}
	if err := st.Announce(pid, "Test", ""); err == nil || !strings.Contains(err.Error(), "not a Discord webhook") {
		t.Errorf("a stored address that is not a Discord webhook must be refused, got %v", err)
	}
	if sent != 0 {
		t.Error("nothing may be sent to it")
	}
}

// An AI tool must not decide where Discord posts go.
func TestMCPCannotSetWebhook(t *testing.T) {
	a := testServer(t)
	a.call(200, "PUT", "/api/settings", map[string]any{"mcp": map[string]any{"enabled": true}})
	hook := map[string]any{"kind": "project", "webhook": "https://discord.com/api/webhooks/1/secret"}
	if text, isErr := a.toolText("create_entry", map[string]any{"name": "World", "fields": hook}); !isErr {
		t.Errorf("create_entry with a webhook must be refused: %s", text)
	}
	text, isErr := a.toolText("create_entry", map[string]any{"name": "World", "fields": map[string]any{"kind": "project"}})
	if isErr {
		t.Fatal(text)
	}
	var created map[string]any
	_ = json.Unmarshal([]byte(text), &created)
	id := created["id"].(string)
	if text, isErr := a.toolText("update_entry", map[string]any{"id": id, "fields": hook}); !isErr {
		t.Errorf("update_entry with a webhook must be refused: %s", text)
	}
	if n, _ := a.s.st.Get(id); n["webhook"] != "" {
		t.Errorf("the webhook was set: %v", n["webhook"])
	}
}

// After a restart of the computer the process number of an old lock can be in use again.
func TestLockFromBeforeRestartIsTakenOver(t *testing.T) {
	if bootID() == "" {
		t.Skip("no boot id on this system")
	}
	dir := t.TempDir()
	host, _ := os.Hostname()
	p := filepath.Join(dir, "todomgr.lock")
	cfg, _ := LoadConfig(dir)
	// the same boot and a process that runs: the lock holds
	os.WriteFile(p, []byte(fmt.Sprintf("%d@%s\n%s\n%s\n", os.Getppid(), bootID(), host, runtime.GOOS)), 0o644)
	if st, err := OpenStore(dir, cfg); err == nil {
		st.Close()
		t.Fatal("the lock of a running process must be kept")
	}
	// another boot: stale, also with the number of a process that runs now
	for _, pid := range []int{os.Getppid(), os.Getpid()} {
		os.WriteFile(p, []byte(fmt.Sprintf("%d@an-older-boot\n%s\n%s\n", pid, host, runtime.GOOS)), 0o644)
		st, err := OpenStore(dir, cfg)
		if err != nil {
			t.Fatalf("a lock from before the restart must be taken over: %v", err)
		}
		st.Close()
	}
}

// Which entry changed when is not kept over a restart, so a page that is behind must refresh first.
func TestSyncAfterRestartMustRefresh(t *testing.T) {
	st, dir := testStore(t)
	a := mustCreate(t, st, "Shared", "", nil)
	base := st.db.Rev
	if _, err := st.Patch("cli", a, map[string]any{"notes": "from cli"}); err != nil {
		t.Fatal(err)
	}
	stale := nodeOf(st, a)
	stale["notes"], stale["name"] = "", "Page rename"
	raw, _ := json.Marshal(stale)
	st.Close()
	cfg, _ := LoadConfig(dir)
	st2, err := OpenStore(dir, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()
	var ce *conflictError
	if _, err := st2.Sync(SyncReq{Base: base, Nodes: map[string]json.RawMessage{a: raw}}); !errors.As(err, &ce) {
		t.Fatalf("a page that is behind must get a conflict after a restart, got %v", err)
	}
	if n, _ := st2.Get(a); n["notes"] != "from cli" {
		t.Error("the newer change was overwritten")
	}
	if _, err := st2.Sync(SyncReq{Base: st2.db.Rev, Nodes: map[string]json.RawMessage{a: raw}}); err != nil {
		t.Errorf("a page that is up to date can write: %v", err)
	}
}
