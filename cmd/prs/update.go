package main

import (
	"context"
	"time"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
)

// newDetailMsg delivers the result of a lazy per-PR fetch for a NEW item (see
// Model.ensureNewDetail / FetchNewDetail).
type newDetailMsg struct {
	detail NewDetail
}

// copyResultMsg is emitted when a Copy() call (triggered by the "o" key)
// completes.
type copyResultMsg struct {
	status string
	err    error
}

// clearStatusMsg fires after the transient status message has been shown for
// a while. epoch is compared against Model.statusEpoch so a stale timer from
// an earlier message can't clear a newer one (the "epoch counter" pattern).
type clearStatusMsg struct {
	epoch int
}

// transitionTickMsg advances a telegraphed Enter/i toggle to its next phase.
// epoch is compared against the live transition's epoch so a stale tick (from
// a since-cancelled or -redirected transition) is ignored.
type transitionTickMsg struct {
	epoch int
}

// transitionTickCmd schedules the next phase advance for the transition with
// the given epoch.
func transitionTickCmd(epoch int) tea.Cmd {
	return tea.Tick(transitionStepDelay, func(time.Time) tea.Msg {
		return transitionTickMsg{epoch: epoch}
	})
}

// Update is the Bubble Tea update function.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		// Entry heights (bullet wrapping) and the list viewport both depend on
		// the terminal size, so re-seat every tab's scroll window.
		for _, tab := range [4]int{tabOutstanding, tabNew, tabDone, tabIgnored} {
			m.clampListScroll(tab)
		}
		return m, nil

	case spinner.TickMsg:
		if !m.loading {
			// Drop the tick chain once loading is done so the spinner stops
			// rescheduling itself.
			return m, nil
		}
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd

	case repoUserResolvedMsg:
		if msg.repo != "" {
			m.repo = msg.repo
		}
		if msg.err != nil {
			m.loading = false
			m.err = msg.err
			return m, nil
		}
		m.user = msg.user

		// On first launch (nothing shown yet), load the persisted store and
		// last cached fetch result so the list/tab counts are populated
		// instantly instead of sitting at zero while the fresh fetch runs.
		// A subsequent "r" refresh already has good in-memory data, so it
		// skips this — no need to touch what's already on screen.
		if !m.hasData {
			if store, err := LoadStore(msg.user); err == nil {
				m.store = store
				if cached, ok := LoadCache(msg.repo, msg.user); ok {
					m.classify(cached)
					m.hasData = true
				}
			}
		}

		// If the cached view opened on a NEW PR, start loading its detail now.
		return m, tea.Batch(m.ensureNewDetail(), fetchAllCmd(msg.repo, msg.user))

	case fetchResultMsg:
		m.loading = false
		if msg.repo != "" {
			m.repo = msg.repo
		}
		if msg.user != "" {
			m.user = msg.user
		}
		if msg.err != nil {
			m.err = msg.err
			return m, nil
		}
		m.err = nil
		m.store = msg.store
		// A refresh reclassifies everything; a mid-flight telegraphed toggle
		// would be pointing at now-stale data, so drop it.
		m.transition = nil
		m.transitionEpoch++
		// A refresh rebuilds NEW items from scratch (no detail), so any lazily
		// fetched detail is gone — clear the tracking so it re-fetches on the
		// next select.
		m.newDetailFetching = nil
		m.newDetailLoaded = nil
		m.classify(msg.items)
		m.hasData = true
		m.detailScroll = 0
		return m, m.ensureNewDetail()

	case newDetailMsg:
		return m.applyNewDetail(msg.detail), nil

	case copyResultMsg:
		m.statusEpoch++
		epoch := m.statusEpoch
		if msg.err != nil {
			m.statusMsg = "Copy failed: " + msg.err.Error()
		} else {
			m.statusMsg = msg.status
		}
		return m, clearStatusCmd(epoch)

	case clearStatusMsg:
		if msg.epoch == m.statusEpoch {
			m.statusMsg = ""
		}
		return m, nil

	case transitionTickMsg:
		if m.transition == nil || msg.epoch != m.transition.epoch {
			return m, nil // stale tick from a cancelled/redirected transition
		}
		m.transition.phase++
		if m.transition.phase >= phaseCommit {
			t := m.transition
			m.transition = nil
			m.applyTransition(t)
			return m, nil
		}
		return m, transitionTickCmd(m.transition.epoch)

	case tea.KeyMsg:
		tm, cmd := m.handleKey(msg)
		return withNewDetailFetch(tm, cmd)

	case tea.MouseMsg:
		tm, cmd := m.handleMouse(msg)
		return withNewDetailFetch(tm, cmd)
	}

	return m, nil
}

// withNewDetailFetch appends a lazy NEW-detail fetch to cmd if the model's
// now-selected item is a NEW PR that needs one — so navigating onto a NEW PR
// (by key or mouse) kicks off loading its comments/reviews/commits. It's a
// no-op for any other selection.
func withNewDetailFetch(tm tea.Model, cmd tea.Cmd) (tea.Model, tea.Cmd) {
	m, ok := tm.(Model)
	if !ok {
		return tm, cmd
	}
	if fetch := m.ensureNewDetail(); fetch != nil {
		return m, tea.Batch(cmd, fetch)
	}
	return m, cmd
}

// handleMouse dispatches a mouse event: the wheel always scrolls the detail
// panel (regardless of cursor X position — the list panel has no scroll of
// its own), and a left click either switches tabs or selects the clicked PR
// in the list, depending on where it landed. As with handleKey, interaction
// is only blocked during the very first fetch (m.loading && !m.hasData) —
// once something is on screen, clicks keep working during a background
// refresh too.
func (m Model) handleMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	switch msg.Button {
	case tea.MouseButtonWheelUp:
		if m.overListPanel(msg.X, msg.Y) {
			m.moveCursor(-1)
			m.detailScroll = 0
			return m, nil
		}
		m.detailScroll -= mouseScrollStep
		if m.detailScroll < 0 {
			m.detailScroll = 0
		}
		return m, nil

	case tea.MouseButtonWheelDown:
		if m.overListPanel(msg.X, msg.Y) {
			m.moveCursor(1)
			m.detailScroll = 0
			return m, nil
		}
		m.detailScroll += mouseScrollStep
		return m, nil

	case tea.MouseButtonLeft:
		if msg.Action != tea.MouseActionPress || (m.loading && !m.hasData) {
			return m, nil
		}
		if idx, ok := m.hitTestTab(msg.X, msg.Y); ok {
			m.activeTab = idx
			m.clampListScroll(m.activeTab)
			m.detailScroll = 0
			return m, nil
		}
		if idx, ok := m.hitTestListItem(msg.X, msg.Y); ok {
			m.cursors[m.activeTab] = idx
			m.clampListScroll(m.activeTab)
			m.detailScroll = 0
			return m, nil
		}
	}
	return m, nil
}

// handleKey dispatches a key press. Quit always works. Refresh is ignored if
// a fetch is already in progress (avoids kicking off a second, overlapping
// pipeline). Every other key is only blocked during the very first fetch,
// before there's anything on screen to navigate — once cached or fetched
// data is showing (m.hasData), navigation/toggle/copy keep working normally
// even while a background refresh is running.
func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// Ctrl+C is a hard quit that always works, even with the help overlay open.
	if msg.String() == "ctrl+c" {
		return m, tea.Quit
	}

	// The help overlay (see keys.Help / Model.showHelp) is modal: while it's
	// open, "?", "q", or Esc close it and every other key is swallowed — so "q"
	// closes the overlay rather than quitting the app. Handled before the Quit
	// binding below for exactly that reason.
	if m.showHelp {
		if key.Matches(msg, m.keys.Help) || msg.String() == "q" || msg.String() == "esc" {
			m.showHelp = false
		}
		return m, nil
	}

	if key.Matches(msg, m.keys.Quit) {
		return m, tea.Quit
	}

	if key.Matches(msg, m.keys.Help) {
		m.showHelp = true
		return m, nil
	}

	if key.Matches(msg, m.keys.Refresh) {
		if m.loading {
			return m, nil
		}
		m.loading = true
		m.err = nil
		m.transition = nil
		m.transitionEpoch++
		return m, tea.Batch(m.spinner.Tick, resolveRepoUserCmd(m.repoOverride, m.userOverride))
	}

	if m.loading && !m.hasData {
		return m, nil
	}

	switch {
	case key.Matches(msg, m.keys.Up):
		m.moveCursor(-1)
		m.detailScroll = 0
		return m, nil

	case key.Matches(msg, m.keys.Down):
		m.moveCursor(1)
		m.detailScroll = 0
		return m, nil

	case key.Matches(msg, m.keys.Left):
		if m.activeTab > tabOutstanding {
			m.activeTab--
		}
		m.clampListScroll(m.activeTab)
		m.detailScroll = 0
		return m, nil

	case key.Matches(msg, m.keys.Right):
		if m.activeTab < tabIgnored {
			m.activeTab++
		}
		m.clampListScroll(m.activeTab)
		m.detailScroll = 0
		return m, nil

	case key.Matches(msg, m.keys.Toggle):
		return m.startTransition(transitionDone)

	case key.Matches(msg, m.keys.Ignore):
		return m.startTransition(transitionIgnore)

	case key.Matches(msg, m.keys.ScrollDown):
		m.detailScroll += detailScrollStep
		return m, nil

	case key.Matches(msg, m.keys.ScrollUp):
		m.detailScroll -= detailScrollStep
		if m.detailScroll < 0 {
			m.detailScroll = 0
		}
		return m, nil

	case key.Matches(msg, m.keys.Layout):
		if m.layout == layoutHorizontal {
			m.layout = layoutVertical
		} else {
			m.layout = layoutHorizontal
		}
		// The list viewport height/width changes with the layout, so re-seat
		// the scroll window around the cursor and reset the detail scroll.
		m.clampListScroll(m.activeTab)
		m.detailScroll = 0
		return m, nil

	case key.Matches(msg, m.keys.Copy):
		return m.copySelected()
	}

	return m, nil
}

// moveCursor shifts the current tab's cursor by delta, clamped to the
// current tab's list bounds, then adjusts the scroll window just enough to
// keep it on-screen. Cursor movement never affects the other tab.
func (m *Model) moveCursor(delta int) {
	n := len(m.items[m.activeTab])
	if n == 0 {
		return
	}
	c := m.cursors[m.activeTab] + delta
	if c < 0 {
		c = 0
	}
	if c > n-1 {
		c = n - 1
	}
	m.cursors[m.activeTab] = c
	m.clampListScroll(m.activeTab)
}

// clampListScroll adjusts tab's scroll offset (the index of the first visible
// entry) by the minimum amount needed to keep its cursor's entry fully within
// the visible window. Entries are variable-height, so it measures their actual
// row counts (at the current layout's list width/height) rather than assuming
// a fixed per-entry height: the window is pushed down only far enough that the
// cursor's entry fits within the available rows.
func (m *Model) clampListScroll(tab int) {
	items := m.items[tab]
	n := len(items)
	if n == 0 {
		m.listScroll[tab] = 0
		return
	}

	width := m.listContentWidth()
	counts := m.entryLineCounts(tab, width)
	avail := m.listViewportHeight() - 1 // one row reserved for the ↑ indicator
	if avail < 1 {
		avail = 1
	}

	cursor := m.cursors[tab]
	start := m.listScroll[tab]
	if start < 0 {
		start = 0
	}
	if start > cursor {
		start = cursor
	}
	// Push the window down until the cursor's entry fits within avail rows
	// measured from the first visible entry.
	for start < cursor {
		used := 0
		for i := start; i <= cursor; i++ {
			used += counts[i]
		}
		if used <= avail {
			break
		}
		start++
	}
	m.listScroll[tab] = start
}

// selectedItem returns the item under the cursor in the active tab, if any.
func (m Model) selectedItem() (Item, bool) {
	items := m.items[m.activeTab]
	cursor := m.cursors[m.activeTab]
	if cursor < 0 || cursor >= len(items) {
		return Item{}, false
	}
	return items[cursor], true
}

// allItems returns every item currently known across all tabs, concatenated
// (used to rebuild the full set before reclassifying after a toggle, so
// items sitting in tabs other than the one the toggle happened in aren't
// silently dropped).
func (m Model) allItems() []Item {
	total := 0
	for _, tab := range m.items {
		total += len(tab)
	}
	all := make([]Item, 0, total)
	for _, tab := range m.items {
		all = append(all, tab...)
	}
	return all
}

// itemByKey finds the item with the given key across all tabs.
func (m Model) itemByKey(key string) (Item, bool) {
	for _, tab := range m.items {
		for _, it := range tab {
			if it.Key == key {
				return it, true
			}
		}
	}
	return Item{}, false
}

// naturalTab returns the tab an item lands in based only on its intrinsic
// state (FetchError/Quiet/Section), ignoring any Done/Ignored store flag —
// i.e. where it goes once it's neither marked done nor ignored. It mirrors the
// non-store branches of classify; keep the two in sync.
func naturalTab(item Item) int {
	switch {
	case item.FetchError != "":
		return tabOutstanding
	case item.Quiet:
		return tabDone
	case item.Section == SectionNew:
		return tabNew
	default:
		return tabOutstanding
	}
}

// destTabFor resolves which tab a keypress of kind would send item toward,
// given the store's current verdict. Enter (transitionDone) heads to Done, or
// back to the item's natural bucket if it's already done; i (transitionIgnore)
// heads to Ignored, or back to the natural bucket if it's already ignored.
// (Marking done clears ignored and vice versa, so e.g. Enter on an ignored PR
// still resolves to Done.)
func (m Model) destTabFor(item Item, kind transitionKind) int {
	if kind == transitionIgnore {
		if m.store.IsIgnored(item) {
			return naturalTab(item)
		}
		return tabIgnored
	}
	if m.store.IsDone(item) {
		return naturalTab(item)
	}
	return tabDone
}

// applyToggle performs the store mutation a keypress of kind implies for item:
// Enter toggles done, i toggles ignored. Marking one clears the other (see
// Store.MarkDone/MarkIgnored), so an item is only ever in one of the two.
func (m *Model) applyToggle(item Item, kind transitionKind) error {
	if kind == transitionIgnore {
		if m.store.IsIgnored(item) {
			return m.store.MarkUnignored(item)
		}
		return m.store.MarkIgnored(item)
	}
	if m.store.IsDone(item) {
		return m.store.MarkUndone(item)
	}
	return m.store.MarkDone(item)
}

// startTransition handles an Enter (transitionDone) or i (transitionIgnore)
// press on the selected PR. It resolves where that press sends the PR — into
// Done/Ignored, or back out to its natural bucket if it's already there (see
// destTabFor) — and telegraphs the move as a phased animation that only
// commits to the store once it finishes (see the transition type and
// transitionTickMsg). If the destination is the tab already in view (nothing
// would visibly move, e.g. un-doing an intrinsically Quiet PR), it's applied
// instantly with no telegraph.
//
// While a transition is in flight: pressing the same key again on the same PR
// cancels it; pressing the other key redirects it toward the other
// destination (restarting the animation). A transition in flight on a
// different PR is committed immediately before this one is acted on, so only
// one is ever live at a time.
func (m Model) startTransition(kind transitionKind) (tea.Model, tea.Cmd) {
	item, ok := m.selectedItem()
	if !ok || m.store == nil {
		return m, nil
	}

	// Same PR already telegraphing + same key ⇒ cancel the pending move.
	if m.transition != nil && m.transition.key == item.Key && m.transition.kind == kind {
		m.transition = nil
		m.transitionEpoch++
		return m, nil
	}

	// A transition in flight on a *different* PR is committed now (its intended
	// end state) before we act on this one. A same-PR transition (i.e. a
	// redirect via the other key) is instead discarded in favor of the new
	// destination resolved below.
	if m.transition != nil && m.transition.key != item.Key {
		m.applyTransition(m.transition)
	}
	m.transition = nil

	destTab := m.destTabFor(item, kind)

	// Destination is the tab already in view — nothing would visibly move, so
	// apply the toggle instantly with no telegraph.
	if destTab == m.activeTab {
		m.transitionEpoch++
		if err := m.applyToggle(item, kind); err != nil {
			m.err = err
			return m, nil
		}
		m.classify(m.allItems())
		m.detailScroll = 0
		return m, nil
	}

	m.transitionEpoch++
	m.transition = &transition{
		key:     item.Key,
		kind:    kind,
		destTab: destTab,
		phase:   phaseCursor,
		epoch:   m.transitionEpoch,
	}
	return m, transitionTickCmd(m.transitionEpoch)
}

// applyTransition commits a telegraphed toggle to the store and reclassifies
// so the PR moves into its destination tab.
func (m *Model) applyTransition(t *transition) {
	item, ok := m.itemByKey(t.key)
	if !ok || m.store == nil {
		return
	}
	if err := m.applyToggle(item, t.kind); err != nil {
		m.err = err
		return
	}
	m.classify(m.allItems())
	m.detailScroll = 0
}

// copySelected copies the selected item's URL to the clipboard.
func (m Model) copySelected() (tea.Model, tea.Cmd) {
	item, ok := m.selectedItem()
	if !ok {
		return m, nil
	}
	url := item.URL
	return m, func() tea.Msg {
		status, err := Copy(url)
		return copyResultMsg{status: status, err: err}
	}
}

// ensureNewDetail returns a command that lazily fetches the selected PR's
// per-PR data when it's a NEW item that hasn't been (and isn't already being)
// fetched. It marks the fetch in-flight so duplicates aren't launched, and
// returns nil when there's nothing to do (selection isn't a NEW PR, it's
// already loaded/loading, or the repo isn't resolved yet).
func (m *Model) ensureNewDetail() tea.Cmd {
	item, ok := m.selectedItem()
	if !ok || m.repo == "" || item.Section != SectionNew {
		return nil
	}
	if m.newDetailLoaded[item.Key] || m.newDetailFetching[item.Key] {
		return nil
	}
	if m.newDetailFetching == nil {
		m.newDetailFetching = make(map[string]bool)
	}
	m.newDetailFetching[item.Key] = true

	repo, key, author, number := m.repo, item.Key, item.Author, item.Number
	return func() tea.Msg {
		return newDetailMsg{detail: FetchNewDetail(context.Background(), repo, key, author, number)}
	}
}

// applyNewDetail merges a completed lazy NEW-detail fetch back into the item it
// was fetched for and marks it loaded so it isn't refetched. A fetch error is
// left unmarked so revisiting the PR retries. Adding review data can change the
// item's list-entry height (review icons in the bullet), so the scroll window
// is re-clamped.
func (m Model) applyNewDetail(d NewDetail) Model {
	delete(m.newDetailFetching, d.Key)
	if d.Err != nil {
		return m // leave it retryable
	}
	if m.newDetailLoaded == nil {
		m.newDetailLoaded = make(map[string]bool)
	}
	m.newDetailLoaded[d.Key] = true

	for tab := range m.items {
		for i := range m.items[tab] {
			if m.items[tab][i].Key != d.Key {
				continue
			}
			m.items[tab][i].Detail = d.Detail
			m.items[tab][i].Commits = d.Commits
			m.items[tab][i].Reviewers = d.Reviewers
			m.items[tab][i].ParticipantLogins = d.ParticipantLogins
			m.items[tab][i].ParticipantCount = d.ParticipantCount
			m.items[tab][i].TotalComments = d.TotalComments
		}
	}
	m.clampListScroll(m.activeTab)
	return m
}

// classify splits items into the outstanding/new/done/ignored tabs based on
// the store's current verdict for each and each item's Section/flags (relative
// order within each resulting tab is preserved from the input slice), then
// clamps every tab's cursor in-bounds. Precedence (highest first):
//   - Ignored (store flag) — an ignored PR stays hidden regardless of anything
//     else, including a fetch error.
//   - FetchError — a PR whose data failed to load is shown in Outstanding so
//     the failure is visible and a refresh is prompted.
//   - Done — either the store's done flag OR an intrinsically Quiet PR (the
//     user is involved but nothing new has happened); both mean "nothing to
//     do right now".
//   - New (SectionNew) — a PR the user hasn't touched at all.
//   - Outstanding — everything else (Reviewing/Authored with new activity).
func (m *Model) classify(items []Item) {
	outstanding := make([]Item, 0, len(items))
	newItems := make([]Item, 0, len(items))
	done := make([]Item, 0, len(items))
	ignored := make([]Item, 0, len(items))
	for _, it := range items {
		switch {
		case m.store != nil && m.store.IsIgnored(it):
			ignored = append(ignored, it)
		case it.FetchError != "":
			outstanding = append(outstanding, it)
		case (m.store != nil && m.store.IsDone(it)) || it.Quiet:
			done = append(done, it)
		case it.Section == SectionNew:
			newItems = append(newItems, it)
		default:
			outstanding = append(outstanding, it)
		}
	}
	m.items[tabOutstanding] = outstanding
	m.items[tabNew] = newItems
	m.items[tabDone] = done
	m.items[tabIgnored] = ignored

	for _, tab := range [4]int{tabOutstanding, tabNew, tabDone, tabIgnored} {
		m.clampCursor(tab)
		m.clampListScroll(tab)
	}
}

// clampCursor keeps the given tab's cursor within [0, len-1] (or 0 if empty).
func (m *Model) clampCursor(tab int) {
	n := len(m.items[tab])
	if n == 0 {
		m.cursors[tab] = 0
		return
	}
	if m.cursors[tab] >= n {
		m.cursors[tab] = n - 1
	}
	if m.cursors[tab] < 0 {
		m.cursors[tab] = 0
	}
}

// clearStatusCmd schedules a clearStatusMsg carrying epoch after
// statusDuration, per the epoch-counter pattern described on Model.statusEpoch.
func clearStatusCmd(epoch int) tea.Cmd {
	return tea.Tick(statusDuration, func(time.Time) tea.Msg {
		return clearStatusMsg{epoch: epoch}
	})
}
