package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

// The AI with a fake helper: an HTTP server that answers like llama-server, so no model is needed.
func TestAI(t *testing.T) {
	a := testServer(t)
	h := a.s.ai
	h.dir = t.TempDir()
	t.Cleanup(h.unload)

	var mu sync.Mutex
	var asked []string // what the helper got: instructions and question
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []struct{ Role, Content string }
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if r.Header.Get("Authorization") != "Bearer k1" || len(req.Messages) != 2 {
			http.Error(w, "bad request", 401)
			return
		}
		mu.Lock()
		asked = append(asked, req.Messages[0].Content+"\n"+req.Messages[1].Content) // instructions and question
		mu.Unlock()
		answer := `{"text":"Hello"}`
		switch sys := req.Messages[0].Content; {
		case strings.Contains(sys, "plan tasks"):
			answer = `{"subtasks":["Write the outline","Existing one"," write the outline ","Book the room"]}`
		case strings.Contains(sys, "quick-add line"):
			answer = `{"quick":" r: Call Anna due:fri @anna ","notes":"Bring the photos."}`
		case strings.Contains(sys, "You sort entries"):
			answer = `{"tags":["writing","writing","nope"]}`
		case strings.Contains(sys, "tidy up"):
			answer = `{"rewritten_notes":"Clean text"}`
		case strings.Contains(sys, "list only what is missing"):
			answer = `{"next_steps_heading":"Next steps","next_steps":["Buy paint","Buy paint"],"to_prepare_heading":"To prepare","to_prepare":[],` +
				`"open_questions_heading":"Open questions","open_questions":["Which colour?","teh guests, the food"],"keep_in_mind_heading":"Keep in mind","keep_in_mind":[]}`
		}
		b, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": answer}}}})
		_, _ = w.Write(b)
	}))
	defer fake.Close()
	starts, stops := 0, 0
	var startErr error
	h.start = func(ctx context.Context) (string, string, <-chan struct{}, func(), error) {
		if startErr != nil {
			return "", "", nil, nil, startErr
		}
		starts++
		return fake.URL, "k1", make(chan struct{}), func() { stops++ }, nil
	}

	// files: the helper program and a model; mmproj files do not count as a model
	if st := a.call(200, "GET", "/api/ai", nil); st["ready"] != false || st["enabled"] != false {
		t.Fatalf("empty ai folder: %v", st)
	}
	bin := "llama-server"
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	for _, f := range []string{bin, "mmproj-F16.gguf", "Qwen3.5-4B-Q4_K_M.gguf"} {
		if err := os.WriteFile(filepath.Join(h.dir, f), []byte("x"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if st := a.call(200, "GET", "/api/ai", nil); st["ready"] != true || st["model"] != "Qwen3.5-4B-Q4_K_M.gguf" || st["helper"] != bin {
		t.Fatalf("ai folder with files: %v", st)
	}
	// the helper in the sub-folder that its download unpacks into
	h2 := &aiHelper{dir: t.TempDir()}
	if err := os.MkdirAll(filepath.Join(h2.dir, "llama-b1"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(h2.dir, "llama-b1", bin), []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	if st := h2.status(true); st["helper"] != "llama-b1/"+bin || st["ready"] != false {
		t.Errorf("helper in a sub-folder, no model: %v", st)
	}

	// off: no AI, and no AI buttons
	p := a.call(201, "POST", "/api/nodes", map[string]any{"name": "Plan the party", "notes": "teh guests, the food"})["id"].(string)
	a.call(201, "POST", "/api/nodes", map[string]any{"name": "Existing one", "parent": p})
	a.call(400, "POST", "/api/ai/subtasks", map[string]any{"id": p})
	if a.call(200, "GET", "/api/info", nil)["ai"] != false {
		t.Error("info.ai must be false while AI is off")
	}

	// on
	if st := a.call(200, "PUT", "/api/settings", map[string]any{"ai": map[string]any{"enabled": true}})["ai"].(map[string]any); st["enabled"] != true {
		t.Fatalf("settings ai: %v", st)
	}
	if a.call(200, "GET", "/api/info", nil)["ai"] != true {
		t.Error("info.ai must be true when AI is on and the files are there")
	}

	// sub-tasks: the existing one and a double are dropped; the question names the existing one
	items := a.call(200, "POST", "/api/ai/subtasks", map[string]any{"id": p})["items"].([]any)
	if len(items) != 2 || items[0] != "Write the outline" || items[1] != "Book the room" {
		t.Errorf("sub-tasks: %v", items)
	}
	if q := asked[len(asked)-1]; !strings.Contains(q, "Plan the party") || !strings.Contains(q, "- Existing one") || !strings.Contains(q, "still missing") {
		t.Errorf("sub-task question: %q", q)
	}

	// rewrite: tidy up (the default) and expand; expand also works without notes, from the name
	if r := a.call(200, "POST", "/api/ai/rewrite", map[string]any{"id": p}); r["text"] != "Clean text" {
		t.Errorf("tidy up: %v", r)
	}
	if q := asked[len(asked)-1]; !strings.Contains(q, "teh guests") || !strings.Contains(q, "Plan the party") {
		t.Errorf("tidy-up question: %q", q)
	}
	empty := a.call(201, "POST", "/api/nodes", map[string]any{"name": "Repaint the fence"})["id"].(string)
	a.call(400, "POST", "/api/ai/rewrite", map[string]any{"id": empty, "mode": "tidy"})
	if r := a.call(200, "POST", "/api/ai/rewrite", map[string]any{"id": empty, "mode": "expand"}); r["text"] != "## Next steps\n- [ ] Buy paint\n\n## Open questions\n- Which colour?\n- teh guests, the food" {
		t.Errorf("expand: %v", r)
	}
	if q := asked[len(asked)-1]; !strings.Contains(q, "Repaint the fence") || !strings.HasSuffix(q, "(none)") {
		t.Errorf("expand question: %q", q)
	}
	// with notes, the new sections go below the unchanged notes
	if r := a.call(200, "POST", "/api/ai/rewrite", map[string]any{"id": p, "mode": "expand"}); r["text"] != "teh guests, the food\n\n## Next steps\n- [ ] Buy paint\n\n## Open questions\n- Which colour?" {
		t.Errorf("expand below the notes: %q", r["text"])
	}
	a.call(400, "POST", "/api/ai/rewrite", map[string]any{"id": p, "mode": "poem"})

	// tags: only from the given list, without the entry's own tags, no doubles
	a.call(200, "PATCH", "/api/nodes/"+p, map[string]any{"tags": []string{"art"}})
	if r := a.call(200, "POST", "/api/ai/tags", map[string]any{"id": p, "tags": []string{"writing", "art", " "}}); fmt.Sprint(r["tags"]) != "[writing]" {
		t.Errorf("ai tags: %v", r)
	}
	if q := asked[len(asked)-1]; !strings.HasSuffix(q, "Tags to choose from: writing") {
		t.Errorf("tag question: %q", q)
	}

	// quick add in normal words: the AI writes a Quick add line, with today's date and the tags to use
	if r := a.call(200, "POST", "/api/ai/quick", map[string]any{"text": "remind me friday to call anna", "tags": []string{"family"}}); r["quick"] != "r: Call Anna due:fri @anna" || r["notes"] != "" {
		t.Errorf("ai quick, one line (no notes): %v", r)
	}
	q1 := asked[len(asked)-1]
	if r := a.call(200, "POST", "/api/ai/quick", map[string]any{"text": "remind me friday to call anna"}); !strings.HasPrefix(str(r["quick"]), "r: ") {
		t.Errorf("remind me must give a reminder: %v", r)
	}
	if r := a.call(200, "POST", "/api/ai/quick", map[string]any{"text": "Call Anna on friday\nShe has the photos."}); r["notes"] != "Bring the photos." {
		t.Errorf("ai quick, several lines (notes): %v", r)
	}
	asked = append(asked, q1)
	if q := asked[len(asked)-1]; !strings.Contains(q, "Today is "+a.s.st.Now().Format("Monday")) || !strings.Contains(q, "these tags, when it clearly fits: family") {
		t.Errorf("quick question: %q", q)
	}
	a.call(400, "POST", "/api/ai/quick", map[string]any{"text": "  "})

	// the helper starts once and stays; Unload stops it, the next question starts it again
	if starts != 1 || a.call(200, "GET", "/api/ai", nil)["running"] != true {
		t.Errorf("starts %d, want 1 and running", starts)
	}
	if st := a.call(200, "POST", "/api/ai/unload", nil); st["running"] != false || stops != 1 {
		t.Errorf("unload: %v, stops %d", st, stops)
	}
	if r := a.call(200, "POST", "/api/ai/test", nil); r["text"] != "Hello" || starts != 2 {
		t.Errorf("test after unload: %v, starts %d", r, starts)
	}

	// switching AI off stops the helper
	a.call(200, "PUT", "/api/settings", map[string]any{"ai": map[string]any{"enabled": false}})
	if stops != 2 {
		t.Errorf("off: stops %d, want 2", stops)
	}

	// two models: the first by name is used until another one is picked; picking another one unloads the AI
	a.call(200, "PUT", "/api/settings", map[string]any{"ai": map[string]any{"enabled": true}})
	if err := os.WriteFile(filepath.Join(h.dir, "Qwen3.5-9B-Q4_K_M.gguf"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if st := a.call(200, "GET", "/api/ai", nil); st["model"] != "Qwen3.5-4B-Q4_K_M.gguf" || len(st["models"].([]any)) != 2 {
		t.Errorf("two models: %v", st)
	}
	a.call(200, "POST", "/api/ai/test", nil) // loaded
	st := a.call(200, "PUT", "/api/settings", map[string]any{"ai": map[string]any{"model": "Qwen3.5-9B-Q4_K_M.gguf"}})["ai"].(map[string]any)
	if st["model"] != "Qwen3.5-9B-Q4_K_M.gguf" || st["running"] != false || stops != 3 || a.s.cfg.Get().AIModel != "Qwen3.5-9B-Q4_K_M.gguf" {
		t.Errorf("pick the other model: %v, stops %d", st, stops)
	}
	a.call(200, "POST", "/api/ai/test", nil)
	a.call(200, "PUT", "/api/settings", map[string]any{"ai": map[string]any{"model": "Qwen3.5-9B-Q4_K_M.gguf"}})
	if stops != 3 {
		t.Errorf("picking the same model again must not unload: stops %d", stops)
	}
	a.call(200, "POST", "/api/ai/unload", nil)

	// a helper that cannot start: its reason reaches the page
	a.call(200, "PUT", "/api/settings", map[string]any{"ai": map[string]any{"enabled": true}})
	startErr = errors.New("the AI helper stopped while starting: out of memory")
	if r := a.call(400, "POST", "/api/ai/test", nil); !strings.Contains(str(r["error"]), "out of memory") {
		t.Errorf("start error: %v", r)
	}
}

func TestFixQuick(t *testing.T) {
	for in, want := range map[string]string{
		"Pay the bills due:30d":           "Pay the bills due:+30d",
		"Kino due:tom at:20:00":           "Kino due:tomorrow at:20:00",
		"Kino due:tom":                    "Kino due:tomorrow",
		"r: Team call due:mon repeat:mon": "r: Team call due:mon repeat:w",
		"Water plants repeat:Weekly":      "Water plants repeat:w",
		"Rent due:+1w repeat:monthly":     "Rent due:+1w repeat:m",
		"Call Anna due:fri @anna":         "Call Anna due:fri @anna",
	} {
		if got := fixQuick(in); got != want {
			t.Errorf("fixQuick(%q) = %q, want %q", in, got, want)
		}
	}
}
