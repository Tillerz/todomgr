package main

import (
	"strings"
	"testing"
	"time"
)

func TestEventEnd(t *testing.T) {
	st, _ := testStore(t)
	ev := map[string]any{"kind": "reminder", "reminder": "2030-12-05T19:00", "end": "2030-12-05T21:00"}
	id := mustCreate(t, st, "Stream", "", ev)
	if _, err := st.Patch("test", id, map[string]any{"end": "2030-12-05T18:00"}); err == nil {
		t.Error("an end before the start must be refused")
	}
	// moving the start keeps the length; start and end in one change take both
	if n, err := st.Patch("test", id, map[string]any{"reminder": "2030-12-06T20:30"}); err != nil || n["end"] != "2030-12-06T22:30" {
		t.Errorf("end after moving the start: %v %v", n["end"], err)
	}
	if n, err := st.Patch("test", id, map[string]any{"reminder": "2030-12-07T10:00", "end": "2030-12-09"}); err != nil || n["end"] != "2030-12-09" {
		t.Errorf("start and end together: %v %v", n["end"], err)
	}
	// across the switch to summer time the clock times stay
	dst := Node{"end": "2027-03-27T21:00"}
	if shiftEnd(dst, "2027-03-27T19:00", "2027-04-03T19:00"); dst.S("end") != "2027-04-03T21:00" {
		t.Errorf("summer time: %s", dst.S("end"))
	}
	// a repeating event moves on with its end
	n := Node{"kind": "reminder", "reminder": "2030-12-05T19:00", "end": "2030-12-05T21:00", "recur": "w"}
	stepReminder(n, time.Date(2030, 12, 5, 19, 0, 30, 0, time.UTC), time.UTC)
	if n.S("reminder") != "2030-12-12T19:00" || n.S("end") != "2030-12-12T21:00" {
		t.Errorf("repeat: %s – %s", n.S("reminder"), n.S("end"))
	}
}

func TestEventToday(t *testing.T) {
	st, _ := testStore(t)
	now := st.Now()
	day := func(d int) string { return now.AddDate(0, 0, d).Format("2006-01-02") }
	mustCreate(t, st, "Convention", "", map[string]any{"kind": "reminder", "reminder": day(-1), "end": day(1)})
	mustCreate(t, st, "Over", "", map[string]any{"kind": "reminder", "reminder": day(-3), "end": day(-2)})
	var names []string
	for _, v := range st.Today()["upcomingReminders"].([]map[string]any) {
		names = append(names, str(v["name"]))
	}
	if strings.Join(names, ",") != "Convention" {
		t.Errorf("Today shows a running event, not an ended one: %v", names)
	}
}

func TestEventExportImport(t *testing.T) {
	st, _ := testStore(t)
	mustCreate(t, st, "Convention", "", map[string]any{"kind": "reminder", "reminder": "2030-12-05", "end": "2030-12-07"})
	mustCreate(t, st, "Stream", "", map[string]any{"kind": "reminder", "reminder": "2030-12-05T19:00", "end": "2030-12-05T21:00"})
	ics := string(st.ExportICS(false))
	for _, want := range []string{"DTSTART;VALUE=DATE:20301205\r\nDTEND;VALUE=DATE:20301208", "DTSTART:20301205T190000\r\nDTEND:20301205T210000"} {
		if !strings.Contains(ics, want) {
			t.Errorf("export lacks %q", want)
		}
	}
	for _, f := range []struct{ format, name string }{{"ics", "cal.ics"}, {"csv", "plan.csv"}} {
		data := []byte(ics)
		if f.format == "csv" {
			data = st.ExportCSV(true)
		}
		res, err := st.ImportEntries("test", f.format, f.name, data)
		if err != nil {
			t.Fatal(err)
		}
		got := importedKids(t, st, str(res["id"]))
		if c := got["Convention"]; c == nil || c.S("reminder") != "2030-12-05" || c.S("end") != "2030-12-07" {
			t.Errorf("%s: all-day event %v", f.format, c)
		}
		if s := got["Stream"]; s == nil || s.S("reminder") != "2030-12-05T19:00" || s.S("end") != "2030-12-05T21:00" {
			t.Errorf("%s: timed event %v", f.format, s)
		}
	}
}

func TestEventImportICS(t *testing.T) {
	st, _ := testStore(t)
	now := st.Now()
	d := func(n int) string { return now.AddDate(0, 0, n).Format("20060102") }
	ics := "BEGIN:VCALENDAR\r\n" +
		// started yesterday, ends tomorrow: still running, so it is imported
		"BEGIN:VEVENT\r\nUID:a\r\nSUMMARY:Running\r\nDTSTART;VALUE=DATE:" + d(-1) + "\r\nDTEND;VALUE=DATE:" + d(2) + "\r\nEND:VEVENT\r\n" +
		"BEGIN:VEVENT\r\nUID:b\r\nSUMMARY:One day\r\nDTSTART;VALUE=DATE:" + d(3) + "\r\nDTEND;VALUE=DATE:" + d(4) + "\r\nEND:VEVENT\r\n" +
		"BEGIN:VEVENT\r\nUID:c\r\nSUMMARY:Two hours\r\nDTSTART:" + d(3) + "T100000\r\nDURATION:PT2H\r\nEND:VEVENT\r\n" +
		"END:VCALENDAR\r\n"
	res, err := st.ImportEntries("test", "ics", "x.ics", []byte(ics))
	if err != nil {
		t.Fatal(err)
	}
	got := importedKids(t, st, str(res["id"]))
	day := func(n int) string { return now.AddDate(0, 0, n).Format("2006-01-02") }
	if r := got["Running"]; r == nil || r.S("reminder") != day(-1) || r.S("end") != day(1) {
		t.Errorf("running event: %v", r)
	}
	if o := got["One day"]; o == nil || o.S("end") != "" {
		t.Errorf("a one-day all-day event has no end: %v", o)
	}
	if h := got["Two hours"]; h == nil || h.S("end") != day(3)+"T12:00" {
		t.Errorf("DURATION: %v", h)
	}
}

// "free" events (they do not block time in the day plan) are TRANSP:TRANSPARENT in calendar files
func TestEventFreeTime(t *testing.T) {
	st, _ := testStore(t)
	mustCreate(t, st, "Novelember", "", map[string]any{"kind": "reminder", "reminder": "2030-11-01T00:00", "end": "2030-11-30T23:00", "free": true})
	mustCreate(t, st, "Convention", "", map[string]any{"kind": "reminder", "reminder": "2030-11-05T09:00", "end": "2030-11-07T18:00"})
	ics := st.ExportICS(false)
	if n := strings.Count(string(ics), "TRANSP:TRANSPARENT"); n != 1 {
		t.Fatalf("TRANSP:TRANSPARENT %d times, want 1:\n%s", n, ics)
	}
	r, err := parseICSImport(ics, st.loc, st.Now())
	if err != nil {
		t.Fatal(err)
	}
	free := map[string]any{}
	for _, it := range r.items {
		free[it.name] = it.f["free"]
	}
	if free["Novelember"] != true || free["Convention"] != nil {
		t.Fatalf("free after import: %v", free)
	}
	if _, err := st.Create("test", "Bad", "", -1, map[string]any{"free": "yes"}); err == nil {
		t.Fatal("free must be true or false")
	}
}
