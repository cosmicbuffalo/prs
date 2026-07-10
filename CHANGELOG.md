# Changelog

All notable changes to `prs` are documented here. The version in this file
stays in sync with the [`VERSION`](VERSION) file: every `VERSION` bump has a
matching `## [x.y.z]` entry below, and the release pipeline uses that entry as
the published release notes.

This project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html)
and the format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

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
