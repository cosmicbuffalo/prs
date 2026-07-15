package main

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
)

// TestViewNeverExceedsWindowHeight guards against the detail pane pushing the
// rest of the TUI off-screen. The regression: an un-truncated section header
// ("Comments"/"Commits (N total commit(s), M since last activity)") could be
// wider than the (narrow) detail column, get soft-wrapped by the enclosing
// lipgloss box, and make the body one row taller than its height budget — so
// View() emitted height+1 rows and the terminal scrolled the top away. It only
// surfaced at taller windows, where there was enough vertical room to render
// down to the offending line rather than truncate it first.
func TestViewNeverExceedsWindowHeight(t *testing.T) {
	// Many comments + commits so both section headers render and the content is
	// long enough to fill a tall panel.
	var detail []DetailLine
	for i := 0; i < 60; i++ {
		detail = append(detail, DetailLine{
			Date:  time.Now().Add(-time.Duration(i) * time.Hour),
			Login: "someuser",
			Kind:  "comment",
			Text:  "a comment body long enough to wrap across a few rows in the detail column",
		})
	}
	var commits []Commit
	for i := 0; i < 8; i++ {
		commits = append(commits, Commit{
			SHA:           "abcdef1234567",
			Message:       "some commit message here",
			CommitterDate: time.Now().Add(-time.Duration(i) * time.Hour),
			AuthorLogin:   "committer",
		})
	}
	item := Item{
		Key:          "o/r#2",
		Number:       2,
		Title:        "A pull request title that is reasonably descriptive and moderately long",
		URL:          "https://github.com/o/r/pull/2",
		Section:      SectionReviewing,
		Detail:       detail,
		Commits:      commits,
		TotalCommits: 25, // makes the "(25 total commit(s), 8 since …)" note wide
	}

	for _, layout := range []layoutMode{layoutHorizontal, layoutVertical} {
		// Include narrow-but-tall windows (small detail column, lots of vertical
		// room) — the exact shape that exposed the bug — plus wider/short ones.
		for _, dim := range []struct{ w, h int }{
			{90, 88}, {90, 40}, {100, 90}, {120, 100}, {160, 50}, {200, 60}, {250, 70},
		} {
			for _, scroll := range []int{0, 20, 200} {
				m := Model{
					keys:         DefaultKeyMap(),
					width:        dim.w,
					height:       dim.h,
					hasData:      true,
					activeTab:    tabOutstanding,
					layout:       layout,
					detailScroll: scroll,
				}
				m.items[tabOutstanding] = []Item{item}
				out := m.View()
				if got := len(strings.Split(out, "\n")); got > dim.h {
					t.Errorf("layout=%d window %dx%d scroll=%d: View() emitted %d rows, exceeds window height %d",
						layout, dim.w, dim.h, scroll, got, dim.h)
				}
				for i, l := range strings.Split(out, "\n") {
					if w := lipgloss.Width(l); w > dim.w {
						t.Errorf("layout=%d window %dx%d scroll=%d: row %d width %d exceeds window width %d",
							layout, dim.w, dim.h, scroll, i, w, dim.w)
						break
					}
				}
			}
		}
	}
}

// TestRenderSectionHeaderTruncates verifies the section header note is
// truncated (with an ellipsis) to the given width rather than overflowing.
func TestRenderSectionHeaderTruncates(t *testing.T) {
	note := fmt.Sprintf(" (%d total commit(s), %d since last activity)", 25, 8)
	out := renderSectionHeader("Commits", note, 20)
	if w := lipgloss.Width(out); w > 20 {
		t.Fatalf("renderSectionHeader width = %d, want <= 20 (%q)", w, out)
	}
	if !strings.Contains(out, "…") {
		t.Errorf("expected the truncated note to end with an ellipsis, got %q", out)
	}
	// A note that already fits is left intact.
	full := renderSectionHeader("Commits", note, 80)
	if !strings.Contains(full, "total commit(s)") {
		t.Errorf("a fitting note should be shown in full, got %q", full)
	}
}

// TestHorizontalStickyHeaderIsOnlyLinkTitleBaseline verifies that in the
// horizontal layout only the PR link, title, and baseline line stay pinned —
// the PR Details / Review Status summary scrolls with the comment thread, so a
// PR with a long participant/review list can't make the panel impossible to
// scroll on a short window.
func TestHorizontalStickyHeaderIsOnlyLinkTitleBaseline(t *testing.T) {
	var detail []DetailLine
	for i := 0; i < 20; i++ {
		text := "a comment body here"
		if i == 19 {
			text = "ZZZLASTCOMMENT" // distinctive marker in the most-recent comment
		}
		detail = append(detail, DetailLine{
			Date:  time.Now().Add(-time.Duration(20-i) * time.Hour),
			Login: "someuser",
			Kind:  "comment",
			Text:  text,
		})
	}
	var revs []ReviewEvent
	for i := 0; i < 5; i++ {
		revs = append(revs, ReviewEvent{Login: fmt.Sprintf("reviewer%d", i), State: ReviewApproved, Date: time.Now()})
	}
	item := Item{
		Key:               "o/r#2",
		Number:            2,
		Title:             "A concise PR title",
		URL:               "https://x/o/r/pull/2",
		Section:           SectionReviewing,
		BaselineLabel:     "your last activity",
		Baseline:          time.Now().Add(-48 * time.Hour),
		Detail:            detail,
		Reviewers:         revs,
		ParticipantLogins: []string{"alice", "bob", "carol", "dave", "erin", "frank", "grace"},
		ParticipantCount:  9,
		Author:            "author",
		TotalComments:     20,
	}

	m := Model{keys: DefaultKeyMap(), width: 120, height: 22, hasData: true, activeTab: tabOutstanding, layout: layoutHorizontal}
	m.items[tabOutstanding] = []Item{item}

	const url = "https://x/o/r/pull/2"
	top := m.renderDetail(func() int { _, r := m.columnWidths(); return r }(), m.bodyHeight(), 0)
	// A large scroll clamps to the bottom of the scroll region.
	bottom := m.renderDetail(func() int { _, r := m.columnWidths(); return r }(), m.bodyHeight(), 1000)

	// The link stays pinned at both scroll extremes.
	if !strings.Contains(top, url) {
		t.Errorf("URL should be pinned at scroll top")
	}
	if !strings.Contains(bottom, url) {
		t.Errorf("URL should stay pinned after scrolling (it is the sticky header)")
	}
	// PR Details is part of the scroll region now: visible at the top, gone once
	// scrolled to the bottom.
	if !strings.Contains(top, "PR Details") {
		t.Errorf("PR Details should be visible at scroll top")
	}
	if strings.Contains(bottom, "PR Details") {
		t.Errorf("PR Details should scroll away (it must no longer be pinned)")
	}
	// The comment thread is reachable by scrolling: the most-recent comment
	// isn't visible at the top but is after scrolling down.
	if strings.Contains(top, "ZZZLASTCOMMENT") {
		t.Errorf("the last comment should not be visible at scroll top")
	}
	if !strings.Contains(bottom, "ZZZLASTCOMMENT") {
		t.Errorf("the last comment should be reachable by scrolling")
	}
}

// TestWindowWithScrollHints verifies the detail-pane scroll hints: none when
// content fits, a down hint at the top, both in the middle, and only an up hint
// at the bottom — always keeping the visible row count stable while overflowing.
func TestWindowWithScrollHints(t *testing.T) {
	const width = 40
	hasUp := func(rows []string) bool { return len(rows) > 0 && strings.Contains(rows[0], "↑ (more)") }
	hasDown := func(rows []string) bool { return len(rows) > 0 && strings.Contains(rows[len(rows)-1], "↓ (more)") }

	// Fits entirely: returned unchanged, no hints, no reserved rows.
	fits := []string{"a", "b", "c"}
	if got := windowWithScrollHints(fits, 0, 10, width); len(got) != 3 || hasUp(got) || hasDown(got) {
		t.Errorf("content that fits should be returned as-is with no hints, got %d rows up=%v down=%v", len(got), hasUp(got), hasDown(got))
	}

	// Overflows: 20 lines into a 10-row region.
	lines := make([]string, 20)
	for i := range lines {
		lines[i] = fmt.Sprintf("line-%d", i)
	}

	top := windowWithScrollHints(lines, 0, 10, width)
	if len(top) != 10 {
		t.Errorf("overflowing window should be exactly height rows, got %d", len(top))
	}
	if hasUp(top) {
		t.Errorf("no up hint expected at the top")
	}
	if !hasDown(top) {
		t.Errorf("down hint expected at the top (more content below)")
	}

	mid := windowWithScrollHints(lines, 5, 10, width)
	if !hasUp(mid) || !hasDown(mid) {
		t.Errorf("both hints expected in the middle, got up=%v down=%v", hasUp(mid), hasDown(mid))
	}

	// A very large scroll clamps to the bottom: up hint only.
	bot := windowWithScrollHints(lines, 100000, 10, width)
	if !hasUp(bot) {
		t.Errorf("up hint expected at the bottom (more content above)")
	}
	if hasDown(bot) {
		t.Errorf("no down hint expected at the bottom")
	}
	// The final content line must be reachable at the bottom.
	if !strings.Contains(strings.Join(bot, "\n"), "line-19") {
		t.Errorf("the last content line should be visible at the bottom")
	}
}
