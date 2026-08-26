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
	colorAmber   = lipgloss.Color("214")
	colorWhite   = lipgloss.Color("15")
	colorBlue    = lipgloss.Color("12")

	styleApproved         = lipgloss.NewStyle().Foreground(colorGreen)
	styleChangesRequested = lipgloss.NewStyle().Foreground(colorRed)
	styleCommented        = lipgloss.NewStyle().Foreground(colorCyan)
	styleNotReviewed      = lipgloss.NewStyle().Foreground(colorGray)

	styleGray   = lipgloss.NewStyle().Foreground(colorGray)
	styleOrange = lipgloss.NewStyle().Foreground(colorOrange)
	styleCode   = lipgloss.NewStyle().Foreground(colorOrange) // `code` spans in comments
	styleLink   = lipgloss.NewStyle().Foreground(colorBlue)   // [text](url) markdown links
	styleYellow = lipgloss.NewStyle().Foreground(colorYellow)
	styleBold   = lipgloss.NewStyle().Bold(true)
	styleTitle  = lipgloss.NewStyle().Bold(true).Italic(true) // PR title in the detail panel
	styleDim    = lipgloss.NewStyle().Faint(true)
	styleItalic = lipgloss.NewStyle().Italic(true)

	sectionReviewStyle = lipgloss.NewStyle().Foreground(colorCyan).Bold(true)
	sectionAuthorStyle = lipgloss.NewStyle().Foreground(colorMagenta).Bold(true)

	inactiveTabStyle = lipgloss.NewStyle().Foreground(colorGray)
	tabBorderStyle   = lipgloss.NewStyle().Foreground(colorGray)

	// styleFooterHint styles the (now subtle, backgroundless) footer keymap
	// hints so they blend with the rest of the TUI instead of standing out
	// as a distinct bar.
	styleFooterHint = lipgloss.NewStyle().Faint(true)

	spinnerStyle = lipgloss.NewStyle().Foreground(chipFg)
	statusStyle  = lipgloss.NewStyle().Foreground(chipFg)
	errorStyle   = lipgloss.NewStyle().Foreground(colorRed).Bold(true)

	// styleUpdateHint styles the footer's "update available" nudge — the accent
	// color (not faint like the other footer hints) so it stands out, without
	// the alarm of an error.
	styleUpdateHint = lipgloss.NewStyle().Foreground(chipFg)
)

// bucketColors is the accent color for each tab/bucket, indexed by tab
// (tabOutstanding/tabNew/tabDone/tabIgnored). It's the single source of truth
// for the per-bucket color used by the list cursor bar, the selected tab
// label, and the telegraphed toggle animation, so all three always agree:
// Outstanding=orange, New=white, Done=green, Ignored=red.
var bucketColors = [4]lipgloss.Color{
	tabOutstanding: colorOrange,
	tabNew:         colorWhite,
	tabDone:        colorGreen,
	tabIgnored:     colorRed,
}

// bucketColor returns the accent color for a tab, falling back to the
// Outstanding orange for any out-of-range index.
func bucketColor(tab int) lipgloss.Color {
	if tab < 0 || tab >= len(bucketColors) {
		return colorOrange
	}
	return bucketColors[tab]
}

// Review styling — the colors and glyphs used to render review states in the
// detail pane's Review Status section and the compact per-PR review-icon
// sequence in the list. These are the built-in defaults; each can be overridden
// from the config file (see config.go / Config.apply). They're read at render
// time (not baked into prebuilt lipgloss styles), so applying config once
// before the program starts is enough for the overrides to take effect.
var (
	reviewTrustedColor = colorAmber // codeowner / trusted-reviewer approval
	reviewRegularColor = colorGreen // a valid approval, but not from the trusted-reviewer team
	reviewChangesColor = colorRed   // changes requested

	reviewTrustedGlyph = "⭑" // U+2B51 Black Small Star
	reviewRegularGlyph = "✓" // U+2713 Check Mark
	reviewChangesGlyph = "✗" // U+2717 Ballot X
)
