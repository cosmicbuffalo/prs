package main

import (
	"path/filepath"
	"testing"
	"time"
)

// testModel builds a Model backed by a scratch store, classifies the given
// items into their tabs, and gives it a non-zero viewport so list-scroll math
// is exercised realistically.
func testModel(t *testing.T, items []Item) Model {
	t.Helper()
	m := Model{
		keys:   DefaultKeyMap(),
		store:  newStoreAtPath(filepath.Join(t.TempDir(), "state.json"), "tester"),
		width:  120,
		height: 40,
	}
	m.classify(items)
	return m
}

// advanceTransition drives the model's in-flight transition through its phase
// ticks until it commits (or fails the test if it never does).
func advanceTransition(t *testing.T, m Model) Model {
	t.Helper()
	for i := 0; i < 10 && m.transition != nil; i++ {
		tm, _ := m.Update(transitionTickMsg{epoch: m.transition.epoch})
		m = tm.(Model)
	}
	if m.transition != nil {
		t.Fatal("transition did not commit within 10 ticks")
	}
	return m
}

// tabOf reports which tab currently holds the item with the given key, or -1.
func tabOf(m Model, key string) int {
	for tab := range m.items {
		for _, it := range m.items[tab] {
			if it.Key == key {
				return tab
			}
		}
	}
	return -1
}

func outstandingItem(key string) Item {
	return Item{Key: key, Section: SectionReviewing, TriggerDate: time.Now()}
}

func TestDestTabFor(t *testing.T) {
	item := outstandingItem("owner/repo#1")
	m := testModel(t, []Item{item})

	if got := m.destTabFor(item, transitionDone); got != tabDone {
		t.Errorf("Enter on an outstanding PR: destTab = %d, want tabDone(%d)", got, tabDone)
	}
	if got := m.destTabFor(item, transitionIgnore); got != tabIgnored {
		t.Errorf("i on an outstanding PR: destTab = %d, want tabIgnored(%d)", got, tabIgnored)
	}

	// Already done ⇒ Enter heads back to the natural bucket (Outstanding).
	if err := m.store.MarkDone(item); err != nil {
		t.Fatalf("MarkDone: %v", err)
	}
	if got := m.destTabFor(item, transitionDone); got != tabOutstanding {
		t.Errorf("Enter on a done PR: destTab = %d, want tabOutstanding(%d)", got, tabOutstanding)
	}
	// ...but i still heads to Ignored (marking ignored will clear done).
	if got := m.destTabFor(item, transitionIgnore); got != tabIgnored {
		t.Errorf("i on a done PR: destTab = %d, want tabIgnored(%d)", got, tabIgnored)
	}
}

func TestTransitionEnterMovesOutstandingToDone(t *testing.T) {
	item := outstandingItem("owner/repo#1")
	m := testModel(t, []Item{item})
	m.activeTab = tabOutstanding

	tm, cmd := m.startTransition(transitionDone)
	m = tm.(Model)

	if m.transition == nil {
		t.Fatal("expected a telegraphed transition to be in flight")
	}
	if cmd == nil {
		t.Fatal("expected a tick command to schedule the first phase")
	}
	if m.transition.destTab != tabDone {
		t.Errorf("destTab = %d, want tabDone(%d)", m.transition.destTab, tabDone)
	}
	if m.transition.phase != phaseCursor {
		t.Errorf("phase = %d, want phaseCursor(%d)", m.transition.phase, phaseCursor)
	}
	// Not committed yet: the PR is still sitting in Outstanding.
	if got := tabOf(m, item.Key); got != tabOutstanding {
		t.Fatalf("mid-transition the PR should still be in Outstanding, found in tab %d", got)
	}

	m = advanceTransition(t, m)
	if got := tabOf(m, item.Key); got != tabDone {
		t.Fatalf("after commit the PR should be in Done, found in tab %d", got)
	}
	if !m.store.IsDone(item) {
		t.Error("expected the store to report the PR as done after commit")
	}
}

func TestTransitionCancelWithSameKey(t *testing.T) {
	item := outstandingItem("owner/repo#1")
	m := testModel(t, []Item{item})

	tm, _ := m.startTransition(transitionDone)
	m = tm.(Model)
	// Same key again cancels the pending move.
	tm, _ = m.startTransition(transitionDone)
	m = tm.(Model)

	if m.transition != nil {
		t.Fatal("expected the pending transition to be cancelled")
	}
	if got := tabOf(m, item.Key); got != tabOutstanding {
		t.Fatalf("a cancelled move should leave the PR in Outstanding, found in tab %d", got)
	}
	if m.store.IsDone(item) {
		t.Error("a cancelled move should not have marked the PR done")
	}
}

func TestTransitionRedirectWithOtherKey(t *testing.T) {
	item := outstandingItem("owner/repo#1")
	m := testModel(t, []Item{item})

	tm, _ := m.startTransition(transitionDone)
	m = tm.(Model)
	// Other key redirects toward Ignored, restarting the animation.
	tm, _ = m.startTransition(transitionIgnore)
	m = tm.(Model)

	if m.transition == nil {
		t.Fatal("expected the redirected transition to still be in flight")
	}
	if m.transition.kind != transitionIgnore || m.transition.destTab != tabIgnored {
		t.Fatalf("expected redirect toward Ignored, got kind=%d destTab=%d", m.transition.kind, m.transition.destTab)
	}
	if m.transition.phase != phaseCursor {
		t.Errorf("redirect should restart from phaseCursor, got phase %d", m.transition.phase)
	}

	m = advanceTransition(t, m)
	if got := tabOf(m, item.Key); got != tabIgnored {
		t.Fatalf("after commit the PR should be in Ignored, found in tab %d", got)
	}
	if m.store.IsDone(item) {
		t.Error("a redirect to Ignored should not have marked the PR done")
	}
}

func TestTransitionReverseOutOfDone(t *testing.T) {
	item := outstandingItem("owner/repo#1")
	m := testModel(t, []Item{item})
	if err := m.store.MarkDone(item); err != nil {
		t.Fatalf("MarkDone: %v", err)
	}
	m.classify(m.allItems())
	if tabOf(m, item.Key) != tabDone {
		t.Fatal("setup: expected the PR to start in Done")
	}
	m.activeTab = tabDone

	// Enter from within Done telegraphs the PR back out to Outstanding.
	tm, cmd := m.startTransition(transitionDone)
	m = tm.(Model)
	if m.transition == nil || cmd == nil {
		t.Fatal("expected a telegraphed reverse transition")
	}
	if m.transition.destTab != tabOutstanding {
		t.Errorf("reverse destTab = %d, want tabOutstanding(%d)", m.transition.destTab, tabOutstanding)
	}

	m = advanceTransition(t, m)
	if got := tabOf(m, item.Key); got != tabOutstanding {
		t.Fatalf("after commit the PR should be back in Outstanding, found in tab %d", got)
	}
	if m.store.IsDone(item) {
		t.Error("reversing out of Done should have cleared the done flag")
	}
}

func TestTransitionIgnoredToDoneClearsIgnored(t *testing.T) {
	item := outstandingItem("owner/repo#1")
	m := testModel(t, []Item{item})
	if err := m.store.MarkIgnored(item); err != nil {
		t.Fatalf("MarkIgnored: %v", err)
	}
	m.classify(m.allItems())
	if tabOf(m, item.Key) != tabIgnored {
		t.Fatal("setup: expected the PR to start in Ignored")
	}
	m.activeTab = tabIgnored

	// Enter on an ignored PR moves it to Done and clears the ignored flag.
	tm, _ := m.startTransition(transitionDone)
	m = tm.(Model)
	if m.transition == nil || m.transition.destTab != tabDone {
		t.Fatalf("expected a transition toward Done, got %+v", m.transition)
	}

	m = advanceTransition(t, m)
	if got := tabOf(m, item.Key); got != tabDone {
		t.Fatalf("after commit the PR should be in Done, found in tab %d", got)
	}
	if !m.store.IsDone(item) {
		t.Error("expected the PR to be done")
	}
	if m.store.IsIgnored(item) {
		t.Error("moving an ignored PR to Done should have cleared the ignored flag")
	}
}

func TestTransitionOnDifferentPRCommitsPrior(t *testing.T) {
	a := outstandingItem("owner/repo#1")
	b := outstandingItem("owner/repo#2")
	m := testModel(t, []Item{a, b})
	m.activeTab = tabOutstanding

	// Start a transition on A, then act on B before A finishes.
	m.cursors[tabOutstanding] = 0
	tm, _ := m.startTransition(transitionDone)
	m = tm.(Model)

	m.cursors[tabOutstanding] = 1 // move to B
	tm, _ = m.startTransition(transitionDone)
	m = tm.(Model)

	// A's pending move should have been committed immediately.
	if !m.store.IsDone(a) {
		t.Error("starting a new transition should have committed the prior PR's move")
	}
	if m.transition == nil || m.transition.key != b.Key {
		t.Fatalf("expected a live transition on B, got %+v", m.transition)
	}

	m = advanceTransition(t, m)
	if !m.store.IsDone(b) {
		t.Error("expected B to be done after its transition commits")
	}
}
