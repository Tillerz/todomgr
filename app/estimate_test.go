package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Estimates from your own history, without AI; the AI only when there is no history.
func TestEstimate(t *testing.T) {
	a := testServer(t)
	done := func(name string, minutes float64, tags ...string) string {
		id := a.call(201, "POST", "/api/nodes", map[string]any{"name": name, "tags": tags})["id"].(string)
		a.call(200, "POST", "/api/nodes/"+id+"/time", map[string]any{"minutes": minutes})
		a.call(200, "POST", "/api/nodes/"+id+"/status", map[string]any{"status": "done"})
		return id
	}
	done("Write chapter 1", 55)
	done("Write chapter 2", 40)
	done("Buy milk", 10)
	est := func(id string, ai bool) map[string]any {
		return a.call(200, "POST", "/api/estimate", map[string]any{"id": id, "ai": ai})
	}

	// similar finished todos: the median of 55 and 40 is 47.5, rounded to 5 minutes; "Buy milk" does not match
	ch3 := a.call(201, "POST", "/api/nodes", map[string]any{"name": "Write chapter 3"})["id"].(string)
	r := est(ch3, false)
	if r["minutes"] != float64(50) || r["source"] != "history" || len(r["used"].([]any)) != 2 ||
		!strings.Contains(str(r["reason"]), "“Write chapter 1” (55 min)") || strings.Contains(str(r["reason"]), "milk") {
		t.Errorf("similar todos: %v", r)
	}

	// a tag alone is enough to count as similar
	done("Sketch the cover", 120, "art")
	art := a.call(201, "POST", "/api/nodes", map[string]any{"name": "Colour the map", "tags": []string{"art"}})["id"].(string)
	if r := est(art, false); r["minutes"] != float64(120) {
		t.Errorf("same tag: %v", r)
	}

	// a repeating todo: its own past, per time it was done (30 + 20 minutes over 2 times)
	day := a.s.st.Now().Format("2006-01-02")
	wk := a.call(201, "POST", "/api/nodes", map[string]any{"name": "Weekly review", "recur": "w", "deadline": day})["id"].(string)
	for _, m := range []float64{30, 20} {
		a.call(200, "POST", "/api/nodes/"+wk+"/time", map[string]any{"minutes": m})
		a.call(200, "POST", "/api/nodes/"+wk+"/status", map[string]any{"status": "done"})
	}
	if r := est(wk, false); r["minutes"] != float64(25) || !strings.Contains(str(r["reason"]), "last 2 times") {
		t.Errorf("repeating todo: %v", r)
	}

	// no history: nothing without AI, also when the page asks for the AI while AI is off
	fence := a.call(201, "POST", "/api/nodes", map[string]any{"name": "Paint the fence"})["id"].(string)
	if r := est(fence, true); r["minutes"] != float64(0) {
		t.Errorf("no history, AI off: %v", r)
	}

	// no history, AI on: the AI guesses (47 → 45), marked as such
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"minutes\":47,\"reason\":\"A small fence takes under an hour.\"}"}}]}`))
	}))
	defer fake.Close()
	h := a.s.ai
	h.dir = t.TempDir()
	t.Cleanup(h.unload)
	for _, f := range []string{"llama-server", "llama-server.exe", "m.gguf"} {
		_ = os.WriteFile(filepath.Join(h.dir, f), []byte("x"), 0o755)
	}
	h.start = func(ctx context.Context) (string, string, <-chan struct{}, func(), error) {
		return fake.URL, "k", make(chan struct{}), func() {}, nil
	}
	a.call(200, "PUT", "/api/settings", map[string]any{"ai": map[string]any{"enabled": true}})
	if r := est(fence, false); r["minutes"] != float64(0) {
		t.Errorf("history only (ai false): %v", r)
	}
	if r := est(fence, true); r["minutes"] != float64(45) || r["source"] != "ai" || r["reason"] != "A small fence takes under an hour." {
		t.Errorf("AI guess: %v", r)
	}
	a.call(404, "POST", "/api/estimate", map[string]any{"id": "nope"})
}

func TestEstRound(t *testing.T) {
	for in, want := range map[float64]int{1: 5, 12: 10, 13: 15, 59: 60, 70: 75, 100: 105, 239: 240, 250: 240, 280: 270} {
		if got := estRound(in); got != want {
			t.Errorf("estRound(%v) = %d, want %d", in, got, want)
		}
	}
}
