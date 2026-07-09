package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// storeEntry records the user's toggled state for a PR (identified by
// Item.Key). DoneUntil: the PR was marked done as of a particular
// TriggerDate — if the PR's TriggerDate later moves past DoneUntil, it's
// treated as no longer done (new activity reopened it). Ignored: the PR was
// explicitly marked ignored; unlike "done", this does NOT reset on new
// activity — it's a permanent mute until the user manually un-ignores it.
type storeEntry struct {
	DoneUntil time.Time `json:"done_until"`
	Ignored   bool      `json:"ignored,omitempty"`
}

// storeFile is the on-disk JSON shape.
type storeFile struct {
	Entries map[string]storeEntry `json:"entries"`
}

// Store persists which PRs the user has marked done, keyed by Item.Key, and
// at what TriggerDate they were marked done.
//
// The mutex guards against concurrent access; in practice Bubble Tea's Elm
// architecture drives Update from a single goroutine, so this is likely
// unnecessary, but it's cheap insurance against future callers (e.g. a
// background fetch goroutine) touching the store off the Update loop.
type Store struct {
	mu      sync.Mutex
	path    string
	entries map[string]storeEntry
}

// stateBaseDir returns the directory persisted state/cache files live in:
// $PRS_STATE_DIR if set, else ~/.local/state/prs. The env override exists so
// a scratch/dev instance of prs can be pointed at an isolated directory
// instead of silently sharing (and, via a fetch's Prune-then-save cycle,
// potentially clobbering) a real user's done/ignored markings and cache —
// always set PRS_STATE_DIR when running a throwaway/test instance of prs
// alongside a real one.
func stateBaseDir() (string, error) {
	if dir := os.Getenv("PRS_STATE_DIR"); dir != "" {
		return dir, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("determine home directory: %w", err)
	}
	return filepath.Join(home, ".local", "state", "prs"), nil
}

// LoadStore loads the state file from stateBaseDir()/state.json, creating an
// empty in-memory store (backed by that path for future saves) if the file
// doesn't exist yet. The directory is created as needed.
func LoadStore() (*Store, error) {
	dir, err := stateBaseDir()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create state directory: %w", err)
	}
	return newStoreAtPath(filepath.Join(dir, "state.json")), nil
}

// newStoreAtPath builds a Store backed by path, loading existing entries from
// disk if present. A missing or corrupt file is treated as an empty store
// (best-effort: a bad state file shouldn't prevent the TUI from opening).
func newStoreAtPath(path string) *Store {
	s := &Store{
		path:    path,
		entries: map[string]storeEntry{},
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return s
	}

	var onDisk storeFile
	if err := json.Unmarshal(data, &onDisk); err != nil {
		return s
	}
	if onDisk.Entries != nil {
		s.entries = onDisk.Entries
	}
	return s
}

// IsDone reports whether item is currently "done": there's a stored done_until
// timestamp for item.Key that is >= item.TriggerDate.
func (s *Store) IsDone(item Item) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	entry, ok := s.entries[item.Key]
	if !ok {
		return false
	}
	return !entry.DoneUntil.Before(item.TriggerDate)
}

// MarkDone records item as done as-of its current TriggerDate and persists
// the store to disk immediately (atomic write: temp file in the same dir + rename).
// Any existing Ignored flag for item.Key is preserved.
func (s *Store) MarkDone(item Item) error {
	s.mu.Lock()
	entry := s.entries[item.Key]
	entry.DoneUntil = item.TriggerDate
	s.entries[item.Key] = entry
	s.mu.Unlock()

	return s.save()
}

// MarkUndone clears the done record for item.Key (leaving any Ignored flag
// intact) and persists the store to disk. The entry is dropped entirely once
// neither flag is set, to avoid the state file accumulating empty entries.
func (s *Store) MarkUndone(item Item) error {
	s.mu.Lock()
	entry := s.entries[item.Key]
	entry.DoneUntil = time.Time{}
	s.setOrDeleteLocked(item.Key, entry)
	s.mu.Unlock()

	return s.save()
}

// IsIgnored reports whether item has been explicitly marked ignored. Unlike
// IsDone, this never resets on its own — it's cleared only by MarkUnignored.
func (s *Store) IsIgnored(item Item) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.entries[item.Key].Ignored
}

// MarkIgnored records item as ignored and persists the store to disk. Any
// existing DoneUntil for item.Key is preserved.
func (s *Store) MarkIgnored(item Item) error {
	s.mu.Lock()
	entry := s.entries[item.Key]
	entry.Ignored = true
	s.entries[item.Key] = entry
	s.mu.Unlock()

	return s.save()
}

// MarkUnignored clears the ignored flag for item.Key (leaving any DoneUntil
// intact) and persists the store to disk.
func (s *Store) MarkUnignored(item Item) error {
	s.mu.Lock()
	entry := s.entries[item.Key]
	entry.Ignored = false
	s.setOrDeleteLocked(item.Key, entry)
	s.mu.Unlock()

	return s.save()
}

// setOrDeleteLocked stores entry under key, or deletes the key entirely if
// entry is now the zero value (neither done nor ignored) — callers must hold
// s.mu. Keeps the state file from accumulating empty leftover entries.
func (s *Store) setOrDeleteLocked(key string, entry storeEntry) {
	if entry.DoneUntil.IsZero() && !entry.Ignored {
		delete(s.entries, key)
		return
	}
	s.entries[key] = entry
}

// Prune drops any stored entries whose key is not present in currentKeys
// (used after each fetch to garbage-collect entries for PRs that no longer
// qualify or have been closed/merged), then persists the store to disk.
func (s *Store) Prune(currentKeys map[string]bool) error {
	s.mu.Lock()
	for key := range s.entries {
		if !currentKeys[key] {
			delete(s.entries, key)
		}
	}
	s.mu.Unlock()

	return s.save()
}

// save atomically writes the store's current entries to s.path: it writes to
// a temp file in the same directory, then renames over the real path, so a
// crash mid-write can't corrupt the file.
func (s *Store) save() error {
	s.mu.Lock()
	onDisk := storeFile{Entries: s.entries}
	path := s.path
	s.mu.Unlock()

	data, err := json.MarshalIndent(onDisk, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal state: %w", err)
	}

	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create temp state file: %w", err)
	}
	tmpPath := tmp.Name()

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("write temp state file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("close temp state file: %w", err)
	}

	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("rename temp state file: %w", err)
	}
	return nil
}
