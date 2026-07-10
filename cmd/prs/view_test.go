package main

import (
	"reflect"
	"testing"
)

func TestCommentMarkupPattern(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{"file path", "see app/models/foo.rb please", []string{"app/models/foo.rb"}},
		{"path trailing period", "edit path/to/file.js.", []string{"path/to/file.js"}},
		{"url wins over path", "https://example.com/a/b.go here", []string{"https://example.com/a/b.go"}},
		{"markdown link", "[f39](https://x.com/c/d) ok", []string{"[f39](https://x.com/c/d)"}},
		{"inline code", "call `foo()` now", []string{"`foo()`"}},
		{"mention", "cc @jsmith_example thanks", []string{"@jsmith_example"}},
		{"version string not a path", "bump 1.2023.master.a40d125", nil},
		{"bare domain not a path", "see example.com for info", nil},
		{"no extension not a path", "in the and/or case", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := commentMarkupPattern.FindAllString(c.in, -1)
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("FindAllString(%q) = %#v, want %#v", c.in, got, c.want)
			}
		})
	}
}

func TestTruncateLogin_LeavesShortLoginsUntouched(t *testing.T) {
	if got := truncateLogin("jsmith_example"); got != "jsmith_example" {
		t.Fatalf("truncateLogin = %q, want unchanged (14 runes, under the cap)", got)
	}
}

func TestTruncateLogin_TruncatesLongLogins(t *testing.T) {
	// 40 runes — mirrors an unusually long anonymized/service-account login
	// that was blowing up list rows before this cap existed.
	long := "a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6_example"
	got := truncateLogin(long)
	if len([]rune(got)) != maxUsernameDisplayRunes {
		t.Fatalf("truncateLogin(%q) = %q (%d runes), want exactly %d runes", long, got, len([]rune(got)), maxUsernameDisplayRunes)
	}
	if got[len(got)-len("…"):] != "…" {
		t.Fatalf("truncateLogin(%q) = %q, want a trailing ellipsis", long, got)
	}
}

func TestSanitizeDetailText_CollapsesExcessiveBlankLines(t *testing.T) {
	got := sanitizeDetailText("first line\n\n\n\n\nsecond line")
	want := "first line\n\nsecond line"
	if got != want {
		t.Fatalf("sanitizeDetailText = %q, want %q (3+ newlines collapsed to 2)", got, want)
	}
}

func TestSanitizeDetailText_LeavesSingleBlankLineUntouched(t *testing.T) {
	got := sanitizeDetailText("first line\n\nsecond line")
	want := "first line\n\nsecond line"
	if got != want {
		t.Fatalf("sanitizeDetailText = %q, want %q (a single blank line shouldn't be touched)", got, want)
	}
}

func TestDisplayLogin_CollapsesCopilotVariants(t *testing.T) {
	for _, login := range []string{"Copilot", "copilot-pull-request-reviewer", "copilot-pull-request-reviewer[bot]"} {
		if got := displayLogin(login); got != "copilot" {
			t.Errorf("displayLogin(%q) = %q, want copilot", login, got)
		}
	}
	if got := displayLogin("jsmith_example"); got != "jsmith_example" {
		t.Errorf("displayLogin left a normal login alone: got %q", got)
	}
}
