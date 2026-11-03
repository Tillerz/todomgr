package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testStore(t *testing.T) (*Store, string) {
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
	return st, dir
}

func mustCreate(t *testing.T, st *Store, name, parent string, f map[string]any) string {
	t.Helper()
	n, err := st.Create("test", name, parent, -1, f)
	if err != nil {
		t.Fatalf("create %q: %v", name, err)
	}
	return n["id"].(string)
}

func TestCreatePatchStatus(t *testing.T) {
	st, _ := testStore(t)
	p := mustCreate(t, st, "Project", "", map[string]any{"kind": "project"})
	a := mustCreate(t, st, "Write intro", p, map[string]any{"tags": []any{"#writing", "wa"}, "prio": 3.0, "deadline": "2026-12-24"})
	n, _ := st.Get(a)
	if n["parent"] != p || n["status"] != "open" || n["prio"].(float64) != 3 {
		t.Fatalf("unexpected node %v", n)
	}
	if got := strs(n["tags"]); len(got) != 2 || got[0] != "writing" {
		t.Fatalf("tags not cleaned: %v", got)
	}
	if _, err := st.Patch("test", a, map[string]any{"bogus": 1}); err == nil {
		t.Fatal("unknown field must be rejected")
	}
	if _, err := st.Patch("test", a, map[string]any{"deadline": "tomorrow"}); err == nil {
		t.Fatal("bad date must be rejected")
	}
	if _, err := st.Patch("test", a, map[string]any{"url": "javascript:alert(1)"}); err == nil {
		t.Fatal("non-http url must be rejected")
	}
	if _, err := st.Patch("test", a, map[string]any{"webhook": "https://evil.example/api/webhooks/1/x"}); err == nil {
		t.Fatal("non-discord webhook must be rejected")
	}
	if _, err := st.SetStatus("test", a, "exploring"); err == nil {
		t.Fatal("idea status on a todo must be rejected")
	}
	n, err := st.SetStatus("test", a, "done")
	if err != nil || n["status"] != "done" || len(n["hist"].([]any)) != 2 {
		t.Fatalf("done failed: %v %v", err, n)
	}
	// the failed patches must not have changed anything
	n, _ = st.Get(a)
	if n["url"] != "" || n["deadline"] != "2026-12-24" {
		t.Fatalf("rejected change leaked: %v", n)
	}
}

func TestRecurringDoneReopens(t *testing.T) {
	st, _ := testStore(t)
	a := mustCreate(t, st, "Water plants", "", map[string]any{"recur": "w", "deadline": "2026-11-02"})
	n, err := st.SetStatus("test", a, "done")
	if err != nil || n["status"] != "open" || n["deadline"] != "2026-11-09" {
		t.Fatalf("recurrence: %v %v", err, n)
	}
}

func TestRecurringAfterDone(t *testing.T) {
	st, _ := testStore(t)
	a := mustCreate(t, st, "Water plants", "", map[string]any{"recur": "3D+", "deadline": "2020-01-01"})
	n, err := st.SetStatus("test", a, "done")
	want := time.Now().In(st.loc).AddDate(0, 0, 3).Format("2006-01-02")
	if err != nil || n["status"] != "open" || n["recur"] != "3d+" || n["deadline"] != want {
		t.Fatalf("after done: %v %v, want deadline %s", err, n, want)
	}
}

func TestReminderHasNoStatus(t *testing.T) {
	st, _ := testStore(t)
	r := mustCreate(t, st, "Stream", "", map[string]any{"kind": "reminder", "reminder": "2026-11-05T19:00"})
	n, _ := st.Get(r)
	if n["status"] != "set" || n["lead"].(float64) != 15 {
		t.Fatalf("reminder defaults: %v", n)
	}
	if _, err := st.SetStatus("test", r, "done"); err == nil {
		t.Fatal("reminders must not accept a status")
	}
}

func TestMoveRulesAndTrash(t *testing.T) {
	st, _ := testStore(t)
	p := mustCreate(t, st, "P", "", nil)
	c := mustCreate(t, st, "C", p, nil)
	g := mustCreate(t, st, "G", c, nil)
	if _, err := st.Move("test", p, g, -1); err == nil {
		t.Fatal("moving into own subtree must fail")
	}
	if _, err := st.Move("test", g, "root", 0); err != nil {
		t.Fatal(err)
	}
	if err := st.Trash("test", c); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Get(c); err != nil {
		t.Fatal("trashed entries stay readable")
	}
	if _, err := st.Restore("test", c); err != nil {
		t.Fatal(err)
	}
	n, _ := st.Get(c)
	if n["parent"] != p {
		t.Fatalf("restore should return to old parent, got %v", n["parent"])
	}
}

func TestDependencyCycleRejected(t *testing.T) {
	st, _ := testStore(t)
	a := mustCreate(t, st, "A", "", nil)
	b := mustCreate(t, st, "B", "", map[string]any{"deps": []any{a}})
	if _, err := st.Patch("test", a, map[string]any{"deps": []any{b}}); err == nil {
		t.Fatal("dependency cycle must be rejected")
	}
}

func TestPersistenceAndLock(t *testing.T) {
	st, dir := testStore(t)
	cfg, _ := LoadConfig(dir)
	if _, err := OpenStore(dir, cfg); err == nil || !strings.Contains(err.Error(), "in use") {
		t.Fatalf("second instance must be refused, got %v", err)
	}
	a := mustCreate(t, st, "Persist me", "", nil)
	st.Close()
	st2, err := OpenStore(dir, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()
	if n, err := st2.Get(a); err != nil || n["name"] != "Persist me" {
		t.Fatalf("not persisted: %v %v", n, err)
	}
	if len(st2.activity) == 0 {
		t.Fatal("activity log not persisted")
	}
}

func TestStaleLockIsTakenOver(t *testing.T) {
	dir := t.TempDir()
	host, _ := os.Hostname()
	os.WriteFile(filepath.Join(dir, "todomgr.lock"), []byte("999999\n"+host+"\n"), 0o644)
	cfg, _ := LoadConfig(dir)
	st, err := OpenStore(dir, cfg)
	if err != nil {
		t.Fatalf("stale lock should be replaced: %v", err)
	}
	st.Close()
}

func TestCorruptFileRecoversFromPrevOrBackup(t *testing.T) {
	st, dir := testStore(t)
	a := mustCreate(t, st, "Keep me", "", nil)
	mustCreate(t, st, "Second", "", nil) // makes database.prev.json hold the state with "Keep me"
	if _, err := st.BackupNow(); err != nil {
		t.Fatal(err)
	}
	st.Close()
	os.WriteFile(filepath.Join(dir, "database.json"), []byte("{ this is not json"), 0o644)
	cfg, _ := LoadConfig(dir)
	st2, err := OpenStore(dir, cfg)
	if err != nil {
		t.Fatalf("should recover: %v", err)
	}
	defer st2.Close()
	if st2.Recovered == "" {
		t.Fatal("recovery must be reported")
	}
	if _, err := st2.Get(a); err != nil {
		t.Fatal("recovered data lost an entry")
	}
	matches, _ := filepath.Glob(filepath.Join(dir, "database.corrupt-*.json"))
	if len(matches) != 1 {
		t.Fatal("damaged file must be kept for inspection")
	}
}

func TestNewerSchemaIsNeverTouched(t *testing.T) {
	dir := t.TempDir()
	body := `{"schema": 999, "app": "9.9", "nodes": {"root": {"id":"root","children":[]}}}`
	os.WriteFile(filepath.Join(dir, "database.json"), []byte(body), 0o644)
	cfg, _ := LoadConfig(dir)
	_, err := OpenStore(dir, cfg)
	if !errors.Is(err, errTooNew) {
		t.Fatalf("expected errTooNew, got %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "database.json")); string(b) != body {
		t.Fatal("file of a newer version was modified")
	}
	if _, err := os.Stat(filepath.Join(dir, "todomgr.lock")); err == nil {
		t.Fatal("lock must be released after a failed open")
	}
}

func TestMigrationChainAndBackup(t *testing.T) {
	oldV, oldM := schemaVersion, migrations
	defer func() { schemaVersion, migrations = oldV, oldM }()
	// a file from the prototype (schema 0) with inline activity
	proto := map[string]any{"v": 2, "nodes": map[string]any{
		"root": map[string]any{"id": "root", "children": []any{"a"}},
		"a":    map[string]any{"id": "a", "name": "Old entry", "parent": "root", "children": []any{}, "status": "open", "kind": "todo"},
	}, "activity": []any{map[string]any{"t": 1.7e12, "id": "a", "type": "create"}}}
	b, _ := json.Marshal(proto)
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "database.json"), b, 0o644)
	// pretend the app moved on to schema 2 with a new field
	schemaVersion = 2
	migrations = map[int]migration{0: oldM[0], 1: func(raw map[string]any) error {
		for _, v := range raw["nodes"].(map[string]any) {
			v.(map[string]any)["migratedField"] = "yes"
		}
		return nil
	}}
	cfg, _ := LoadConfig(dir)
	st, err := OpenStore(dir, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	n, err := st.Get("a")
	if err != nil || n["migratedField"] != "yes" || n["url"] != "" {
		t.Fatalf("migration result: %v %v", n, err)
	}
	if st.db.Schema != 2 || len(st.activity) != 1 {
		t.Fatalf("schema=%d activity=%d", st.db.Schema, len(st.activity))
	}
	if m, _ := filepath.Glob(filepath.Join(dir, "backups", "pre-migrate-*.json")); len(m) != 1 {
		t.Fatal("a backup must be taken before migrating")
	}
	var saved map[string]any
	data, _ := os.ReadFile(filepath.Join(dir, "database.json"))
	json.Unmarshal(data, &saved)
	if saved["schema"].(float64) != 2 || saved["app"] != Version {
		t.Fatalf("file must carry schema and app version: %v %v", saved["schema"], saved["app"])
	}
}

func TestSyncConflictAndValidation(t *testing.T) {
	st, _ := testStore(t)
	a := mustCreate(t, st, "Shared", "", nil)
	base := st.db.Rev
	// somebody else (CLI) changes the entry after the UI loaded its state
	if _, err := st.Patch("cli", a, map[string]any{"notes": "from cli"}); err != nil {
		t.Fatal(err)
	}
	n, _ := st.Get(a)
	delete(n, "childIds")
	n["name"] = "UI rename"
	raw, _ := json.Marshal(n)
	_, err := st.Sync(SyncReq{Base: base, Nodes: map[string]json.RawMessage{a: raw}})
	var ce *conflictError
	if !errors.As(err, &ce) {
		t.Fatalf("stale write must conflict, got %v", err)
	}
	if got, _ := st.Get(a); got["name"] != "Shared" || got["notes"] != "from cli" {
		t.Fatal("conflicting sync must not apply anything")
	}
	// a UI that is up to date can write; an orphaning change is rejected
	n["name"] = "UI rename"
	raw, _ = json.Marshal(n)
	if _, err := st.Sync(SyncReq{Base: st.db.Rev, Nodes: map[string]json.RawMessage{a: raw}}); err != nil {
		t.Fatalf("up-to-date sync failed: %v", err)
	}
	if _, err := st.Sync(SyncReq{Base: st.db.Rev, Nodes: map[string]json.RawMessage{a: []byte("null")}}); err == nil {
		t.Fatal("deleting an entry that is still listed in its parent must be rejected")
	}
	if got, _ := st.Get(a); got["name"] != "UI rename" {
		t.Fatal("rejected sync must roll back")
	}
}

func TestKeepBackupsRetention(t *testing.T) {
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.Local)
	var list []BackupInfo
	add := func(kind string, ago time.Duration) {
		tm := now.Add(-ago)
		list = append(list, BackupInfo{Name: kind + "-" + tm.Format("20060102-150405") + ".json", Kind: kind, Time: tm})
	}
	for i := 0; i < 12; i++ { // every 10 minutes for 2 hours -> all kept
		add("auto", time.Duration(i*10)*time.Minute)
	}
	for i := 0; i < 6; i++ { // 6 within the same old hour -> only one kept
		add("auto", 5*time.Hour+30*time.Minute+time.Duration(i)*time.Minute)
	}
	add("auto", 400*24*time.Hour) // too old
	add("pre-import", 20*24*time.Hour)
	keep := keepBackups(list, now)
	count := func(prefix string) int {
		n := 0
		for _, b := range list {
			if strings.HasPrefix(b.Name, prefix) && keep[b.Name] {
				n++
			}
		}
		return n
	}
	if got := count("auto"); got != 13 {
		t.Fatalf("expected 12 recent + 1 hourly kept, got %d", got)
	}
	if count("pre-import") != 1 {
		t.Fatal("few pre- backups must be kept")
	}
	if keep[list[len(list)-2].Name] {
		t.Fatal("backup older than 26 weeks must be pruned")
	}
}

func TestRestoreBackupAndImport(t *testing.T) {
	st, _ := testStore(t)
	a := mustCreate(t, st, "Before", "", nil)
	name, err := st.BackupNow()
	if err != nil {
		t.Fatal(err)
	}
	mustCreate(t, st, "After", "", nil)
	if err := st.RestoreBackup(name); err != nil {
		t.Fatal(err)
	}
	if got := st.List(Filter{Q: "After", Limit: 5}); len(got) != 0 {
		t.Fatal("restore did not roll back")
	}
	if _, err := st.Get(a); err != nil {
		t.Fatal(err)
	}
	if err := st.RestoreBackup("../../etc/passwd"); err == nil {
		t.Fatal("path traversal must be rejected")
	}
	exp, _ := st.Export()
	if err := st.Import(exp); err != nil {
		t.Fatalf("own export must be importable: %v", err)
	}
	if err := st.Import([]byte(`{"nodes":{"a":{"id":"a","parent":"zzz"}}}`)); err == nil {
		t.Fatal("broken import must be rejected")
	}
	if _, err := st.Get(a); err != nil {
		t.Fatal("failed import must not change data")
	}
}

func TestQuickParse(t *testing.T) {
	now := time.Date(2026, 11, 1, 10, 0, 0, 0, time.Local) // a Sunday
	name, f := parseQuick("Call Anna #work @anna !high due:fri at:14:30 ~1h", now)
	if name != "Call Anna" || f["prio"] != 3.0 || f["deadline"] != "2026-11-06T14:30" || f["est"] != 60.0 || strs(f["tags"])[0] != "work" {
		t.Fatalf("%q %v", name, f)
	}
	name, f = parseQuick("? Podcast idea", now)
	if name != "Podcast idea" || f["kind"] != "idea" {
		t.Fatalf("%q %v", name, f)
	}
	name, f = parseQuick("r: Community stream at:19:00 due:thu repeat:weekly", now)
	if name != "Community stream" || f["kind"] != "reminder" || f["reminder"] != "2026-11-05T19:00" || f["recur"] != "w" {
		t.Fatalf("%q %v", name, f)
	}
	name, _ = parseQuick("Buy milk !zzz", now)
	if name != "Buy milk !zzz" {
		t.Fatalf("unknown token must stay in the name, got %q", name)
	}
}

func TestListTreeStats(t *testing.T) {
	st, _ := testStore(t)
	p := mustCreate(t, st, "Novel", "", map[string]any{"kind": "project"})
	mustCreate(t, st, "Chapter 1", p, map[string]any{"tags": []any{"draft"}})
	c2 := mustCreate(t, st, "Chapter 2", p, nil)
	st.SetStatus("test", c2, "done")
	if got := st.List(Filter{Project: "novel", Open: true}); len(got) != 1 || got[0]["name"] != "Chapter 1" {
		t.Fatalf("project/open filter: %v", got)
	}
	if got := st.List(Filter{Tag: "#draft"}); len(got) != 1 {
		t.Fatalf("tag filter: %v", got)
	}
	tr, _ := st.Tree("", 3)
	if items := tr["items"].([]any); len(items) != 1 {
		t.Fatalf("tree: %v", tr)
	}
	n, _ := st.Get(p)
	if n["progress"].(float64) != 0.5 {
		t.Fatalf("derived progress should be 0.5, got %v", n["progress"])
	}
	s := st.Stats(7)
	if s["closedThisWeek"].(int) != 1 || len(s["days"].([]map[string]any)) != 7 {
		t.Fatalf("stats: %v", s)
	}
}

func TestNotesFormat(t *testing.T) {
	st, _ := testStore(t)
	id := mustCreate(t, st, "Article", "", map[string]any{"notesFormat": "bbcode", "notes": "[b]x[/b]"})
	if n, _ := st.Get(id); n["notesFormat"] != "bbcode" {
		t.Errorf("notesFormat %v", n["notesFormat"])
	}
	if n, _ := st.Get(mustCreate(t, st, "Plain", "", nil)); n["notesFormat"] != "markdown" {
		t.Errorf("default notesFormat %v", n["notesFormat"])
	}
	if _, err := st.Patch("test", id, map[string]any{"notesFormat": "html"}); err == nil {
		t.Error("notesFormat html must be refused")
	}
}

func TestProjectIcon(t *testing.T) {
	st, _ := testStore(t)
	p := mustCreate(t, st, "Novel", "", map[string]any{"kind": "project", "icon": "book"})
	if n, _ := st.Get(p); n["icon"] != "book" {
		t.Fatalf("icon: %v", n["icon"])
	}
	for _, bad := range []any{"Book", "<svg>", "a b", 5, strings.Repeat("a", 31)} {
		if _, err := st.Patch("test", p, map[string]any{"icon": bad}); err == nil {
			t.Errorf("icon %v must be refused", bad)
		}
	}
}

func TestQuickKinds(t *testing.T) {
	now := time.Date(2026, 11, 1, 10, 0, 0, 0, time.Local)
	for in, want := range map[string][2]string{"t: Buy milk": {"Buy milk", ""}, "i:Board game": {"Board game", "idea"}, "I: Board game": {"Board game", "idea"},
		"p: Garden": {"Garden", "project"}, "R: Call mum": {"Call mum", "reminder"}, "x: Not a prefix": {"x: Not a prefix", ""}, "Ratio p: 2": {"Ratio p: 2", ""}} {
		if name, f := parseQuick(in, now); name != want[0] || str(f["kind"]) != want[1] {
			t.Errorf("parseQuick(%q) = %q %v, want %q kind %q", in, name, f["kind"], want[0], want[1])
		}
	}
	st, _ := testStore(t)
	p, err := st.Quick("test", "p: Garden #home", "")
	if err != nil || p["parent"] != "root" || p["kind"] != "project" || p["status"] != "planning" {
		t.Fatalf("p: must be a top-level project: %v %v", err, p)
	}
	if n, _ := st.Quick("test", "t: Buy milk", ""); n["parent"] == "root" || n["kind"] != "todo" {
		t.Fatalf("t: must go into the Inbox: %v", n)
	}
}

// only the entry moved to the trash has the mark; its sub-entries count as in the trash too, in every action
func TestTrashCoversSubEntries(t *testing.T) {
	st, _ := testStore(t)
	p := mustCreate(t, st, "Project", "", map[string]any{"kind": "project"})
	a := mustCreate(t, st, "A", p, nil)
	b := mustCreate(t, st, "B", a, nil)
	other := mustCreate(t, st, "Other", "", nil)
	if err := st.Trash("test", b); err != nil { // B alone first, then the whole project
		t.Fatal(err)
	}
	if err := st.Trash("test", p); err != nil {
		t.Fatal(err)
	}
	if err := st.Trash("test", a); err == nil {
		t.Error("a sub-entry of a trashed project must count as in the trash already")
	}
	if _, err := st.Patch("test", a, map[string]any{"name": "x"}); err == nil {
		t.Error("editing a sub-entry of a trashed project must fail")
	}
	if _, err := st.SetStatus("test", a, "done"); err == nil {
		t.Error("setting the status of a sub-entry of a trashed project must fail")
	}
	if _, err := st.Create("test", "New", a, -1, nil); err == nil {
		t.Error("creating below a sub-entry of a trashed project must fail")
	}
	if _, err := st.Move("test", other, a, -1); err == nil {
		t.Error("moving into a trashed project must fail")
	}
	if _, err := st.Move("test", a, "", -1); err == nil {
		t.Error("moving out of a trashed project must fail (restore it instead)")
	}
	// B's old parent A is in the trash: restoring B alone puts it at the top
	if _, err := st.Restore("test", b); err != nil {
		t.Fatal(err)
	}
	if got := nodeOf(st, b).S("parent"); got != "root" {
		t.Errorf("restored B went to %q, want root", got)
	}
	// restoring the project brings its sub-entries back with it
	if _, err := st.Restore("test", p); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Patch("test", a, map[string]any{"name": "A2"}); err != nil {
		t.Errorf("A must be live again after its project is restored: %v", err)
	}
}
