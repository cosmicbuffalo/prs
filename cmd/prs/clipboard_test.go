package main

import (
	"errors"
	"testing"
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
