package main

import "github.com/charmbracelet/lipgloss"

// chipFg is the accent color originally lifted from the user's pmux tmux
// status bar theme (~/pmux_stack/pmux/src/config.rs); it's kept as a subtle
// accent for the spinner and status line.
var chipFg = lipgloss.Color("#7dcfff")

// Content palette — mirrors the bash prototype (~/bin/gh-prs) exactly.
var (
	colorGreen   = lipgloss.Color("2")
	colorRed     = lipgloss.Color("1")
	colorCyan    = lipgloss.Color("6")
	colorGray    = lipgloss.Color("8")
	colorMagenta = lipgloss.Color("5")
	colorOrange  = lipgloss.Color("208")
	colorYellow  = lipgloss.Color("3")

	styleApproved         = lipgloss.NewStyle().Foreground(colorGreen)
	styleWeakApproved     = lipgloss.NewStyle().Foreground(colorYellow) // an approval that's valid but not from the trusted-reviewer team — yellow so it's clearly distinct from both a full (green) approval and a superseded (gray) one
	styleChangesRequested = lipgloss.NewStyle().Foreground(colorRed)
	styleCommented        = lipgloss.NewStyle().Foreground(colorCyan)
	styleNotReviewed      = lipgloss.NewStyle().Foreground(colorGray)

	styleGray   = lipgloss.NewStyle().Foreground(colorGray)
	styleOrange = lipgloss.NewStyle().Foreground(colorOrange)
	styleYellow = lipgloss.NewStyle().Foreground(colorYellow)
	styleBold   = lipgloss.NewStyle().Bold(true)
	styleDim    = lipgloss.NewStyle().Faint(true)
	styleItalic = lipgloss.NewStyle().Italic(true)

	sectionReviewStyle = lipgloss.NewStyle().Foreground(colorCyan).Bold(true)
	sectionAuthorStyle = lipgloss.NewStyle().Foreground(colorMagenta).Bold(true)

	activeTabStyle   = lipgloss.NewStyle().Bold(true)
	inactiveTabStyle = lipgloss.NewStyle().Foreground(colorGray)
	tabBorderStyle   = lipgloss.NewStyle().Foreground(colorGray)

	// styleFooterHint styles the (now subtle, backgroundless) footer keymap
	// hints so they blend with the rest of the TUI instead of standing out
	// as a distinct bar.
	styleFooterHint = lipgloss.NewStyle().Faint(true)

	spinnerStyle = lipgloss.NewStyle().Foreground(chipFg)
	statusStyle  = lipgloss.NewStyle().Foreground(chipFg)
	errorStyle   = lipgloss.NewStyle().Foreground(colorRed).Bold(true)
)
