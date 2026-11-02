package main

import (
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Backups live in data/backups as complete copies of database.json.
//
//	auto-…      rolling snapshots (at most one per backupIntervalMin, only when something changed)
//	manual-…    "Back up now"
//	pre-…       safety copies taken before a migration, import or restore
//
// Retention of auto/manual copies: everything from the last 2 hours, then one
// per hour for 24 hours, one per day for 30 days, one per week for 26 weeks.
// pre-… copies are kept for 7 days (and at least the newest 15).
type BackupInfo struct {
	Name string    `json:"name"`
	Kind string    `json:"kind"`
	Time time.Time `json:"time"`
	Size int64     `json:"size"`
}

var reBackup = regexp.MustCompile(`^(.+)-(\d{8}-\d{6})(?:-\d+)?\.json$`)

func (s *Store) listBackups() ([]BackupInfo, error) {
	ents, err := os.ReadDir(s.path("backups"))
	if err != nil {
		return nil, err
	}
	var out []BackupInfo
	for _, e := range ents {
		m := reBackup.FindStringSubmatch(e.Name())
		if m == nil || e.IsDir() {
			continue
		}
		t, err := time.ParseInLocation("20060102-150405", m[2], time.Local)
		if err != nil {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, BackupInfo{Name: e.Name(), Kind: m[1], Time: t, Size: info.Size()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Time.After(out[j].Time) })
	return out, nil
}

func (s *Store) snapshotFileLocked(kind string, data []byte) (string, error) {
	stamp := time.Now().Format("20060102-150405")
	name := fmt.Sprintf("%s-%s.json", kind, stamp)
	for i := 2; ; i++ {
		if _, err := os.Stat(s.path(filepath.Join("backups", name))); errors.Is(err, os.ErrNotExist) {
			break
		}
		name = fmt.Sprintf("%s-%s-%d.json", kind, stamp, i)
	}
	if err := writeFileAtomic(s.path(filepath.Join("backups", name)), data, ""); err != nil {
		return "", err
	}
	s.lastSnap = time.Now()
	if kind == "auto" || kind == "manual" {
		s.dirty = false
	}
	s.pruneLocked()
	return name, nil
}

// BackupNow creates a manual snapshot of the current state.
func (s *Store) BackupNow() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closedLocked() {
		return "", errClosed
	}
	if s.last == nil {
		return "", errors.New("nothing to back up yet")
	}
	return s.snapshotFileLocked("manual", s.last)
}

func (s *Store) afterSaveLocked() {
	if s.dirty && time.Since(s.lastSnap) >= time.Duration(s.cfg.Get().BackupIntervalMin)*time.Minute {
		if _, err := s.snapshotFileLocked("auto", s.last); err != nil {
			log.Printf("backup failed: %v", err)
		}
	}
}

func (s *Store) snapshotLoop() {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-s.done:
			return
		case <-t.C:
			s.mu.Lock()
			if !s.closedLocked() {
				s.afterSaveLocked()
			}
			s.mu.Unlock()
		}
	}
}

// keepBackups decides which backups survive pruning (pure function, unit-tested).
func keepBackups(list []BackupInfo, now time.Time) map[string]bool {
	keep := map[string]bool{}
	sorted := append([]BackupInfo(nil), list...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Time.After(sorted[j].Time) })
	buckets := map[string]bool{}
	pre := 0
	for _, b := range sorted {
		age := now.Sub(b.Time)
		if strings.HasPrefix(b.Kind, "pre-") {
			pre++
			if age < 7*24*time.Hour || pre <= 15 {
				keep[b.Name] = true
			}
			continue
		}
		key := ""
		switch {
		case age < 2*time.Hour:
			keep[b.Name] = true
			continue
		case age < 24*time.Hour:
			key = "h" + b.Time.Format("2006010215")
		case age < 30*24*time.Hour:
			key = "d" + b.Time.Format("20060102")
		case age < 182*24*time.Hour:
			y, w := b.Time.ISOWeek()
			key = fmt.Sprintf("w%d-%d", y, w)
		default:
			continue
		}
		if !buckets[key] {
			buckets[key] = true
			keep[b.Name] = true
		}
	}
	return keep
}

func (s *Store) pruneLocked() {
	list, err := s.listBackups()
	if err != nil {
		return
	}
	keep := keepBackups(list, time.Now())
	for _, b := range list {
		if !keep[b.Name] {
			_ = os.Remove(s.path(filepath.Join("backups", b.Name)))
		}
	}
}

// ListBackups is the public, locked version.
func (s *Store) ListBackups() ([]BackupInfo, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.listBackups()
}

// RestoreBackup replaces the current data with a backup (after taking a
// pre-restore safety copy of the current data).
func (s *Store) RestoreBackup(name string) error {
	if name != filepath.Base(name) || !reBackup.MatchString(name) {
		return errors.New("invalid backup name")
	}
	data, err := os.ReadFile(s.path(filepath.Join("backups", name)))
	if err != nil {
		return err
	}
	db, act, _, err := parseDB(data)
	if err != nil {
		return fmt.Errorf("that backup cannot be used: %w", err)
	}
	return s.Replace(db, act, act != nil, "pre-restore")
}
