package main

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
)

// Config is data/config.json: server settings, kept next to the data so the
// whole folder stays portable.
type ConfigData struct {
	Port              int    `json:"port"`
	Token             string `json:"token"`
	MCPEnabled        bool   `json:"mcpEnabled"`  // OFF by default
	MCPReadOnly       bool   `json:"mcpReadOnly"` // when on, MCP can read but not change anything
	BackupIntervalMin int    `json:"backupIntervalMin"`
	Timezone          string `json:"timezone"`   // IANA name; empty = system time zone
	CalKey            string `json:"calKey"`     // calendar subscription key; empty = off (the default)
	LANEnabled        bool   `json:"lanEnabled"` // OFF by default: other devices may connect (with the token); needs a restart
}

type Config struct {
	mu   sync.RWMutex
	path string
	d    ConfigData
}

func newToken() string {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

func LoadConfig(dir string) (*Config, error) {
	c := &Config{path: filepath.Join(dir, "config.json")}
	c.d = ConfigData{Port: DefaultPort, BackupIntervalMin: DefaultBackupIntervalMin, // see defaults.go
		LANEnabled: DefaultLANEnabled, MCPEnabled: DefaultMCPEnabled, MCPReadOnly: DefaultMCPReadOnly}
	b, err := os.ReadFile(c.path)
	switch {
	case err == nil:
		if err := json.Unmarshal(b, &c.d); err != nil {
			return nil, errors.New("config.json is not valid JSON: " + err.Error())
		}
	case !errors.Is(err, fs.ErrNotExist):
		return nil, err
	}
	changed := errors.Is(err, fs.ErrNotExist)
	if changed && DefaultCalendarFeed {
		c.d.CalKey = newToken()
	}
	if c.d.Token == "" {
		c.d.Token, changed = newToken(), true
	}
	if c.d.Port <= 0 || c.d.Port > 65535 {
		c.d.Port, changed = DefaultPort, true
	}
	if c.d.BackupIntervalMin < 1 {
		c.d.BackupIntervalMin, changed = DefaultBackupIntervalMin, true
	}
	if changed {
		if err := c.save(); err != nil {
			return nil, err
		}
	}
	return c, nil
}

func (c *Config) Get() ConfigData {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.d
}

func (c *Config) Update(fn func(d *ConfigData)) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	fn(&c.d)
	return c.save()
}

func (c *Config) save() error {
	b, _ := json.MarshalIndent(c.d, "", "  ")
	return writeFileAtomic(c.path, b, "")
}

// writeFileAtomic writes to a temp file, fsyncs it and renames it over the
// target, so a crash can never leave a half-written file. If prev is given the
// old file is kept there as a one-step-back copy.
func writeFileAtomic(path string, data []byte, prev string) error {
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if prev != "" {
		if _, err := os.Stat(path); err == nil {
			_ = os.Rename(path, prev)
		}
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	syncDir(filepath.Dir(path))
	return nil
}
