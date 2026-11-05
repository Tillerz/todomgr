package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
)

var errTooNew = errors.New("the data file was written by a newer version of this app")

// Store owns data/database.json. It is the ONLY writer: the UI, the CLI and the
// MCP server all go through it, so they can be used at the same time without
// ever corrupting the file.
type Store struct {
	mu       sync.RWMutex
	dir      string
	cfg      *Config
	loc      *time.Location
	hub      *Hub
	lock     *fileLock
	db       *DB
	last     []byte // exact bytes of the last saved database.json
	activity []map[string]any
	actFile  *os.File
	nodeRev  map[string]int64
	floorRev int64 // clients with a base revision below this must refresh
	lastSnap time.Time
	dirty    bool
	done     chan struct{}

	Recovered string // non-empty if the data had to be restored from a backup
}

type conflictError struct{ ids []string }

func (e *conflictError) Error() string {
	return "these entries were changed elsewhere in the meantime: " + strings.Join(e.ids, ", ")
}

func (s *Store) path(name string) string { return filepath.Join(s.dir, name) }

func OpenStore(dir string, cfg *Config) (*Store, error) {
	if err := os.MkdirAll(filepath.Join(dir, "backups"), 0o755); err != nil {
		return nil, err
	}
	lk, err := acquireLock(dir)
	if err != nil {
		return nil, err
	}
	s := &Store{dir: dir, cfg: cfg, hub: NewHub(), lock: lk, nodeRev: map[string]int64{}, done: make(chan struct{}), loc: time.Local}
	if tz := cfg.Get().Timezone; tz != "" {
		if l, err := time.LoadLocation(tz); err == nil {
			s.loc = l
		} else {
			log.Printf("warning: unknown timezone %q in config.json, using system time zone", tz)
		}
	}
	if err := s.load(); err != nil {
		lk.release()
		return nil, err
	}
	s.loadActivity()
	s.lastSnap = time.Now()
	go s.snapshotLoop()
	go s.reminderLoop()
	return s, nil
}

// ------------------------------------------------------------------ loading

func parseDB(data []byte) (*DB, []map[string]any, bool, error) {
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, nil, false, fmt.Errorf("invalid JSON: %w", err)
	}
	if raw == nil || raw["nodes"] == nil {
		return nil, nil, false, errors.New("no entries found in the file")
	}
	from := int(num(raw["schema"]))
	if from > schemaVersion {
		return nil, nil, false, fmt.Errorf("%w (file schema %d, this app understands up to %d, last written by app version %v) — please update the app", errTooNew, from, schemaVersion, raw["app"])
	}
	migrated := false
	for v := from; v < schemaVersion; v++ {
		m, ok := migrations[v]
		if !ok {
			return nil, nil, false, fmt.Errorf("no migration from schema %d", v)
		}
		if err := m(raw); err != nil {
			return nil, nil, false, fmt.Errorf("migration %d→%d failed: %w", v, v+1, err)
		}
		migrated = true
	}
	var act []map[string]any
	if a, ok := raw["activity"].([]any); ok {
		for _, e := range a {
			if m, ok := e.(map[string]any); ok {
				act = append(act, m)
			}
		}
	}
	delete(raw, "activity")
	delete(raw, "v")
	raw["schema"] = float64(schemaVersion)
	b, _ := json.Marshal(raw)
	db := &DB{}
	if err := json.Unmarshal(b, db); err != nil {
		return nil, nil, false, err
	}
	if db.Prefs == nil {
		db.Prefs = map[string]any{}
	}
	db.Schema, db.App = schemaVersion, Version
	if err := validateDB(db); err != nil {
		return nil, nil, false, fmt.Errorf("integrity check failed: %w", err)
	}
	return db, act, migrated, nil
}

func emptyDB() *DB {
	return &DB{Schema: schemaVersion, App: Version, Prefs: map[string]any{},
		Nodes: map[string]Node{"root": {"id": "root", "name": "root", "children": []any{}}}}
}

func (s *Store) load() error {
	main := s.path("database.json")
	data, err := os.ReadFile(main)
	if err == nil {
		db, act, migrated, perr := parseDB(data)
		if perr == nil {
			s.db = db
			if migrated {
				if _, e := s.snapshotFileLocked("pre-migrate", data); e != nil {
					return fmt.Errorf("could not back up before migrating: %w", e)
				}
				if e := s.saveLocked(); e != nil {
					return e
				}
				log.Printf("migrated data file to schema %d (backup kept in backups/)", schemaVersion)
			} else {
				s.last = data
			}
			s.importActivityLocked(act)
			return nil
		}
		if errors.Is(perr, errTooNew) {
			return perr // never touch data of a newer version
		}
		log.Printf("database.json is damaged: %v", perr)
		stamp := time.Now().Format("20060102-150405")
		_ = os.Rename(main, s.path("database.corrupt-"+stamp+".json"))
		return s.recover(fmt.Sprintf("database.json was damaged (%v); the damaged file was kept as database.corrupt-%s.json", perr, stamp))
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if _, perr := os.Stat(s.path("database.prev.json")); perr == nil {
		return s.recover("database.json was missing (interrupted save?)")
	}
	s.db = emptyDB()
	return s.saveLocked()
}

// recover restores the newest valid copy: database.prev.json, then backups.
func (s *Store) recover(reason string) error {
	cands := []string{s.path("database.prev.json")}
	if list, err := s.listBackups(); err == nil {
		for _, b := range list {
			cands = append(cands, s.path(filepath.Join("backups", b.Name)))
		}
	}
	for _, p := range cands {
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		db, act, _, err := parseDB(data)
		if err != nil {
			continue
		}
		s.db = db
		if err := s.saveLocked(); err != nil {
			return err
		}
		s.importActivityLocked(act)
		s.Recovered = reason + "; restored from " + filepath.Base(p)
		log.Print(s.Recovered)
		return nil
	}
	return errors.New(reason + " and no usable backup was found — see " + s.dir)
}

func (s *Store) importActivityLocked(act []map[string]any) {
	if len(act) == 0 {
		return
	}
	s.activity = nil
	_ = s.rewriteActivityLocked(act)
}

func (s *Store) loadActivity() {
	s.activity = nil
	f, err := os.Open(s.path("activity.jsonl"))
	if err == nil {
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		for sc.Scan() {
			var m map[string]any
			if json.Unmarshal(sc.Bytes(), &m) == nil && m != nil {
				s.activity = append(s.activity, m)
			}
		}
		f.Close()
	}
	s.actFile, _ = os.OpenFile(s.path("activity.jsonl"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
}

func (s *Store) appendActivityLocked(acts []map[string]any) {
	if len(acts) == 0 {
		return
	}
	if s.actFile == nil {
		s.actFile, _ = os.OpenFile(s.path("activity.jsonl"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	}
	var sb strings.Builder
	for _, a := range acts {
		b, _ := json.Marshal(a)
		sb.Write(b)
		sb.WriteByte('\n')
	}
	if s.actFile != nil {
		_, _ = s.actFile.WriteString(sb.String())
	}
	s.activity = append(s.activity, acts...)
}

func (s *Store) rewriteActivityLocked(act []map[string]any) error {
	var sb strings.Builder
	for _, a := range act {
		b, _ := json.Marshal(a)
		sb.Write(b)
		sb.WriteByte('\n')
	}
	if s.actFile != nil {
		s.actFile.Close()
		s.actFile = nil
	}
	if err := writeFileAtomic(s.path("activity.jsonl"), []byte(sb.String()), s.path("activity.prev.jsonl")); err != nil {
		return err
	}
	s.activity = act
	s.actFile, _ = os.OpenFile(s.path("activity.jsonl"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	return nil
}

// ------------------------------------------------------------------ saving

func (s *Store) saveLocked() error {
	s.db.Schema, s.db.App = schemaVersion, Version
	s.db.SavedAt = time.Now().Format(time.RFC3339)
	data, err := json.MarshalIndent(s.db, "", " ")
	if err != nil {
		return err
	}
	if err := writeFileAtomic(s.path("database.json"), data, s.path("database.prev.json")); err != nil {
		return err
	}
	s.last = data
	s.dirty = true
	return nil
}

func (s *Store) reloadLocked() {
	if s.last == nil {
		return
	}
	if db, _, _, err := parseDB(s.last); err == nil {
		s.db = db
	}
}

// Mutate runs fn as one atomic, validated, durable change and announces it to
// all connected clients. If fn (or validation, or saving) fails, nothing changes.
func (s *Store) Mutate(actor, client string, fn func(tx *Tx) error) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closedLocked() {
		return 0, errClosed
	}
	tx := &Tx{db: s.db, actor: actor, loc: s.loc, touched: map[string]bool{}}
	err := fn(tx)
	if err == nil {
		err = validateDB(s.db)
	}
	if err != nil {
		s.reloadLocked()
		return 0, err
	}
	s.db.Rev++
	rev := s.db.Rev
	if err := s.saveLocked(); err != nil {
		s.reloadLocked()
		return 0, fmt.Errorf("saving failed: %w", err)
	}
	for id := range tx.touched {
		s.nodeRev[id] = rev
	}
	s.appendActivityLocked(tx.acts)
	s.hub.Publish("change", map[string]any{"rev": rev, "by": actor, "client": client, "ids": sortedKeys(tx.touched)})
	s.afterSaveLocked()
	return rev, nil
}

// errClosed: a change that waited for the lock while the store was closing is not saved
// (the lock file is released and the activity file is closed by then).
var errClosed = errors.New("the data store is closed")

func (s *Store) closedLocked() bool {
	select {
	case <-s.done:
		return true
	default:
		return false
	}
}

func (s *Store) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	select {
	case <-s.done:
		return
	default:
		close(s.done)
	}
	if s.dirty && s.last != nil {
		_, _ = s.snapshotFileLocked("auto", s.last)
	}
	if s.actFile != nil {
		s.actFile.Close()
	}
	s.lock.release()
}

// ------------------------------------------------------------------ whole-state operations

// Replace swaps the whole database (import / restore). A backup of the current
// state is taken first, and every connected UI is told to reload.
func (s *Store) Replace(newDB *DB, act []map[string]any, replaceActivity bool, snapKind string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closedLocked() {
		return errClosed
	}
	if s.last != nil {
		if _, err := s.snapshotFileLocked(snapKind, s.last); err != nil {
			return fmt.Errorf("could not back up current data first: %w", err)
		}
	}
	old := s.db
	newDB.Rev = old.Rev + 1
	s.db = newDB
	if err := s.saveLocked(); err != nil {
		s.db = old
		return err
	}
	if replaceActivity {
		if err := s.rewriteActivityLocked(act); err != nil {
			return err
		}
	}
	s.nodeRev = map[string]int64{}
	s.floorRev = newDB.Rev
	s.hub.Publish("reload", map[string]any{"rev": newDB.Rev})
	return nil
}

// Export returns the database plus the activity log as one JSON document.
func (s *Store) Export() ([]byte, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var m map[string]any
	if err := json.Unmarshal(s.last, &m); err != nil {
		return nil, err
	}
	m["activity"] = s.activity
	return json.MarshalIndent(m, "", " ")
}

// Import accepts a full export of this app or a prototype export.
func (s *Store) Import(data []byte) error {
	db, act, _, err := parseDB(data)
	if err != nil {
		return err
	}
	return s.Replace(db, act, act != nil, "pre-import")
}

// Reset replaces everything with an empty database ("start from scratch"). The
// settings of the UI are kept. The previous data stays restorable from a
// pre-reset backup; the activity log is moved aside, not deleted.
func (s *Store) Reset() error {
	s.mu.RLock()
	db := emptyDB()
	for k, v := range s.db.Prefs {
		db.Prefs[k] = deepCopy(v)
	}
	s.mu.RUnlock()
	if b, err := os.ReadFile(s.path("activity.jsonl")); err == nil && len(b) > 0 {
		keep := s.path("activity.before-reset-" + time.Now().Format("20060102-150405") + ".jsonl")
		if err := os.WriteFile(keep, b, 0o644); err != nil {
			return fmt.Errorf("could not keep the activity log: %w", err)
		}
	}
	return s.Replace(db, []map[string]any{}, true, "pre-reset")
}

func (s *Store) tzName() string { return s.loc.String() }
func (s *Store) Now() time.Time { return time.Now().In(s.loc) }

// ------------------------------------------------------------------ lock

type fileLock struct{ path string }

func acquireLock(dir string) (*fileLock, error) {
	p := filepath.Join(dir, "todomgr.lock")
	host, _ := os.Hostname()
	for attempt := 0; attempt < 2; attempt++ {
		f, err := os.OpenFile(p, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err == nil {
			fmt.Fprintf(f, "%d\n%s\n%s\n", os.Getpid(), host, runtime.GOOS)
			f.Close()
			return &fileLock{p}, nil
		}
		if !errors.Is(err, fs.ErrExist) {
			return nil, err
		}
		b, _ := os.ReadFile(p)
		parts := strings.Fields(string(b))
		pid := 0
		if len(parts) > 0 {
			fmt.Sscanf(parts[0], "%d", &pid)
		}
		lockHost, lockOS := "", runtime.GOOS // a lock of an older version has no OS line
		if len(parts) > 1 {
			lockHost = parts[1]
		}
		if len(parts) > 2 {
			lockOS = parts[2]
		}
		// only a lock of this host and this OS can be checked: Windows cannot see the processes of WSL
		// (same host name), and the other way round, so such a lock must never count as stale
		if lockHost == host && lockOS == runtime.GOOS && pid > 0 && pid != os.Getpid() && !processAlive(pid) {
			_ = os.Remove(p) // stale lock of a crashed instance
			continue
		}
		if lockHost != "" && lockHost != host {
			return nil, fmt.Errorf("the data folder is locked by another computer (%s). If you are sure nobody is using it, delete %s", lockHost, p)
		}
		if lockOS != runtime.GOOS {
			return nil, fmt.Errorf("the data folder is in use by the %s version of the program (process %d). Stop it first; if it is not running, delete %s", lockOS, pid, p)
		}
		return nil, fmt.Errorf("the data folder is already in use by process %d (lock file %s)", pid, p)
	}
	return nil, errors.New("could not acquire the data folder lock")
}

func (l *fileLock) release() { _ = os.Remove(l.path) }

// ------------------------------------------------------------------ misc

func (s *Store) sortedActivityCopy() []map[string]any {
	out := make([]map[string]any, len(s.activity))
	copy(out, s.activity)
	sort.SliceStable(out, func(i, j int) bool { return num(out[i]["t"]) < num(out[j]["t"]) })
	return out
}
