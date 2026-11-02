package main

import (
	"strings"
	"testing"
	"time"
)

// importedKids returns the entries below id by name.
func importedKids(t *testing.T, st *Store, id string) map[string]Node {
	t.Helper()
	st.mu.RLock()
	defer st.mu.RUnlock()
	out := map[string]Node{}
	for _, c := range st.db.Nodes[id].Kids() {
		n := st.db.Nodes[c]
		out[n.S("name")] = n
	}
	return out
}

func TestImportICS(t *testing.T) {
	st, _ := testStore(t)
	st.loc = time.FixedZone("UTC+5:30", 5*3600+1800) // not UTC on any machine, so the conversion is tested
	now := st.Now()
	next := now.AddDate(0, 0, 10)
	past := now.AddDate(0, 0, -10)
	ics := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\n" +
		// child before its parent: the tree must still be built
		"BEGIN:VTODO\r\nUID:child\r\nRELATED-TO;RELTYPE=PARENT:parent\r\nSUMMARY:Chapter 1\\, draft\r\nSTATUS:COMPLETED\r\nEND:VTODO\r\n" +
		"BEGIN:VTODO\r\nUID:parent\r\nSUMMARY:Novel\r\nDESCRIPTION:line one\\nline two that is long enough to be fol\r\n ded\r\n" +
		"DUE:20301224T130000Z\r\nPRIORITY:1\r\nCATEGORIES:writing,book\\,s\r\nPERCENT-COMPLETE:40\r\nLOCATION:Desk\r\nURL:https://example.com/n\r\n" +
		"BEGIN:VALARM\r\nACTION:DISPLAY\r\nTRIGGER;VALUE=DATE-TIME:20200101T090000Z\r\nEND:VALARM\r\nEND:VTODO\r\n" +
		"BEGIN:VEVENT\r\nUID:e1\r\nSUMMARY:Stream\r\nDTSTART:" + next.Format("20060102") + "T190000\r\n" +
		"BEGIN:VALARM\r\nACTION:DISPLAY\r\nTRIGGER:-PT30M\r\nEND:VALARM\r\nEND:VEVENT\r\n" +
		"BEGIN:VEVENT\r\nUID:e2\r\nSUMMARY:Old meeting\r\nDTSTART:" + past.Format("20060102") + "T100000\r\nEND:VEVENT\r\n" +
		"BEGIN:VEVENT\r\nUID:e3\r\nSUMMARY:Weekly call\r\nDTSTART:" + past.Format("20060102") + "T100000\r\nRRULE:FREQ=WEEKLY\r\nEND:VEVENT\r\n" +
		"BEGIN:VEVENT\r\nUID:e3\r\nRECURRENCE-ID:" + past.Format("20060102") + "T100000\r\nSUMMARY:Weekly call (moved)\r\nDTSTART:" + past.Format("20060102") + "T110000\r\nEND:VEVENT\r\n" +
		"BEGIN:VEVENT\r\nUID:e4\r\nSUMMARY:Cancelled\r\nSTATUS:CANCELLED\r\nDTSTART:" + next.Format("20060102") + "\r\nEND:VEVENT\r\n" +
		"BEGIN:VEVENT\r\nUID:e5\r\nSUMMARY:Every 2 weeks\r\nDTSTART;VALUE=DATE:" + next.Format("20060102") + "\r\nRRULE:FREQ=WEEKLY;INTERVAL=2\r\nEND:VEVENT\r\n" +
		"END:VCALENDAR\r\n"
	res, err := st.ImportEntries("test", "", "cal.ics", []byte(ics))
	if err != nil {
		t.Fatal(err)
	}
	if res["added"] != 5 || res["skipped"] != 3 || !strings.HasPrefix(str(res["name"]), "Import: cal.ics, ") {
		t.Fatalf("result %v, want 5 added, 3 skipped (old meeting, moved date, cancelled)", res)
	}
	top := importedKids(t, st, str(res["id"]))
	novel := top["Novel"]
	if novel == nil || len(top) != 4 {
		t.Fatalf("top level %v", top)
	}
	if got := novel.S("deadline"); got != time.Date(2030, 12, 24, 13, 0, 0, 0, time.UTC).In(st.loc).Format("2006-01-02T15:04") {
		t.Errorf("deadline %q (UTC must become local)", got)
	}
	if novel.N("prio") != 4 || strings.Join(strs(novel["tags"]), "|") != "writing|book,s" || novel.S("location") != "Desk" || novel.S("url") != "https://example.com/n" {
		t.Errorf("fields: prio %v tags %v location %q url %q", novel["prio"], novel["tags"], novel.S("location"), novel.S("url"))
	}
	if n := novel.S("notes"); !strings.HasPrefix(n, "line one\nline two that is long enough to be folded") || !strings.Contains(n, "Alarm: ") || novel.S("reminder") != "" {
		t.Errorf("notes %q, reminder %q (a past alarm goes into the notes)", n, novel.S("reminder"))
	}
	if num(novel.Obj("prog")["done"]) != 40 {
		t.Errorf("progress %v", novel["prog"])
	}
	kids := importedKids(t, st, novel.S("id"))
	if c := kids["Chapter 1, draft"]; c == nil || c.S("status") != "done" {
		t.Errorf("child of Novel: %v", kids)
	}
	if s := top["Stream"]; s == nil || s.S("kind") != "reminder" || s.S("reminder") != next.Format("2006-01-02")+"T19:00" || s.N("lead") != 30 {
		t.Errorf("event: %v", s)
	}
	w := top["Weekly call"]
	if w == nil || w.S("recur") != "w" || isPast(w.S("reminder"), st.loc, now) {
		t.Errorf("repeating event must move to its next date: %v", w)
	}
	if e := top["Every 2 weeks"]; e == nil || !strings.Contains(e.S("notes"), "INTERVAL=2") {
		t.Errorf("a rule the app cannot repeat goes into the notes: %v", e)
	}

	if _, err := st.ImportEntries("test", "ics", "x.ics", []byte("hello")); err == nil {
		t.Error("a file that is no calendar must fail")
	}
}

func TestImportCSVRoundTrip(t *testing.T) {
	st, _ := testStore(t)
	p := mustCreate(t, st, "Book", "", map[string]any{"kind": "project"})
	a := mustCreate(t, st, "=SUM(A1)", p, map[string]any{"prio": 3, "tags": "a,b", "people": "Anna", "deadline": "2030-05-01T14:00",
		"notes": "line1\nline2", "est": 30, "recur": "w"})
	// progress is exported for entries without sub-entries only (otherwise it comes from them)
	mustCreate(t, st, "Sub", a, map[string]any{"kind": "idea", "status": "exploring", "prog": map[string]any{"mode": "units", "done": 450, "total": 1200, "unit": "words"}})

	res, err := st.ImportEntries("test", "csv", "plan.csv", st.ExportCSV(true))
	if err != nil {
		t.Fatal(err)
	}
	if res["added"] != 3 || res["skipped"] != 0 {
		t.Fatalf("result %v", res)
	}
	book := importedKids(t, st, str(res["id"]))["Book"]
	if book == nil || book.S("kind") != "project" {
		t.Fatalf("Book not rebuilt: %v", book)
	}
	x := importedKids(t, st, book.S("id"))["=SUM(A1)"]
	if x == nil {
		t.Fatal("the ' before = must be removed again")
	}
	if x.N("prio") != 3 || strings.Join(strs(x["tags"]), ",") != "a,b" || strings.Join(strs(x["people"]), ",") != "Anna" ||
		x.S("deadline") != "2030-05-01T14:00" || x.S("notes") != "line1\nline2" || x.N("est") != 30 || x.S("recur") != "w" {
		t.Errorf("fields not kept: %v", x)
	}
	s := importedKids(t, st, x.S("id"))["Sub"]
	if s == nil || s.S("kind") != "idea" || s.S("status") != "exploring" {
		t.Fatalf("sub-entry: %v", s)
	}
	if pr := s.Obj("prog"); pr["mode"] != "units" || num(pr["done"]) != 450 || num(pr["total"]) != 1200 || pr["unit"] != "words" {
		t.Errorf("progress: %v", pr)
	}
}

func TestImportCSVOtherTools(t *testing.T) {
	st, _ := testStore(t)
	csv := "\xEF\xBB\xBFTitle;Due Date;Labels;Completed;Priority;Description\r\n" +
		"Buy paper;05.11.2030;office, shop;yes;High;for the printer\r\n" +
		"Call printer shop;someday soon;;no;5;\r\n" +
		";;;;;\r\n" +
		";2030-01-01;;;;row without a name\r\n"
	res, err := st.ImportEntries("test", "csv", "excel.csv", []byte(csv))
	if err != nil {
		t.Fatal(err)
	}
	if res["added"] != 2 || res["skipped"] != 1 {
		t.Fatalf("result %v, want 2 added, 1 skipped (empty rows are ignored)", res)
	}
	top := importedKids(t, st, str(res["id"]))
	b := top["Buy paper"]
	if b == nil || b.S("deadline") != "2030-11-05" || b.S("status") != "done" || b.N("prio") != 3 ||
		strings.Join(strs(b["tags"]), ",") != "office,shop" || b.S("notes") != "for the printer" {
		t.Errorf("Buy paper: %v", b)
	}
	c := top["Call printer shop"]
	if c == nil || c.S("deadline") != "" || !strings.Contains(c.S("notes"), "Due: someday soon") || !strings.Contains(c.S("notes"), "Priority: 5") {
		t.Errorf("values that do not fit go into the notes: %v", c)
	}

	// German Excel: Windows-1252, not UTF-8
	if len(cp1252) != 32 || fromWindows1252([]byte{0x80, 0x84, 0x93, 0x94, 0x96, 0x9F, 0xE4, 0xF6, 0xFC, 0xDF, 0xC4}) != "€„“”–ŸäöüßÄ" {
		t.Errorf("Windows-1252 table: %d entries, %q", len(cp1252), fromWindows1252([]byte{0x80, 0x84, 0x93, 0x94, 0x96, 0x9F, 0xE4, 0xF6, 0xFC, 0xDF, 0xC4}))
	}
	res, err = st.ImportEntries("test", "csv", "win.csv", []byte("Name;Notes\r\nM\xfcller anrufen;Gr\xfc\xdfe \x80 5\r\n"))
	if err != nil {
		t.Fatal(err)
	}
	if m := importedKids(t, st, str(res["id"]))["Müller anrufen"]; m == nil || m.S("notes") != "Grüße € 5" {
		t.Errorf("Windows-1252 file: %v", importedKids(t, st, str(res["id"])))
	}

	if _, err := st.ImportEntries("test", "csv", "x.csv", []byte("foo,bar\n1,2\n")); err == nil || !strings.Contains(err.Error(), "no name column") {
		t.Errorf("missing name column: %v", err)
	}
	if _, err := st.ImportEntries("test", "", "x.txt", []byte("a")); err == nil {
		t.Error("unknown format must fail")
	}
}
