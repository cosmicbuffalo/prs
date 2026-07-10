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
- `Enter` to mark a PR done (auto-reopens on new activity) and `i` to ignore a
  PR (a permanent mute), both persisted to `~/.local/state/prs/state.json`.
- Detail panel with PR metadata, a participant list, review status, and the
  recent comment/commit thread.
- Keyboard (arrows or `hjkl`) and mouse navigation, `o` to copy a PR's URL via
  OSC52, and `r` to refresh.
- `curl | sh` installer that downloads a prebuilt binary, falling back to a
  source build.
