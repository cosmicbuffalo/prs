package main

import "time"

// Section identifies which of the two source categories an Item came from.
type Section string

const (
	SectionReviewing Section = "REVIEW"
	SectionAuthored  Section = "AUTHOR"
	// SectionNew marks an open, non-draft PR the user has no activity on at
	// all (never commented, reviewed, or authored it). These populate the
	// "New" tab, shown between Outstanding and Done.
	SectionNew Section = "NEW"
)

// ReviewState mirrors GitHub's PullRequestReview state values that matter to us.
type ReviewState string

const (
	ReviewApproved         ReviewState = "APPROVED"
	ReviewChangesRequested ReviewState = "CHANGES_REQUESTED"
	ReviewCommented        ReviewState = "COMMENTED"
	ReviewStateNone        ReviewState = ""
)

// ActivityKind identifies the kind of activity entry for detail-pane rendering.
type ActivityKind string

const (
	ActivityComment       ActivityKind = "comment"
	ActivityInlineComment ActivityKind = "inline_comment"
	ActivityReview        ActivityKind = "review"
)

// Activity is a single comment/inline-comment/review on a PR.
type Activity struct {
	Date  time.Time
	Login string
	Kind  ActivityKind
	State ReviewState // only set for Kind == ActivityReview
	Body  string

	// OnBehalfOfTeams lists the team slugs this review (Kind ==
	// ActivityReview only) was submitted "on behalf of" — GitHub's own
	// authoritative signal (surfaced in its UI as "approved on behalf of
	// <team>") for a review that satisfies a team-based CODEOWNERS/required-
	// review request. Sourced from GraphQL's PullRequestReview.onBehalfOf.
	// A single review can satisfy multiple teams at once if the reviewer
	// belongs to more than one CODEOWNERS team relevant to the changed
	// files.
	OnBehalfOfTeams []string

	// IsBot is true when the actor is a bot account (a GitHub App like
	// Copilot's review bot or github-actions, rather than a real person) —
	// used to exclude bots from the PR Details "participants" count (see
	// uniqueParticipants). Doesn't affect anything else: bot comments/
	// reviews still show up in the detail pane and count toward "X
	// comments" as usual.
	IsBot bool
}

// Commit is a minimal view of a PR commit.
type Commit struct {
	SHA            string
	Message        string
	CommitterDate  time.Time
	AuthorLogin    string
	CommitterLogin string
	// AuthorIsBot/CommitterIsBot mirror Activity.IsBot, for the same
	// participants-count purpose — AuthorLogin/CommitterLogin themselves are
	// left untouched and still used/rendered everywhere else exactly as
	// before (e.g. a bot-authored commit still counts as "newer activity by
	// someone else").
	AuthorIsBot    bool
	CommitterIsBot bool
}

// DetailLine is one pre-rendered, structured comment/review entry for the
// detail pane. Actual coloring happens in view.go; this just carries data.
type DetailLine struct {
	Date       time.Time
	Login      string
	ShowLogin  bool
	Simplified string // "approved" | "changes_requested" => bare badge-style line
	Kind       string // "comment" | "inline_comment" | "review"
	State      ReviewState
	Text       string
}

// ReviewEvent is one formal review action (an Approve or a ChangesRequested
// review; plain "commented" reviews aren't tracked here) in chronological
// arrival order. Superseded is true when the same reviewer later submitted
// another formal review changing their stance — e.g. a ChangesRequested
// that reviewer subsequently resolved by approving. Rendering decides what
// to do with superseded entries (the detail panel grays out a superseded
// ChangesRequested rather than hiding it; the list panel hides it entirely).
type ReviewEvent struct {
	Login      string
	State      ReviewState // ReviewApproved | ReviewChangesRequested
	Date       time.Time
	Superseded bool

	// IsCodeowner is true if this approval counts toward a CODEOWNERS
	// requirement (only meaningful when State == ReviewApproved and
	// !Superseded — a currently-valid approval). Currently always false
	// (stub) pending a decision on how to compute it reliably.
	IsCodeowner bool
}

// Item is a single PR surfaced by prs. Most items have new activity relative
// to the relevant baseline (and land in Outstanding/New); a Quiet item is one
// the user is involved in but that has no new activity (it lands in Done), and
// a FetchError item is one whose per-PR data couldn't be loaded this refresh.
type Item struct {
	Key           string // "<owner>/<repo>#<number>"
	Number        int
	Title         string
	URL           string
	Section       Section
	Badge         ReviewState // reviewing only: my latest review state on this PR
	TriggerDate   time.Time   // latest qualifying commit/comment date
	Baseline      time.Time   // the "your last activity" / "your last push" date
	BaselineLabel string
	Detail        []DetailLine // reviewing: my comments/reviews; authored: others' newer comments/reviews
	Commits       []Commit     // newer commits not authored/committed by me (the ones actually shown)

	// Quiet is true when the user is involved in this PR (authored it, or
	// commented/reviewed on it) but there's been no new activity from anyone
	// else since their baseline — i.e. nothing needs attention. These land in
	// the Done tab (see classify) rather than being dropped, so involved-but-
	// idle PRs stay visible. A later fetch that finds new activity produces a
	// non-Quiet item, moving it back to Outstanding automatically.
	Quiet bool

	// FetchError, when non-empty, means this PR's per-PR data (comments,
	// reviews, commits) couldn't be fetched on the last refresh. The item is
	// still shown (in Outstanding) from the metadata already in hand, with the
	// error surfaced in place of the usual detail, and clears on the next
	// successful refresh.
	FetchError string

	// TotalCommits is the PR's total commit count (all authors, regardless
	// of date, from GitHub's own aggregate count — not len(Commits)), used
	// alongside len(Commits) for the Commits section header ("X total
	// commits, Y since last activity").
	TotalCommits int

	// LatestSummary is a one-line summary of the single most recent
	// qualifying commit/activity entry (the one that produced TriggerDate),
	// used for the third line of each list entry. See classifyReviewing /
	// classifyAuthored in github.go for how it's derived.
	LatestSummary string

	// Author is the PR's own author login (from GraphQL), used e.g. to give
	// the "opened by <author>" New-tab summary a consistent per-user color
	// tag without needing to re-parse LatestSummary.
	Author string

	// Reviewers holds the PR's full formal review history (approvals and
	// change requests, oldest first), computed from the full activity
	// history (not the possibly-capped/filtered Detail slice) so it's
	// accurate regardless of how far back a review was. Nil for New items
	// (no activity has been fetched for those).
	Reviewers []ReviewEvent

	// PR-level summary fields for the detail panel's "PR Details" section,
	// sourced from GitHub's own aggregate GraphQL fields (so they reflect
	// the whole PR, not just what's fetched/displayed) — available
	// uniformly for all three sections, including New.
	CreatedAt        time.Time
	Additions        int
	Deletions        int
	ChangedFiles     int
	TotalComments    int
	ParticipantCount int

	// ParticipantLogins lists the same participants ParticipantCount counts,
	// ordered by how much they've contributed (most commits+comments/
	// reviews first) — used to render the "PR Details" participants list.
	// Nil for New items (no per-PR activity/commits data is fetched for
	// those; ParticipantCount there falls back to GitHub's own aggregate
	// field instead).
	ParticipantLogins []string
}
