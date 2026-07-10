# prs

A terminal UI that surfaces GitHub PRs in need of your attention for whatever repo you're currently in.

The two primary categories of PRs it watches out for:

- PRs you **reviewed** that have more recent activity you haven't seen yet.
- PRs you **opened** that have new activity you haven't addressed yet.

`prs` categorizes open PRs on your current repo into four buckets, `Outstanding`, `New`, `Done`, and `Ignored`. Any PR matching one of the two primary scenarios above is automatically moved into the `Outstanding` tab so that you can see what's new since you last reviewed/committed first. `New` contains PRs you haven't looked at yet, and `Done` and `Ignored` are the two buckets PRs move to as you work through them. Moving a PR to `Done` will track it as completed until new activity is detected, and moving a PR to `Ignored` will get that PR out of your backlog permanently, regardless of new activity.

## Install

The one-liner downloads a prebuilt binary for your platform and installs it to `~/.local/bin/prs`:

```bash
curl -fsSL https://raw.githubusercontent.com/cosmicbuffalo/prs/main/install.sh | sh
```

Make sure `~/.local/bin` is on your `PATH`. Prebuilt binaries are published for macOS and Linux on both `amd64` and `arm64`; on anything else — or if no release has been published yet — the installer automatically falls back to building from source (which does require [Go 1.26+](https://go.dev/dl/)).

### From source

```bash
git clone https://github.com/cosmicbuffalo/prs.git
cd prs
make install     # builds and installs to ~/.local/bin/prs
make uninstall   # removes it
```

## Usage

Run it from inside a git repo with a GitHub remote. The TUI will automatically detect the current github user based on `gh` cli auth.

```bash
prs
```

Or point it at any repo or user context explicitly (works from anywhere, for any github username):

```bash
prs --repo owner/name --as_user someone
```

| Flag | Default | Meaning |
|------|---------|---------|
| `--repo` | detected via `gh repo view` in the current directory | `owner/repo` to check |
| `--as_user` | detected via `gh api user` | GitHub login to view PRs from the perspective of |

`prs` has no credentials of its own — it shells out to the [GitHub CLI](https://cli.github.com/) (`gh`) for every piece of data. Run `gh auth status` first to make sure you're authenticated.

## Tabs

PRs are sorted into four tabs, switched with `←`/`→`. Only open, non-draft PRs are ever shown.

| Tab | What lands here |
|------|-----------------|
| **Outstanding** | PRs needing your attention: ones you reviewed that got new commits, and ones you authored that got new comments/reviews. |
| **New** | Open PRs you haven't interacted with at all yet. |
| **Done** | PRs you've marked done with `Enter`. |
| **Ignored** | PRs you've muted with `i`. |

### How a PR is classified

- **Reviewing** — a PR authored by someone else that you've commented on or reviewed, where new activity has landed since your last activity (a commit by someone other than you, or a comment/review from someone else). Lands in **Outstanding**.
- **Authored** — your own PR where a comment, review, or commit from someone else landed after your last pushed commit. Lands in **Outstanding**.
- **New** — an open PR you've never touched (not the author, never commented or reviewed). Lands in **New**.

### How PRs move between tabs

- **`Enter` marks a PR done** → it moves to **Done**. This records the PR's current activity timestamp. Done is *not* a permanent mute: on each refresh, if new activity has landed since you marked it done, the PR automatically returns to **Outstanding** (or **New**). Pressing `Enter` again on a Done PR clears it manually.
  - For PRs you're participating in (Reviewing/Authored), *any* new comment, review, or commit reopens it.
  - For **New** PRs, only a new **commit** reopens it — comments alone won't.
- **`i` marks a PR ignored** → it moves to **Ignored**. Unlike Done, this is a permanent mute: it never comes back on its own, no matter what activity lands. Press `i` again on an Ignored PR to un-mute it, and it drops back into whichever tab it naturally belongs in.

A PR lives in exactly one bucket: marking it done clears any ignored state and vice versa. Moving a PR (in either direction, including back out of Done/Ignored) is **telegraphed** with a brief animation — the cursor recolors to the destination bucket, the destination tab flashes, and its count ticks up before the PR actually moves. While it's animating you can **cancel** by pressing the same key again, or **redirect** it to the other bucket by pressing the other key, before the move commits.

Your Done and Ignored states are saved to disk (`~/.local/state/prs/state.json`), scoped per user context, and survive restarts.

## Detail panel

Selecting a PR shows its details alongside the list: the URL and title, a summary (who opened it, diff size, commit/comment counts, participant list), the review status, and the recent comment/commit thread.

The **Review Status** section is always shown, with a grayed-out "No reviews yet" when a PR has none. For **New** PRs the full comment/review/commit data is fetched lazily the first time you select the PR (you'll briefly see "Loading…", and its counts show `…` until it lands), so the initial load and refreshes stay fast.

## Keybindings

| Key | Action |
|-----|--------|
| `↓` / `↑` (or `j` / `k`) | Move the cursor within the current tab |
| `←` / `→` (or `h` / `l`) | Switch tabs |
| `Enter` | Toggle the selected PR's **done** state |
| `i` | Toggle the selected PR's **ignored** state |
| `o` | Copy the selected PR's URL to the clipboard |
| `Ctrl+D` / `Ctrl+U` | Scroll the detail panel down / up |
| `v` | Toggle between horizontal and vertical layout |
| `r` | Re-fetch everything from scratch |
| `?` | Show/hide the keybindings help overlay |
| `q` / `Ctrl+C` | Quit |

Only `Enter`, `i`, `?`, and `q` are shown in the footer; press `?` for the full list above in a floating overlay.

The mouse works too: click a tab or a PR to select it, and scroll the wheel over the list or the detail panel to scroll that side.

### Layout

`v` toggles between two layouts:

- **Horizontal** (default) — PR list on the left, detail panel on the right. The detail panel's header (URL, title, PR details, review status) stays pinned while the comment/commit thread below it scrolls.
- **Vertical** — PR list across the full width on top, detail panel across the full width on the bottom. The whole detail panel scrolls as one.

## Requirements

- **[`gh`](https://cli.github.com/), authenticated** — the only data source; run `gh auth status` to check.
- **A repo `gh` can resolve** — a checkout with a GitHub remote, or pass `--repo owner/name`.
- **Go 1.26+** to build from source (check `go version`).
- **Network access to `api.github.com`** on every launch and refresh.
- **Clipboard (`o`)** — tries a native tool (`pbcopy`, `wl-copy`, `xclip`/`xsel`) and always also emits an OSC52 escape sequence, so copy works over SSH and inside tmux as long as your terminal supports OSC52 (most modern ones do).

## Releasing

Releases are automated. To cut one:

1. Bump the version in [`VERSION`](VERSION) (e.g. `0.1.0` → `0.2.0`).
2. Add a matching `## [x.y.z]` entry to [`CHANGELOG.md`](CHANGELOG.md).
3. Merge to `main`.

The [release workflow](.github/workflows/release.yml) triggers on any change to `VERSION`, tags the commit `v<version>`, cross-compiles the macOS/Linux · amd64/arm64 binaries, and publishes a GitHub release with those assets and the CHANGELOG entry as its notes. A bump without a corresponding CHANGELOG entry fails the build, and re-running for an already-released version is a no-op.
