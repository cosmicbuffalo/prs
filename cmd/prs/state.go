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

// storeFile is the on-disk JSON shape. Done/ignored state is scoped per
// GitHub login (Users[<login>][<item.Key>]) so running the TUI with
// different --as_user values keeps independent state and never overlaps.
//
// Entries is the legacy pre-user-scoping flat map. It's still read for
// backward compatibility and folded into the current user's bucket on first
// load (see newStoreAtPath); once the store is saved again it's written only
// under Users and the legacy field disappears.
type storeFile struct {
	Users   map[string]map[string]storeEntry `json:"users"`
	Entries map[string]storeEntry            `json:"entries,omitempty"`
}

// Store persists which PRs a given user has marked done/ignored, keyed by
// Item.Key, and at what TriggerDate they were marked done. Each Store is
// bound to a single GitHub login (the resolved --user / current gh user) and
// only ever reads and writes that user's slice of the file, leaving other
// users' state untouched.
//
// The mutex guards against concurrent access; in practice Bubble Tea's Elm
// architecture drives Update from a single goroutine, so this is likely
// unnecessary, but it's cheap insurance against future callers (e.g. a
// background fetch goroutine) touching the store off the Update loop.
type Store struct {
	mu    sync.Mutex
	path  string
	user  string
	users map[string]map[string]storeEntry
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

// LoadStore loads the state file from stateBaseDir()/state.json, scoped to
// user, creating an empty in-memory store (backed by that path for future
// saves) if the file doesn't exist yet. The directory is created as needed.
func LoadStore(user string) (*Store, error) {
	dir, err := stateBaseDir()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create state directory: %w", err)
	}
	return newStoreAtPath(filepath.Join(dir, "state.json"), user), nil
}

// newStoreAtPath builds a Store backed by path and scoped to user, loading
// existing entries from disk if present. A missing or corrupt file is treated
// as an empty store (best-effort: a bad state file shouldn't prevent the TUI
// from opening). Any legacy flat entries (from before state was user-scoped)
// are migrated into user's bucket — this attributes pre-upgrade state to
// whichever login first runs after the upgrade, which is the default user in
// normal use.
func newStoreAtPath(path, user string) *Store {
	s := &Store{
		path:  path,
		user:  user,
		users: map[string]map[string]storeEntry{},
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return s
	}

	var onDisk storeFile
	if err := json.Unmarshal(data, &onDisk); err != nil {
		return s
	}
	if onDisk.Users != nil {
		s.users = onDisk.Users
	}
	// Migrate legacy flat entries into this user's bucket without clobbering
	// anything already there under Users.
	if len(onDisk.Entries) > 0 {
		mine := s.mineLocked()
		for k, v := range onDisk.Entries {
			if _, exists := mine[k]; !exists {
				mine[k] = v
			}
		}
	}
	return s
}

// mineLocked returns the current user's entry map, creating it if needed.
// Callers must hold s.mu (or be in single-threaded setup like newStoreAtPath).
func (s *Store) mineLocked() map[string]storeEntry {
	m := s.users[s.user]
	if m == nil {
		m = map[string]storeEntry{}
		s.users[s.user] = m
	}
	return m
}

// IsDone reports whether item is currently "done" for this user: there's a
// stored done_until timestamp for item.Key that is >= item.TriggerDate.
func (s *Store) IsDone(item Item) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	entry, ok := s.users[s.user][item.Key]
	if !ok {
		return false
	}
	return !entry.DoneUntil.Before(item.TriggerDate)
}

// MarkDone records item as done as-of its current TriggerDate and persists
// the store to disk immediately (atomic write: temp file in the same dir + rename).
// Marking done also clears any Ignored flag for item.Key: an item lives in
// exactly one of Done/Ignored, so moving it into Done takes it out of Ignored.
func (s *Store) MarkDone(item Item) error {
	s.mu.Lock()
	mine := s.mineLocked()
	entry := mine[item.Key]
	entry.DoneUntil = item.TriggerDate
	entry.Ignored = false
	mine[item.Key] = entry
	s.mu.Unlock()

	return s.save()
}

// MarkUndone clears the done record for item.Key (leaving any Ignored flag
// intact) and persists the store to disk. The entry is dropped entirely once
// neither flag is set, to avoid the state file accumulating empty entries.
func (s *Store) MarkUndone(item Item) error {
	s.mu.Lock()
	mine := s.mineLocked()
	entry := mine[item.Key]
	entry.DoneUntil = time.Time{}
	s.setOrDeleteLocked(item.Key, entry)
	s.mu.Unlock()

	return s.save()
}

// IsIgnored reports whether item has been explicitly marked ignored by this
// user. Unlike IsDone, this never resets on its own — it's cleared only by
// MarkUnignored.
func (s *Store) IsIgnored(item Item) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.users[s.user][item.Key].Ignored
}

// MarkIgnored records item as ignored and persists the store to disk. Marking
// ignored also clears any DoneUntil for item.Key: an item lives in exactly one
// of Done/Ignored, so moving it into Ignored takes it out of Done.
func (s *Store) MarkIgnored(item Item) error {
	s.mu.Lock()
	mine := s.mineLocked()
	entry := mine[item.Key]
	entry.Ignored = true
	entry.DoneUntil = time.Time{}
	mine[item.Key] = entry
	s.mu.Unlock()

	return s.save()
}

// MarkUnignored clears the ignored flag for item.Key (leaving any DoneUntil
// intact) and persists the store to disk.
func (s *Store) MarkUnignored(item Item) error {
	s.mu.Lock()
	mine := s.mineLocked()
	entry := mine[item.Key]
	entry.Ignored = false
	s.setOrDeleteLocked(item.Key, entry)
	s.mu.Unlock()

	return s.save()
}

// setOrDeleteLocked stores entry under key in the current user's bucket, or
// deletes the key entirely if entry is now the zero value (neither done nor
// ignored) — callers must hold s.mu. Keeps the state file from accumulating
// empty leftover entries.
func (s *Store) setOrDeleteLocked(key string, entry storeEntry) {
	mine := s.mineLocked()
	if entry.DoneUntil.IsZero() && !entry.Ignored {
		delete(mine, key)
		return
	}
	mine[key] = entry
}

// Prune drops any of the current user's stored entries whose key is not
// present in currentKeys (used after each fetch to garbage-collect entries
// for PRs that no longer qualify or have been closed/merged), then persists
// the store to disk. Other users' entries are left untouched.
func (s *Store) Prune(currentKeys map[string]bool) error {
	s.mu.Lock()
	mine := s.mineLocked()
	for key := range mine {
		if !currentKeys[key] {
			delete(mine, key)
		}
	}
	s.mu.Unlock()

	return s.save()
}

// save atomically writes the store's current entries to s.path: it writes to
// a temp file in the same directory, then renames over the real path, so a
// crash mid-write can't corrupt the file. Empty per-user buckets are dropped
// so the file doesn't accumulate blank users.
func (s *Store) save() error {
	s.mu.Lock()
	users := make(map[string]map[string]storeEntry, len(s.users))
	for user, entries := range s.users {
		if len(entries) == 0 {
			continue
		}
		users[user] = entries
	}
	onDisk := storeFile{Users: users}
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
