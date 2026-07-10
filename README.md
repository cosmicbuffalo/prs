# prs

A terminal UI that surfaces only the open GitHub PRs in a repo that actually need your attention right now — not every open PR.

It watches for the two ways a PR quietly goes stale after you've looked at it:

- You **reviewed** someone's PR, they pushed new commits, and it fell off your radar.
- You **opened** a PR, someone commented, and nothing pulled it back to your attention.

`prs` finds exactly those situations, plus brand-new PRs you haven't seen yet, and lets you triage them into simple per-PR states that persist across restarts.

## Install

One-liner (requires [Go 1.26+](https://go.dev/dl/) and `git`):

```bash
curl -fsSL https://raw.githubusercontent.com/cosmicbuffalo/prs/main/install.sh | sh
```

This clones the repo, builds the binary, and installs it to `~/.local/bin/prs`. Make sure that directory is on your `PATH`.

### From source

```bash
git clone https://github.com/cosmicbuffalo/prs.git
cd prs
make install     # builds and installs to ~/.local/bin/prs
make uninstall   # removes it
```

## Usage

Run it from inside a git checkout with a GitHub remote:

```bash
prs
```

Or point it at any repo/user explicitly (works from anywhere):

```bash
prs --repo owner/name --user someone
```

| Flag | Default | Meaning |
|------|---------|---------|
| `--repo` | detected via `gh repo view` in the current directory | `owner/repo` to check |
| `--user` | detected via `gh api user` | GitHub login whose activity to check against |

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
- **Authored** — your own PR where a comment or review from someone else landed after your last pushed commit. Lands in **Outstanding**.
- **New** — an open PR you've never touched (not the author, never commented or reviewed). Lands in **New**.

### How PRs move between tabs

- **`Enter` marks a PR done** → it moves to **Done**. This records the PR's current activity timestamp. Done is *not* a permanent mute: on each refresh, if new activity has landed since you marked it done, the PR automatically returns to **Outstanding** (or **New**). Pressing `Enter` again on a Done PR clears it manually.
  - For PRs you're participating in (Reviewing/Authored), *any* new comment, review, or commit reopens it.
  - For **New** PRs, only a new **commit** reopens it — comments alone won't.
- **`i` marks a PR ignored** → it moves to **Ignored**. Unlike Done, this is a permanent mute: it never comes back on its own, no matter what activity lands. Press `i` again on an Ignored PR to un-mute it, and it drops back into whichever tab it naturally belongs in.

Ignored takes precedence over Done, which takes precedence over New/Outstanding — so a PR that's both ignored and done shows in Ignored until un-ignored.

Your Done and Ignored states are saved to disk (`~/.local/state/prs/state.json`) and survive restarts.

## Keybindings

| Key | Action |
|-----|--------|
| `↑` / `↓` (or `k` / `j`) | Move the cursor within the current tab |
| `←` / `→` (or `h` / `l`) | Switch tabs |
| `Enter` | Toggle the selected PR's **done** state |
| `i` | Toggle the selected PR's **ignored** state |
| `o` | Copy the selected PR's URL to the clipboard |
| `Ctrl+D` / `Ctrl+U` | Scroll the detail panel down / up |
| `r` | Re-fetch everything from scratch |
| `q` / `Ctrl+C` | Quit |

The mouse works too: click a tab or a PR to select it, and scroll the wheel over the list or the detail panel to scroll that side.

## Requirements

- **[`gh`](https://cli.github.com/), authenticated** — the only data source; run `gh auth status` to check.
- **A repo `gh` can resolve** — a checkout with a GitHub remote, or pass `--repo owner/name`.
- **Go 1.26+** to build from source (check `go version`).
- **Network access to `api.github.com`** on every launch and refresh.
- **Clipboard (`o`)** — tries a native tool (`pbcopy`, `wl-copy`, `xclip`/`xsel`) and always also emits an OSC52 escape sequence, so copy works over SSH and inside tmux as long as your terminal supports OSC52 (most modern ones do).
