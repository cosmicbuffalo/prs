package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestIsDoneFalseForUnknownKey(t *testing.T) {
	s := newStoreAtPath(filepath.Join(t.TempDir(), "state.json"), "alice")
	item := Item{Key: "owner/repo#1", TriggerDate: time.Now()}
	if s.IsDone(item) {
		t.Fatal("expected IsDone to be false for a key that was never marked done")
	}
}

func TestMarkDoneThenIsDone(t *testing.T) {
	s := newStoreAtPath(filepath.Join(t.TempDir(), "state.json"), "alice")
	item := Item{Key: "owner/repo#1", TriggerDate: time.Now()}

	if err := s.MarkDone(item); err != nil {
		t.Fatalf("MarkDone: %v", err)
	}
	if !s.IsDone(item) {
		t.Fatal("expected IsDone to be true right after MarkDone")
	}
}

func TestMarkDoneThenNewerActivityReappears(t *testing.T) {
	s := newStoreAtPath(filepath.Join(t.TempDir(), "state.json"), "alice")
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
	s := newStoreAtPath(filepath.Join(t.TempDir(), "state.json"), "alice")
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
	s := newStoreAtPath(filepath.Join(t.TempDir(), "state.json"), "alice")
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
	s := newStoreAtPath(filepath.Join(t.TempDir(), "state.json"), "alice")
	item := Item{Key: "owner/repo#1", TriggerDate: time.Now()}
	if s.IsIgnored(item) {
		t.Fatal("expected IsIgnored to be false for a key that was never marked ignored")
	}
}

func TestMarkIgnoredThenIsIgnored(t *testing.T) {
	s := newStoreAtPath(filepath.Join(t.TempDir(), "state.json"), "alice")
	item := Item{Key: "owner/repo#1", TriggerDate: time.Now()}

	if err := s.MarkIgnored(item); err != nil {
		t.Fatalf("MarkIgnored: %v", err)
	}
	if !s.IsIgnored(item) {
		t.Fatal("expected IsIgnored to be true right after MarkIgnored")
	}
}

func TestMarkUnignoredRemovesIgnoredFlag(t *testing.T) {
	s := newStoreAtPath(filepath.Join(t.TempDir(), "state.json"), "alice")
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

func TestMarkingOneBucketClearsTheOther(t *testing.T) {
	item := Item{Key: "owner/repo#1", TriggerDate: time.Now()}

	// Done then Ignored: the item ends up only ignored.
	s := newStoreAtPath(filepath.Join(t.TempDir(), "state.json"), "alice")
	if err := s.MarkDone(item); err != nil {
		t.Fatalf("MarkDone: %v", err)
	}
	if err := s.MarkIgnored(item); err != nil {
		t.Fatalf("MarkIgnored: %v", err)
	}
	if !s.IsIgnored(item) {
		t.Fatal("expected IsIgnored to be true after MarkIgnored")
	}
	if s.IsDone(item) {
		t.Fatal("expected MarkIgnored to clear the done flag — an item lives in exactly one bucket")
	}

	// Ignored then Done: the item ends up only done.
	s2 := newStoreAtPath(filepath.Join(t.TempDir(), "state.json"), "alice")
	if err := s2.MarkIgnored(item); err != nil {
		t.Fatalf("MarkIgnored: %v", err)
	}
	if err := s2.MarkDone(item); err != nil {
		t.Fatalf("MarkDone: %v", err)
	}
	if !s2.IsDone(item) {
		t.Fatal("expected IsDone to be true after MarkDone")
	}
	if s2.IsIgnored(item) {
		t.Fatal("expected MarkDone to clear the ignored flag — an item lives in exactly one bucket")
	}
}

func TestSaveThenLoadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s := newStoreAtPath(path, "alice")
	item := Item{Key: "owner/repo#1", TriggerDate: time.Now().Truncate(time.Second)}

	if err := s.MarkDone(item); err != nil {
		t.Fatalf("MarkDone: %v", err)
	}

	reloaded := newStoreAtPath(path, "alice")
	if !reloaded.IsDone(item) {
		t.Fatal("expected reloaded store to report the item as done")
	}

	newer := Item{Key: item.Key, TriggerDate: item.TriggerDate.Add(time.Hour)}
	if reloaded.IsDone(newer) {
		t.Fatal("expected reloaded store to still respect the done_until timestamp for newer activity")
	}
}

func TestStateIsScopedPerUser(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	item := Item{Key: "owner/repo#1", TriggerDate: time.Now()}

	// alice marks the PR ignored.
	alice := newStoreAtPath(path, "alice")
	if err := alice.MarkIgnored(item); err != nil {
		t.Fatalf("alice MarkIgnored: %v", err)
	}

	// bob, reading the same file, must see none of alice's state.
	bob := newStoreAtPath(path, "bob")
	if bob.IsDone(item) {
		t.Fatal("expected bob not to see alice's done state")
	}
	if bob.IsIgnored(item) {
		t.Fatal("expected bob not to see alice's ignored state")
	}

	// bob's own marking must not disturb alice's, across reloads.
	if err := bob.MarkDone(item); err != nil {
		t.Fatalf("bob MarkDone: %v", err)
	}
	aliceReloaded := newStoreAtPath(path, "alice")
	if !aliceReloaded.IsIgnored(item) {
		t.Fatal("expected alice's ignored state to survive bob's writes")
	}
	if aliceReloaded.IsDone(item) {
		t.Fatal("expected alice not to see bob's done state")
	}
}

func TestLegacyFlatEntriesMigrateToCurrentUser(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	// Write a pre-user-scoping state file (flat "entries").
	legacy := `{"entries":{"owner/repo#1":{"done_until":"0001-01-01T00:00:00Z","ignored":true}}}`
	if err := os.WriteFile(path, []byte(legacy), 0o644); err != nil {
		t.Fatalf("write legacy file: %v", err)
	}

	item := Item{Key: "owner/repo#1", TriggerDate: time.Now()}
	s := newStoreAtPath(path, "alice")
	if !s.IsIgnored(item) {
		t.Fatal("expected legacy flat entry to be migrated into the current user's bucket")
	}

	// After a save, the legacy flat entries should be gone and the state
	// stored under the user, so a fresh load still sees it and a different
	// user does not.
	if err := s.MarkIgnored(item); err != nil { // triggers a save
		t.Fatalf("MarkIgnored: %v", err)
	}
	if newStoreAtPath(path, "bob").IsIgnored(item) {
		t.Fatal("expected migrated state to be scoped to alice, not visible to bob")
	}
	if !newStoreAtPath(path, "alice").IsIgnored(item) {
		t.Fatal("expected alice to still see the migrated state after save+reload")
	}
}
