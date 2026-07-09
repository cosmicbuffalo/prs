package main

import (
	"path/filepath"
	"testing"
	"time"
)

func TestIsDoneFalseForUnknownKey(t *testing.T) {
	s := newStoreAtPath(filepath.Join(t.TempDir(), "state.json"))
	item := Item{Key: "owner/repo#1", TriggerDate: time.Now()}
	if s.IsDone(item) {
		t.Fatal("expected IsDone to be false for a key that was never marked done")
	}
}

func TestMarkDoneThenIsDone(t *testing.T) {
	s := newStoreAtPath(filepath.Join(t.TempDir(), "state.json"))
	item := Item{Key: "owner/repo#1", TriggerDate: time.Now()}

	if err := s.MarkDone(item); err != nil {
		t.Fatalf("MarkDone: %v", err)
	}
	if !s.IsDone(item) {
		t.Fatal("expected IsDone to be true right after MarkDone")
	}
}

func TestMarkDoneThenNewerActivityReappears(t *testing.T) {
	s := newStoreAtPath(filepath.Join(t.TempDir(), "state.json"))
	now := time.Now()
	original := Item{Key: "owner/repo#1", TriggerDate: now}

	if err := s.MarkDone(original); err != nil {
		t.Fatalf("MarkDone: %v", err)
	}

	updated := Item{Key: "owner/repo#1", TriggerDate: now.Add(time.Hour)}
	if s.IsDone(updated) {
		t.Fatal("expected IsDone to be false once the item has a newer TriggerDate than when it was marked done")
	}
}

func TestMarkUndoneRemovesEntry(t *testing.T) {
	s := newStoreAtPath(filepath.Join(t.TempDir(), "state.json"))
	item := Item{Key: "owner/repo#1", TriggerDate: time.Now()}

	if err := s.MarkDone(item); err != nil {
		t.Fatalf("MarkDone: %v", err)
	}
	if err := s.MarkUndone(item); err != nil {
		t.Fatalf("MarkUndone: %v", err)
	}
	if s.IsDone(item) {
		t.Fatal("expected IsDone to be false after MarkUndone")
	}
}

func TestPruneDropsEntriesNotInKeySet(t *testing.T) {
	s := newStoreAtPath(filepath.Join(t.TempDir(), "state.json"))
	keep := Item{Key: "owner/repo#1", TriggerDate: time.Now()}
	drop := Item{Key: "owner/repo#2", TriggerDate: time.Now()}

	if err := s.MarkDone(keep); err != nil {
		t.Fatalf("MarkDone(keep): %v", err)
	}
	if err := s.MarkDone(drop); err != nil {
		t.Fatalf("MarkDone(drop): %v", err)
	}

	if err := s.Prune(map[string]bool{keep.Key: true}); err != nil {
		t.Fatalf("Prune: %v", err)
	}

	if !s.IsDone(keep) {
		t.Fatal("expected kept key to remain done after Prune")
	}
	if s.IsDone(drop) {
		t.Fatal("expected dropped key's entry to be removed by Prune")
	}
}

func TestIsIgnoredFalseForUnknownKey(t *testing.T) {
	s := newStoreAtPath(filepath.Join(t.TempDir(), "state.json"))
	item := Item{Key: "owner/repo#1", TriggerDate: time.Now()}
	if s.IsIgnored(item) {
		t.Fatal("expected IsIgnored to be false for a key that was never marked ignored")
	}
}

func TestMarkIgnoredThenIsIgnored(t *testing.T) {
	s := newStoreAtPath(filepath.Join(t.TempDir(), "state.json"))
	item := Item{Key: "owner/repo#1", TriggerDate: time.Now()}

	if err := s.MarkIgnored(item); err != nil {
		t.Fatalf("MarkIgnored: %v", err)
	}
	if !s.IsIgnored(item) {
		t.Fatal("expected IsIgnored to be true right after MarkIgnored")
	}
}

func TestMarkUnignoredRemovesIgnoredFlag(t *testing.T) {
	s := newStoreAtPath(filepath.Join(t.TempDir(), "state.json"))
	item := Item{Key: "owner/repo#1", TriggerDate: time.Now()}

	if err := s.MarkIgnored(item); err != nil {
		t.Fatalf("MarkIgnored: %v", err)
	}
	if err := s.MarkUnignored(item); err != nil {
		t.Fatalf("MarkUnignored: %v", err)
	}
	if s.IsIgnored(item) {
		t.Fatal("expected IsIgnored to be false after MarkUnignored")
	}
}

func TestIgnoredAndDoneAreIndependent(t *testing.T) {
	s := newStoreAtPath(filepath.Join(t.TempDir(), "state.json"))
	item := Item{Key: "owner/repo#1", TriggerDate: time.Now()}

	if err := s.MarkDone(item); err != nil {
		t.Fatalf("MarkDone: %v", err)
	}
	if err := s.MarkIgnored(item); err != nil {
		t.Fatalf("MarkIgnored: %v", err)
	}
	if !s.IsDone(item) || !s.IsIgnored(item) {
		t.Fatal("expected both IsDone and IsIgnored to be true when both flags are set")
	}

	if err := s.MarkUnignored(item); err != nil {
		t.Fatalf("MarkUnignored: %v", err)
	}
	if s.IsIgnored(item) {
		t.Fatal("expected IsIgnored to be false after MarkUnignored")
	}
	if !s.IsDone(item) {
		t.Fatal("expected IsDone to remain true after MarkUnignored — the two flags should be independent")
	}
}

func TestSaveThenLoadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s := newStoreAtPath(path)
	item := Item{Key: "owner/repo#1", TriggerDate: time.Now().Truncate(time.Second)}

	if err := s.MarkDone(item); err != nil {
		t.Fatalf("MarkDone: %v", err)
	}

	reloaded := newStoreAtPath(path)
	if !reloaded.IsDone(item) {
		t.Fatal("expected reloaded store to report the item as done")
	}

	newer := Item{Key: item.Key, TriggerDate: item.TriggerDate.Add(time.Hour)}
	if reloaded.IsDone(newer) {
		t.Fatal("expected reloaded store to still respect the done_until timestamp for newer activity")
	}
}
