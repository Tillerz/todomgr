package main

import (
	csvfmt "encoding/csv"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestExportCSV(t *testing.T) {
	st, _ := testStore(t)
	p := mustCreate(t, st, "Novel, \"Ashen\" Crown", "", map[string]any{"kind": "project"})
	a := mustCreate(t, st, "=SUM(A1)", p, map[string]any{"prio": 3, "tags": "a,b", "deadline": "2030-05-01", "notes": "line1\nline2", "est": 30})
	b := mustCreate(t, st, "Finished", p, nil)
	if _, err := st.SetStatus("test", b, "done"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.LogTime("test", a, 90*60); err != nil {
		t.Fatal(err)
	}

	raw := st.ExportCSV(true)
	if !strings.HasPrefix(string(raw), "\xEF\xBB\xBF") {
		t.Fatal("missing byte order mark")
	}
	rows, err := csvfmt.NewReader(strings.NewReader(strings.TrimPrefix(string(raw), "\xEF\xBB\xBF"))).ReadAll()
	if err != nil {
		t.Fatalf("not valid CSV: %v", err)
	}
	if len(rows) != 4 { // header + 3 entries
		t.Fatalf("expected 4 rows, got %d", len(rows))
	}
	col := map[string]int{}
	for i, h := range rows[0] {
		col[h] = i
	}
	if rows[1][col["name"]] != `Novel, "Ashen" Crown` || rows[1][col["type"]] != "project" {
		t.Fatalf("quoting broke: %v", rows[1])
	}
	r := rows[2]
	if r[col["name"]] != "'=SUM(A1)" {
		t.Fatalf("formula must be neutralised, got %q", r[col["name"]])
	}
	if r[col["priority"]] != "high" || r[col["tags"]] != "a, b" || r[col["deadline"]] != "2030-05-01" ||
		r[col["tracked_min"]] != "90" || r[col["estimate_min"]] != "30" || r[col["notes"]] != "line1\nline2" || r[col["parent_id"]] != p {
		t.Fatalf("unexpected row %v", r)
	}
	if rows[3][col["closed"]] == "" || rows[3][col["progress_percent"]] != "100" {
		t.Fatalf("done entry: %v", rows[3])
	}
	if open := string(st.ExportCSV(false)); strings.Contains(open, "Finished") {
		t.Fatal("closed entries must be left out when asked")
	}
}

func TestExportICS(t *testing.T) {
	st, _ := testStore(t)
	long := strings.Repeat("Straße ", 30)
	mustCreate(t, st, "Weekly stream; live, loud", "", map[string]any{"kind": "reminder", "reminder": "2030-05-01T19:00", "recur": "w", "lead": 15, "url": "https://example.com/live"})
	mustCreate(t, st, "Pay invoice", "", map[string]any{"deadline": "2030-05-03", "prio": 4, "reminder": "2030-05-02T09:30", "notes": long, "tags": "money"})
	done := mustCreate(t, st, "Old", "", map[string]any{"deadline": "2030-01-01"})
	if _, err := st.SetStatus("test", done, "done"); err != nil {
		t.Fatal(err)
	}
	mustCreate(t, st, "No date at all", "", nil)

	ics := string(st.ExportICS(false))
	for _, want := range []string{
		"BEGIN:VCALENDAR\r\n", "END:VCALENDAR\r\n", "VERSION:2.0\r\n",
		"BEGIN:VEVENT\r\n", "DTSTART:20300501T190000\r\n", "RRULE:FREQ=WEEKLY\r\n", `SUMMARY:Weekly stream\; live\, loud` + "\r\n",
		"TRIGGER:-PT15M\r\n", "TRIGGER:PT0S\r\n", "URL:https://example.com/live\r\n",
		"BEGIN:VTODO\r\n", "DUE;VALUE=DATE:20300503\r\n", "PRIORITY:1\r\n", "STATUS:NEEDS-ACTION\r\n", "CATEGORIES:money\r\n", "TRIGGER;VALUE=DATE-TIME:",
	} {
		if !strings.Contains(ics, want) {
			t.Errorf("missing %q in:\n%s", want, ics)
		}
	}
	if strings.Contains(ics, "Old") || strings.Contains(ics, "No date at all") {
		t.Fatal("closed entries and entries without a date must not be exported")
	}
	if strings.Count(ics, "BEGIN:VEVENT") != 1 || strings.Count(ics, "BEGIN:VTODO") != 1 {
		t.Fatalf("expected one event and one task:\n%s", ics)
	}
	for _, line := range strings.Split(ics, "\r\n") {
		if len(line) > 75 {
			t.Fatalf("line longer than 75 octets: %d", len(line))
		}
		if !utf8.ValidString(line) {
			t.Fatal("a fold split a character")
		}
	}
	// unfolded, the long note is intact
	if !strings.Contains(strings.ReplaceAll(ics, "\r\n ", ""), strings.TrimSpace(long)) {
		t.Fatal("folding lost text")
	}
	if all := string(st.ExportICS(true)); !strings.Contains(all, "STATUS:COMPLETED") || !strings.Contains(all, "COMPLETED:") {
		t.Fatal("closed entries must be exported on request")
	}
}
