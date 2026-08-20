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

// advanceTransition drives every in-flight transition through its phase ticks
// until they all commit (or fails the test if they never do). Ticking the head
// transition's epoch repeatedly advances then commits it (removing it), then
// the next becomes the head.
func advanceTransition(t *testing.T, m Model) Model {
	t.Helper()
	for i := 0; i < 100 && len(m.transitions) > 0; i++ {
		tm, _ := m.Update(transitionTickMsg{epoch: m.transitions[0].epoch})
		m = tm.(Model)
	}
	if len(m.transitions) > 0 {
		t.Fatal("transitions did not all commit within the tick budget")
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

	tr := m.transitionByKey(item.Key)
	if tr == nil {
		t.Fatal("expected a telegraphed transition to be in flight")
	}
	if cmd == nil {
		t.Fatal("expected a tick command to schedule the first phase")
	}
	if tr.destTab != tabDone {
		t.Errorf("destTab = %d, want tabDone(%d)", tr.destTab, tabDone)
	}
	if tr.phase != phaseCursor {
		t.Errorf("phase = %d, want phaseCursor(%d)", tr.phase, phaseCursor)
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
	// Same key again cancels the pending move. (Single item, so the cursor
	// stayed put and is still on it.)
	tm, _ = m.startTransition(transitionDone)
	m = tm.(Model)

	if len(m.transitions) != 0 {
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

	tr := m.transitionByKey(item.Key)
	if tr == nil {
		t.Fatal("expected the redirected transition to still be in flight")
	}
	if tr.kind != transitionIgnore || tr.destTab != tabIgnored {
		t.Fatalf("expected redirect toward Ignored, got kind=%d destTab=%d", tr.kind, tr.destTab)
	}
	if tr.phase != phaseCursor {
		t.Errorf("redirect should restart from phaseCursor, got phase %d", tr.phase)
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
	tr := m.transitionByKey(item.Key)
	if tr == nil || cmd == nil {
		t.Fatal("expected a telegraphed reverse transition")
	}
	if tr.destTab != tabOutstanding {
		t.Errorf("reverse destTab = %d, want tabOutstanding(%d)", tr.destTab, tabOutstanding)
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
	if tr := m.transitionByKey(item.Key); tr == nil || tr.destTab != tabDone {
		t.Fatalf("expected a transition toward Done, got %+v", tr)
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

// TestToggleAdvancesCursorToNext: acting on a PR stages its move and advances
// the cursor to the next PR right away; once the staged PR commits and leaves,
// the cursor stays locked on the PR it advanced to.
func TestToggleAdvancesCursorToNext(t *testing.T) {
	a := outstandingItem("owner/repo#1")
	b := outstandingItem("owner/repo#2")
	c := outstandingItem("owner/repo#3")
	m := testModel(t, []Item{a, b, c})
	m.activeTab = tabOutstanding
	m.cursors[tabOutstanding] = 0 // on A

	tm, _ := m.startTransition(transitionDone)
	m = tm.(Model)
	if m.transitionByKey(a.Key) == nil {
		t.Fatal("expected A to be staged")
	}
	if sel, _ := m.selectedItem(); sel.Key != b.Key {
		t.Fatalf("cursor should have advanced to B, got %q", sel.Key)
	}
	if got := tabOf(m, a.Key); got != tabOutstanding {
		t.Fatalf("A should still be in Outstanding mid-telegraph, found tab %d", got)
	}

	m = advanceTransition(t, m)
	if got := tabOf(m, a.Key); got != tabDone {
		t.Fatalf("A should be in Done after commit, found tab %d", got)
	}
	if sel, ok := m.selectedItem(); !ok || sel.Key != b.Key {
		t.Fatalf("cursor should stay locked on B, got %q (ok=%v)", sel.Key, ok)
	}
}

// TestToggleLastItemMovesCursorToPrevious: with no next PR to advance to, the
// cursor stays on the acted-on PR while it telegraphs, then falls back to the
// previous PR once it leaves.
func TestToggleLastItemMovesCursorToPrevious(t *testing.T) {
	a := outstandingItem("owner/repo#1")
	b := outstandingItem("owner/repo#2")
	m := testModel(t, []Item{a, b})
	m.activeTab = tabOutstanding
	m.cursors[tabOutstanding] = 1 // on B, the last item

	tm, _ := m.startTransition(transitionDone)
	m = tm.(Model)
	if sel, _ := m.selectedItem(); sel.Key != b.Key {
		t.Fatalf("with no next item the cursor should stay on B, got %q", sel.Key)
	}

	m = advanceTransition(t, m)
	if sel, ok := m.selectedItem(); !ok || sel.Key != a.Key {
		t.Fatalf("cursor should fall back to A after B leaves, got %q (ok=%v)", sel.Key, ok)
	}
}

func TestStackedTransitionsCoexistAndCommit(t *testing.T) {
	a := outstandingItem("owner/repo#1")
	b := outstandingItem("owner/repo#2")
	c := outstandingItem("owner/repo#3")
	m := testModel(t, []Item{a, b, c})
	m.activeTab = tabOutstanding
	m.cursors[tabOutstanding] = 0

	// Tap i three times: each stages the PR under the cursor toward Ignored and
	// advances, so all three end up staged at once with none committing early.
	for i := 0; i < 3; i++ {
		tm, _ := m.startTransition(transitionIgnore)
		m = tm.(Model)
	}
	if len(m.transitions) != 3 {
		t.Fatalf("expected 3 stacked transitions, got %d", len(m.transitions))
	}
	for _, it := range []Item{a, b, c} {
		if m.store.IsIgnored(it) {
			t.Fatalf("no PR should be committed yet, but %s is ignored", it.Key)
		}
	}

	// They then all commit, one per delay, landing every PR in Ignored.
	m = advanceTransition(t, m)
	for _, it := range []Item{a, b, c} {
		if got := tabOf(m, it.Key); got != tabIgnored {
			t.Fatalf("%s should be in Ignored after commit, found tab %d", it.Key, got)
		}
		if !m.store.IsIgnored(it) {
			t.Errorf("%s should be marked ignored", it.Key)
		}
	}
}

// TestCancelOneOfSeveralStagedMoves: with several moves stacked, navigating back
// to one and pressing its key again cancels just that PR's move; the rest still
// commit.
func TestCancelOneOfSeveralStagedMoves(t *testing.T) {
	a := outstandingItem("owner/repo#1")
	b := outstandingItem("owner/repo#2")
	c := outstandingItem("owner/repo#3")
	m := testModel(t, []Item{a, b, c})
	m.activeTab = tabOutstanding
	m.cursors[tabOutstanding] = 0

	for i := 0; i < 3; i++ {
		tm, _ := m.startTransition(transitionIgnore)
		m = tm.(Model)
	}

	// Nothing has committed, so Outstanding still holds [A, B, C]; go back to B.
	m.cursors[tabOutstanding] = 1
	if sel, _ := m.selectedItem(); sel.Key != b.Key {
		t.Fatalf("setup: expected cursor on B, got %q", sel.Key)
	}
	tm, _ := m.startTransition(transitionIgnore) // same key ⇒ cancel B's move
	m = tm.(Model)

	if m.transitionByKey(b.Key) != nil {
		t.Fatal("B's staged move should have been cancelled")
	}
	if len(m.transitions) != 2 {
		t.Fatalf("expected 2 remaining staged moves, got %d", len(m.transitions))
	}

	m = advanceTransition(t, m)
	if !m.store.IsIgnored(a) || !m.store.IsIgnored(c) {
		t.Error("A and C should be ignored after the rest commit")
	}
	if m.store.IsIgnored(b) {
		t.Error("B's move was cancelled, so it should not be ignored")
	}
	if got := tabOf(m, b.Key); got != tabOutstanding {
		t.Fatalf("B should remain in Outstanding, found tab %d", got)
	}
}
