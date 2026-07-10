package main

import (
	"context"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
)

// Tab indices into Model.items / Model.cursors.
const (
	tabOutstanding = 0
	tabNew         = 1
	tabDone        = 2
	tabIgnored     = 3
)

// statusDuration is how long a transient status message (from "o" copy or an
// error) stays visible before auto-clearing.
const statusDuration = 2 * time.Second

// detailScrollStep is how many lines ctrl+d/ctrl+u scroll the detail panel
// per press.
const detailScrollStep = 8

// mouseScrollStep is how many lines the mouse wheel scrolls the detail
// panel per notch — smaller than detailScrollStep since a wheel typically
// sends several events per physical "click".
const mouseScrollStep = 3

// transitionStepDelay is how long each phase of a telegraphed Enter/i toggle
// lingers before advancing to the next (see transitionPhase). Total time
// from keypress to the PR actually moving is roughly 3× this.
const transitionStepDelay = 350 * time.Millisecond

// transitionKind identifies which key started a telegraphed toggle — Enter
// (the Done/undone key) or i (the Ignored/un-ignored key). It's used to match
// a follow-up keypress against an in-flight transition (same key ⇒ cancel,
// other key ⇒ redirect); the actual destination bucket a press resolves to is
// computed separately (see destTabFor) since either key can also move an item
// back OUT of Done/Ignored toward its natural bucket.
type transitionKind int

const (
	transitionDone   transitionKind = iota // Enter key: → Done, or back out of Done
	transitionIgnore                       // i key: → Ignored, or back out of Ignored
)

// transitionPhase steps a pending toggle through its telegraph animation
// before the change is committed to the store and the PR actually moves.
type transitionPhase int

const (
	phaseCursor transitionPhase = iota // cursor bar recolored; PR still in place
	phaseTab                           // destination tab label highlighted
	phaseCount                         // destination tab count shown incremented
	phaseCommit                        // apply to store + reclassify (PR leaves)
)

// transition is an in-flight, not-yet-committed Enter/i toggle being
// telegraphed to the user (so they can watch it, cancel it by pressing the
// same key again, or redirect it by pressing the other key). Exactly one is
// active at a time. epoch guards against stale tick timers after a
// cancel/redirect: only a tick whose epoch matches the live transition's is
// acted on.
type transition struct {
	key  string
	kind transitionKind
	// destTab is the tab the PR is telegraphing toward — tabDone/tabIgnored
	// when marking, or its natural bucket (Outstanding/New/Done) when a press
	// moves it back out. It drives which tab flashes and the animation color
	// (bucketColor(destTab)), so a move back out shows its destination's color
	// rather than always green/red.
	destTab int
	phase   transitionPhase
	epoch   int
}

// Model is the root Bubble Tea model for the prs TUI.
type Model struct {
	keys KeyMap

	// repoOverride / userOverride are the --repo / --as_user flag values,
	// threaded through to RepoFromCwd / CurrentUser on every fetch.
	repoOverride string
	userOverride string

	// repo / user are the resolved values from the most recent successful
	// fetch, used for header display and the loading message.
	repo string
	user string

	store *Store

	loading bool
	spinner spinner.Model

	// hasData is true once the list/detail panels have ever shown real
	// content — either a cached result loaded instantly on startup, or a
	// completed fresh fetch. Once true, it stays true: a later "r" refresh
	// or background reload shows a small "Refreshing..." status-line
	// indicator instead of hiding everything behind the full loading
	// spinner again.
	hasData bool

	width  int
	height int

	activeTab int
	items     [4][]Item // indexed by tabOutstanding / tabNew / tabDone / tabIgnored
	cursors   [4]int    // per-tab remembered cursor position

	// listScroll is each tab's persisted list-window scroll offset (the
	// index of the first visible item). It only moves the minimum amount
	// needed to keep the cursor on-screen — the cursor can reach the very
	// last visible row before the window scrolls, rather than always being
	// kept centered. Adjusted via clampListScroll() wherever the cursor,
	// active tab, or item counts might have changed.
	listScroll [4]int

	// detailScroll is how many lines the detail (right) panel is scrolled
	// down by (ctrl+d/ctrl+u). Reset to 0 whenever the selected item might
	// change (cursor move, tab switch, toggle, refresh) so a new PR's detail
	// always opens scrolled to the top.
	detailScroll int

	// transition holds the currently-telegraphing Enter/i toggle, if any (see
	// the transition type). transitionEpoch is bumped on every start/cancel/
	// redirect so stale phase-tick timers can be ignored.
	transition      *transition
	transitionEpoch int

	// statusMsg is the transient one-line message shown above the footer
	// (e.g. "Copied to clipboard", or a copy/fetch error). statusEpoch
	// implements the "epoch counter" pattern: each time statusMsg is set we
	// bump the epoch and schedule a clearStatusMsg carrying that epoch, so a
	// stale timer from an earlier message can't clobber a newer one.
	statusMsg   string
	statusEpoch int

	// err holds the most recent fetch error, if any. It's cleared whenever a
	// fetch is (re)started or succeeds. Unlike statusMsg it isn't
	// auto-cleared by a timer, since a fetch failure is worth leaving on
	// screen until the user retries.
	err error
}

// NewModel builds the initial Model, ready to be handed to tea.NewProgram.
// The fetch itself doesn't happen until Init() runs.
func NewModel(repoOverride, userOverride string) Model {
	sp := spinner.New()
	sp.Spinner = spinner.Dot
	sp.Style = spinnerStyle

	return Model{
		keys:         DefaultKeyMap(),
		repoOverride: repoOverride,
		userOverride: userOverride,
		loading:      true,
		spinner:      sp,
		activeTab:    tabOutstanding,
	}
}

// Init starts the spinner animation and kicks off repo/user resolution (the
// first of two sequential steps — see repoUserResolvedMsg).
func (m Model) Init() tea.Cmd {
	return tea.Batch(m.spinner.Tick, resolveRepoUserCmd(m.repoOverride, m.userOverride))
}

// repoUserResolvedMsg is emitted as soon as the repo/user are known — this
// is a separate, fast, local-only step (no network calls) from the rest of
// the fetch pipeline, so the header can show "Repo: owner/name" right away
// instead of waiting on the much slower GitHub API calls in fetchAllCmd.
type repoUserResolvedMsg struct {
	repo string
	user string
	err  error
}

// resolveRepoUserCmd resolves the repo and user. It's used both for the
// initial load (from Init) and for a mid-session refresh (the "r" key).
func resolveRepoUserCmd(repoOverride, userOverride string) tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()

		repo, err := RepoFromCwd(ctx, repoOverride)
		if err != nil {
			return repoUserResolvedMsg{err: err}
		}

		user, err := CurrentUser(ctx, userOverride)
		if err != nil {
			return repoUserResolvedMsg{repo: repo, err: err}
		}

		return repoUserResolvedMsg{repo: repo, user: user}
	}
}

// fetchResultMsg is emitted when the (already repo/user-resolved) fetch
// pipeline completes, whether triggered by the initial launch or a
// mid-session "r" refresh.
type fetchResultMsg struct {
	repo  string
	user  string
	items []Item
	store *Store
	err   error
}

// fetchAllCmd fetches+classifies every PR needing attention for the given
// (already-resolved) repo/user, loads the persisted done/undone state, and
// prunes stale entries. Re-runs the whole pipeline from scratch rather than
// trying to patch the existing state in place.
func fetchAllCmd(repo, user string) tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()

		items, err := FetchAll(ctx, repo, user)
		if err != nil {
			return fetchResultMsg{repo: repo, user: user, err: err}
		}

		// Best-effort: a cache-write failure shouldn't block showing the
		// fresh results that were just fetched successfully.
		_ = SaveCache(repo, user, items)

		store, err := LoadStore(user)
		if err != nil {
			return fetchResultMsg{repo: repo, user: user, err: err}
		}

		currentKeys := make(map[string]bool, len(items))
		for _, it := range items {
			currentKeys[it.Key] = true
		}
		if err := store.Prune(currentKeys); err != nil {
			return fetchResultMsg{repo: repo, user: user, err: err}
		}

		return fetchResultMsg{repo: repo, user: user, items: items, store: store}
	}
}
