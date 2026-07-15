# Changelog

All notable changes to `prs` are documented here. The version in this file
stays in sync with the [`VERSION`](VERSION) file: every `VERSION` bump has a
matching `## [x.y.z]` entry below, and the release pipeline uses that entry as
the published release notes.

This project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html)
and the format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

## [Unreleased]

### Added
- `--version` (also `-version`) flag: prints the installed prs version and
  exits. The version is stamped into the binary at build time from the
  `VERSION` file.
- Scroll hints in the detail pane: when its contents overflow the panel, a
  centered `↑ (more)` / `↓ (more)` appears at the top/bottom of the scroll
  region to signal hidden content in that direction, each disappearing once
  you scroll to that end (mirroring the PR list's scroll indicators).

### Changed
- In the horizontal layout, only the PR link, title, and baseline line stay
  pinned at the top of the detail pane; the PR Details and Review Status
  sections now scroll along with the comment/commit thread. Previously they
  were pinned too, which could make the detail pane impossible to scroll on a
  short window when a PR had a long participant or review list.

## [0.1.1] - 2026-07-14

### Fixed
- The detail pane no longer pushes the top of the TUI off-screen on taller
  windows. An un-truncated **Comments**/**Commits** section header could be
  wider than a narrow detail column and get soft-wrapped by the enclosing box,
  making the body one row taller than its height budget so the whole frame
  scrolled up by a line (the bug only surfaced on taller windows, where there
  was enough room to render down to the offending line instead of truncating
  it away). Section-header notes are now truncated to the column width, every
  detail-box line is hard-capped to the interior width, and the rendered frame
  is clamped to the terminal height as a final backstop.

## [0.1.0] - 2026-07-10

### Added
- Initial release.
- Four tabs — **Outstanding**, **New**, **Done**, **Ignored** — surfacing open,
  non-draft PRs that need attention: PRs you've reviewed that got new commits,
  your own PRs that got new comments/reviews, and PRs you haven't seen yet.
  Each bucket has its own accent color (Outstanding orange, New white, Done
  green, Ignored red) used for the cursor bar and the selected tab.
- `Enter` to mark a PR done (auto-reopens on new activity) and `i` to ignore a
  PR (a permanent mute). A PR lives in exactly one bucket, and both states are
  persisted per user to `~/.local/state/prs/state.json`. Toggling either is
  telegraphed with a brief animation you can cancel (press the same key again)
  or redirect (press the other key) before it commits.
- Detail panel with PR metadata, a participant list, an always-shown review
  status, and the recent comment/commit thread. In comments, `code` spans
  render orange, links and URLs blue, and file paths yellow. New-tab PRs load
  their full comment/review/commit data lazily the first time you select them.
- Horizontal (list · detail side by side) and vertical (list over detail)
  layouts, toggled with `v`.
- A floating `?` help overlay with the full keymap; the footer keeps just the
  essentials.
- Keyboard (arrows or `hjkl`) and mouse navigation, `o` to copy a PR's URL via
  OSC52, and `r` to refresh.
- `curl | sh` installer that downloads a prebuilt binary, falling back to a
  source build.
