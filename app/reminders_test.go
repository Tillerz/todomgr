package main

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func nodeOf(st *Store, id string) Node {
	st.mu.RLock()
	defer st.mu.RUnlock()
	return Node(deepCopy(map[string]any(st.db.Nodes[id])).(map[string]any))
}

func at(st *Store, s string) time.Time {
	t, ok := parseWhen(s, st.loc)
	if !ok {
		panic("bad time " + s)
	}
	return t
}

func alertOf(n Node) (string, float64) {
	a := n.Obj("alert")
	if a == nil {
		return "", 0
	}
	return str(a["when"]), num(a["at"])
}

func TestReminderEntryWarnsThenFires(t *testing.T) {
	st, _ := testStore(t)
	id := mustCreate(t, st, "Stream", "", map[string]any{"kind": "reminder", "reminder": "2030-05-01T19:00", "lead": 15})

	st.checkReminders(at(st, "2030-05-01T18:30"))
	if w, _ := alertOf(nodeOf(st, id)); w != "" {
		t.Fatalf("nothing should fire 30 min before, got %q", w)
	}
	st.checkReminders(at(st, "2030-05-01T18:50"))
	if w, _ := alertOf(nodeOf(st, id)); w != "soon" {
		t.Fatalf("expected the early warning, got %q", w)
	}
	st.checkReminders(at(st, "2030-05-01T18:51")) // must not re-announce
	if err := st.AckAlert(id, "ok"); err != nil {
		t.Fatal(err)
	}
	st.checkReminders(at(st, "2030-05-01T18:52"))
	if w, _ := alertOf(nodeOf(st, id)); w != "" {
		t.Fatalf("an acknowledged warning came back: %q", w)
	}
	st.checkReminders(at(st, "2030-05-01T19:00"))
	n := nodeOf(st, id)
	if w, _ := alertOf(n); w != "now" {
		t.Fatalf("expected the pop-up at the time, got %q", w)
	}
	if n.S("reminder") != "2030-05-01T19:00" {
		t.Fatal("a one-off reminder must keep its time")
	}
	rev := st.db.Rev
	st.checkReminders(at(st, "2030-05-01T19:01"))
	if st.db.Rev != rev {
		t.Fatal("an idle check must not write anything")
	}
}

func TestRecurringReminderMovesOn(t *testing.T) {
	st, _ := testStore(t)
	id := mustCreate(t, st, "Weekly", "", map[string]any{"kind": "reminder", "reminder": "2030-05-01T19:00", "recur": "w"})
	st.checkReminders(at(st, "2030-05-01T19:00"))
	n := nodeOf(st, id)
	if n.S("reminder") != "2030-05-08T19:00" {
		t.Fatalf("expected the next week, got %s", n.S("reminder"))
	}
	if w, a := alertOf(n); w != "now" || a != float64(at(st, "2030-05-01T19:00").UnixMilli()) {
		t.Fatalf("the alert must describe the occurrence that fired, got %q %v", w, a)
	}
	// the app was off for three weeks: one pop-up, and the next occurrence is in the future
	st.checkReminders(at(st, "2030-05-22T10:00"))
	n = nodeOf(st, id)
	if n.S("reminder") != "2030-05-22T19:00" {
		t.Fatalf("expected to catch up to 2030-05-22, got %s", n.S("reminder"))
	}
}

func TestSnoozeAndTaskReminder(t *testing.T) {
	st, _ := testStore(t)
	rid := mustCreate(t, st, "Call", "", map[string]any{"kind": "reminder", "reminder": "2030-05-01T09:00"})
	tid := mustCreate(t, st, "Pay invoice", "", map[string]any{"reminder": "2030-05-01T08:00"})

	st.checkReminders(at(st, "2030-05-01T08:00"))
	if w, _ := alertOf(nodeOf(st, tid)); w != "task" {
		t.Fatalf("a todo with a reminder time must raise a task alert, got %q", w)
	}
	if err := st.AckAlert(tid, "ok"); err != nil {
		t.Fatal(err)
	}
	st.checkReminders(at(st, "2030-05-01T08:30"))
	if w, _ := alertOf(nodeOf(st, tid)); w != "" {
		t.Fatal("a task reminder fires only once")
	}

	st.checkReminders(at(st, "2030-05-01T09:00"))
	if err := st.AckAlert(rid, "snooze"); err != nil {
		t.Fatal(err)
	}
	n := nodeOf(st, rid)
	if w, _ := alertOf(n); w != "" || n.N("snoozeUntil") == 0 {
		t.Fatalf("snooze must clear the alert and set snoozeUntil: %v", n)
	}
	st.checkReminders(time.Now().Add(snoozeMinutes*time.Minute + time.Minute))
	if w, _ := alertOf(nodeOf(st, rid)); w != "snoozed" {
		t.Fatalf("expected the snoozed alert, got %q", w)
	}
	// changing the time re-arms everything
	if _, err := st.Patch("test", tid, map[string]any{"reminder": "2030-06-01T08:00"}); err != nil {
		t.Fatal(err)
	}
	if nodeOf(st, tid)["remDone"] != nil {
		t.Fatal("a new reminder time must re-arm the reminder")
	}
}

func TestClosedTaskDoesNotRemind(t *testing.T) {
	st, _ := testStore(t)
	id := mustCreate(t, st, "Done already", "", map[string]any{"reminder": "2030-05-01T08:00"})
	if _, err := st.SetStatus("test", id, "done"); err != nil {
		t.Fatal(err)
	}
	st.checkReminders(at(st, "2030-05-02T08:00"))
	if w, _ := alertOf(nodeOf(st, id)); w != "" {
		t.Fatal("finished tasks must not remind")
	}
}

func TestDiscordAnnounce(t *testing.T) {
	st, _ := testStore(t)
	var got []string
	old := discordSend
	discordSend = func(hook, content string) error {
		got = append(got, hook+"|"+content)
		return nil
	}
	defer func() { discordSend = old }()

	hook := "https://discord.com/api/webhooks/123456/abc-DEF_1"
	pid := mustCreate(t, st, "World", "", map[string]any{"kind": "project", "webhook": hook})
	sub := mustCreate(t, st, "Chapter", pid, nil)
	deep := mustCreate(t, st, "Scene", sub, nil)
	loose := mustCreate(t, st, "Loose", "", nil)

	if err := st.Announce(deep, "Scene 1 finished", "example.com/s1"); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != hook+"|Scene 1 finished\nhttps://example.com/s1" {
		t.Fatalf("unexpected post: %v", got)
	}
	if err := st.Announce(pid, "Test", ""); err != nil || got[1] != hook+"|Test" {
		t.Fatalf("a project may post to itself: %v %v", err, got)
	}
	if err := st.Announce(loose, "x", ""); err == nil || !strings.Contains(err.Error(), "no Discord webhook") {
		t.Fatalf("an entry outside any project must be refused, got %v", err)
	}
	if err := st.Announce(deep, "  ", ""); err == nil {
		t.Fatal("an empty title must be refused")
	}
	if err := st.Announce(deep, "x", "javascript:alert(1)"); err == nil {
		t.Fatal("only http(s) links may be posted")
	}
	if err := st.Announce("nope", "x", ""); err == nil {
		t.Fatal("unknown entry must be refused")
	}
	if len(got) != 2 {
		t.Fatalf("refused announcements must not be sent: %v", got)
	}
}

func TestReminderPostsToDiscordWhenAsked(t *testing.T) {
	st, _ := testStore(t)
	sent := make(chan string, 4)
	old := discordSend
	discordSend = func(hook, content string) error { sent <- content; return nil }
	defer func() { discordSend = old }()

	pid := mustCreate(t, st, "Show", "", map[string]any{"kind": "project", "webhook": "https://discord.com/api/webhooks/1/x"})
	mustCreate(t, st, "Live stream", pid, map[string]any{"kind": "reminder", "reminder": "2030-05-01T19:00", "discord": true, "url": "https://example.com/live"})
	mustCreate(t, st, "Silent", pid, map[string]any{"kind": "reminder", "reminder": "2030-05-01T19:00"})
	mustCreate(t, st, "Late", pid, map[string]any{"kind": "reminder", "reminder": "2030-04-01T19:00", "discord": true})

	st.checkReminders(at(st, "2030-05-01T19:00"))
	select {
	case c := <-sent:
		if c != "⏰ Live stream\nhttps://example.com/live" {
			t.Fatalf("unexpected message %q", c)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the reminder was not posted")
	}
	select {
	case c := <-sent:
		t.Fatalf("only the reminder with the Discord switch that is on time may be posted, also got %q", c)
	case <-time.After(300 * time.Millisecond):
	}
}

func TestResetKeepsPrefsAndBackup(t *testing.T) {
	st, dir := testStore(t)
	mustCreate(t, st, "Old", "", nil)
	if _, err := st.Mutate("test", "", func(tx *Tx) error { tx.db.Prefs["theme"] = "dark"; return nil }); err != nil {
		t.Fatal(err)
	}
	if err := st.Reset(); err != nil {
		t.Fatal(err)
	}
	st.mu.RLock()
	count, theme := len(st.db.Nodes), st.db.Prefs["theme"]
	st.mu.RUnlock()
	if count != 1 || theme != "dark" {
		t.Fatalf("expected only the root and the kept settings, got %d nodes, theme %v", count, theme)
	}
	if m, _ := filepath.Glob(filepath.Join(dir, "backups", "pre-reset-*.json")); len(m) != 1 {
		t.Fatal("a pre-reset backup must exist")
	}
	if m, _ := filepath.Glob(filepath.Join(dir, "activity.before-reset-*.jsonl")); len(m) != 1 {
		t.Fatal("the old activity log must be kept aside")
	}
	if len(st.activity) != 0 {
		t.Fatal("statistics must start fresh")
	}
	mustCreate(t, st, "New", "", nil) // the store keeps working
}

func TestRedactSecrets(t *testing.T) {
	in := map[string]any{"items": []map[string]any{{"name": "a", "webhook": "https://discord.com/api/webhooks/1/x"}, {"webhook": ""}}}
	out := redactSecrets(in).(map[string]any)
	items := out["items"].([]any)
	if items[0].(map[string]any)["webhook"] != "(set)" {
		t.Fatal("webhook must be hidden")
	}
	if items[1].(map[string]any)["webhook"] != "" {
		t.Fatal("an empty webhook stays empty")
	}
}

// a reminder inside a project in the trash must not fire (only the project has the trash mark)
func TestReminderInTrashedProjectIsQuiet(t *testing.T) {
	st, _ := testStore(t)
	p := mustCreate(t, st, "Old import", "", map[string]any{"kind": "project"})
	id := mustCreate(t, st, "Stream", p, map[string]any{"kind": "reminder", "reminder": "2030-05-01T19:00"})
	if err := st.Trash("test", p); err != nil {
		t.Fatal(err)
	}
	st.checkReminders(at(st, "2030-05-01T19:00"))
	if w, _ := alertOf(nodeOf(st, id)); w != "" {
		t.Fatalf("a reminder in the trash fired: %q", w)
	}
}
