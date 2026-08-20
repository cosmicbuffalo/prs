package main

import (
	"errors"
	"testing"

	osc52 "github.com/aymanbagabas/go-osc52/v2"
)

// lookPathNone simulates a PATH where no clipboard tool is installed.
func lookPathNone(name string) (string, error) {
	return "", errors.New("not found: " + name)
}

// lookPathOnly simulates a PATH where only the given tool name resolves.
func lookPathOnly(found string) func(string) (string, error) {
	return func(name string) (string, error) {
		if name == found {
			return "/usr/bin/" + name, nil
		}
		return "", errors.New("not found: " + name)
	}
}

func TestNativeToolDarwinAlwaysUsesPbcopy(t *testing.T) {
	tool, ok := nativeTool("darwin", "", "", lookPathNone)
	if !ok || tool != "pbcopy" {
		t.Fatalf("expected pbcopy/true on darwin, got %q/%v", tool, ok)
	}
}

func TestNativeToolWaylandUsesWlCopyWhenFound(t *testing.T) {
	tool, ok := nativeTool("linux", "wayland-0", "", lookPathOnly("wl-copy"))
	if !ok || tool != "wl-copy" {
		t.Fatalf("expected wl-copy/true, got %q/%v", tool, ok)
	}
}

func TestNativeToolX11UsesXclipWhenFound(t *testing.T) {
	tool, ok := nativeTool("linux", "", ":0", lookPathOnly("xclip"))
	if !ok || tool != "xclip" {
		t.Fatalf("expected xclip/true, got %q/%v", tool, ok)
	}
}

func TestNativeToolX11FallsBackToXsel(t *testing.T) {
	tool, ok := nativeTool("linux", "", ":0", lookPathOnly("xsel"))
	if !ok || tool != "xsel" {
		t.Fatalf("expected xsel/true when only xsel is present, got %q/%v", tool, ok)
	}
}

func TestNativeToolNoneAvailable(t *testing.T) {
	tool, ok := nativeTool("linux", "", "", lookPathNone)
	if ok || tool != "" {
		t.Fatalf("expected no native tool when neither WAYLAND_DISPLAY nor DISPLAY is set, got %q/%v", tool, ok)
	}
}

func TestNativeToolNoneAvailableWithDisplaySetButNoTools(t *testing.T) {
	tool, ok := nativeTool("linux", "", ":0", lookPathNone)
	if ok || tool != "" {
		t.Fatalf("expected no native tool when DISPLAY is set but no tools are installed, got %q/%v", tool, ok)
	}
}

func TestOSC52ModeOutsideTmuxIsPlain(t *testing.T) {
	// Outside tmux the option values are irrelevant and must be ignored.
	if got := osc52Mode(false, "off", "off"); got != osc52.DefaultMode {
		t.Fatalf("expected DefaultMode outside tmux, got %v", got)
	}
}

func TestOSC52ModeSetClipboardOnIsPlain(t *testing.T) {
	// With set-clipboard on, tmux forwards a plain sequence itself; wrapping it
	// in passthrough would defeat that. Passthrough being off must not matter.
	if got := osc52Mode(true, "on", "off"); got != osc52.DefaultMode {
		t.Fatalf("expected DefaultMode with set-clipboard on, got %v", got)
	}
	if got := osc52Mode(true, "external", "off"); got != osc52.DefaultMode {
		t.Fatalf("expected DefaultMode with set-clipboard external, got %v", got)
	}
}

func TestOSC52ModePassthroughOnlyWhenNeededAndAllowed(t *testing.T) {
	// set-clipboard off but allow-passthrough on|all: passthrough is the only
	// way to reach the outer terminal.
	if got := osc52Mode(true, "off", "on"); got != osc52.TmuxMode {
		t.Fatalf("expected TmuxMode with set-clipboard off + allow-passthrough on, got %v", got)
	}
	if got := osc52Mode(true, "off", "all"); got != osc52.TmuxMode {
		t.Fatalf("expected TmuxMode with set-clipboard off + allow-passthrough all, got %v", got)
	}
}

func TestOSC52ModeFallsBackToPlainWhenPassthroughDisabled(t *testing.T) {
	// This is the broken-config case that motivated the fix: set-clipboard not
	// forwarding and allow-passthrough off. Passthrough would be swallowed by
	// tmux, so we send plain as a best effort rather than guaranteed-dead DCS.
	if got := osc52Mode(true, "off", "off"); got != osc52.DefaultMode {
		t.Fatalf("expected DefaultMode fallback when passthrough disabled, got %v", got)
	}
	if got := osc52Mode(true, "", ""); got != osc52.DefaultMode {
		t.Fatalf("expected DefaultMode fallback when options unknown, got %v", got)
	}
}
