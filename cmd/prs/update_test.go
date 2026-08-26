package main

import (
	"path/filepath"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
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

// keyR is a capital-R key press (the live-refresh toggle).
var keyR = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'R'}}

// TestLiveRefreshToggle: R flips the flag, invalidates any pending tick by
// bumping the epoch, and shows a status message; toggling back on returns a
// (re-armed) tick command.
func TestLiveRefreshToggle(t *testing.T) {
	m := testModel(t, []Item{outstandingItem("owner/repo#1")})
	m.liveRefresh = true
	m.hasData = true

	// Off.
	tm, _ := m.handleKey(keyR)
	m = tm.(Model)
	if m.liveRefresh {
		t.Fatal("R should have turned live refresh off")
	}
	if m.statusMsg != "Live refresh off" {
		t.Errorf("status = %q, want %q", m.statusMsg, "Live refresh off")
	}
	offEpoch := m.liveRefreshEpoch

	// A tick scheduled before the toggle-off is now stale and must be ignored.
	tm, cmd := m.Update(liveRefreshTickMsg{epoch: offEpoch})
	m = tm.(Model)
	if m.loading {
		t.Error("a live-refresh tick should not start a fetch while live refresh is off")
	}
	if cmd != nil {
		t.Error("a stale/off live-refresh tick should not reschedule")
	}

	// Back on: re-arms (bumps epoch, returns a tick command).
	tm, cmd = m.handleKey(keyR)
	m = tm.(Model)
	if !m.liveRefresh {
		t.Fatal("R should have turned live refresh back on")
	}
	if m.liveRefreshEpoch == offEpoch {
		t.Error("turning live refresh on should have re-armed with a fresh epoch")
	}
	if cmd == nil {
		t.Error("turning live refresh on should return a re-arm command batch")
	}
	if m.statusMsg != "Live refresh on" {
		t.Errorf("status = %q, want %q", m.statusMsg, "Live refresh on")
	}
}

// TestLiveRefreshTickTriggersRefresh: a current-epoch tick with live refresh on
// (and no fetch in flight) starts a refresh.
func TestLiveRefreshTickTriggersRefresh(t *testing.T) {
	m := testModel(t, []Item{outstandingItem("owner/repo#1")})
	m.liveRefresh = true
	m.hasData = true

	tm, cmd := m.Update(liveRefreshTickMsg{epoch: m.liveRefreshEpoch})
	m = tm.(Model)
	if !m.loading {
		t.Error("a current-epoch live-refresh tick should have started a fetch")
	}
	if cmd == nil {
		t.Error("beginRefresh should return a command batch")
	}
}

// TestLiveRefreshStaleTickIgnored: a tick whose epoch no longer matches (e.g.
// the countdown was re-armed since) is a no-op even with live refresh on.
func TestLiveRefreshStaleTickIgnored(t *testing.T) {
	m := testModel(t, []Item{outstandingItem("owner/repo#1")})
	m.liveRefresh = true
	m.hasData = true

	tm, cmd := m.Update(liveRefreshTickMsg{epoch: m.liveRefreshEpoch - 1})
	m = tm.(Model)
	if m.loading {
		t.Error("a stale-epoch tick should not start a fetch")
	}
	if cmd != nil {
		t.Error("a stale-epoch tick should not reschedule")
	}
}

// TestLiveRefreshTickWhileLoadingReArmsOnly: if a fetch is already running when
// a tick fires, it re-arms the countdown without stacking a second fetch.
func TestLiveRefreshTickWhileLoadingReArmsOnly(t *testing.T) {
	m := testModel(t, []Item{outstandingItem("owner/repo#1")})
	m.liveRefresh = true
	m.hasData = true
	m.loading = true
	prevEpoch := m.liveRefreshEpoch

	tm, cmd := m.Update(liveRefreshTickMsg{epoch: m.liveRefreshEpoch})
	m = tm.(Model)
	if m.liveRefreshEpoch == prevEpoch {
		t.Error("a tick during a fetch should still re-arm the countdown")
	}
	if cmd == nil {
		t.Error("a tick during a fetch should reschedule the next tick")
	}
}

// TestManualRefreshResetsLiveCountdown: beginRefresh (the "r" key path) re-arms
// the live-refresh countdown, so a manual refresh restarts the 5-minute timer.
func TestManualRefreshResetsLiveCountdown(t *testing.T) {
	m := testModel(t, []Item{outstandingItem("owner/repo#1")})
	m.liveRefresh = true
	m.hasData = true
	prevEpoch := m.liveRefreshEpoch

	tm, _ := m.beginRefresh()
	m = tm.(Model)
	if !m.loading {
		t.Error("beginRefresh should set loading")
	}
	if m.liveRefreshEpoch == prevEpoch {
		t.Error("a manual refresh should re-arm (bump the epoch of) the live countdown")
	}

	// With live refresh off, beginRefresh must not arm anything.
	m2 := testModel(t, []Item{outstandingItem("owner/repo#2")})
	m2.liveRefresh = false
	before := m2.liveRefreshEpoch
	tm2, _ := m2.beginRefresh()
	m2 = tm2.(Model)
	if m2.liveRefreshEpoch != before {
		t.Error("with live refresh off, beginRefresh should not touch the epoch")
	}
}

func TestFooterAge(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name string
		t    time.Time
		want string
	}{
		{"zero", time.Time{}, "never"},
		{"seconds", now.Add(-5 * time.Second), "5 seconds ago"},
		{"one second", now.Add(-1 * time.Second), "1 second ago"},
		{"minutes", now.Add(-3 * time.Minute), "3 minutes ago"},
		{"hours", now.Add(-2 * time.Hour), "2 hours ago"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := footerAge(c.t); got != c.want {
				t.Errorf("footerAge = %q, want %q", got, c.want)
			}
		})
	}
}
