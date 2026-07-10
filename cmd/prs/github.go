package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// outerConcurrency bounds how many PRs are fetched from the GitHub REST API
// at once across the whole FetchAll call.
const outerConcurrency = 8

// prMeta is the minimal PR metadata gathered from the two GraphQL searches,
// keyed by PR number.
type prMeta struct {
	Number int
	Title  string
	URL    string
	Author string
	// AuthorIsBot is true if the PR's author is a bot account (e.g. a
	// dependabot-authored PR) — excluded from uniqueParticipants like any
	// other bot actor.
	AuthorIsBot bool

	// PR-level summary fields for the detail panel's "PR Details" section —
	// sourced straight from the GraphQL search node (see searchQueryGraphQL),
	// so they're available uniformly across all three sections without any
	// extra REST calls. TotalCommentCount and ParticipantCount from the
	// search node are deliberately NOT carried here: GitHub's own
	// comments.totalCount only counts top-level issue/conversation comments
	// (missing inline diff comments and review bodies entirely) and
	// participants.totalCount uses GitHub's own narrower notion of
	// "participant" — both undercount what this tool means by those words.
	// classifyReviewing/classifyAuthored compute them instead from the full
	// activity/commits already fetched for those two sections (see
	// meaningfulCommentCount/uniqueParticipants); only classifyNew (which has
	// no per-PR REST data) still falls back to the GraphQL node's fields
	// directly, as an approximation.
	CreatedAt        time.Time
	Additions        int
	Deletions        int
	ChangedFiles     int
	TotalCommitCount int
}

// prMetaFromNode builds a prMeta from a raw GraphQL search node.
func prMetaFromNode(n searchNode) prMeta {
	return prMeta{
		Number:           n.Number,
		Title:            n.Title,
		URL:              n.URL,
		Author:           n.Author.Login,
		AuthorIsBot:      n.Author.Typename == "Bot",
		CreatedAt:        n.CreatedAt,
		Additions:        n.Additions,
		Deletions:        n.Deletions,
		ChangedFiles:     n.ChangedFiles,
		TotalCommitCount: n.Commits.TotalCount,
	}
}

// ghUser is the subset of a GitHub API "user" object we care about. Type is
// "User", "Bot", "Organization", etc — used to exclude bot accounts (e.g.
// github-actions[bot], Copilot) from the "participants" count.
type ghUser struct {
	Login string `json:"login"`
	Type  string `json:"type"`
}

// ghComment matches the shape of both issue comments and pull request review
// (inline) comments returned by the REST API — the fields we use are identical.
type ghComment struct {
	Body      string    `json:"body"`
	User      ghUser    `json:"user"`
	CreatedAt time.Time `json:"created_at"`
}

// ghqlReviewNode is a single PullRequestReview from the GraphQL reviews
// query. Reviews are fetched via GraphQL rather than REST specifically to
// get onBehalfOf — GitHub's own authoritative "this review satisfies a
// team-based CODEOWNERS/required-review request" signal (surfaced in the
// GitHub UI as "approved on behalf of <team>"), which the REST reviews
// endpoint doesn't expose at all.
type ghqlReviewNode struct {
	State       string     `json:"state"`
	Body        string     `json:"body"`
	SubmittedAt *time.Time `json:"submittedAt"`
	Author      struct {
		Login    string `json:"login"`
		Typename string `json:"__typename"` // "Bot" for a GitHub App review (e.g. Copilot's), "User" otherwise
	} `json:"author"`
	OnBehalfOf struct {
		Nodes []struct {
			Slug string `json:"slug"`
		} `json:"nodes"`
	} `json:"onBehalfOf"`
}

// reviewsQueryGraphQL fetches a single PR's reviews (paginated via
// cursor) including onBehalfOf.
const reviewsQueryGraphQL = `
query($owner: String!, $repo: String!, $number: Int!, $cursor: String) {
  repository(owner: $owner, name: $repo) {
    pullRequest(number: $number) {
      reviews(first: 100, after: $cursor) {
        pageInfo {
          hasNextPage
          endCursor
        }
        nodes {
          state
          body
          submittedAt
          author {
            login
            __typename
          }
          onBehalfOf(first: 10) {
            nodes {
              slug
            }
          }
        }
      }
    }
  }
}`

// ghqlReviewsResponse is the top-level shape of the reviews GraphQL response.
type ghqlReviewsResponse struct {
	Data struct {
		Repository struct {
			PullRequest struct {
				Reviews struct {
					PageInfo struct {
						HasNextPage bool   `json:"hasNextPage"`
						EndCursor   string `json:"endCursor"`
					} `json:"pageInfo"`
					Nodes []ghqlReviewNode `json:"nodes"`
				} `json:"reviews"`
			} `json:"pullRequest"`
		} `json:"repository"`
	} `json:"data"`
}

// fetchReviews fetches all of a PR's reviews via GraphQL (paginating as
// needed), so onBehalfOf is available for every review regardless of PR size.
func fetchReviews(ctx context.Context, repo string, number int) ([]ghqlReviewNode, error) {
	owner, name, ok := strings.Cut(repo, "/")
	if !ok {
		return nil, fmt.Errorf("invalid repo %q, expected owner/name", repo)
	}

	var all []ghqlReviewNode
	cursor := ""
	for {
		args := []string{
			"api", "graphql",
			"-f", "query=" + reviewsQueryGraphQL,
			"-f", "owner=" + owner,
			"-f", "repo=" + name,
			"-F", "number=" + strconv.Itoa(number),
		}
		if cursor != "" {
			args = append(args, "-f", "cursor="+cursor)
		}
		out, err := runGH(ctx, args...)
		if err != nil {
			return nil, err
		}
		var resp ghqlReviewsResponse
		if err := json.Unmarshal(out, &resp); err != nil {
			return nil, fmt.Errorf("parsing reviews graphql response for #%d: %w", number, err)
		}
		reviews := resp.Data.Repository.PullRequest.Reviews
		all = append(all, reviews.Nodes...)
		if !reviews.PageInfo.HasNextPage {
			break
		}
		cursor = reviews.PageInfo.EndCursor
	}
	return all, nil
}

// ghCommit matches a pull request commit returned by the REST API. Author and
// Committer are pointers because GitHub omits/nulls them when the commit's
// email isn't linked to a GitHub account.
type ghCommit struct {
	SHA    string `json:"sha"`
	Commit struct {
		Message   string `json:"message"`
		Committer struct {
			Date time.Time `json:"date"`
		} `json:"committer"`
	} `json:"commit"`
	Author    *ghUser `json:"author"`
	Committer *ghUser `json:"committer"`
}

// searchNode is a single PullRequest node from the GraphQL search response.
// CreatedAt and Commits are only needed by the "all open" search (used to
// build SectionNew items without a REST round-trip), but are harmless
// no-ops for the commenter/author searches.
type searchNode struct {
	Number       int       `json:"number"`
	Title        string    `json:"title"`
	URL          string    `json:"url"`
	CreatedAt    time.Time `json:"createdAt"`
	Additions    int       `json:"additions"`
	Deletions    int       `json:"deletions"`
	ChangedFiles int       `json:"changedFiles"`
	Author       struct {
		Login    string `json:"login"`
		Typename string `json:"__typename"`
	} `json:"author"`
	Comments struct {
		TotalCount int `json:"totalCount"`
	} `json:"comments"`
	Participants struct {
		TotalCount int `json:"totalCount"`
	} `json:"participants"`
	Commits struct {
		TotalCount int `json:"totalCount"`
		Nodes      []struct {
			Commit struct {
				CommittedDate time.Time `json:"committedDate"`
			} `json:"commit"`
		} `json:"nodes"`
	} `json:"commits"`
}

// searchResponse is the top-level shape of the GraphQL search response.
type searchResponse struct {
	Data struct {
		Search struct {
			PageInfo struct {
				HasNextPage bool   `json:"hasNextPage"`
				EndCursor   string `json:"endCursor"`
			} `json:"pageInfo"`
			Nodes []searchNode `json:"nodes"`
		} `json:"search"`
	} `json:"data"`
}

// searchQueryGraphQL is the GraphQL document used for all three searches
// (commenter, author, and "all open"). It requests author.login (in addition
// to what the bash gh-prs tool fetched) so callers can tell whether a PR's
// author is the current user without relying on which search qualifier
// surfaced it. createdAt and the last commit's committedDate are requested
// so the "all open" search can build SectionNew items entirely from GraphQL
// data, without a REST round-trip per PR. additions/deletions/changedFiles
// and the comments/participants/commits totalCounts feed the detail panel's
// "PR Details" summary — these are cheap aggregate fields GitHub computes
// server-side, so no extra REST round-trip is needed for them either, and
// they're available uniformly for all three sections (including New, which
// otherwise has no per-PR data beyond this search).
const searchQueryGraphQL = `
query($searchQuery: String!, $cursor: String) {
  search(query: $searchQuery, type: ISSUE, first: 100, after: $cursor) {
    pageInfo {
      hasNextPage
      endCursor
    }
    nodes {
      ... on PullRequest {
        number
        title
        url
        createdAt
        author {
          login
          __typename
        }
        additions
        deletions
        changedFiles
        comments {
          totalCount
        }
        participants {
          totalCount
        }
        commits(last: 1) {
          totalCount
          nodes {
            commit {
              committedDate
            }
          }
        }
      }
    }
  }
}`

// repoCodeownersQueryGraphQL checks for a CODEOWNERS file at each of the
// three locations GitHub recognizes (repo root, .github/, docs/) in a single
// call, fetching its text so the catch-all owner can be parsed out too —
// present ⇒ non-nil (a missing path resolves to a null object, not a Blob).
const repoCodeownersQueryGraphQL = `
query($owner: String!, $repo: String!) {
  repository(owner: $owner, name: $repo) {
    root: object(expression: "HEAD:CODEOWNERS") { ... on Blob { text } }
    github: object(expression: "HEAD:.github/CODEOWNERS") { ... on Blob { text } }
    docs: object(expression: "HEAD:docs/CODEOWNERS") { ... on Blob { text } }
  }
}`

// codeownersBlob carries a CODEOWNERS file's raw text, present only when the
// GraphQL object at that path actually resolved to a Blob.
type codeownersBlob struct {
	Text string `json:"text"`
}

// repoCodeownersResponse is the top-level shape of the codeowners GraphQL
// response. Each field is a pointer so a missing file (null object)
// unmarshals to nil rather than a zero-value struct.
type repoCodeownersResponse struct {
	Data struct {
		Repository struct {
			Root   *codeownersBlob `json:"root"`
			Github *codeownersBlob `json:"github"`
			Docs   *codeownersBlob `json:"docs"`
		} `json:"repository"`
	} `json:"data"`
}

// codeownersInfo carries what a repo's CODEOWNERS file (if any) says about
// which team represents "the" required-approval group, for the gray/green
// approval-check distinction (see reviewEvents). A repo can have several
// CODEOWNERS teams — e.g. a broad "*" catch-all assigned to a general
// "trusted-reviewers" team, with narrower path-specific owners carved out
// for exceptions (a "db-team" scoped to /db/, a "docs-team" scoped to
// /docs/, etc). Only an approval satisfying the catch-all team is what the
// user means by "the trusted reviewer requirement" — the narrower
// path-specific teams also show up in a review's onBehalfOf when relevant,
// but aren't what the gray/green check is meant to track, so they're
// deliberately not treated as "trusted" here.
type codeownersInfo struct {
	// Exists is true if the repo has a CODEOWNERS file at all. If false, no
	// approval could ever satisfy a codeowners requirement that doesn't
	// exist, so every approval counts as the (non-existent) requirement
	// being trivially satisfied — i.e. every check renders green.
	Exists bool
	// PrimaryTeams are the team slugs assigned to CODEOWNERS' exact "*"
	// catch-all pattern, org prefix stripped (GraphQL's Team.slug is
	// unqualified). Empty if CODEOWNERS exists but has no catch-all line —
	// callers then fall back to treating any non-empty onBehalfOf as
	// "trusted", since there's no more specific signal available.
	PrimaryTeams []string
}

// isTrusted reports whether a review whose onBehalfOf teams are `teams`
// should render as a trusted-reviewer (green) check, per ci's rules — see
// codeownersInfo's doc comment for the three cases.
func (ci codeownersInfo) isTrusted(teams []string) bool {
	if !ci.Exists {
		return true
	}
	if len(ci.PrimaryTeams) == 0 {
		return len(teams) > 0
	}
	for _, t := range teams {
		for _, p := range ci.PrimaryTeams {
			if strings.EqualFold(t, p) {
				return true
			}
		}
	}
	return false
}

// fetchCodeownersInfo locates the repo's CODEOWNERS file (if any) at the
// three standard locations and parses out its catch-all team(s). Errors
// (including no CODEOWNERS file existing) resolve to the zero codeownersInfo
// — a lookup failure is a purely cosmetic detail and shouldn't fail the
// whole fetch, and its Exists=false zero value happens to be exactly the
// right fallback ("no CODEOWNERS" ⇒ every check green).
func fetchCodeownersInfo(ctx context.Context, repo string) codeownersInfo {
	owner, name, ok := strings.Cut(repo, "/")
	if !ok {
		return codeownersInfo{}
	}
	out, err := runGH(ctx, "api", "graphql",
		"-f", "query="+repoCodeownersQueryGraphQL,
		"-f", "owner="+owner,
		"-f", "repo="+name,
	)
	if err != nil {
		return codeownersInfo{}
	}
	var resp repoCodeownersResponse
	if err := json.Unmarshal(out, &resp); err != nil {
		return codeownersInfo{}
	}
	r := resp.Data.Repository
	blob := r.Root
	if blob == nil {
		blob = r.Github
	}
	if blob == nil {
		blob = r.Docs
	}
	if blob == nil {
		return codeownersInfo{}
	}
	return codeownersInfo{Exists: true, PrimaryTeams: primaryCodeownersTeams(blob.Text)}
}

// primaryCodeownersTeams parses a CODEOWNERS file's exact "*" catch-all
// pattern line(s) and returns the associated team slugs, with any "@org/"
// prefix stripped. Plain "@user" owners are kept too (harmlessly — they'll
// simply never match a review's team-only onBehalfOf list).
func primaryCodeownersTeams(raw string) []string {
	var teams []string
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 || fields[0] != "*" {
			continue
		}
		for _, owner := range fields[1:] {
			owner = strings.TrimPrefix(owner, "@")
			if idx := strings.LastIndex(owner, "/"); idx != -1 {
				owner = owner[idx+1:]
			}
			teams = append(teams, owner)
		}
	}
	return teams
}

// runGH shells out to the gh CLI and returns its stdout, wrapping any error
// with the command's stderr output for debuggability.
func runGH(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "gh", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("gh %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), nil
}

// RepoFromCwd returns override if non-empty, else detects "owner/repo" via
// `gh repo view --json nameWithOwner -q .nameWithOwner`.
func RepoFromCwd(ctx context.Context, override string) (string, error) {
	if override != "" {
		return override, nil
	}
	out, err := runGH(ctx, "repo", "view", "--json", "nameWithOwner", "-q", ".nameWithOwner")
	if err != nil {
		return "", fmt.Errorf("detecting repo from cwd: %w", err)
	}
	repo := strings.TrimSpace(string(out))
	if repo == "" {
		return "", fmt.Errorf("detecting repo from cwd: gh returned empty output")
	}
	return repo, nil
}

// CurrentUser returns override if non-empty, else detects the login via
// `gh api user -q .login`.
func CurrentUser(ctx context.Context, override string) (string, error) {
	if override != "" {
		return override, nil
	}
	out, err := runGH(ctx, "api", "user", "-q", ".login")
	if err != nil {
		return "", fmt.Errorf("detecting current user: %w", err)
	}
	user := strings.TrimSpace(string(out))
	if user == "" {
		return "", fmt.Errorf("detecting current user: gh returned empty output")
	}
	return user, nil
}

// searchOpenPRs runs the GraphQL search for open, non-draft PRs in repo
// matching the given search qualifier (e.g. "commenter:someone" or
// "author:someone"). An empty qualifier runs the bare "all open, non-draft
// PRs" search.
func searchOpenPRs(ctx context.Context, repo, qualifier string) ([]searchNode, error) {
	searchQuery := "repo:" + repo + " is:pr is:open draft:false"
	if qualifier != "" {
		searchQuery += " " + qualifier
	}

	var all []searchNode
	cursor := ""
	for {
		args := []string{
			"api", "graphql",
			"-f", "query=" + searchQueryGraphQL,
			"-f", "searchQuery=" + searchQuery,
		}
		if cursor != "" {
			args = append(args, "-f", "cursor="+cursor)
		}
		out, err := runGH(ctx, args...)
		if err != nil {
			return nil, err
		}
		var resp searchResponse
		if err := json.Unmarshal(out, &resp); err != nil {
			return nil, fmt.Errorf("parsing search response for query %q: %w", qualifier, err)
		}
		all = append(all, resp.Data.Search.Nodes...)
		if !resp.Data.Search.PageInfo.HasNextPage {
			break
		}
		cursor = resp.Data.Search.PageInfo.EndCursor
	}
	return all, nil
}

// fetchPRData fetches issue comments, inline review comments, reviews, and
// commits for a single PR, concurrently. Reviews with a null submitted_at
// (pending/draft reviews) are dropped.
func fetchPRData(ctx context.Context, repo string, number int) (comments, inline []ghComment, reviews []ghqlReviewNode, commits []ghCommit, err error) {
	var wg sync.WaitGroup
	errs := make([]error, 4)

	wg.Add(4)
	go func() {
		defer wg.Done()
		out, e := runGH(ctx, "api", "--paginate", fmt.Sprintf("repos/%s/issues/%d/comments", repo, number))
		if e != nil {
			errs[0] = e
			return
		}
		if e := json.Unmarshal(out, &comments); e != nil {
			errs[0] = fmt.Errorf("parsing issue comments for #%d: %w", number, e)
		}
	}()
	go func() {
		defer wg.Done()
		out, e := runGH(ctx, "api", "--paginate", fmt.Sprintf("repos/%s/pulls/%d/comments", repo, number))
		if e != nil {
			errs[1] = e
			return
		}
		if e := json.Unmarshal(out, &inline); e != nil {
			errs[1] = fmt.Errorf("parsing inline comments for #%d: %w", number, e)
		}
	}()
	go func() {
		defer wg.Done()
		all, e := fetchReviews(ctx, repo, number)
		if e != nil {
			errs[2] = e
			return
		}
		for _, r := range all {
			if r.SubmittedAt != nil {
				reviews = append(reviews, r)
			}
		}
	}()
	go func() {
		defer wg.Done()
		out, e := runGH(ctx, "api", "--paginate", fmt.Sprintf("repos/%s/pulls/%d/commits", repo, number))
		if e != nil {
			errs[3] = e
			return
		}
		if e := json.Unmarshal(out, &commits); e != nil {
			errs[3] = fmt.Errorf("parsing commits for #%d: %w", number, e)
		}
	}()
	wg.Wait()

	for _, e := range errs {
		if e != nil {
			return nil, nil, nil, nil, e
		}
	}
	return comments, inline, reviews, commits, nil
}

// buildActivity flattens issue comments, inline comments, and (already
// submitted-only) reviews into a single unordered slice of Activity.
func buildActivity(comments, inline []ghComment, reviews []ghqlReviewNode) []Activity {
	activity := make([]Activity, 0, len(comments)+len(inline)+len(reviews))
	for _, c := range comments {
		activity = append(activity, Activity{
			Date:  c.CreatedAt,
			Login: c.User.Login,
			Kind:  ActivityComment,
			Body:  c.Body,
			IsBot: c.User.Type == "Bot",
		})
	}
	for _, c := range inline {
		activity = append(activity, Activity{
			Date:  c.CreatedAt,
			Login: c.User.Login,
			Kind:  ActivityInlineComment,
			Body:  c.Body,
			IsBot: c.User.Type == "Bot",
		})
	}
	for _, r := range reviews {
		teams := make([]string, 0, len(r.OnBehalfOf.Nodes))
		for _, n := range r.OnBehalfOf.Nodes {
			teams = append(teams, n.Slug)
		}
		activity = append(activity, Activity{
			Date:            *r.SubmittedAt,
			Login:           r.Author.Login,
			Kind:            ActivityReview,
			State:           ReviewState(r.State),
			Body:            r.Body,
			OnBehalfOfTeams: teams,
			IsBot:           r.Author.Typename == "Bot",
		})
	}
	return activity
}

// toCommits converts REST commit payloads to the package's Commit type,
// treating a missing/unlinked author or committer account as "".
func toCommits(raw []ghCommit) []Commit {
	commits := make([]Commit, 0, len(raw))
	for _, c := range raw {
		author, committer := "", ""
		authorIsBot, committerIsBot := false, false
		if c.Author != nil {
			author = c.Author.Login
			authorIsBot = c.Author.Type == "Bot"
		}
		if c.Committer != nil {
			committer = c.Committer.Login
			committerIsBot = c.Committer.Type == "Bot"
		}
		commits = append(commits, Commit{
			SHA:            c.SHA,
			Message:        c.Commit.Message,
			CommitterDate:  c.Commit.Committer.Date,
			AuthorLogin:    author,
			CommitterLogin: committer,
			AuthorIsBot:    authorIsBot,
			CommitterIsBot: committerIsBot,
		})
	}
	return commits
}

// filterActivityToDetail applies the "drop empty comments, simplify bare
// review approvals/changes-requested" rule (mirroring the bash tool's
// filter_activity + print_activity_lines) and converts the surviving
// activity entries into DetailLines. showLogin is copied onto every line.
func filterActivityToDetail(activity []Activity, showLogin bool) []DetailLine {
	var lines []DetailLine
	for _, a := range activity {
		if a.Body == "" {
			if a.Kind != ActivityReview || (a.State != ReviewApproved && a.State != ReviewChangesRequested) {
				continue
			}
			simplified := "approved"
			if a.State == ReviewChangesRequested {
				simplified = "changes_requested"
			}
			lines = append(lines, DetailLine{
				Date:       a.Date,
				Login:      a.Login,
				ShowLogin:  showLogin,
				Simplified: simplified,
				Kind:       string(a.Kind),
				State:      a.State,
			})
			continue
		}
		lines = append(lines, DetailLine{
			Date:      a.Date,
			Login:     a.Login,
			ShowLogin: showLogin,
			Kind:      string(a.Kind),
			State:     a.State,
			Text:      a.Body,
		})
	}
	return lines
}

// meaningfulCommentCount returns how many of activity's entries would render
// as an actual comment/review line in the detail pane (the same "drop
// empty, non-formal-review" rule as filterActivityToDetail), giving a
// PR-wide "X comments" figure. Computed ourselves rather than trusted from
// GraphQL's comments.totalCount, which only counts top-level issue/
// conversation comments — it misses inline (diff) review comments and
// review bodies entirely, which is why a PR with a lot of code-review
// discussion can look like it has almost no comments at all.
func meaningfulCommentCount(activity []Activity) int {
	return len(filterActivityToDetail(activity, true))
}

// participantsOrdered returns the PR's distinct *human* participant logins —
// its author, every commit's author/committer, and everyone who left a
// comment/inline comment/review — matching what "participants" is meant to
// mean here (authors/coauthors + commenters + reviewers), ordered by how
// much they've contributed (most commits+comments/reviews first,
// alphabetical as a tiebreak for determinism). Computed ourselves rather
// than trusted from GraphQL's participants.totalCount, which uses GitHub's
// own narrower notion of "participant" and was observed to undercount PRs
// with several distinct reviewers.
//
// Bot accounts (github-actions[bot], Copilot's review bot, etc, identified
// by the API's own user "type") are excluded, as is the well-known
// "web-flow" pseudo-account GitHub uses to attribute commits made via its
// web UI (e.g. squash-merges) — it's typed as a regular "User" by GitHub's
// API despite not being a real contributor, so it needs an explicit
// name-based exception rather than a type check.
func participantsOrdered(author string, authorIsBot bool, activity []Activity, commits []Commit) []string {
	counts := make(map[string]int)
	touch := func(login string, isBot bool) {
		if login == "" || login == "web-flow" || isBot {
			return
		}
		counts[login]++
	}
	touch(author, authorIsBot)
	for _, c := range commits {
		// A commit where the author and committer are the same person (the
		// common case) counts as one contribution, not two.
		if c.AuthorLogin == c.CommitterLogin {
			touch(c.AuthorLogin, c.AuthorIsBot)
		} else {
			touch(c.AuthorLogin, c.AuthorIsBot)
			touch(c.CommitterLogin, c.CommitterIsBot)
		}
	}
	for _, a := range activity {
		touch(a.Login, a.IsBot)
	}

	logins := make([]string, 0, len(counts))
	for login := range counts {
		logins = append(logins, login)
	}
	sort.Slice(logins, func(i, j int) bool {
		if counts[logins[i]] != counts[logins[j]] {
			return counts[logins[i]] > counts[logins[j]]
		}
		return logins[i] < logins[j]
	})
	return logins
}

// classifyReviewing builds the "reviewing" Item for a PR the user has
// commented on or reviewed (authored by someone else). If new activity from
// someone else has landed since the user's last activity — a commit not
// authored/committed by them, or a comment/review from someone else — the
// item is a normal (Outstanding) one; otherwise it's marked Quiet (lands in
// Done). It never returns nil, so an involved PR is always shown somewhere.
// Caller is responsible for having already checked meta.Author != user and
// that the PR came from the commenter search.
func classifyReviewing(repo string, meta prMeta, user string, activity []Activity, commits []Commit, ci codeownersInfo) *Item {
	var mine []Activity
	for _, a := range activity {
		if a.Login == user {
			mine = append(mine, a)
		}
	}
	sort.Slice(mine, func(i, j int) bool { return mine[i].Date.Before(mine[j].Date) })
	// Baseline is the user's last activity on the PR. If the commenter search
	// surfaced a PR we somehow see no activity from the user on, fall back to
	// the PR's creation date so it's still classified (never dropped).
	baseline := meta.CreatedAt
	if len(mine) > 0 {
		baseline = mine[len(mine)-1].Date
	}

	var newerCommits []Commit
	for _, c := range commits {
		if c.CommitterDate.After(baseline) && c.AuthorLogin != user && c.CommitterLogin != user {
			newerCommits = append(newerCommits, c)
		}
	}
	var newerActivity []Activity
	for _, a := range activity {
		if a.Login != user && a.Date.After(baseline) {
			newerActivity = append(newerActivity, a)
		}
	}
	// No new activity from anyone else since the user's last look ⇒ a "quiet"
	// PR: still returned (lands in Done via classify) rather than dropped.
	quiet := len(newerCommits) == 0 && len(newerActivity) == 0
	var trigger time.Time
	var latestSummary string
	if quiet {
		trigger = latestActivityOrCommit(activity, commits, baseline)
		latestSummary = "No new activity since your last review"
	} else {
		trigger, latestSummary = latestEventSummary(newerCommits, newerActivity)
	}
	sort.Slice(newerCommits, func(i, j int) bool { return newerCommits[i].CommitterDate.Before(newerCommits[j].CommitterDate) })

	badge := ReviewStateNone
	for _, a := range mine {
		if a.Kind == ActivityReview {
			badge = a.State
		}
	}

	// The detail pane shows the full comment/review thread from everyone on
	// the PR (not just the user's own activity), so it's a useful record of
	// what's been discussed, not just a log of the user's own comments.
	allActivity := make([]Activity, len(activity))
	copy(allActivity, activity)
	sort.Slice(allActivity, func(i, j int) bool { return allActivity[i].Date.Before(allActivity[j].Date) })

	participants := participantsOrdered(meta.Author, meta.AuthorIsBot, activity, commits)

	return &Item{
		Key:               repo + "#" + strconv.Itoa(meta.Number),
		Number:            meta.Number,
		Title:             meta.Title,
		URL:               meta.URL,
		Section:           SectionReviewing,
		Badge:             badge,
		TriggerDate:       trigger,
		Baseline:          baseline,
		BaselineLabel:     "your last activity",
		Detail:            filterActivityToDetail(allActivity, true),
		Commits:           newerCommits,
		LatestSummary:     latestSummary,
		Quiet:             quiet,
		Author:            meta.Author,
		Reviewers:         reviewEvents(activity, ci),
		TotalCommits:      meta.TotalCommitCount,
		CreatedAt:         meta.CreatedAt,
		Additions:         meta.Additions,
		Deletions:         meta.Deletions,
		ChangedFiles:      meta.ChangedFiles,
		TotalComments:     meaningfulCommentCount(activity),
		ParticipantCount:  len(participants),
		ParticipantLogins: participants,
	}
}

// latestActivityOrCommit returns the most recent date across all activity and
// commits, or fallback if there are none — used as a "quiet" PR's TriggerDate
// so its "updated X ago" reflects the genuinely most recent thing on the PR.
func latestActivityOrCommit(activity []Activity, commits []Commit, fallback time.Time) time.Time {
	latest := fallback
	for _, a := range activity {
		if a.Date.After(latest) {
			latest = a.Date
		}
	}
	for _, c := range commits {
		if c.CommitterDate.After(latest) {
			latest = c.CommitterDate
		}
	}
	return latest
}

// latestEventSummary picks whichever of the given (already-filtered "newer,
// by someone else") commits or activity entries is the single most recent
// by date, and formats a one-line summary for it — reusing the commit
// summary format ("N new commit(s) · latest: <message>") when a commit is
// the most recent event, or the activity summary format ("<login>: <text>",
// reviewEvents builds the PR's full formal review history (approvals and
// change requests only — plain "commented" reviews aren't formal review
// state and are excluded) in chronological order, marking every event
// except each reviewer's last as Superseded. Returns nil if there are no
// formal review events at all.
func reviewEvents(activity []Activity, ci codeownersInfo) []ReviewEvent {
	var formal []Activity
	for _, a := range activity {
		if a.Kind == ActivityReview && (a.State == ReviewApproved || a.State == ReviewChangesRequested) {
			formal = append(formal, a)
		}
	}
	if len(formal) == 0 {
		return nil
	}
	sort.Slice(formal, func(i, j int) bool { return formal[i].Date.Before(formal[j].Date) })

	// Every formal review action is kept (including same-state repeats, e.g.
	// approve-then-approve-again) so the Review Status section can show the
	// PR's full timeline — Superseded marks every entry except the last one
	// per reviewer (their current, still-in-effect state), and rendering
	// decides what to do with superseded entries (grayed out, not hidden).
	lastIndexByLogin := make(map[string]int, len(formal))
	for i, a := range formal {
		lastIndexByLogin[a.Login] = i
	}

	events := make([]ReviewEvent, len(formal))
	for i, a := range formal {
		events[i] = ReviewEvent{
			Login:      a.Login,
			State:      a.State,
			Date:       a.Date,
			Superseded: i != lastIndexByLogin[a.Login],
			// Sourced from GitHub's own PullRequestReview.onBehalfOf (via
			// GraphQL) — the same signal shown in the GitHub UI as "approved
			// on behalf of <team>" for a review that satisfies a team-based
			// CODEOWNERS/required-review request — filtered down to just the
			// repo's catch-all/"trusted reviewer" team via ci.isTrusted, so
			// narrower path-specific CODEOWNERS teams don't render green too.
			IsCodeowner: ci.isTrusted(a.OnBehalfOfTeams),
		}
	}
	return events
}

// latestEventSummary picks whichever of the given (already-filtered "newer,
// by someone else") commits or activity entries is the single most recent
// by date, and formats a one-line summary for it — reusing the commit
// summary format ("N new commit(s) · latest: <message>") when a commit is
// the most recent event, or the activity summary format ("<login>: <text>",
// or a bare approved/changes-requested badge) when an activity entry is.
// Returns the zero time and an empty string if both slices are empty.
func latestEventSummary(commits []Commit, activity []Activity) (time.Time, string) {
	var latestCommit *Commit
	for i, c := range commits {
		if latestCommit == nil || c.CommitterDate.After(latestCommit.CommitterDate) {
			latestCommit = &commits[i]
		}
	}
	var latestActivity *Activity
	for i, a := range activity {
		if latestActivity == nil || a.Date.After(latestActivity.Date) {
			latestActivity = &activity[i]
		}
	}

	if latestCommit != nil && (latestActivity == nil || !latestActivity.Date.After(latestCommit.CommitterDate)) {
		latestMsg := strings.SplitN(latestCommit.Message, "\n", 2)[0]
		summary := fmt.Sprintf("%d new commit(s) · latest: %s", len(commits), truncateSummary(latestMsg, 60))
		return latestCommit.CommitterDate, summary
	}
	if latestActivity != nil {
		var summaryText string
		if latestActivity.Kind == ActivityReview && latestActivity.Body == "" &&
			(latestActivity.State == ReviewApproved || latestActivity.State == ReviewChangesRequested) {
			if latestActivity.State == ReviewApproved {
				summaryText = "✓ Approved"
			} else {
				summaryText = "✗ Changes requested"
			}
		} else {
			firstLine := strings.SplitN(latestActivity.Body, "\n", 2)[0]
			summaryText = truncateSummary(firstLine, 60)
		}
		return latestActivity.Date, fmt.Sprintf("%s: %s", latestActivity.Login, summaryText)
	}
	return time.Time{}, ""
}

// truncateSummary truncates s to at most n runes, appending "…" if it had to
// cut anything. It's a small local duplicate of view.go's truncateRunes,
// kept here so github.go's classification logic doesn't need to reach into
// view.go for a one-line helper.
func truncateSummary(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	if n <= 1 {
		return string(runes[:n])
	}
	return string(runes[:n-1]) + "…"
}

// classifyAuthored builds the "authored" Item for a PR the user opened. The
// baseline is the user's last push (or the PR's open timestamp if they have
// no commit of their own on it). If new activity from someone else has landed
// since — a commit not authored/committed by them, or a comment/review from
// someone else — the item is a normal (Outstanding) one; otherwise it's
// marked Quiet (lands in Done). It never returns nil, so an authored PR is
// always shown somewhere. Caller is responsible for having already checked
// meta.Author == user.
func classifyAuthored(repo string, meta prMeta, user string, activity []Activity, commits []Commit, ci codeownersInfo) *Item {
	var myCommits []Commit
	for _, c := range commits {
		if c.AuthorLogin == user || c.CommitterLogin == user {
			myCommits = append(myCommits, c)
		}
	}
	// Baseline is the user's last push. If they authored the PR but have no
	// commit attributed to their account on it (e.g. it's all a co-author's
	// commits), fall back to the PR's open timestamp so it's still classified
	// (never dropped) rather than requiring an authored commit.
	baseline := meta.CreatedAt
	baselineLabel := "opened"
	if len(myCommits) > 0 {
		baseline = myCommits[0].CommitterDate
		for _, c := range myCommits[1:] {
			if c.CommitterDate.After(baseline) {
				baseline = c.CommitterDate
			}
		}
		baselineLabel = "your last push"
	}

	var newerCommits []Commit
	for _, c := range commits {
		if c.CommitterDate.After(baseline) && c.AuthorLogin != user && c.CommitterLogin != user {
			newerCommits = append(newerCommits, c)
		}
	}
	var newer []Activity
	for _, a := range activity {
		if a.Login != user && a.Date.After(baseline) {
			newer = append(newer, a)
		}
	}
	// No new activity from anyone else since the user's baseline ⇒ a "quiet"
	// PR: still returned (lands in Done via classify) rather than dropped.
	quiet := len(newerCommits) == 0 && len(newer) == 0
	var trigger time.Time
	var latestSummary string
	if quiet {
		trigger = latestActivityOrCommit(activity, commits, baseline)
		if len(myCommits) > 0 {
			latestSummary = "No new activity since your last push"
		} else {
			latestSummary = "No new activity since you opened it"
		}
	} else {
		trigger, latestSummary = latestEventSummary(newerCommits, newer)
	}
	sort.Slice(newerCommits, func(i, j int) bool { return newerCommits[i].CommitterDate.Before(newerCommits[j].CommitterDate) })
	sort.Slice(newer, func(i, j int) bool { return newer[i].Date.Before(newer[j].Date) })

	participants := participantsOrdered(meta.Author, meta.AuthorIsBot, activity, commits)

	return &Item{
		Key:               repo + "#" + strconv.Itoa(meta.Number),
		Number:            meta.Number,
		Title:             meta.Title,
		URL:               meta.URL,
		Section:           SectionAuthored,
		TriggerDate:       trigger,
		Baseline:          baseline,
		BaselineLabel:     baselineLabel,
		Detail:            filterActivityToDetail(newer, true),
		Commits:           newerCommits,
		LatestSummary:     latestSummary,
		Quiet:             quiet,
		Author:            meta.Author,
		Reviewers:         reviewEvents(activity, ci),
		TotalCommits:      meta.TotalCommitCount,
		CreatedAt:         meta.CreatedAt,
		Additions:         meta.Additions,
		Deletions:         meta.Deletions,
		ChangedFiles:      meta.ChangedFiles,
		TotalComments:     meaningfulCommentCount(activity),
		ParticipantCount:  len(participants),
		ParticipantLogins: participants,
	}
}

// classifyNew builds an Item for an open, non-draft PR the user has never
// interacted with (it appeared in the "all open" search but neither the
// commenter nor the author search). It's built entirely from that search's
// GraphQL node — no REST calls are made for these PRs, since there is
// nothing user-specific to fetch. Per the "not participating at all" case,
// only new commits (not comments) should re-trigger these, so TriggerDate
// is the PR's latest commit date, falling back to its creation date if it
// has no commits yet. TotalComments/ParticipantCount fall back to GitHub's
// own (narrower, sometimes-undercounting) GraphQL fields here specifically
// — unlike classifyReviewing/classifyAuthored, there's no per-PR
// activity/commits data available to compute them precisely without an
// extra REST round-trip this section is designed to avoid.
func classifyNew(repo string, node searchNode) Item {
	trigger := node.CreatedAt
	if len(node.Commits.Nodes) > 0 {
		trigger = node.Commits.Nodes[0].Commit.CommittedDate
	}

	return Item{
		Key:              repo + "#" + strconv.Itoa(node.Number),
		Number:           node.Number,
		Title:            node.Title,
		URL:              node.URL,
		Section:          SectionNew,
		Badge:            ReviewStateNone,
		TriggerDate:      trigger,
		Baseline:         node.CreatedAt,
		BaselineLabel:    "opened",
		Detail:           nil,
		Commits:          nil,
		LatestSummary:    fmt.Sprintf("opened by %s", node.Author.Login),
		Author:           node.Author.Login,
		TotalCommits:     node.Commits.TotalCount,
		CreatedAt:        node.CreatedAt,
		Additions:        node.Additions,
		Deletions:        node.Deletions,
		ChangedFiles:     node.ChangedFiles,
		TotalComments:    node.Comments.TotalCount,
		ParticipantCount: node.Participants.TotalCount,
	}
}

// erroredItem builds a placeholder Item for a PR whose per-PR data couldn't be
// fetched, from the metadata already in hand. It carries FetchError so the TUI
// can show the PR (in Outstanding) with the error surfaced instead of dropping
// it silently. TriggerDate falls back to the PR's creation date for ordering.
func erroredItem(repo string, meta prMeta, user string, err error) Item {
	section := SectionReviewing
	if meta.Author == user {
		section = SectionAuthored
	}
	return Item{
		Key:          repo + "#" + strconv.Itoa(meta.Number),
		Number:       meta.Number,
		Title:        meta.Title,
		URL:          meta.URL,
		Section:      section,
		Author:       meta.Author,
		FetchError:   err.Error(),
		TriggerDate:  meta.CreatedAt,
		CreatedAt:    meta.CreatedAt,
		TotalCommits: meta.TotalCommitCount,
	}
}

// FetchAll fetches and classifies all open, non-draft PRs for `user` in
// `repo`. Returns items sorted by TriggerDate ascending (oldest activity
// first, so the longest-waiting PRs surface at the top). A per-PR fetch
// failure yields a FetchError placeholder item (shown in Outstanding) rather
// than a dropped PR — only failures of the searches themselves return an error.
func FetchAll(ctx context.Context, repo, user string) ([]Item, error) {
	var commenterNodes, authorNodes, allOpenNodes []searchNode
	var commenterErr, authorErr, allOpenErr error
	var ci codeownersInfo

	var searchWG sync.WaitGroup
	searchWG.Add(4)
	go func() {
		defer searchWG.Done()
		ci = fetchCodeownersInfo(ctx, repo)
	}()
	go func() {
		defer searchWG.Done()
		commenterNodes, commenterErr = searchOpenPRs(ctx, repo, "commenter:"+user)
	}()
	go func() {
		defer searchWG.Done()
		authorNodes, authorErr = searchOpenPRs(ctx, repo, "author:"+user)
	}()
	go func() {
		defer searchWG.Done()
		allOpenNodes, allOpenErr = searchOpenPRs(ctx, repo, "")
	}()
	searchWG.Wait()

	if commenterErr != nil {
		return nil, fmt.Errorf("searching PRs commented on by %s in %s: %w", user, repo, commenterErr)
	}
	if authorErr != nil {
		return nil, fmt.Errorf("searching PRs authored by %s in %s: %w", user, repo, authorErr)
	}
	if allOpenErr != nil {
		return nil, fmt.Errorf("searching all open PRs in %s: %w", repo, allOpenErr)
	}

	metas := make(map[int]prMeta)
	commenterSet := make(map[int]bool)
	authorSet := make(map[int]bool)
	for _, n := range commenterNodes {
		metas[n.Number] = prMetaFromNode(n)
		commenterSet[n.Number] = true
	}
	for _, n := range authorNodes {
		metas[n.Number] = prMetaFromNode(n)
		authorSet[n.Number] = true
	}
	_ = authorSet // tracked for symmetry/debuggability; classification keys off meta.Author

	var items []Item

	// PRs from the "all open" search that the user hasn't participated in at
	// all (neither commented/reviewed nor authored) become SectionNew items,
	// built entirely from GraphQL metadata already in hand — no REST fetch.
	for _, n := range allOpenNodes {
		if commenterSet[n.Number] || authorSet[n.Number] {
			continue
		}
		items = append(items, classifyNew(repo, n))
	}

	sem := make(chan struct{}, outerConcurrency)
	var itemsMu sync.Mutex
	var prWG sync.WaitGroup

	for number, meta := range metas {
		number, meta := number, meta
		prWG.Add(1)
		sem <- struct{}{}
		go func() {
			defer prWG.Done()
			defer func() { <-sem }()

			comments, inline, reviews, rawCommits, err := fetchPRData(ctx, repo, number)
			if err != nil {
				// Don't drop the PR — surface it with the error instead.
				itemsMu.Lock()
				items = append(items, erroredItem(repo, meta, user, err))
				itemsMu.Unlock()
				return
			}
			activity := buildActivity(comments, inline, reviews)
			commits := toCommits(rawCommits)

			var item *Item
			switch {
			case meta.Author == user:
				item = classifyAuthored(repo, meta, user, activity, commits, ci)
			case meta.Author != user && commenterSet[number]:
				item = classifyReviewing(repo, meta, user, activity, commits, ci)
			}
			if item == nil {
				return
			}

			itemsMu.Lock()
			items = append(items, *item)
			itemsMu.Unlock()
		}()
	}
	prWG.Wait()

	// Oldest activity first, so the PRs that have been waiting longest surface
	// at the top of each tab.
	sort.Slice(items, func(i, j int) bool { return items[i].TriggerDate.Before(items[j].TriggerDate) })
	return items, nil
}
