# prs

A terminal UI that tells me which open GitHub PRs in a repo actually need my attention right now, instead of every open PR. I kept running into two flavors of "this went stale after I looked at it": I'd review someone's PR, they'd push new commits, and it would quietly fall out of my head — or I'd push my own PR, someone would leave a comment, and it would sit unanswered because nothing forced it back onto my radar. `prs` watches for exactly those two situations and surfaces only the PRs where something changed since the last time I touched them.

## How It Works

```
                     gh api / gh api graphql
                              │
                              ▼
                    open PRs in owner/repo
                              │
              ┌───────────────┴───────────────┐
              ▼                                ▼
      Reviewing check                   Authored check
  (PRs I commented/reviewed          (my own open PRs)
   on, authored by someone else)              │
              │                                │
   new commits pushed since             new comments/reviews
   my last comment/review,              from others since
   by someone other than me             my last pushed commit
              │                                │
              └───────────────┬────────────────┘
                              ▼
                    Outstanding / Done tabs
                    (state fingerprinted to
                     a point in time)
```

Two `gh api graphql` searches find every open PR in the repo where I'm either a `commenter` or the `author`. Each candidate PR's comments, inline review comments, reviews, and commits are then fetched and classified:

- **Reviewing** — PRs someone else authored where I've left at least one comment or review, and a commit has landed since my latest activity that wasn't authored or committed by me.
- **Authored** — my own PRs where a comment or review from someone else landed after my last pushed commit.

Items that qualify show up in the **Outstanding** tab. Pressing Enter marks the selected item **Done**, which records the PR's current "trigger" timestamp (the newest qualifying commit/comment date). Pressing Enter again on a Done item moves it back to Outstanding. The key part: "done" isn't a permanent mute. On every fetch, a PR marked done is only kept in the Done tab if nothing newer than that recorded timestamp has shown up — if it has, the PR automatically reappears in Outstanding, because it means real new activity happened after you called it done.

## Install

Today the only documented path is building from a local checkout:

```bash
git clone <this repo>
cd prs
make install
```

This builds the binary and installs it to `~/.local/share/prs/bin/prs`, symlinked from `~/.local/bin/prs` (make sure that's on your `PATH`). `make uninstall` removes both.

`install.sh` also exists for a future `curl | sh`-style install, but it currently only fully supports two cases: running it from inside a local checkout (same as `make install`, just via the script), or being pointed at a remote with `PRS_REPO_URL=<git-url> sh install.sh` (it `git clone --depth=1`s that URL, builds, and installs). There's no default remote configured yet, so piping it straight from a URL without setting `PRS_REPO_URL` first will just fail with a clear error — this isn't wired up as a real one-liner install until this project has a pushed git remote to default to.

Building requires **Go 1.26+** (see `go.mod`). Check `go version` first — a system-default `go` that's older than that will fail to build this.

## Usage

Run it from inside a git checkout with a GitHub remote `gh` recognizes:

```bash
prs
```

Or point it at a specific repo/user explicitly (works from anywhere, since it skips the `gh repo view` detection):

```bash
prs --repo owner/name --user someone
```

| Flag | Default | Meaning |
|------|---------|---------|
| `--repo` | detected via `gh repo view` in the cwd | `owner/repo` to check |
| `--user` | detected via `gh api user` | GitHub login whose activity to check against |

### Keybindings

| Key | Action |
|-----|--------|
| `↑` / `↓` | Move the cursor within the current tab |
| `←` / `→` | Switch between Outstanding / Done |
| `Enter` | Toggle the selected PR's done state |
| `o` | Copy the selected PR's URL to the clipboard |
| `r` | Full re-fetch from scratch (same loading spinner as launch) |
| `q` / `Ctrl+C` | Quit |

## Dependencies / Environment

- **`gh` (GitHub CLI), installed and authenticated.** `prs` has no credentials of its own — it shells out to `gh api`, `gh api graphql`, and `gh repo view` for every piece of data it shows. Run `gh auth status` first; if `gh` isn't authenticated, `prs` will fail during its fetch and show the error in the status line rather than crashing.
- **A repo `gh` can resolve**, i.e. a git checkout with a GitHub remote configured, unless you pass `--repo owner/name` to skip that detection entirely.
- **Go 1.26+ to build from source.** Check `go version` — an older system `go` (this machine's default is 1.19.8) will not build it; point at a newer toolchain instead.
- **Network access to `api.github.com`** (via `gh`) on every launch and every `r` refresh.
- **Clipboard (`o` key):** `prs` tries a native tool appropriate to the session first — `pbcopy` on macOS, or on Linux `wl-copy` (if `$WAYLAND_DISPLAY` is set) then `xclip`/`xsel` (if `$DISPLAY` is set) — but it *always* also emits an OSC52 terminal escape sequence (auto-wrapped for tmux passthrough when `$TMUX` is set) as a reliable fallback. That fallback is what makes `o` work over SSH or in tmux with no display or clipboard tool available at all; it just depends on your terminal emulator supporting OSC52 (most modern ones do — iTerm2, Kitty, Alacritty, WezTerm, Windows Terminal, etc.) and, if you're in tmux, a reasonably modern tmux version.
- **Local state:** `~/.local/state/prs/state.json` tracks which PRs are marked done and as of what timestamp. It's pruned automatically on every fetch (entries for PRs that no longer qualify or have closed/merged are dropped), so it won't grow unbounded. Safe to delete any time to reset all done/outstanding state.
