package main

import "testing"

// a new data folder gets the defaults from defaults.go, and the page gets its defaults from the program
func TestDefaults(t *testing.T) {
	cfg, err := LoadConfig(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	c := cfg.Get()
	if c.Port != DefaultPort || c.BackupIntervalMin != DefaultBackupIntervalMin || c.LANEnabled != DefaultLANEnabled ||
		c.MCPEnabled != DefaultMCPEnabled || c.MCPReadOnly != DefaultMCPReadOnly || (c.CalKey != "") != DefaultCalendarFeed {
		t.Errorf("new config %+v does not match defaults.go", c)
	}
	a := testServer(t)
	d, _ := a.call(200, "GET", "/api/info", nil)["defaults"].(map[string]any)
	for _, k := range []string{"style", "theme", "font", "fontSize", "rowH", "detailW", "wide", "workDay", "tagIcons"} {
		if _, ok := d[k]; !ok {
			t.Errorf("/api/info defaults lack %q: %v", k, d)
		}
	}
}
