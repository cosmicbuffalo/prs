package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// stateBaseDir is defined in state.go; cache.json lives alongside
// state.json in the same directory (and honors the same $PRS_STATE_DIR
// override).

// cacheFile is the on-disk shape of the last successful fetch, keyed to the
// repo/user it was fetched for so switching repos doesn't show stale data
// from a different one. A single slot (not one file per repo) — simple, and
// covers the common case of working in one repo at a time; a repo switch
// just falls back to the normal full loading spinner, same as a cold start.
type cacheFile struct {
	Repo    string    `json:"repo"`
	User    string    `json:"user"`
	SavedAt time.Time `json:"saved_at"`
	Items   []Item    `json:"items"`
}

// cachePath returns stateBaseDir()/cache.json, creating the containing
// directory if needed.
func cachePath() (string, error) {
	dir, err := stateBaseDir()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create cache directory: %w", err)
	}
	return filepath.Join(dir, "cache.json"), nil
}

// LoadCache returns the last cached fetch result for repo/user, and whether
// one was found. A missing, corrupt, or repo/user-mismatched cache file is
// treated as "no cache" (ok=false) rather than an error — a cache miss
// should never block the TUI from opening, it just means falling back to
// the normal loading spinner.
func LoadCache(repo, user string) (items []Item, ok bool) {
	path, err := cachePath()
	if err != nil {
		return nil, false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	var cf cacheFile
	if err := json.Unmarshal(data, &cf); err != nil {
		return nil, false
	}
	if cf.Repo != repo || cf.User != user {
		return nil, false
	}
	return cf.Items, true
}

// SaveCache atomically writes items as the new cache for repo/user (temp
// file in the same directory + rename, so a crash mid-write can't corrupt
// it). Errors are the caller's to decide how to handle — a cache-write
// failure should never block showing fresh results, so callers typically
// ignore it (best-effort).
func SaveCache(repo, user string, items []Item) error {
	path, err := cachePath()
	if err != nil {
		return err
	}

	data, err := json.Marshal(cacheFile{Repo: repo, User: user, SavedAt: time.Now(), Items: items})
	if err != nil {
		return fmt.Errorf("marshal cache: %w", err)
	}

	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create temp cache file: %w", err)
	}
	tmpPath := tmp.Name()

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("write temp cache file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("close temp cache file: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("rename temp cache file: %w", err)
	}
	return nil
}
