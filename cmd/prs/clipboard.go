package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	osc52 "github.com/aymanbagabas/go-osc52/v2"
)

// nativeCopyTimeout bounds how long we'll wait on a native clipboard tool
// before giving up, so a hung/broken tool can't block the TUI.
const nativeCopyTimeout = 2 * time.Second

// nativeTool decides which native clipboard command (if any) to try for the
// current session, based on OS and display environment variables. lookPath
// is injected (normally exec.LookPath) so this decision logic can be unit
// tested without touching the real PATH or spawning real binaries.
//
// Precedence: macOS always uses pbcopy; otherwise Wayland (wl-copy) is
// preferred over X11 (xclip, then xsel) when both happen to be set.
func nativeTool(goos, waylandDisplay, display string, lookPath func(string) (string, error)) (name string, ok bool) {
	if goos == "darwin" {
		return "pbcopy", true
	}
	if waylandDisplay != "" {
		if _, err := lookPath("wl-copy"); err == nil {
			return "wl-copy", true
		}
	}
	if display != "" {
		if _, err := lookPath("xclip"); err == nil {
			return "xclip", true
		}
		if _, err := lookPath("xsel"); err == nil {
			return "xsel", true
		}
	}
	return "", false
}

// nativeCopyArgs returns the argv (after the tool name) needed to make each
// supported tool read the clipboard text from stdin.
func nativeCopyArgs(tool string) []string {
	switch tool {
	case "xclip":
		return []string{"-selection", "clipboard"}
	case "xsel":
		return []string{"--clipboard", "--input"}
	default: // pbcopy, wl-copy take piped stdin with no args
		return nil
	}
}

// tryNativeCopy best-effort copies text using a native tool appropriate to
// the current session, if one is available. It never returns an error: a
// missing or failing native tool is not a problem, since OSC52 is the
// reliable fallback. The returned bool/name indicate what was attempted, for
// building the status message.
func tryNativeCopy(text string) (tool string, attempted bool) {
	tool, ok := nativeTool(runtime.GOOS, os.Getenv("WAYLAND_DISPLAY"), os.Getenv("DISPLAY"), exec.LookPath)
	if !ok {
		return "", false
	}

	ctx, cancel := context.WithTimeout(context.Background(), nativeCopyTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, tool, nativeCopyArgs(tool)...)
	cmd.Stdin = strings.NewReader(text)
	_ = cmd.Run() // best-effort only; ignore failures and fall through to OSC52

	return tool, true
}

// tmuxShow reads a single global tmux option value (best-effort). It returns
// the trimmed value, or "" on any error (tmux missing, option unset, timeout).
func tmuxShow(option string) string {
	ctx, cancel := context.WithTimeout(context.Background(), nativeCopyTimeout)
	defer cancel()

	out, err := exec.CommandContext(ctx, "tmux", "show", "-gv", option).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// osc52Mode decides how to emit the OSC52 sequence given whether we're running
// inside tmux and the relevant tmux options. The tmux option values are only
// consulted when inTmux is true.
//
// The subtlety this exists to handle: tmux gates DCS passthrough behind its
// allow-passthrough option (added in tmux 3.3, default off). Blindly wrapping
// every sequence in tmux passthrough — as go-osc52's .Tmux() does — means that
// on any tmux ≥3.3 with the default config, tmux silently swallows the whole
// sequence and nothing is copied. So:
//
//   - set-clipboard on|external: tmux itself intercepts a *plain* OSC52
//     sequence and forwards it to the outer terminal. Send it plain; wrapping
//     it in passthrough would bypass (and defeat) that handling.
//   - otherwise, allow-passthrough on|all: tmux won't forward OSC52 for us, but
//     it will pass a DCS-wrapped sequence straight through to the outer
//     terminal, so use passthrough mode.
//   - otherwise: send it plain as a best effort. Passthrough would just be
//     swallowed, so plain is no worse and works if tmux forwards after all.
func osc52Mode(inTmux bool, setClipboard, allowPassthrough string) osc52.Mode {
	if !inTmux {
		return osc52.DefaultMode
	}
	switch setClipboard {
	case "on", "external":
		return osc52.DefaultMode
	}
	if allowPassthrough == "on" || allowPassthrough == "all" {
		return osc52.TmuxMode
	}
	return osc52.DefaultMode
}

// Copy sends text to the system/terminal clipboard. It first tries a native
// tool appropriate to the current session (pbcopy on macOS; wl-copy if
// $WAYLAND_DISPLAY is set and wl-copy exists; xclip or xsel if $DISPLAY is
// set and one of them exists), and ALWAYS additionally emits an OSC52
// sequence so it reaches the user's real terminal clipboard over SSH/tmux
// regardless of whether a native tool was available or worked.
//
// Inside tmux, the sequence is only wrapped in DCS passthrough when tmux
// actually needs it (see osc52Mode); with tmux's common set-clipboard=on
// config a plain sequence is sent and tmux forwards it to the outer terminal.
//
// Returns a short human-readable description of what was attempted, for
// display in the TUI's status line (e.g. "Copied to clipboard"). Returns a
// non-nil error only if the OSC52 write itself failed (e.g. couldn't write
// to stdout) — a failed/absent native tool attempt should NOT be treated as
// an overall failure, since OSC52 is the reliable fallback.
func Copy(text string) (string, error) {
	tool, attempted := tryNativeCopy(text)

	seq := osc52.New(text)
	if os.Getenv("TMUX") != "" {
		seq = seq.Mode(osc52Mode(true, tmuxShow("set-clipboard"), tmuxShow("allow-passthrough")))
	}
	if _, err := seq.WriteTo(os.Stdout); err != nil {
		return "", fmt.Errorf("write OSC52 sequence: %w", err)
	}

	if attempted {
		return fmt.Sprintf("Copied to clipboard (%s + OSC52)", tool), nil
	}
	return "Copied to clipboard (OSC52)", nil
}
