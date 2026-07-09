package main

import "github.com/charmbracelet/bubbles/key"

// KeyMap defines every key binding the TUI recognizes. Cursor/tab movement
// also accepts vim-style hjkl as a silent alias for the arrow keys — same
// binding, so key.Matches treats them identically; the footer hints (which
// are hardcoded strings, not derived from these bindings) intentionally
// keep showing only the arrows.
type KeyMap struct {
	Up         key.Binding
	Down       key.Binding
	Left       key.Binding
	Right      key.Binding
	Toggle     key.Binding
	Ignore     key.Binding
	Copy       key.Binding
	Refresh    key.Binding
	ScrollUp   key.Binding
	ScrollDown key.Binding
	Quit       key.Binding
}

// DefaultKeyMap returns the app's standard key bindings.
func DefaultKeyMap() KeyMap {
	return KeyMap{
		Up: key.NewBinding(
			key.WithKeys("up", "k"),
			key.WithHelp("↑", "move up"),
		),
		Down: key.NewBinding(
			key.WithKeys("down", "j"),
			key.WithHelp("↓", "move down"),
		),
		Left: key.NewBinding(
			key.WithKeys("left", "h"),
			key.WithHelp("←", "previous tab"),
		),
		Right: key.NewBinding(
			key.WithKeys("right", "l"),
			key.WithHelp("→", "next tab"),
		),
		Toggle: key.NewBinding(
			key.WithKeys("enter"),
			key.WithHelp("⏎", "toggle done"),
		),
		Ignore: key.NewBinding(
			key.WithKeys("i"),
			key.WithHelp("i", "toggle ignore"),
		),
		Copy: key.NewBinding(
			key.WithKeys("o"),
			key.WithHelp("o", "copy link"),
		),
		// NOTE: intentionally "r" for Refresh, not "u". "u" is deliberately
		// left unbound — reserved for a possible future "self-update the
		// binary" feature, out of scope here.
		Refresh: key.NewBinding(
			key.WithKeys("r"),
			key.WithHelp("r", "refresh"),
		),
		// Scroll the detail (right) panel when its content is taller than
		// the available height. Vim-style ctrl+d/ctrl+u, chosen because
		// plain up/down are reserved for moving the list cursor.
		ScrollDown: key.NewBinding(
			key.WithKeys("ctrl+d"),
			key.WithHelp("^d", "scroll detail down"),
		),
		ScrollUp: key.NewBinding(
			key.WithKeys("ctrl+u"),
			key.WithHelp("^u", "scroll detail up"),
		),
		Quit: key.NewBinding(
			key.WithKeys("q", "ctrl+c"),
			key.WithHelp("q", "quit"),
		),
	}
}
