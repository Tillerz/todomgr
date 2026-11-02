package main

import (
	"encoding/json"
	"os"
	"testing"
	"time"
)

// testdata/recur_cases.json is shared with notes_test.js, so the server and the page
// compute the same next dates.
func TestNextOcc(t *testing.T) {
	raw, err := os.ReadFile("testdata/recur_cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var f struct{ Cases [][3]string }
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	loc := time.FixedZone("UTC+1", 3600)
	for _, c := range f.Cases {
		want := c[2]
		if want == "" {
			want = c[0]
		}
		if got := nextOcc(c[0], c[1], loc); got != want {
			t.Errorf("nextOcc(%q, %q) = %q, want %q", c[0], c[1], got, want)
		}
	}
}

func TestRecurRules(t *testing.T) {
	for in, want := range map[string]string{"": "", "weekly": "w", "1w": "w", " 2W ": "2w", "m:1FR,3fr": "m:1fr,3fr", "m:-1su": "m:-1su"} {
		if got, ok := normRecur(in); !ok || got != want {
			t.Errorf("normRecur(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
	for _, in := range []string{"x", "0d", "1000w", "m:", "m:6fr", "m:0fr", "m:1xx", "2m:1fr", "biweekly"} {
		if _, ok := normRecur(in); ok {
			t.Errorf("normRecur(%q) must be refused", in)
		}
	}
	for r, want := range map[string]string{"w": "FREQ=WEEKLY", "1w": "FREQ=WEEKLY", "2w": "FREQ=WEEKLY;INTERVAL=2", "m:1fr,-1fr": "FREQ=MONTHLY;BYDAY=1FR,-1FR", "": ""} {
		if got := recurRRULE(r); got != want {
			t.Errorf("recurRRULE(%q) = %q, want %q", r, got, want)
		}
	}
	// the export's rule comes back unchanged through the import
	for _, r := range []string{"d", "3d", "2w", "6m", "2y", "m:1fr,3fr,5fr", "m:-1su"} {
		if got, exact, _ := icsRule(recurRRULE(r), time.UTC); got != r || !exact {
			t.Errorf("icsRule(recurRRULE(%q)) = %q, exact %v", r, got, exact)
		}
	}
}
