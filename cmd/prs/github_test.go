package main

import (
	"errors"
	"testing"
	"time"
)

func mustParse(t *testing.T, s string) time.Time {
	t.Helper()
	ts, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("parsing time %q: %v", s, err)
	}
	return ts
}

func TestClassifyReviewing_QuietWhenNoNewActivity(t *testing.T) {
	// The user reviewed the PR and nothing new has landed from anyone else
	// since. It must still be returned — marked Quiet, so it lands in Done
	// rather than being dropped.
	meta := prMeta{Number: 1, Title: "Some PR", URL: "https://example.com/1", Author: "someone-else"}
	t0 := mustParse(t, "2026-01-01T00:00:00Z")

	activity := []Activity{
		{Date: t0, Login: "me", Kind: ActivityComment, Body: "looks good"},
	}
	commits := []Commit{
		// Committed before the user's activity, so it's not "new".
		{SHA: "aaa", CommitterDate: t0.Add(-time.Hour), AuthorLogin: "someone-else", CommitterLogin: "someone-else"},
	}

	item := classifyReviewing("owner/repo", meta, "me", activity, commits, codeownersInfo{})
	if item == nil {
		t.Fatalf("expected a non-nil item (involved PRs are never dropped)")
	}
	if !item.Quiet {
		t.Errorf("expected Quiet=true when there's no new activity, got %+v", item)
	}
	if item.Section != SectionReviewing {
		t.Errorf("Section = %q, want %q", item.Section, SectionReviewing)
	}
}

func TestClassifyReviewing_NoActivityFromUserFallsBackToCreatedAt(t *testing.T) {
	// The commenter search surfaced a PR we see no activity from the user on;
	// baseline falls back to the PR's creation date, so later activity from
	// others still makes it a (non-Quiet) reviewing item rather than dropping.
	t0 := mustParse(t, "2026-01-01T00:00:00Z")
	meta := prMeta{Number: 1, Title: "Some PR", URL: "https://example.com/1", Author: "someone-else", CreatedAt: t0}

	activity := []Activity{
		{Date: t0.Add(time.Hour), Login: "someone-else", Kind: ActivityComment, Body: "self comment"},
	}
	commits := []Commit{
		{SHA: "aaa", CommitterDate: t0.Add(time.Hour), AuthorLogin: "someone-else", CommitterLogin: "someone-else"},
	}

	item := classifyReviewing("owner/repo", meta, "me", activity, commits, codeownersInfo{})
	if item == nil {
		t.Fatalf("expected a non-nil item")
	}
	if item.Quiet {
		t.Errorf("expected Quiet=false (there's activity after the createdAt baseline), got %+v", item)
	}
}

func TestClassifyReviewing_QualifyingCommitFromOther(t *testing.T) {
	meta := prMeta{Number: 42, Title: "Some PR", URL: "https://example.com/42", Author: "someone-else"}
	t0 := mustParse(t, "2026-01-01T00:00:00Z")
	commitDate := t0.Add(time.Hour)

	activity := []Activity{
		{Date: t0, Login: "me", Kind: ActivityReview, State: ReviewApproved, Body: ""},
	}
	commits := []Commit{
		{SHA: "bbb", Message: "fix stuff", CommitterDate: commitDate, AuthorLogin: "someone-else", CommitterLogin: "someone-else"},
	}

	item := classifyReviewing("owner/repo", meta, "me", activity, commits, codeownersInfo{})
	if item == nil {
		t.Fatalf("expected a non-nil item")
	}
	if item.Key != "owner/repo#42" {
		t.Errorf("Key = %q, want %q", item.Key, "owner/repo#42")
	}
	if item.Section != SectionReviewing {
		t.Errorf("Section = %q, want %q", item.Section, SectionReviewing)
	}
	if !item.TriggerDate.Equal(commitDate) {
		t.Errorf("TriggerDate = %v, want %v", item.TriggerDate, commitDate)
	}
	if !item.Baseline.Equal(t0) {
		t.Errorf("Baseline = %v, want %v", item.Baseline, t0)
	}
	if item.BaselineLabel != "your last activity" {
		t.Errorf("BaselineLabel = %q, want %q", item.BaselineLabel, "your last activity")
	}
	if item.Badge != ReviewApproved {
		t.Errorf("Badge = %q, want %q", item.Badge, ReviewApproved)
	}
	if len(item.Commits) != 1 || item.Commits[0].SHA != "bbb" {
		t.Errorf("Commits = %+v, want a single commit bbb", item.Commits)
	}
	wantSummary := "1 new commit(s) · latest: fix stuff"
	if item.LatestSummary != wantSummary {
		t.Errorf("LatestSummary = %q, want %q", item.LatestSummary, wantSummary)
	}
}

func TestClassifyReviewing_ExcludesCommitByUser(t *testing.T) {
	meta := prMeta{Number: 7, Title: "Some PR", URL: "https://example.com/7", Author: "someone-else"}
	t0 := mustParse(t, "2026-01-01T00:00:00Z")

	activity := []Activity{
		{Date: t0, Login: "me", Kind: ActivityComment, Body: "will fix"},
	}
	commits := []Commit{
		// Newer than baseline, but pushed by the user themself (e.g. they
		// committed after commenting) — must not count as new activity.
		{SHA: "ccc", CommitterDate: t0.Add(time.Hour), AuthorLogin: "me", CommitterLogin: "me"},
	}

	item := classifyReviewing("owner/repo", meta, "me", activity, commits, codeownersInfo{})
	if item == nil {
		t.Fatalf("expected a non-nil item")
	}
	if !item.Quiet {
		t.Errorf("expected Quiet=true — the only newer commit is the user's own, so nothing new from others, got %+v", item)
	}
}

func TestClassifyReviewing_QualifiesSolelyFromNewCommentNoNewCommit(t *testing.T) {
	// Previously Reviewing only looked at commits to decide whether a PR
	// qualifies; a new comment from someone else with no new commit would
	// not have qualified. Now activity alone must be enough.
	meta := prMeta{Number: 10, Title: "Some PR", URL: "https://example.com/10", Author: "someone-else"}
	t0 := mustParse(t, "2026-01-01T00:00:00Z")
	commentDate := t0.Add(time.Hour)

	activity := []Activity{
		{Date: t0, Login: "me", Kind: ActivityComment, Body: "looks good so far"},
		{Date: commentDate, Login: "someone-else", Kind: ActivityComment, Body: "actually, one more thing"},
	}
	commits := []Commit{
		// Older than the user's activity, so it doesn't qualify on its own.
		{SHA: "aaa", CommitterDate: t0.Add(-time.Hour), AuthorLogin: "someone-else", CommitterLogin: "someone-else"},
	}

	item := classifyReviewing("owner/repo", meta, "me", activity, commits, codeownersInfo{})
	if item == nil {
		t.Fatalf("expected a non-nil item when someone else commented after the user's last activity")
	}
	if !item.TriggerDate.Equal(commentDate) {
		t.Errorf("TriggerDate = %v, want %v", item.TriggerDate, commentDate)
	}
	if len(item.Commits) != 0 {
		t.Errorf("Commits = %+v, want none", item.Commits)
	}
	wantSummary := "someone-else: actually, one more thing"
	if item.LatestSummary != wantSummary {
		t.Errorf("LatestSummary = %q, want %q", item.LatestSummary, wantSummary)
	}
}

func TestClassifyReviewing_LatestSummaryPicksMostRecentAcrossCommitAndActivity(t *testing.T) {
	meta := prMeta{Number: 11, Title: "Some PR", URL: "https://example.com/11", Author: "someone-else"}
	t0 := mustParse(t, "2026-01-01T00:00:00Z")
	commentDate := t0.Add(time.Hour)
	commitDate := t0.Add(2 * time.Hour) // newer than the comment

	activity := []Activity{
		{Date: t0, Login: "me", Kind: ActivityComment, Body: "please address this"},
		{Date: commentDate, Login: "someone-else", Kind: ActivityComment, Body: "on it"},
	}
	commits := []Commit{
		{SHA: "bbb", Message: "address feedback", CommitterDate: commitDate, AuthorLogin: "someone-else", CommitterLogin: "someone-else"},
	}

	item := classifyReviewing("owner/repo", meta, "me", activity, commits, codeownersInfo{})
	if item == nil {
		t.Fatalf("expected a non-nil item")
	}
	if !item.TriggerDate.Equal(commitDate) {
		t.Errorf("TriggerDate = %v, want %v (the commit, since it's newer than the comment)", item.TriggerDate, commitDate)
	}
	wantSummary := "1 new commit(s) · latest: address feedback"
	if item.LatestSummary != wantSummary {
		t.Errorf("LatestSummary = %q, want %q", item.LatestSummary, wantSummary)
	}
}

func TestClassifyReviewing_BadgeIsNoneWithOnlyComments(t *testing.T) {
	meta := prMeta{Number: 8, Title: "Some PR", URL: "https://example.com/8", Author: "someone-else"}
	t0 := mustParse(t, "2026-01-01T00:00:00Z")

	activity := []Activity{
		{Date: t0, Login: "me", Kind: ActivityComment, Body: "just a comment"},
	}
	commits := []Commit{
		{SHA: "ddd", CommitterDate: t0.Add(time.Hour), AuthorLogin: "someone-else", CommitterLogin: "someone-else"},
	}

	item := classifyReviewing("owner/repo", meta, "me", activity, commits, codeownersInfo{})
	if item == nil {
		t.Fatalf("expected a non-nil item")
	}
	if item.Badge != ReviewStateNone {
		t.Errorf("Badge = %q, want %q", item.Badge, ReviewStateNone)
	}
}

func TestClassifyAuthored_QuietWhenNoNewActivity(t *testing.T) {
	// The user's own PR with no new activity from others since their last push
	// must still be returned — marked Quiet, so it lands in Done.
	meta := prMeta{Number: 3, Title: "My PR", URL: "https://example.com/3", Author: "me"}
	t0 := mustParse(t, "2026-01-01T00:00:00Z")

	commits := []Commit{
		{SHA: "aaa", CommitterDate: t0, AuthorLogin: "me", CommitterLogin: "me"},
	}
	activity := []Activity{
		// Older than the last push, so it's not "new".
		{Date: t0.Add(-time.Hour), Login: "reviewer", Kind: ActivityComment, Body: "old comment"},
	}

	item := classifyAuthored("owner/repo", meta, "me", activity, commits, codeownersInfo{})
	if item == nil {
		t.Fatalf("expected a non-nil item (authored PRs are never dropped)")
	}
	if !item.Quiet {
		t.Errorf("expected Quiet=true when there is no newer activity, got %+v", item)
	}
	if item.BaselineLabel != "your last push" {
		t.Errorf("BaselineLabel = %q, want %q", item.BaselineLabel, "your last push")
	}
}

func TestClassifyAuthored_NoCommitsQuietUsesOpenTimestamp(t *testing.T) {
	// An authored PR with no commits at all: baseline falls back to the open
	// timestamp, and with no activity it's a Quiet item labeled "opened".
	t0 := mustParse(t, "2026-01-01T00:00:00Z")
	meta := prMeta{Number: 3, Title: "My PR", URL: "https://example.com/3", Author: "me", CreatedAt: t0}
	item := classifyAuthored("owner/repo", meta, "me", nil, nil, codeownersInfo{})
	if item == nil {
		t.Fatalf("expected a non-nil item")
	}
	if !item.Quiet {
		t.Errorf("expected Quiet=true, got %+v", item)
	}
	if item.BaselineLabel != "opened" {
		t.Errorf("BaselineLabel = %q, want %q (no own commits ⇒ baseline is the open timestamp)", item.BaselineLabel, "opened")
	}
	if !item.Baseline.Equal(t0) {
		t.Errorf("Baseline = %v, want the PR createdAt %v", item.Baseline, t0)
	}
}

func TestClassifyAuthored_NoCommitsOfOwnUsesOpenTimestampAndSurfacesOthersCommits(t *testing.T) {
	// A co-authored PR where the user opened it but has no commit of their own
	// (someone else pushed everything). Baseline falls back to the open
	// timestamp, so others' commits after it surface it as a (non-Quiet)
	// authored item rather than dropping it.
	t0 := mustParse(t, "2026-01-01T00:00:00Z")
	meta := prMeta{Number: 4, Title: "My PR", URL: "https://example.com/4", Author: "me", CreatedAt: t0}

	commits := []Commit{
		{SHA: "aaa", CommitterDate: t0.Add(time.Hour), AuthorLogin: "someone-else", CommitterLogin: "someone-else"},
	}

	item := classifyAuthored("owner/repo", meta, "me", nil, commits, codeownersInfo{})
	if item == nil {
		t.Fatalf("expected a non-nil item (never dropped even without the user's own commits)")
	}
	if item.Quiet {
		t.Errorf("expected Quiet=false (a commit from someone else landed after open), got %+v", item)
	}
	if len(item.Commits) != 1 || item.Commits[0].SHA != "aaa" {
		t.Errorf("Commits = %+v, want the single others' commit aaa", item.Commits)
	}
}

func TestClassifyAuthored_QualifyingActivity(t *testing.T) {
	meta := prMeta{Number: 99, Title: "My PR", URL: "https://example.com/99", Author: "me"}
	t0 := mustParse(t, "2026-01-01T00:00:00Z")
	commentDate := t0.Add(time.Hour)

	commits := []Commit{
		{SHA: "aaa", CommitterDate: t0, AuthorLogin: "me", CommitterLogin: "me"},
	}
	activity := []Activity{
		{Date: commentDate, Login: "reviewer", Kind: ActivityComment, Body: "please fix"},
		// Own later comment must not count as "activity from others".
		{Date: commentDate.Add(time.Minute), Login: "me", Kind: ActivityComment, Body: "will do"},
	}

	item := classifyAuthored("owner/repo", meta, "me", activity, commits, codeownersInfo{})
	if item == nil {
		t.Fatalf("expected a non-nil item")
	}
	if item.Key != "owner/repo#99" {
		t.Errorf("Key = %q, want %q", item.Key, "owner/repo#99")
	}
	if item.Section != SectionAuthored {
		t.Errorf("Section = %q, want %q", item.Section, SectionAuthored)
	}
	if !item.Baseline.Equal(t0) {
		t.Errorf("Baseline = %v, want %v", item.Baseline, t0)
	}
	if item.BaselineLabel != "your last push" {
		t.Errorf("BaselineLabel = %q, want %q", item.BaselineLabel, "your last push")
	}
	if !item.TriggerDate.Equal(commentDate) {
		t.Errorf("TriggerDate = %v, want %v", item.TriggerDate, commentDate)
	}
	if len(item.Detail) != 1 || item.Detail[0].Login != "reviewer" {
		t.Errorf("Detail = %+v, want a single entry from reviewer", item.Detail)
	}
	if len(item.Commits) != 0 {
		t.Errorf("Commits = %+v, want none (no qualifying commits from others in this case)", item.Commits)
	}
	wantSummary := "reviewer: please fix"
	if item.LatestSummary != wantSummary {
		t.Errorf("LatestSummary = %q, want %q", item.LatestSummary, wantSummary)
	}
}

func TestClassifyAuthored_BaselineIsOwnLastPushNotMaxAcrossAllCommits(t *testing.T) {
	// Old behavior used max(CommitterDate) across ALL commits as baseline.
	// New behavior uses max(CommitterDate) across the user's OWN commits
	// only. Here someone else pushed a commit *after* the user's own last
	// push; under the old baseline that commit would have set the baseline
	// itself (never qualifying as "newer"), but under the new baseline it
	// must be treated as newer activity from someone else and qualify.
	meta := prMeta{Number: 55, Title: "My PR", URL: "https://example.com/55", Author: "me"}
	t0 := mustParse(t, "2026-01-01T00:00:00Z")
	othersCommitDate := t0.Add(time.Hour)

	commits := []Commit{
		{SHA: "mine", Message: "my commit", CommitterDate: t0, AuthorLogin: "me", CommitterLogin: "me"},
		{SHA: "theirs", Message: "their commit", CommitterDate: othersCommitDate, AuthorLogin: "someone-else", CommitterLogin: "someone-else"},
	}

	item := classifyAuthored("owner/repo", meta, "me", nil, commits, codeownersInfo{})
	if item == nil {
		t.Fatalf("expected a non-nil item")
	}
	if !item.Baseline.Equal(t0) {
		t.Errorf("Baseline = %v, want %v (user's own last push)", item.Baseline, t0)
	}
	if !item.TriggerDate.Equal(othersCommitDate) {
		t.Errorf("TriggerDate = %v, want %v", item.TriggerDate, othersCommitDate)
	}
	if len(item.Commits) != 1 || item.Commits[0].SHA != "theirs" {
		t.Errorf("Commits = %+v, want a single commit 'theirs'", item.Commits)
	}
	wantSummary := "1 new commit(s) · latest: their commit"
	if item.LatestSummary != wantSummary {
		t.Errorf("LatestSummary = %q, want %q", item.LatestSummary, wantSummary)
	}
}

func TestClassifyAuthored_LatestSummaryPicksMostRecentAcrossCommitAndActivity(t *testing.T) {
	// When both a qualifying commit and a qualifying activity entry exist,
	// LatestSummary/TriggerDate must reflect whichever is actually more
	// recent, not always the same kind.
	meta := prMeta{Number: 56, Title: "My PR", URL: "https://example.com/56", Author: "me"}
	t0 := mustParse(t, "2026-01-01T00:00:00Z")
	commitDate := t0.Add(time.Hour)
	commentDate := t0.Add(2 * time.Hour) // newer than the commit

	commits := []Commit{
		{SHA: "mine", CommitterDate: t0, AuthorLogin: "me", CommitterLogin: "me"},
		{SHA: "theirs", Message: "tweak", CommitterDate: commitDate, AuthorLogin: "someone-else", CommitterLogin: "someone-else"},
	}
	activity := []Activity{
		{Date: commentDate, Login: "reviewer", Kind: ActivityComment, Body: "still needs work"},
	}

	item := classifyAuthored("owner/repo", meta, "me", activity, commits, codeownersInfo{})
	if item == nil {
		t.Fatalf("expected a non-nil item")
	}
	if !item.TriggerDate.Equal(commentDate) {
		t.Errorf("TriggerDate = %v, want %v (the comment, since it's newer than the commit)", item.TriggerDate, commentDate)
	}
	wantSummary := "reviewer: still needs work"
	if item.LatestSummary != wantSummary {
		t.Errorf("LatestSummary = %q, want %q", item.LatestSummary, wantSummary)
	}
	if len(item.Commits) != 1 || item.Commits[0].SHA != "theirs" {
		t.Errorf("Commits = %+v, want a single commit 'theirs' even though it's not the trigger", item.Commits)
	}
}

func TestClassifyNew_WithCommits(t *testing.T) {
	createdAt := mustParse(t, "2026-01-01T00:00:00Z")
	lastCommitDate := mustParse(t, "2026-01-02T00:00:00Z")

	node := searchNode{Number: 12, Title: "Untouched PR", URL: "https://example.com/12", CreatedAt: createdAt}
	node.Author.Login = "someone-else"
	node.Commits.Nodes = []struct {
		Commit struct {
			CommittedDate time.Time `json:"committedDate"`
		} `json:"commit"`
	}{
		{Commit: struct {
			CommittedDate time.Time `json:"committedDate"`
		}{CommittedDate: lastCommitDate}},
	}

	item := classifyNew("owner/repo", node)
	if item.Key != "owner/repo#12" {
		t.Errorf("Key = %q, want %q", item.Key, "owner/repo#12")
	}
	if item.Section != SectionNew {
		t.Errorf("Section = %q, want %q", item.Section, SectionNew)
	}
	if !item.TriggerDate.Equal(lastCommitDate) {
		t.Errorf("TriggerDate = %v, want %v (latest commit date)", item.TriggerDate, lastCommitDate)
	}
	if !item.Baseline.Equal(createdAt) {
		t.Errorf("Baseline = %v, want %v", item.Baseline, createdAt)
	}
	if item.BaselineLabel != "opened" {
		t.Errorf("BaselineLabel = %q, want %q", item.BaselineLabel, "opened")
	}
	if item.Badge != ReviewStateNone {
		t.Errorf("Badge = %q, want %q", item.Badge, ReviewStateNone)
	}
	if item.Detail != nil {
		t.Errorf("Detail = %+v, want nil", item.Detail)
	}
	if item.Commits != nil {
		t.Errorf("Commits = %+v, want nil", item.Commits)
	}
	wantSummary := "opened by someone-else"
	if item.LatestSummary != wantSummary {
		t.Errorf("LatestSummary = %q, want %q", item.LatestSummary, wantSummary)
	}
}

func TestClassifyNew_NoCommitsFallsBackToCreatedAt(t *testing.T) {
	createdAt := mustParse(t, "2026-01-01T00:00:00Z")

	node := searchNode{Number: 13, Title: "Brand new PR", URL: "https://example.com/13", CreatedAt: createdAt}
	node.Author.Login = "someone-else"

	item := classifyNew("owner/repo", node)
	if !item.TriggerDate.Equal(createdAt) {
		t.Errorf("TriggerDate = %v, want %v (createdAt fallback)", item.TriggerDate, createdAt)
	}
	if !item.Baseline.Equal(createdAt) {
		t.Errorf("Baseline = %v, want %v", item.Baseline, createdAt)
	}
}

func TestFilterActivityToDetail(t *testing.T) {
	t0 := mustParse(t, "2026-01-01T00:00:00Z")

	activity := []Activity{
		// Empty-body plain comment: dropped entirely.
		{Date: t0, Login: "alice", Kind: ActivityComment, Body: ""},
		// Empty-body approval: kept, simplified.
		{Date: t0.Add(time.Minute), Login: "bob", Kind: ActivityReview, State: ReviewApproved, Body: ""},
		// Empty-body changes-requested: kept, simplified.
		{Date: t0.Add(2 * time.Minute), Login: "carol", Kind: ActivityReview, State: ReviewChangesRequested, Body: ""},
		// Empty-body "commented" review (no approval/changes-requested state): dropped.
		{Date: t0.Add(3 * time.Minute), Login: "dave", Kind: ActivityReview, State: ReviewCommented, Body: ""},
		// Non-empty body: kept as-is, verbatim, not simplified.
		{Date: t0.Add(4 * time.Minute), Login: "erin", Kind: ActivityComment, Body: "please rebase"},
	}

	lines := filterActivityToDetail(activity, true)
	if len(lines) != 3 {
		t.Fatalf("got %d detail lines, want 3: %+v", len(lines), lines)
	}

	if lines[0].Login != "bob" || lines[0].Simplified != "approved" || lines[0].Text != "" {
		t.Errorf("lines[0] = %+v, want simplified approval from bob", lines[0])
	}
	if !lines[0].ShowLogin {
		t.Errorf("lines[0].ShowLogin = false, want true")
	}
	if lines[1].Login != "carol" || lines[1].Simplified != "changes_requested" || lines[1].Text != "" {
		t.Errorf("lines[1] = %+v, want simplified changes_requested from carol", lines[1])
	}
	if lines[2].Login != "erin" || lines[2].Simplified != "" || lines[2].Text != "please rebase" {
		t.Errorf("lines[2] = %+v, want verbatim comment from erin", lines[2])
	}
}

func TestReviewEvents_NoFormalReviews(t *testing.T) {
	activity := []Activity{
		{Login: "alice", Kind: ActivityComment, Date: mustParse(t, "2026-01-01T00:00:00Z"), Body: "hi"},
		{Login: "bob", Kind: ActivityReview, State: ReviewCommented, Date: mustParse(t, "2026-01-01T01:00:00Z")},
	}
	if got := reviewEvents(activity, codeownersInfo{}); got != nil {
		t.Fatalf("reviewEvents = %+v, want nil (no approve/changes-requested reviews)", got)
	}
}

func TestReviewEvents_SingleApprovalNotSuperseded(t *testing.T) {
	activity := []Activity{
		{Login: "alice", Kind: ActivityReview, State: ReviewApproved, Date: mustParse(t, "2026-01-01T00:00:00Z")},
	}
	got := reviewEvents(activity, codeownersInfo{})
	if len(got) != 1 {
		t.Fatalf("got %d events, want 1: %+v", len(got), got)
	}
	if got[0].Login != "alice" || got[0].State != ReviewApproved || got[0].Superseded {
		t.Errorf("got %+v, want alice's approval, not superseded", got[0])
	}
}

func TestReviewEvents_ChangesRequestedThenApprovedBySameUser(t *testing.T) {
	activity := []Activity{
		{Login: "alice", Kind: ActivityReview, State: ReviewChangesRequested, Date: mustParse(t, "2026-01-01T00:00:00Z")},
		{Login: "alice", Kind: ActivityReview, State: ReviewApproved, Date: mustParse(t, "2026-01-02T00:00:00Z")},
	}
	got := reviewEvents(activity, codeownersInfo{})
	if len(got) != 2 {
		t.Fatalf("got %d events, want 2 (full history retained): %+v", len(got), got)
	}
	if got[0].State != ReviewChangesRequested || !got[0].Superseded {
		t.Errorf("got[0] = %+v, want alice's changes-requested marked Superseded=true", got[0])
	}
	if got[1].State != ReviewApproved || got[1].Superseded {
		t.Errorf("got[1] = %+v, want alice's approval marked Superseded=false (current)", got[1])
	}
}

func TestReviewEvents_TrailingCommentDoesNotClearPriorApproval(t *testing.T) {
	// A plain "commented" review after a formal approval shouldn't count as
	// a new formal state, so the approval must still show as current (not
	// superseded).
	activity := []Activity{
		{Login: "alice", Kind: ActivityReview, State: ReviewApproved, Date: mustParse(t, "2026-01-01T00:00:00Z")},
		{Login: "alice", Kind: ActivityReview, State: ReviewCommented, Date: mustParse(t, "2026-01-02T00:00:00Z")},
	}
	got := reviewEvents(activity, codeownersInfo{})
	if len(got) != 1 {
		t.Fatalf("got %d events, want 1 (the plain comment isn't a formal review): %+v", len(got), got)
	}
	if got[0].Superseded {
		t.Errorf("got[0] = %+v, want Superseded=false — a later plain comment shouldn't supersede a prior approval", got[0])
	}
}

func TestReviewEvents_ConsecutiveSameStateBothKeptOnlyLastIsCurrent(t *testing.T) {
	// The full timeline is preserved (nothing dropped) — rendering is
	// responsible for graying out everything but a reviewer's last event.
	activity := []Activity{
		{Login: "alice", Kind: ActivityReview, State: ReviewApproved, Date: mustParse(t, "2026-01-01T00:00:00Z")},
		{Login: "alice", Kind: ActivityReview, State: ReviewApproved, Date: mustParse(t, "2026-01-05T00:00:00Z")},
	}
	got := reviewEvents(activity, codeownersInfo{})
	if len(got) != 2 {
		t.Fatalf("got %d events, want 2 (re-approving should still appear in the timeline): %+v", len(got), got)
	}
	if !got[0].Superseded {
		t.Errorf("got[0] = %+v, want Superseded=true (not the reviewer's last event)", got[0])
	}
	if got[1].Superseded {
		t.Errorf("got[1] = %+v, want Superseded=false (the reviewer's current/last event)", got[1])
	}
}

func TestReviewEvents_MultipleReviewersOrderedByDate(t *testing.T) {
	activity := []Activity{
		{Login: "bob", Kind: ActivityReview, State: ReviewApproved, Date: mustParse(t, "2026-01-02T00:00:00Z")},
		{Login: "alice", Kind: ActivityReview, State: ReviewChangesRequested, Date: mustParse(t, "2026-01-01T00:00:00Z")},
	}
	got := reviewEvents(activity, codeownersInfo{})
	if len(got) != 2 {
		t.Fatalf("got %d events, want 2: %+v", len(got), got)
	}
	if got[0].Login != "alice" || got[1].Login != "bob" {
		t.Errorf("got events in order %s, %s; want alice (earlier) then bob (later)", got[0].Login, got[1].Login)
	}
}

func TestReviewEvents_NoCodeownersFile_EveryApprovalIsTrusted(t *testing.T) {
	activity := []Activity{
		{Login: "alice", Kind: ActivityReview, State: ReviewApproved, Date: mustParse(t, "2026-01-01T00:00:00Z")},
	}
	got := reviewEvents(activity, codeownersInfo{Exists: false})
	if len(got) != 1 || !got[0].IsCodeowner {
		t.Fatalf("got %+v, want IsCodeowner=true when the repo has no CODEOWNERS file at all", got)
	}
}

func TestReviewEvents_CodeownersWithoutCatchAll_AnyOnBehalfOfTeamIsTrusted(t *testing.T) {
	activity := []Activity{
		{Login: "alice", Kind: ActivityReview, State: ReviewApproved, Date: mustParse(t, "2026-01-01T00:00:00Z"), OnBehalfOfTeams: []string{"some-team"}},
		{Login: "bob", Kind: ActivityReview, State: ReviewApproved, Date: mustParse(t, "2026-01-02T00:00:00Z")},
	}
	got := reviewEvents(activity, codeownersInfo{Exists: true})
	if len(got) != 2 {
		t.Fatalf("got %d events, want 2: %+v", len(got), got)
	}
	if !got[0].IsCodeowner {
		t.Errorf("got[0] = %+v, want IsCodeowner=true (some onBehalfOf team, no catch-all to narrow it down)", got[0])
	}
	if got[1].IsCodeowner {
		t.Errorf("got[1] = %+v, want IsCodeowner=false (no onBehalfOf team at all)", got[1])
	}
}

func TestReviewEvents_CodeownersWithCatchAll_OnlyPrimaryTeamIsTrusted(t *testing.T) {
	// A repo whose CODEOWNERS has a broad "*" catch-all assigned to
	// trusted-reviewers, plus a narrower path-specific "billing-team" for
	// certain files. An approval on behalf of the narrower team shouldn't
	// render as satisfying the trusted-reviewer requirement the gray/green
	// check is meant to track.
	ci := codeownersInfo{Exists: true, PrimaryTeams: []string{"trusted-reviewers"}}
	activity := []Activity{
		{Login: "billing-reviewer", Kind: ActivityReview, State: ReviewApproved, Date: mustParse(t, "2026-01-01T00:00:00Z"), OnBehalfOfTeams: []string{"billing-team"}},
		{Login: "trusted-reviewer", Kind: ActivityReview, State: ReviewApproved, Date: mustParse(t, "2026-01-02T00:00:00Z"), OnBehalfOfTeams: []string{"trusted-reviewers"}},
	}
	got := reviewEvents(activity, ci)
	if len(got) != 2 {
		t.Fatalf("got %d events, want 2: %+v", len(got), got)
	}
	if got[0].IsCodeowner {
		t.Errorf("got[0] = %+v, want IsCodeowner=false (on behalf of billing-team, not the catch-all team)", got[0])
	}
	if !got[1].IsCodeowner {
		t.Errorf("got[1] = %+v, want IsCodeowner=true (on behalf of the catch-all trusted-reviewer team)", got[1])
	}
}

func TestPrimaryCodeownersTeams_ParsesCatchAllLine(t *testing.T) {
	raw := `# Set the trusted-reviewers group as the default reviewers
*                  @acme/trusted-reviewers

# Spec-only changes do not require trusted reviewer approval
/spec/             @acme/billing-team

# Database migrations and schema files
/db/               @acme/db-team
`
	got := primaryCodeownersTeams(raw)
	if len(got) != 1 || got[0] != "trusted-reviewers" {
		t.Fatalf("got %v, want [trusted-reviewers] (only the exact \"*\" line's owner)", got)
	}
}

func TestPrimaryCodeownersTeams_NoCatchAllLine(t *testing.T) {
	raw := `/spec/ @org/billing-team
/db/   @org/db-team
`
	got := primaryCodeownersTeams(raw)
	if len(got) != 0 {
		t.Fatalf("got %v, want empty (no \"*\" catch-all present)", got)
	}
}

func TestMeaningfulCommentCount_CountsInlineCommentsAndReviewsNotJustIssueComments(t *testing.T) {
	// Mirrors the real-world case that prompted this: a PR whose GraphQL
	// comments.totalCount (top-level issue comments only) looked tiny next
	// to its actual amount of code-review discussion.
	activity := []Activity{
		{Login: "alice", Kind: ActivityComment, Date: mustParse(t, "2026-01-01T00:00:00Z"), Body: "issue comment"},
		{Login: "bob", Kind: ActivityInlineComment, Date: mustParse(t, "2026-01-01T01:00:00Z"), Body: "inline comment"},
		{Login: "carol", Kind: ActivityReview, State: ReviewApproved, Date: mustParse(t, "2026-01-01T02:00:00Z")}, // bare approval, no body
		{Login: "dave", Kind: ActivityReview, State: ReviewCommented, Date: mustParse(t, "2026-01-01T03:00:00Z")}, // empty-body plain comment review: dropped
		{Login: "erin", Kind: ActivityReview, State: ReviewCommented, Date: mustParse(t, "2026-01-01T04:00:00Z"), Body: "looks good but one nit"},
	}
	if got := meaningfulCommentCount(activity); got != 4 {
		t.Fatalf("meaningfulCommentCount = %d, want 4 (issue comment + inline comment + bare approval + review-with-body; the empty-body plain-comment review is dropped)", got)
	}
}

func TestParticipantsOrdered_DedupesAcrossAuthorCommitsAndActivity(t *testing.T) {
	commits := []Commit{
		{AuthorLogin: "alice", CommitterLogin: "alice"},
		{AuthorLogin: "bob", CommitterLogin: "carol"}, // co-authored: both count
	}
	activity := []Activity{
		{Login: "alice", Kind: ActivityComment, Body: "dup of author"},
		{Login: "dave", Kind: ActivityReview, State: ReviewApproved},
		{Login: "dave", Kind: ActivityReview, State: ReviewApproved}, // dup reviewer entry
	}
	got := participantsOrdered("alice", false, activity, commits)
	if len(got) != 4 {
		t.Fatalf("participantsOrdered = %v, want 4 logins (alice, bob, carol, dave — each counted once)", got)
	}
}

func TestParticipantsOrdered_NoActivityOrCommitsJustAuthor(t *testing.T) {
	if got := participantsOrdered("alice", false, nil, nil); len(got) != 1 || got[0] != "alice" {
		t.Fatalf("participantsOrdered = %v, want [alice]", got)
	}
}

func TestParticipantsOrdered_ExcludesBotActivityAndBotCommitAuthors(t *testing.T) {
	// Mirrors the real-world case that prompted this: a PR whose
	// participants.totalCount looked inflated because it was counting
	// github-actions[bot], Copilot's review bot, etc alongside real people.
	commits := []Commit{
		{AuthorLogin: "alice", CommitterLogin: "alice"},
		{AuthorLogin: "dependabot[bot]", CommitterLogin: "dependabot[bot]", AuthorIsBot: true, CommitterIsBot: true},
	}
	activity := []Activity{
		{Login: "bob", Kind: ActivityComment, Body: "a real comment"},
		{Login: "github-actions[bot]", Kind: ActivityComment, Body: "CI status update", IsBot: true},
		{Login: "copilot-pull-request-reviewer[bot]", Kind: ActivityReview, State: ReviewCommented, Body: "nit", IsBot: true},
	}
	got := participantsOrdered("alice", false, activity, commits)
	if len(got) != 2 {
		t.Fatalf("participantsOrdered = %v, want 2 logins (alice, bob — bots excluded)", got)
	}
}

func TestParticipantsOrdered_ExcludesWebFlowPseudoCommitter(t *testing.T) {
	// web-flow is GitHub's own pseudo-account for commits made through its
	// web UI (e.g. a squash-merge) — typed as a regular "User" by GitHub's
	// API, so it needs its own name-based exclusion, not just a bot check.
	commits := []Commit{
		{AuthorLogin: "alice", CommitterLogin: "web-flow"},
	}
	got := participantsOrdered("alice", false, nil, commits)
	if len(got) != 1 || got[0] != "alice" {
		t.Fatalf("participantsOrdered = %v, want [alice] (web-flow excluded)", got)
	}
}

func TestParticipantsOrdered_ExcludesBotAuthoredPR(t *testing.T) {
	got := participantsOrdered("dependabot[bot]", true, nil, nil)
	if len(got) != 0 {
		t.Fatalf("participantsOrdered = %v, want empty (bot-authored PR, no other activity)", got)
	}
}

func TestParticipantsOrdered_RankedByContributionCount(t *testing.T) {
	// alice: 3 commits (self-authored/committed, each counts once) = 3.
	// bob: 2 activity entries = 2. carol: 1 activity entry = 1.
	commits := []Commit{
		{AuthorLogin: "alice", CommitterLogin: "alice"},
		{AuthorLogin: "alice", CommitterLogin: "alice"},
		{AuthorLogin: "alice", CommitterLogin: "alice"},
	}
	activity := []Activity{
		{Login: "bob", Kind: ActivityComment, Body: "first"},
		{Login: "bob", Kind: ActivityComment, Body: "second"},
		{Login: "carol", Kind: ActivityReview, State: ReviewApproved},
	}
	got := participantsOrdered("alice", false, activity, commits)
	want := []string{"alice", "bob", "carol"}
	if len(got) != len(want) {
		t.Fatalf("participantsOrdered = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("participantsOrdered = %v, want %v (most contributions first)", got, want)
		}
	}
}

func TestParticipantsOrdered_TiebreakIsAlphabetical(t *testing.T) {
	activity := []Activity{
		{Login: "zoe", Kind: ActivityComment, Body: "hi"},
		{Login: "amy", Kind: ActivityComment, Body: "hi"},
	}
	got := participantsOrdered("", false, activity, nil)
	want := []string{"amy", "zoe"}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("participantsOrdered = %v, want %v (tied counts break alphabetically)", got, want)
	}
}

func TestErroredItem_CarriesMetaAndError(t *testing.T) {
	t0 := mustParse(t, "2026-01-01T00:00:00Z")
	meta := prMeta{Number: 5, Title: "Broken PR", URL: "https://example.com/5", Author: "me", CreatedAt: t0}

	it := erroredItem("owner/repo", meta, "me", errors.New("boom"))
	if it.Key != "owner/repo#5" {
		t.Errorf("Key = %q, want owner/repo#5", it.Key)
	}
	if it.FetchError != "boom" {
		t.Errorf("FetchError = %q, want boom", it.FetchError)
	}
	if it.Section != SectionAuthored {
		t.Errorf("Section = %q, want AUTHOR (author == user)", it.Section)
	}
	if !it.TriggerDate.Equal(t0) {
		t.Errorf("TriggerDate = %v, want createdAt %v", it.TriggerDate, t0)
	}

	other := prMeta{Number: 6, Title: "Other", URL: "https://example.com/6", Author: "someone-else", CreatedAt: t0}
	if it := erroredItem("owner/repo", other, "me", errors.New("x")); it.Section != SectionReviewing {
		t.Errorf("Section = %q, want REVIEW (author != user)", it.Section)
	}
}
