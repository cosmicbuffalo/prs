package main

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// maxDetailTextRunes caps how long a single detail entry's text can be
// before it's truncated with a trailing "…". Comment/commit lines now wrap
// across multiple rows (rather than being force-fit onto one truncated
// line), so this is just a generous backstop against pathologically long
// bot comments, not the primary length control.
const maxDetailTextRunes = 2000

// maxRecentComments caps how many of a PR's most recent comments/reviews are
// shown in the detail pane's Comments section; older ones are summarized
// with a single "(not showing N older comments)" note instead.
const maxRecentComments = 10

// Column layout for the Comments/Commits sections in the detail pane: a
// fixed-width timestamp column, a gap, then a content column that word-wraps
// (continuation lines re-align under the content column's start).
const (
	detailRowIndent   = "  "
	timestampColWidth = 14
	detailColumnGap   = 2
)

// columnGutter is the fixed-width blank gap between the list and detail
// columns in the body layout.
const columnGutter = 2

// leftMargin is the fixed-width blank gap to the left of the list column
// (and, for visual alignment, the header/tab bar above it).
const leftMargin = 2

// leftMarginStr returns leftMargin columns of blank space.
func leftMarginStr() string {
	return strings.Repeat(" ", leftMargin)
}

// highlightBarWidth is the width of the vertical highlight bar column to the
// left of every list entry, spanning the full height of the currently-
// selected PR entry (all 3 of its lines, not just the title line). This is
// the only cursor indicator (there used to also be a separate "●" dot
// column, removed since the bar alone conveys the selection clearly).
const highlightBarWidth = 2

// highlightBar renders the vertical highlight bar for one list line: a "▊" (a
// partial block glyph that reads as ~75% of a full cell's width — thick, but
// not a solid full-width block) in the given color followed by a blank space
// when this line belongs to the selected entry, or blank space of the same
// total width otherwise. The color is the current bucket's accent (see
// bucketColor), so the cursor reads green in Done, red in Ignored, etc.
func highlightBar(selected bool, color lipgloss.Color) string {
	if selected {
		return lipgloss.NewStyle().Foreground(color).Render("▊") + " "
	}
	return strings.Repeat(" ", highlightBarWidth)
}

// transitionBar renders the cursor bar recolored for an in-flight toggle, in
// the destination bucket's accent color (green heading to Done, red to
// Ignored, orange/white heading back out).
func transitionBar(destTab int) string {
	return lipgloss.NewStyle().Foreground(bucketColor(destTab)).Render("▊") + " "
}

// View renders the full TUI: a blank top margin, header, tab bar, a blank
// spacer, body (spinner or two-column list+detail), a one-line status area,
// and the footer hint line.
func (m Model) View() string {
	if m.width <= 0 || m.height <= 0 {
		return "Initializing…"
	}

	const topMargin = ""
	const tabBarSpacer = ""

	header := m.renderHeader()
	tabBar := m.renderTabBar()
	status := m.renderStatus()
	footer := renderFooter(m.width)
	bodyHeight := m.bodyHeight()

	var body string
	if m.loading && !m.hasData {
		// Nothing to show yet (cold start, no cache) — the full spinner
		// screen. Once there's real (possibly cached) data on screen, a
		// background refresh keeps showing it instead of hiding it behind
		// this — see renderStatus for the small "Refreshing..." indicator.
		body = m.renderLoading(bodyHeight)
	} else {
		body = m.renderBody(bodyHeight)
	}

	// The help overlay floats on top of the current body (list/detail stay
	// visible behind it) rather than replacing it.
	if m.showHelp {
		body = overlayBox(body, helpBox(), m.width, bodyHeight)
	}

	full := lipgloss.JoinVertical(lipgloss.Left, topMargin, header, tabBar, tabBarSpacer, body, status, footer)

	// Final backstop: never emit more rows than the terminal has. Each panel
	// already sizes itself to fit (see bodyHeight/renderDetailBox), so this
	// only bites on a terminal too short to hold even the fixed chrome — in
	// which case rendering the full frame would scroll the top off-screen.
	// Clamp rather than let the frame overflow.
	if lines := strings.Split(full, "\n"); len(lines) > m.height {
		full = strings.Join(lines[:m.height], "\n")
	}
	return full
}

func (m Model) renderHeader() string {
	repo := m.repo
	if repo == "" {
		repo = "…"
	}
	user := m.user
	if user == "" {
		user = "…"
	}
	return leftMarginStr() + styleBold.Render(fmt.Sprintf("Repo: %s (%s)", repo, user))
}

// renderTabBar draws Outstanding/New/Done as a real tabline: the active tab
// gets a raised box whose bottom edge is left open (blending into the body
// below it), while inactive tabs sit flush on the baseline rule. The rule
// extends to fill the rest of the terminal width.
// tabDef pairs a tab's rendered label with its Model.items/cursors index.
// highlight, when non-nil, is the color the label should be drawn in — set
// on the destination tab of an in-flight telegraphed toggle so it flashes as
// the PR heads toward it.
type tabDef struct {
	label     string
	idx       int
	highlight *lipgloss.Color
}

// tabGapWidth is the blank/rule gap rendered between adjacent tabs.
const tabGapWidth = 2

// tabDefs returns the 4 tabs' labels (which embed each tab's live item
// count, so their widths are dynamic) paired with their tab index. Shared
// by renderTabBar (to render them) and tabBoundaries (to hit-test mouse
// clicks against them), so the two can never drift out of sync.
func (m Model) tabDefs() []tabDef {
	counts := [4]int{
		len(m.items[tabOutstanding]),
		len(m.items[tabNew]),
		len(m.items[tabDone]),
		len(m.items[tabIgnored]),
	}

	// During a telegraphed toggle, flash the destination tab's label (from
	// phaseTab on) and show its count already incremented (from phaseCount
	// on) — before the PR has actually moved there.
	destTab := -1
	var destColor lipgloss.Color
	if m.transition != nil {
		destTab = m.transition.destTab
		destColor = bucketColor(destTab)
		if m.transition.phase >= phaseCount {
			counts[destTab]++
		}
	}

	labels := [4]string{
		fmt.Sprintf(" Outstanding (%d) ", counts[tabOutstanding]),
		fmt.Sprintf(" New (%d) ", counts[tabNew]),
		fmt.Sprintf(" Done (%d) ", counts[tabDone]),
		fmt.Sprintf(" Ignored (%d) ", counts[tabIgnored]),
	}
	idxs := [4]int{tabOutstanding, tabNew, tabDone, tabIgnored}

	defs := make([]tabDef, 4)
	for i := 0; i < 4; i++ {
		d := tabDef{label: labels[i], idx: idxs[i]}
		if idxs[i] == destTab && m.transition != nil && m.transition.phase >= phaseTab {
			c := destColor
			d.highlight = &c
		}
		defs[i] = d
	}
	return defs
}

// tabBoundaries returns, for each tab in tabDefs() order, the half-open
// column range [startCol, endCol) it occupies in the tab bar (including its
// left margin offset and border characters) — used to hit-test mouse clicks.
func (m Model) tabBoundaries() []struct{ start, end, idx int } {
	tabs := m.tabDefs()
	bounds := make([]struct{ start, end, idx int }, len(tabs))
	col := leftMargin
	for i, t := range tabs {
		if i > 0 {
			col += tabGapWidth
		}
		w := lipgloss.Width(t.label) + 2 // +2 for the box's left/right border chars
		bounds[i] = struct{ start, end, idx int }{col, col + w, t.idx}
		col += w
	}
	return bounds
}

func (m Model) renderTabBar() string {
	tabs := m.tabDefs()

	const gapWidth = tabGapWidth
	blankGap := strings.Repeat(" ", gapWidth)
	ruleGap := tabBorderStyle.Render(strings.Repeat("─", gapWidth))

	margin := leftMarginStr()
	top, mid, bottom := margin, margin, margin
	for i, t := range tabs {
		tTop, tMid, tBottom := renderTab(t.label, m.activeTab == t.idx, bucketColor(t.idx), t.highlight)
		if i > 0 {
			top += blankGap
			mid += blankGap
			// The bottom row is the baseline rule, so its gap is filled with
			// the same rule character rather than left blank, keeping the
			// baseline visually continuous between tabs.
			bottom += ruleGap
		}
		top += tTop
		mid += tMid
		bottom += tBottom
	}

	if pad := m.width - lipgloss.Width(bottom); pad > 0 {
		bottom += tabBorderStyle.Render(strings.Repeat("─", pad))
	}

	return top + "\n" + mid + "\n" + bottom
}

// renderTab renders one tab's three rows. An active tab is a raised box
// (rounded top corners); its bottom row is open in the middle (blending into
// the content below) but its two side edges curve down into the baseline
// rule ("╯"/"╰") so the tab reads as connected to the line beneath it,
// rather than floating above a gap. An inactive tab is plain text sitting
// flush on the baseline rule.
func renderTab(label string, active bool, activeColor lipgloss.Color, highlight *lipgloss.Color) (top, mid, bottom string) {
	width := lipgloss.Width(label)
	border := tabBorderStyle

	// The label text style: a transition-highlight color (bold) takes
	// precedence; otherwise the active tab is drawn in its own bucket's accent
	// color (bold) and inactive tabs stay gray.
	labelStyle := inactiveTabStyle
	if active {
		labelStyle = lipgloss.NewStyle().Foreground(activeColor).Bold(true)
	}
	if highlight != nil {
		labelStyle = lipgloss.NewStyle().Foreground(*highlight).Bold(true)
	}

	if active {
		top = border.Render("╭" + strings.Repeat("─", width) + "╮")
		mid = border.Render("│") + labelStyle.Render(label) + border.Render("│")
		bottom = border.Render("╯") + strings.Repeat(" ", width) + border.Render("╰")
		return
	}
	top = strings.Repeat(" ", width+2)
	mid = " " + labelStyle.Render(label) + " "
	bottom = border.Render(strings.Repeat("─", width+2))
	return
}

func (m Model) renderStatus() string {
	if m.loading && m.hasData {
		return leftMarginStr() + fmt.Sprintf("%s Refreshing...", m.spinner.View())
	}
	if m.statusMsg != "" {
		return statusStyle.Render(m.statusMsg)
	}
	if m.err != nil {
		return errorStyle.Render(fmt.Sprintf("Error: %v", m.err))
	}
	return ""
}

// helpBox builds the full keymap reference as a bordered box — the "floating"
// window opened with "?" (composited onto the body by overlayBox). The footer
// only carries a handful of essential hints; everything else lives here, with a
// grayed-out "Esc/q to close" hint centered along the bottom.
func helpBox() string {
	rows := [][2]string{
		{"↓ ↑ / j k", "move cursor"},
		{"← → / h l", "switch tab"},
		{"Enter", "toggle done"},
		{"i", "toggle ignore"},
		{"o", "copy PR link"},
		{"v", "toggle layout"},
		{"^d / ^u", "scroll detail"},
		{"r", "refresh"},
		{"?", "toggle help"},
		{"q", "quit"},
	}

	keyW := 0
	for _, r := range rows {
		if w := lipgloss.Width(r[0]); w > keyW {
			keyW = w
		}
	}

	var rowLines []string
	for _, r := range rows {
		rowLines = append(rowLines, styleYellow.Render(padRight(r[0], keyW))+"   "+r[1])
	}

	// Center the title and the dismiss hint over the widest shortcut row.
	contentW := 0
	for _, l := range rowLines {
		if w := lipgloss.Width(l); w > contentW {
			contentW = w
		}
	}
	title := lipgloss.PlaceHorizontal(contentW, lipgloss.Center, styleBold.Render("Keyboard shortcuts"))
	hint := lipgloss.PlaceHorizontal(contentW, lipgloss.Center, styleGray.Render("Esc/q to close"))

	lines := append([]string{title, ""}, rowLines...)
	lines = append(lines, "", hint)

	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(colorGray).
		Padding(1, 3).
		Render(strings.Join(lines, "\n"))
}

// overlayBox composites box centered on top of base (a width×height rendered
// area), so the box appears to float over the existing content instead of
// replacing it. It's ANSI-aware: on each row the box occupies, the base cells
// to the left and right of the box are preserved (with a reset inserted around
// the box so neither side's styling bleeds into the other).
func overlayBox(base, box string, width, height int) string {
	baseLines := strings.Split(base, "\n")
	for len(baseLines) < height {
		baseLines = append(baseLines, "")
	}

	boxLines := strings.Split(box, "\n")
	boxW := 0
	for _, l := range boxLines {
		if w := ansi.StringWidth(l); w > boxW {
			boxW = w
		}
	}

	top := (height - len(boxLines)) / 2
	if top < 0 {
		top = 0
	}
	left := (width - boxW) / 2
	if left < 0 {
		left = 0
	}

	for i, bl := range boxLines {
		row := top + i
		if row >= len(baseLines) {
			break
		}
		baseLine := baseLines[row]

		leftPart := ansi.Truncate(baseLine, left, "")
		if w := ansi.StringWidth(leftPart); w < left {
			leftPart += strings.Repeat(" ", left-w)
		}
		if w := ansi.StringWidth(bl); w < boxW {
			bl += strings.Repeat(" ", boxW-w)
		}
		rightPart := ansi.TruncateLeft(baseLine, left+boxW, "")

		baseLines[row] = leftPart + "\x1b[0m" + bl + "\x1b[0m" + rightPart
	}

	return strings.Join(baseLines, "\n")
}

func (m Model) renderLoading(height int) string {
	repo := m.repo
	if repo == "" {
		repo = "current repo"
	}
	text := leftMarginStr() + fmt.Sprintf("%s Fetching PRs for %s...", m.spinner.View(), repo)
	return lipgloss.NewStyle().Height(height).Render(text)
}

// columnWidths splits the terminal width between the PR list (left) and the
// detail pane (right), reserving leftMargin columns before the list and
// columnGutter columns of blank space between the two columns, so
// leftMargin+left+columnGutter+right always equals m.width.
func (m Model) columnWidths() (left, right int) {
	avail := m.width - leftMargin - columnGutter
	if avail < 0 {
		avail = 0
	}
	left = avail * 2 / 5
	if left < 28 {
		left = 28
	}
	if left > avail-20 {
		left = avail - 20
	}
	if left < 0 {
		left = 0
	}
	right = avail - left
	if right < 0 {
		right = 0
	}
	return left, right
}

// Fixed row offsets in the rendered View(), used for mouse click
// hit-testing: row 0 is the blank top margin, row 1 is the header, rows 2-4
// are the 3-line tab bar, row 5 is the blank spacer, and the body starts at
// row 6. These never shift (margin/header/spacer are always exactly 1 line,
// the tab bar is always exactly 3), so hard-coding them here is safe.
const (
	rowTabBarTop    = 2
	rowTabBarBottom = 4
	rowBodyStart    = 6
)

// overListPanel reports whether screen position (x, y) falls within the list
// panel rather than the detail panel — used to route mouse wheel scrolling to
// whichever panel the pointer is over. In horizontal layout the split is by
// column (list on the left); in vertical layout it's by row (list on top).
func (m Model) overListPanel(x, y int) bool {
	if m.layout == layoutVertical {
		return y < rowBodyStart+m.listViewportHeight()
	}
	leftWidth, _ := m.columnWidths()
	return x < leftMargin+leftWidth
}

// hitTestTab returns the tab index at screen position (x, y), if any.
func (m Model) hitTestTab(x, y int) (int, bool) {
	if y < rowTabBarTop || y > rowTabBarBottom {
		return 0, false
	}
	for _, b := range m.tabBoundaries() {
		if x >= b.start && x < b.end {
			return b.idx, true
		}
	}
	return 0, false
}

// hitTestListItem returns the index (into the active tab's item slice) of
// the list entry at screen position (x, y), if any. Because entries are
// variable-height, it walks their cumulative row counts (from the first
// visible entry) to find which one contains the clicked row.
func (m Model) hitTestListItem(x, y int) (int, bool) {
	width := m.listContentWidth()
	if x < leftMargin || x >= leftMargin+width {
		return 0, false
	}
	// In vertical layout the list only occupies the top half of the body;
	// clicks below it belong to the detail panel, not the list.
	if m.layout == layoutVertical && y >= rowBodyStart+m.listViewportHeight() {
		return 0, false
	}

	row := y - rowBodyStart - 1 // -1 for the reserved top indicator line
	if row < 0 {
		return 0, false
	}

	items, start, end := m.listWindow(width, m.listViewportHeight())
	if len(items) == 0 {
		return 0, false
	}
	counts := m.entryLineCounts(m.activeTab, width)
	acc := 0
	for i := start; i < end; i++ {
		if row < acc+counts[i] {
			return i, true
		}
		acc += counts[i]
	}
	return 0, false
}

// bodyHeight returns how many rows are available for the body area (the
// list+detail panels, or the loading spinner) given the current terminal
// height and the fixed-size chrome around it (margin/header/tab bar/spacer/
// status/footer). Shared by View() (to size the body) and mouse click
// hit-testing (to know where the body starts), so the two can't drift.
func (m Model) bodyHeight() int {
	header := m.renderHeader()
	tabBar := m.renderTabBar()
	status := m.renderStatus()
	footer := renderFooter(m.width)

	fixed := 1 + lipgloss.Height(header) + lipgloss.Height(tabBar) + 1 +
		lipgloss.Height(status) + lipgloss.Height(footer)
	h := m.height - fixed
	if h < 1 {
		h = 1
	}
	return h
}

func (m Model) renderBody(height int) string {
	if m.layout == layoutVertical {
		return m.renderBodyVertical(height)
	}
	return m.renderBodyHorizontal(height)
}

// renderBodyHorizontal is the default side-by-side arrangement: PR list in the
// left column, detail in the right column.
func (m Model) renderBodyHorizontal(height int) string {
	leftWidth, rightWidth := m.columnWidths()

	margin := lipgloss.NewStyle().Width(leftMargin).Height(height).Render("")
	listBox := lipgloss.NewStyle().Width(leftWidth).Height(height).Render(m.renderList(leftWidth, height))
	gutter := lipgloss.NewStyle().Width(columnGutter).Height(height).Render("")
	detailBox := lipgloss.NewStyle().Width(rightWidth).Height(height).Render(m.renderDetail(rightWidth, height, m.detailScroll))

	return lipgloss.JoinHorizontal(lipgloss.Top, margin, listBox, gutter, detailBox)
}

// renderBodyVertical stacks the panels: PR list across the full width on the
// top half, detail across the full width on the bottom half.
func (m Model) renderBodyVertical(height int) string {
	topH := m.listViewportHeight()
	if topH > height {
		topH = height
	}
	botH := height - topH
	if botH < 0 {
		botH = 0
	}

	innerWidth := m.width - leftMargin
	if innerWidth < 1 {
		innerWidth = 1
	}
	marginStyle := lipgloss.NewStyle().Width(leftMargin)

	listMargin := marginStyle.Height(topH).Render("")
	listBox := lipgloss.NewStyle().Width(innerWidth).Height(topH).Render(m.renderList(innerWidth, topH))
	top := lipgloss.JoinHorizontal(lipgloss.Top, listMargin, listBox)

	detailMargin := marginStyle.Height(botH).Render("")
	detailBox := lipgloss.NewStyle().Width(innerWidth).Height(botH).Render(m.renderDetail(innerWidth, botH, m.detailScroll))
	bottom := lipgloss.JoinHorizontal(lipgloss.Top, detailMargin, detailBox)

	return lipgloss.JoinVertical(lipgloss.Left, top, bottom)
}

// listContentWidth is the column width available to each list entry — the left
// column in horizontal layout, or the full width (minus the left margin) in
// vertical layout. Shared by the renderer and the scroll-window math so wrap
// depth (and thus entry heights) are measured against the same width the list
// is actually drawn at.
func (m Model) listContentWidth() int {
	if m.layout == layoutVertical {
		w := m.width - leftMargin
		if w < 1 {
			w = 1
		}
		return w
	}
	left, _ := m.columnWidths()
	return left
}

// listViewportHeight is the number of body rows the list panel occupies: the
// full body height in horizontal layout, or the top half in vertical layout
// (matching renderBodyVertical's split).
func (m Model) listViewportHeight() int {
	if m.layout == layoutVertical {
		h := m.bodyHeight() / 2
		if h < 1 {
			h = 1
		}
		return h
	}
	return m.bodyHeight()
}

// entryLineCounts returns, for each item in tab, the number of terminal rows
// its rendered block occupies at the given content width — its content lines
// (title + possibly-wrapped bullet + summary) plus the one trailing separator
// row. The scroll-window math (listWindow / clampListScroll) and the renderer
// share this so they always agree on where variable-height entries begin and
// end.
func (m Model) entryLineCounts(tab, width int) []int {
	items := m.items[tab]
	counts := make([]int, len(items))
	for i, it := range items {
		counts[i] = len(m.renderListEntry(it, false, width)) + 1
	}
	return counts
}

// renderList renders the current tab's PR list, windowed (manual scroll
// offset) so the selected item is always visible when the list is taller
// than the available height. Each item renders as a fixed listEntryLines-line
// block, so the windowing math operates in item-count units and only
// converts to line-count when actually joining the rendered lines.
func (m Model) renderList(width, height int) string {
	items, start, end := m.listWindow(width, height)
	if len(items) == 0 {
		return styleDim.Render("Nothing here.")
	}
	cursor := m.cursors[m.activeTab]

	// A reserved line at the very top always exists (blank, or the "↑ (N
	// more)" indicator) so its appearance/disappearance never shifts the
	// rest of the list up or down.
	var lines []string
	if start > 0 {
		lines = append(lines, centerGray(fmt.Sprintf("↑ (%d more)", start), width))
	} else {
		lines = append(lines, "")
	}

	for i := start; i < end; i++ {
		lines = append(lines, m.renderListEntry(items[i], i == cursor, width)...)
		if i == end-1 && end < len(items) {
			// Reuse the trailing blank separator line as the down
			// indicator instead of adding a new row.
			lines = append(lines, centerGray(fmt.Sprintf("↓ (%d more)", len(items)-end), width))
		} else {
			lines = append(lines, "")
		}
	}
	return strings.Join(lines, "\n")
}

// centerGray centers s within width (padding with blank space on both
// sides, extra space on the right if odd) and renders it dim gray — used
// for the list panel's scroll indicators.
func centerGray(s string, width int) string {
	pad := width - lipgloss.Width(s)
	if pad < 0 {
		pad = 0
	}
	left := pad / 2
	right := pad - left
	return strings.Repeat(" ", left) + styleGray.Render(s) + strings.Repeat(" ", right)
}

// listWindow computes the current tab's item slice and the [start, end) range
// of it that's actually visible, packing variable-height entries into the
// available rows from the tab's persisted listScroll offset (kept cursor-
// visible by clampListScroll in update.go). One row is always reserved at the
// top for the "↑ (N more)" indicator. At least the first entry is always
// included even if it's taller than the viewport. Shared by renderList (to
// render exactly that window) and mouse hit-testing (to map a clicked row back
// to an item index) so the two can never drift out of sync.
func (m Model) listWindow(width, height int) (items []Item, start, end int) {
	items = m.items[m.activeTab]
	if len(items) == 0 {
		return items, 0, 0
	}

	counts := m.entryLineCounts(m.activeTab, width)
	avail := height - 1 // one row reserved for the top "↑ (N more)" indicator
	if avail < 1 {
		avail = 1
	}

	start = m.listScroll[m.activeTab]
	if start < 0 {
		start = 0
	}
	if start > len(items)-1 {
		start = len(items) - 1
	}

	used := 0
	end = start
	for end < len(items) {
		if used+counts[end] > avail && end > start {
			break
		}
		used += counts[end]
		end++
	}
	return items, start, end
}

// renderListEntry renders one PR as a 3-line block, each line prefixed with
// the dedicated cursor column: the "(<number>) <title>" line (the only one
// that can show the "●" cursor dot), a section/badge bullet line, and a
// latest-activity summary bullet line.
func (m Model) renderListEntry(item Item, selected bool, width int) []string {
	contentWidth := width - highlightBarWidth
	if contentWidth < 1 {
		contentWidth = 1
	}
	titleLine := renderEntryTitleLine(item, contentWidth)
	if selected {
		titleLine = styleItalic.Render(titleLine)
	}
	// The cursor bar takes the active bucket's accent color (green in Done,
	// red in Ignored, orange in Outstanding, white in New).
	bar := highlightBar(selected, bucketColor(m.activeTab))
	// A PR mid-telegraphed-toggle shows its cursor bar recolored to its
	// destination bucket regardless of where the cursor actually is, so the
	// in-flight move is visible on its own row.
	if m.transition != nil && m.transition.key == item.Key {
		bar = transitionBar(m.transition.destTab)
	}
	// Hard-cap each line to the column's display width so a line with
	// wide runes (emoji/CJK) that rune-count truncation under-cuts can't
	// overflow, wrap to column 0, and break the alignment of the cursor bar.
	// MaxWidth is display-width-aware and ANSI-safe, and truncates the end,
	// leaving the leading bar intact.
	cap := func(line string) string {
		return lipgloss.NewStyle().MaxWidth(contentWidth).Render(line)
	}

	// The bullet line can wrap onto multiple rows in a narrow column (see
	// renderEntryBulletLines) — this is what makes list entries variable-
	// height. The title and summary are always exactly one row each. The
	// cursor bar is repeated down every row of the entry, wrapped bullet rows
	// included, so the selection reads as one contiguous block.
	out := []string{bar + cap(titleLine)}
	for _, bl := range renderEntryBulletLines(item, contentWidth) {
		out = append(out, bar+cap(bl))
	}
	out = append(out, bar+cap(renderEntrySummaryLine(item, contentWidth)))
	return out
}

// renderEntryTitleLine renders the "(#<number>) <title>" line, right-padded
// to width. No background/highlight styling — the cursor's position is
// conveyed entirely by the "●" in the cursor column, not by row highlighting.
func renderEntryTitleLine(item Item, width int) string {
	numberPart := fmt.Sprintf("(#%d)", item.Number)
	fixed := lipgloss.Width(numberPart) + 1
	budget := width - fixed
	if budget < 3 {
		budget = 3
	}
	title := truncateRunes(item.Title, budget)

	row := numberPart + " " + title
	if pad := width - lipgloss.Width(row); pad > 0 {
		row += strings.Repeat(" ", pad)
	}
	return row
}

// entryBulletPrefix is the leading indent used for both bullet lines under a
// list entry's title line.
const entryBulletPrefix = "  - "

// renderEntryBulletLines renders the second list-entry "bullet" as one or more
// rows: "NEW" (yellow) for PRs the user hasn't touched, "AUTHOR" for authored
// items, or "REVIEWER " + the bracketed badge label for reviewing items,
// followed by the "(updated X ago)" recency suffix and the review-icon
// sequence. Unlike before, nothing is dropped in a narrow column — the whole
// bullet word-wraps instead, with continuation rows hanging-indented to align
// under where the content begins after "- " (not back at the cursor bar).
func renderEntryBulletLines(item Item, width int) []string {
	// A PR whose per-PR data failed to load shows a red error marker in place
	// of the usual section/badge line — the normal "updated X ago"/icons don't
	// apply since we never got the data to compute them.
	if item.FetchError != "" {
		return wrapWithHangingIndent(entryBulletPrefix, errorStyle.Render("⚠ failed to load"), width)
	}

	// The section word plus, for reviewing items, the bracketed review-state
	// tag (e.g. "[APPROVED]").
	var labelRendered string
	switch item.Section {
	case SectionNew:
		labelRendered = styleYellow.Render("NEW")
	case SectionAuthored:
		labelRendered = sectionAuthorStyle.Render("AUTHOR")
	default:
		word := "REVIEWER"
		badgeStyle := lipgloss.NewStyle().Foreground(reviewStateColor(item.Badge))
		labelRendered = sectionReviewStyle.Render(word) + " " + badgeStyle.Render(reviewStateLabel(item.Badge))
	}

	content := labelRendered + styleDim.Render(" (updated "+relativeTime(item.TriggerDate)+")")
	if icons := renderReviewIconSequence(item.Reviewers); icons != "" {
		content += " " + icons
	}
	return wrapWithHangingIndent(entryBulletPrefix, content, width)
}

// renderEntrySummaryLine renders the third list-entry line: a dim one-line
// summary of the single most recent qualifying commit/activity.
func renderEntrySummaryLine(item Item, width int) string {
	budget := width - len(entryBulletPrefix)
	if budget < 1 {
		budget = 1
	}

	if item.FetchError != "" {
		return styleDim.Render(entryBulletPrefix + truncateRunes("press r to retry", budget))
	}

	if item.Section == SectionNew && item.Author != "" {
		const prefix = "opened by "
		authorBudget := budget - len(prefix)
		if authorBudget < 1 {
			authorBudget = 1
		}
		author := truncateRunes(item.Author, authorBudget)
		return entryBulletPrefix + styleDim.Render(prefix) + usernameColored(author)
	}
	return styleDim.Render(entryBulletPrefix + truncateRunes(item.LatestSummary, budget))
}

// renderReviewIconSequence renders a compact, space-separated sequence of
// review icons — one per reviewer, showing only their latest valid (i.e.
// current/non-Superseded) state, in arrival order. Coloring matches the
// detail panel's Review Status section exactly: red ✗ for a current change
// request, green ✓ for a current trusted-reviewer-satisfying approval,
// yellow ✓ for a current approval that isn't from the required
// trusted-reviewer team — yellow rather than gray so it's not confusable
// with a superseded/invalidated entry.
func renderReviewIconSequence(events []ReviewEvent) string {
	var parts []string
	for _, ev := range events {
		if ev.Superseded {
			continue
		}
		switch {
		case ev.State == ReviewChangesRequested:
			parts = append(parts, styleChangesRequested.Render("✗"))
		case ev.IsCodeowner:
			parts = append(parts, styleApproved.Render("✓"))
		default:
			parts = append(parts, styleWeakApproved.Render("✓"))
		}
	}
	return strings.Join(parts, " ")
}

// renderDetail renders the detail pane for the currently-selected item as a
// hand-drawn rounded box with the PR number centered in the top border.
// Content is built as a plain string and truncated/cut off to fit the
// available width/height; there's no scroll keybinding wired to it per spec
// (arrows only move the list cursor).
func (m Model) renderDetail(width, height, scroll int) string {
	item, ok := m.selectedItem()
	if !ok {
		return styleDim.Render("No PR selected.")
	}
	if width < 6 {
		width = 6
	}

	innerWidth := width - 4 // 2 for "│ " and 2 for " │"
	if innerWidth < 1 {
		innerWidth = 1
	}

	maxInterior := height - 2
	if maxInterior < 0 {
		maxInterior = 0
	}

	// A PR whose per-PR data failed to load: show what we have (URL, title)
	// plus the error and a retry hint, in place of the usual detail.
	if item.FetchError != "" {
		var lines []string
		lines = append(lines, styleOrange.Render(truncateRunes(item.URL, innerWidth)))
		for _, l := range strings.Split(lipgloss.NewStyle().Width(innerWidth).Render(item.Title), "\n") {
			lines = append(lines, styleTitle.Render(l))
		}
		lines = append(lines, "")
		lines = append(lines, errorStyle.Render(truncateRunes("⚠ Failed to load this PR's details", innerWidth)))
		lines = append(lines, "")
		for _, l := range strings.Split(lipgloss.NewStyle().Width(innerWidth).Render(item.FetchError), "\n") {
			lines = append(lines, styleGray.Render(l))
		}
		lines = append(lines, "")
		lines = append(lines, styleDim.Render(truncateRunes("Press r to refresh and try again.", innerWidth)))
		return renderDetailBox(item.Number, width, innerWidth, maxInterior, lines)
	}

	// Sticky header: URL, wrapped title, baseline, Review Status (if any),
	// and a blank spacer — always shown at the top of the panel regardless
	// of scroll position. Only Comments/Commits below it actually scroll.
	var header []string
	header = append(header, styleOrange.Render(truncateRunes(item.URL, innerWidth)))
	titleLines := strings.Split(lipgloss.NewStyle().Width(innerWidth).Render(item.Title), "\n")
	for _, l := range titleLines {
		header = append(header, styleTitle.Render(l))
	}
	// For New items, Baseline is just the PR's creation date — the same
	// thing the PR Details section's "<author> opened X ago" line already
	// says, so skip this otherwise-redundant line for that section only.
	if item.BaselineLabel != "opened" {
		baselineText := item.BaselineLabel + ": " + relativeTime(item.Baseline)
		header = append(header, styleGray.Render(truncateRunes(baselineText, innerWidth)))
	}
	header = append(header, "")
	header = append(header, m.renderSummaryRow(item, innerWidth)...)
	header = append(header, "")

	var scrollable []string
	shown := item.Detail
	var hiddenNote string
	if len(shown) > maxRecentComments {
		hidden := len(shown) - maxRecentComments
		shown = shown[hidden:] // Detail is sorted ascending; the tail is the most recent.
		hiddenNote = fmt.Sprintf(" (not showing %d older comments)", hidden)
	}
	// Shared across the Comments and Commits sections so both align their
	// content to the same column, sized to the widest timestamp actually shown.
	tsColWidth := maxTimestampWidth(shown, item.Commits)

	scrollable = append(scrollable, renderSectionHeader("Comments", hiddenNote, innerWidth))
	if len(shown) == 0 {
		// A NEW PR whose per-PR data is still being lazily fetched shows a
		// loading note rather than the "none" message (see ensureNewDetail).
		empty := "No comments/reviews."
		if item.Section == SectionNew && m.newDetailFetching[item.Key] {
			empty = "Loading…"
		}
		scrollable = append(scrollable, styleDim.Render(truncateRunes(empty, innerWidth)))
	}
	for _, d := range shown {
		scrollable = append(scrollable, renderTimestampContentLine(relativeTime(d.Date), renderDetailContent(d), tsColWidth, innerWidth)...)
	}

	if len(item.Commits) > 0 {
		scrollable = append(scrollable, "")
		commitsNote := fmt.Sprintf(" (%d total commit(s), %d since last activity)", item.TotalCommits, len(item.Commits))
		scrollable = append(scrollable, renderSectionHeader("Commits", commitsNote, innerWidth))
		for _, c := range item.Commits {
			sha := c.SHA
			if len(sha) > 7 {
				sha = sha[:7]
			}
			msg := strings.SplitN(c.Message, "\n", 2)[0]
			commitContent := styleYellow.Render(sha) + "  " + usernameTag(c.AuthorLogin) + msg
			scrollable = append(scrollable, renderTimestampContentLine(relativeTime(c.CommitterDate), commitContent, tsColWidth, innerWidth)...)
		}
	}
	scrollable = append(scrollable, "")

	// In vertical layout the whole panel scrolls together (no sticky header),
	// since the shorter panel makes a pinned header eat too much of the
	// visible area; in horizontal layout the header (URL/title/summary) stays
	// pinned and only the Comments/Commits below it scroll.
	if m.layout == layoutVertical {
		full := append(append([]string{}, header...), scrollable...)
		maxScroll := len(full) - maxInterior
		if maxScroll < 0 {
			maxScroll = 0
		}
		if scroll > maxScroll {
			scroll = maxScroll
		}
		if scroll < 0 {
			scroll = 0
		}
		end := scroll + maxInterior
		if end > len(full) {
			end = len(full)
		}
		return renderDetailBox(item.Number, width, innerWidth, maxInterior, full[scroll:end])
	}

	available := maxInterior - len(header)
	if available < 0 {
		available = 0
	}
	maxScroll := len(scrollable) - available
	if maxScroll < 0 {
		maxScroll = 0
	}
	if scroll > maxScroll {
		scroll = maxScroll
	}
	if scroll < 0 {
		scroll = 0
	}
	end := scroll + available
	if end > len(scrollable) {
		end = len(scrollable)
	}
	scrollable = scrollable[scroll:end]

	content := append(header, scrollable...)
	return renderDetailBox(item.Number, width, innerWidth, maxInterior, content)
}

// renderSectionHeader renders a detail-pane section header — a bold title
// (e.g. "Comments") followed by an optional gray note (e.g. " (not showing 3
// older comments)") — truncated as a whole to width. The note is truncated
// (with a trailing "…") rather than allowed to overflow, since an over-wide
// header line would wrap inside the enclosing detail box and push the panel
// past its height budget (see renderDetailBox's per-line cap for the backstop).
func renderSectionHeader(title, note string, width int) string {
	rendered := styleBold.Render(title)
	if note == "" {
		return rendered
	}
	noteBudget := width - lipgloss.Width(title)
	if noteBudget < 1 {
		return rendered
	}
	return rendered + styleGray.Render(truncateRunes(note, noteBudget))
}

// renderDetailBox draws the rounded detail box (top border with the PR number,
// each content line padded within innerWidth, bottom border), truncating
// content to maxInterior lines so it never overflows the panel height.
func renderDetailBox(number, width, innerWidth, maxInterior int, content []string) string {
	if len(content) > maxInterior {
		content = content[:maxInterior]
	}
	var b strings.Builder
	b.WriteString(renderDetailTopBorder(number, width))
	b.WriteString("\n")
	for _, line := range content {
		b.WriteString("│ ")
		// Hard-cap each line to the box's interior width before padding.
		// padVisible only pads (it assumes the line already fits); a line
		// wider than innerWidth would make the assembled "│ … │" row exceed
		// the panel width, get soft-wrapped by the enclosing lipgloss box in
		// renderBody*, and push the whole body one row past its height budget
		// — scrolling the top of the TUI off-screen. MaxWidth is display-
		// width-aware and ANSI-safe, matching the list column's own cap.
		capped := lipgloss.NewStyle().MaxWidth(innerWidth).Render(line)
		b.WriteString(padVisible(capped, innerWidth))
		b.WriteString(" │\n")
	}
	b.WriteString(renderDetailBottomBorder(width))
	return b.String()
}

// renderDetailTopBorder draws the box's top border with " #<number> "
// centered (bold) within the box's total width; any odd leftover dash goes
// on the right side.
func renderDetailTopBorder(number int, width int) string {
	totalWidth := width - 2 // excluding the two corners
	if totalWidth < 0 {
		totalWidth = 0
	}

	title := fmt.Sprintf(" #%d ", number)
	titleWidth := lipgloss.Width(title)
	if titleWidth > totalWidth {
		title = truncateRunes(title, totalWidth)
		titleWidth = lipgloss.Width(title)
	}

	leftDashes := (totalWidth - titleWidth) / 2
	rightDashes := totalWidth - titleWidth - leftDashes

	return "╭" + strings.Repeat("─", leftDashes) + styleBold.Render(title) + strings.Repeat("─", rightDashes) + "╮"
}

// renderDetailBottomBorder draws the box's plain bottom border (no title).
func renderDetailBottomBorder(width int) string {
	totalWidth := width - 2
	if totalWidth < 0 {
		totalWidth = 0
	}
	return "╰" + strings.Repeat("─", totalWidth) + "╯"
}

// padVisible pads s with trailing spaces (measured with lipgloss.Width, not
// len, since s may already contain ANSI color codes) until it's exactly
// width columns wide. Assumes s is already at most width columns wide.
func padVisible(s string, width int) string {
	if pad := width - lipgloss.Width(s); pad > 0 {
		return s + strings.Repeat(" ", pad)
	}
	return s
}

// padRight is padVisible without the "already too long" guard needed
// elsewhere — used for the fixed-width timestamp column, whose content
// (relativeTime's output) is always well under timestampColWidth.
func padRight(s string, width int) string {
	if w := lipgloss.Width(s); w < width {
		return s + strings.Repeat(" ", width-w)
	}
	return s
}

// padLeft pads s on the left (right-aligning it within width) — used for the
// Review Status section's right-hand timestamp column.
func padLeft(s string, width int) string {
	if w := lipgloss.Width(s); w < width {
		return strings.Repeat(" ", width-w) + s
	}
	return s
}

// summaryGap is the blank gutter between the PR Details and Review Status
// columns.
const summaryGap = 2

// renderSummaryRow renders the "PR Details" column and, if the PR has any
// formal reviews, the "Review Status" column to its right — side by side,
// each taking half the available width. Below a minimum width the two are
// stacked vertically instead, since squeezing both into very narrow columns
// would make each unreadable.
func (m Model) renderSummaryRow(item Item, innerWidth int) []string {
	// A NEW PR's per-PR data is fetched lazily (see ensureNewDetail); until
	// that lands its comment/participant counts (from search metadata) and its
	// reviews aren't trustworthy, so treat it as "not ready" and show loading
	// placeholders rather than stale/absent values.
	ready := item.Section != SectionNew || m.newDetailLoaded[item.Key]

	colWidth := (innerWidth - summaryGap) / 2
	if colWidth < 20 {
		// Too narrow for side-by-side; stack instead.
		var stacked []string
		stacked = append(stacked, renderPRDetailsBlock(item, innerWidth, ready)...)
		stacked = append(stacked, "")
		stacked = append(stacked, m.reviewStatusLines(item, ready, innerWidth)...)
		return stacked
	}

	prDetails := renderPRDetailsBlock(item, colWidth, ready)
	reviewStatus := m.reviewStatusLines(item, ready, colWidth)

	left := lipgloss.NewStyle().Width(colWidth).Render(strings.Join(prDetails, "\n"))
	gutter := strings.Repeat(" ", summaryGap)
	right := lipgloss.NewStyle().Width(colWidth).Render(strings.Join(reviewStatus, "\n"))
	combined := lipgloss.JoinHorizontal(lipgloss.Top, left, gutter, right)
	return strings.Split(combined, "\n")
}

// reviewStatusLines renders the "Review Status" section, which is always shown:
// the formal review timeline when the PR has reviews, a grayed-out "No reviews
// yet" when it has none, or "Loading…" for a NEW PR whose data hasn't been
// fetched yet (so an empty section doesn't misleadingly read as "no reviews"
// before we actually know).
func (m Model) reviewStatusLines(item Item, ready bool, width int) []string {
	lines := []string{styleBold.Render(truncateRunes("Review Status", width))}
	switch {
	case !ready:
		lines = append(lines, styleGray.Render(truncateRunes("Loading…", width)))
	case len(item.Reviewers) == 0:
		lines = append(lines, styleGray.Render(truncateRunes("No reviews yet", width)))
	default:
		for _, ev := range item.Reviewers {
			lines = append(lines, renderReviewStatusLine(ev, width)...)
		}
	}
	return lines
}

// renderPRDetailsBlock renders the compact "PR Details" summary: when the
// PR was opened, its diff size (green additions / red deletions), total
// commit/comment counts on one line, and the participant list — all sourced
// from GitHub's own aggregate GraphQL fields (counts) plus this tool's own
// computed, ranked participant list (see participantsOrdered in github.go),
// so they reflect the whole PR regardless of what this panel actually
// fetched/displays elsewhere.
func renderPRDetailsBlock(item Item, width int, ready bool) []string {
	openedLine := usernameColored(item.Author) + " opened " + relativeTime(item.CreatedAt)
	filesLine := fmt.Sprintf("%d files changed  ", item.ChangedFiles) +
		styleApproved.Render(fmt.Sprintf("+%d", item.Additions)) + "/" +
		styleChangesRequested.Render(fmt.Sprintf("-%d", item.Deletions))

	// The commit count and diff size come from GitHub's aggregate fields and
	// are accurate even for a not-yet-loaded NEW PR; the comment count and
	// participant list are not, so they show a "…" placeholder until the
	// per-PR data lands (see renderSummaryRow's `ready`).
	comments := "… comments"
	if ready {
		comments = fmt.Sprintf("%d comments", item.TotalComments)
	}
	commitsCommentsLine := truncateRunes(fmt.Sprintf("%d commits · %s", item.TotalCommits, comments), width)

	lines := []string{
		styleBold.Render(truncateRunes("PR Details", width)),
		openedLine,
		filesLine,
		commitsCommentsLine,
	}
	if ready {
		return append(lines, renderParticipantsLines(item, width)...)
	}
	return append(lines, styleDim.Render(truncateRunes("… participants", width)))
}

// renderParticipantsLines renders "N participants: name, name, ... (+N
// reviewers)" — every participant other than the PR author and formal
// reviewers is listed, each name colored via usernameColored, ranked by
// contribution (see participantsOrdered). The author is silently dropped
// (already named on the "opened by" line right above). Reviewers are left
// out of the name list too — they're already shown with a colored tag in
// the adjacent Review Status column, so repeating them here would just be
// visual duplication — and folded into a trailing gray "(+N reviewers)"
// note instead (or, if every participant is the author or a reviewer and
// there are no names to list at all, just "(N reviewers)" with no "+", e.g.
// "4 participants: (3 reviewers)"). If the whole thing doesn't fit on one
// line it wraps, with continuation lines indented to align under where the
// names start (see wrapWithHangingIndent) rather than restarting at the
// row's left edge — and the "(+N reviewers)" note itself is joined with a
// non-breaking space so it always wraps as a whole unit, never split
// mid-tag. Falls back to the plain "N participants" (no names) for New
// items, which have no per-PR participant list computed (see
// Item.ParticipantLogins's doc comment).
func renderParticipantsLines(item Item, width int) []string {
	if len(item.ParticipantLogins) == 0 {
		return []string{truncateRunes(fmt.Sprintf("%d participants", item.ParticipantCount), width)}
	}

	reviewerLogins := make(map[string]bool, len(item.Reviewers))
	for _, ev := range item.Reviewers {
		reviewerLogins[ev.Login] = true
	}

	var shown []string
	excluded := 0
	for _, login := range item.ParticipantLogins {
		// The author is already shown on the "opened by" line right above,
		// so silently drop them here — no separate note, unlike reviewers,
		// since there's exactly one and it's already explained.
		if login == item.Author {
			continue
		}
		if reviewerLogins[login] {
			excluded++
			continue
		}
		shown = append(shown, usernameColored(login))
	}

	// Nothing left to name once the author (and any reviewers) are removed —
	// fall back to the plain count with no colon/empty list line.
	if len(shown) == 0 && excluded == 0 {
		return []string{truncateRunes(fmt.Sprintf("%d participants", item.ParticipantCount), width)}
	}

	label := fmt.Sprintf("%d participants:", item.ParticipantCount)
	if excluded == 0 {
		return wrapBelowLabel(label, strings.Join(shown, ", "), width, participantIndent)
	}
	unit := "reviewers"
	if excluded == 1 {
		unit = "reviewer"
	}
	if len(shown) == 0 {
		// Nothing but the author and reviewers — no names to list, so skip
		// the "+" (there's nothing being added to), e.g. "4 participants:
		// (3 reviewers)" rather than "4 participants: (+3 reviewers)".
		note := styleGray.Render(fmt.Sprintf("(%d %s)", excluded, unit))
		return wrapBelowLabel(label, note, width, participantIndent)
	}
	note := styleGray.Render(fmt.Sprintf("(+%d %s)", excluded, unit))
	return wrapBelowLabel(label, strings.Join(shown, ", ")+" "+note, width, participantIndent)
}

// wrapWithHangingIndent word-wraps content to fit within width (accounting
// for prefix's width on the first line), indenting any wrapped continuation
// lines to align under where content starts on the first line — instead of
// letting them restart at the row's left edge, matching the "hanging
// indent" pattern renderTimestampContentLine uses for the Comments section.
// participantIndent is how far the participant name list is indented on the
// line(s) below the "N participants:" label.
const participantIndent = 2

// wrapBelowLabel renders label on its own line, then word-wraps content onto
// the following line(s), each indented by indent spaces. Used for the
// participant list so the names always start on the line below the count
// (indented) rather than trailing the label — reads consistently at every
// width and never wraps the first name up awkwardly against the label.
func wrapBelowLabel(label, content string, width, indent int) []string {
	lines := []string{truncateRunes(label, width)}
	contentWidth := width - indent
	if contentWidth < 1 {
		contentWidth = 1
	}
	wrapped := lipgloss.NewStyle().Width(contentWidth).Render(content)
	pad := strings.Repeat(" ", indent)
	for _, l := range strings.Split(wrapped, "\n") {
		lines = append(lines, pad+l)
	}
	return lines
}

func wrapWithHangingIndent(prefix, content string, width int) []string {
	indent := lipgloss.Width(prefix)
	contentWidth := width - indent
	if contentWidth < 1 {
		contentWidth = 1
	}
	wrapped := lipgloss.NewStyle().Width(contentWidth).Render(content)
	rawLines := strings.Split(wrapped, "\n")

	lines := make([]string, len(rawLines))
	for i, l := range rawLines {
		if i == 0 {
			lines[i] = prefix + l
			continue
		}
		lines[i] = strings.Repeat(" ", indent) + l
	}
	return lines
}

// renderReviewStatusLine renders one formal review event, in the PR's full
// timeline (nothing is hidden — see reviewEvents): a checkmark/✗, the
// reviewer's username tag, and a relative timestamp right-aligned in a
// fixed-width column at the row's right edge (so every row's timestamp
// lines up in one column, regardless of username length).
//
// Coloring:
//   - Superseded (not this reviewer's current/last formal review — whether
//     because a later review changed their stance, or just re-affirmed it)
//     is grayed out entirely: gray symbol AND gray username tag.
//   - A current ChangesRequested is red, with a colored username tag.
//   - A current Approved is green (IsCodeowner) or yellow (not — an approval
//     still counts, it's just not from the required trusted-reviewer team;
//     yellow rather than gray so it doesn't read as superseded/invalidated)
//     for the symbol, but its username tag stays colored either way, since
//     the approval itself is still currently valid.
//
// reviewEventGlyph returns the base (uncolored) symbol for a review event's
// state, used for superseded entries where both symbol and username end up
// gray regardless of which glyph it is.
func reviewEventGlyph(state ReviewState) string {
	if state == ReviewApproved {
		return "✓"
	}
	return "✗"
}

func renderReviewStatusLine(ev ReviewEvent, innerWidth int) []string {
	var symbol, usernameRendered string
	switch {
	case ev.Superseded:
		symbol = styleGray.Render(reviewEventGlyph(ev.State))
		usernameRendered = styleGray.Render(ev.Login)
	case ev.State == ReviewChangesRequested:
		symbol = styleChangesRequested.Render("✗")
		usernameRendered = usernameColored(ev.Login)
	case ev.IsCodeowner:
		symbol = styleApproved.Render("✓")
		usernameRendered = usernameColored(ev.Login)
	default:
		symbol = styleWeakApproved.Render("✓")
		usernameRendered = usernameColored(ev.Login)
	}
	left := detailRowIndent + symbol + " " + usernameRendered

	// Try to keep the "X ago" timestamp right-aligned on the same row.
	gap := innerWidth - lipgloss.Width(left) - timestampColWidth
	if gap >= 1 {
		ts := styleGray.Render(padLeft(relativeTime(ev.Date), timestampColWidth))
		return []string{left + strings.Repeat(" ", gap) + ts}
	}

	// Not enough room — drop the whole "X ago" onto its own line rather than
	// splitting it, indented to align under the username (just past the ✓/✗
	// glyph and its trailing space).
	indent := lipgloss.Width(detailRowIndent) + 2 // glyph (1) + space (1)
	return []string{
		left,
		strings.Repeat(" ", indent) + styleGray.Render(relativeTime(ev.Date)),
	}
}

// renderTimestampContentLine renders one Comments/Commits row as an
// aligned two-column line: a fixed-width timestamp column, then a
// word-wrapped content column. If content is too wide to fit in one line,
// it wraps onto additional lines, each re-indented to start at the content
// column's position (not restarting at the row's left edge). boxInnerWidth
// is the full available width inside the detail box (indent included).
func renderTimestampContentLine(ts, content string, tsColWidth, boxInnerWidth int) []string {
	// The timestamp column is padded to tsColWidth — the width of the longest
	// timestamp in this section (see renderDetail) — so every row's content
	// starts at the same column (alignment is the priority), with just
	// detailColumnGap space(s) after the longest timestamp rather than a wide
	// fixed gap. Continuation lines hang-indent to that same content start.
	indent := lipgloss.Width(detailRowIndent) + tsColWidth + detailColumnGap
	contentWidth := boxInnerWidth - indent
	if contentWidth < 1 {
		contentWidth = 1
	}

	wrapped := lipgloss.NewStyle().Width(contentWidth).Render(content)
	rawLines := strings.Split(wrapped, "\n")

	continuationIndent := strings.Repeat(" ", indent)
	lines := make([]string, len(rawLines))
	for i, l := range rawLines {
		if i == 0 {
			lines[i] = detailRowIndent + styleGray.Render(padRight(ts, tsColWidth)) + strings.Repeat(" ", detailColumnGap) + l
			continue
		}
		lines[i] = continuationIndent + l
	}
	return lines
}

// maxTimestampWidth returns the display width of the widest "X ago" timestamp
// across the given comment and commit entries — used as the shared timestamp
// column width so the Comments and Commits sections align to the same content
// column while keeping the gap as tight as the data allows.
func maxTimestampWidth(comments []DetailLine, commits []Commit) int {
	w := 0
	for _, d := range comments {
		if tw := lipgloss.Width(relativeTime(d.Date)); tw > w {
			w = tw
		}
	}
	for _, c := range commits {
		if tw := lipgloss.Width(relativeTime(c.CommitterDate)); tw > w {
			w = tw
		}
	}
	return w
}

// relativeTime formats t as a short "X ago" string (e.g. "5 minutes ago",
// "3 hours ago", "2 days ago") instead of a raw timestamp.
func relativeTime(t time.Time) string {
	if t.IsZero() {
		return "unknown"
	}
	d := time.Since(t)
	if d < 0 {
		d = 0
	}

	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return pluralAgo(int(d/time.Minute), "minute")
	case d < 24*time.Hour:
		return pluralAgo(int(d/time.Hour), "hour")
	case d < 30*24*time.Hour:
		return pluralAgo(int(d/(24*time.Hour)), "day")
	case d < 365*24*time.Hour:
		return pluralAgo(int(d/(30*24*time.Hour)), "month")
	default:
		return pluralAgo(int(d/(365*24*time.Hour)), "year")
	}
}

// pluralAgo formats "<n> <unit>(s) ago" with correct pluralization.
func pluralAgo(n int, unit string) string {
	if n == 1 {
		return fmt.Sprintf("1 %s ago", unit)
	}
	return fmt.Sprintf("%d %ss ago", n, unit)
}

// usernameColorPalette holds visually-distinct accent colors used to give
// each commenter/committer a consistent, unique-looking tag color. Pure red
// and pure green are avoided so tags don't get confused with the
// approved/changes-requested semantic colors used elsewhere.
var usernameColorPalette = []lipgloss.Color{
	lipgloss.Color("33"),  // blue
	lipgloss.Color("214"), // orange
	lipgloss.Color("141"), // purple
	lipgloss.Color("79"),  // teal
	lipgloss.Color("219"), // pink
	lipgloss.Color("111"), // light blue
	lipgloss.Color("222"), // tan
	lipgloss.Color("183"), // lavender
	lipgloss.Color("108"), // sage
	lipgloss.Color("174"), // salmon
}

// usernameColor deterministically maps a login to one of usernameColorPalette,
// so the same person always gets the same color.
func usernameColor(login string) lipgloss.Color {
	if login == "" {
		return colorGray
	}
	var h uint32
	for _, r := range login {
		h = h*31 + uint32(r)
	}
	return usernameColorPalette[h%uint32(len(usernameColorPalette))]
}

// maxUsernameDisplayRunes caps how many characters of a username are shown
// before truncating with a trailing "…". This org's SSO-provisioned logins
// can be unusually long (e.g. a 38-character anonymized service-account
// login), and showing the whole thing would blow up list rows/summary lines
// that are meant to stay compact.
const maxUsernameDisplayRunes = 25

// displayLogin maps a raw login to the name shown in the UI. GitHub's Copilot
// review/comment bot logs in under verbose names ("copilot-pull-request-
// reviewer", "copilot-pull-request-reviewer[bot]", "Copilot"); those all
// collapse to a plain "copilot". Everything else is returned unchanged.
func displayLogin(login string) string {
	if strings.HasPrefix(strings.ToLower(login), "copilot") {
		return "copilot"
	}
	return login
}

// truncateLogin maps login to its display name (see displayLogin) and
// truncates to maxUsernameDisplayRunes runes (ellipsis if cut) for display
// only — identity comparisons elsewhere always use the untruncated raw login.
func truncateLogin(login string) string {
	return truncateRunes(displayLogin(login), maxUsernameDisplayRunes)
}

// usernameColored renders login (mapped to its display name and truncated —
// see truncateLogin) in that user's unique color, or "" if login is empty.
// The color is keyed on the display name so all of a bot's login variants
// (e.g. every "copilot*") share one color. Callers add whatever trailing
// punctuation/spacing fits their context (a colon before comment text, a
// trailing space before a commit message, etc).
func usernameColored(login string) string {
	if login == "" {
		return ""
	}
	return lipgloss.NewStyle().Foreground(usernameColor(displayLogin(login))).Render(truncateLogin(login))
}

// usernameTag renders "<login> " in that user's unique color, or "" if login
// is empty.
func usernameTag(login string) string {
	if login == "" {
		return ""
	}
	return usernameColored(login) + " "
}

// commentMarkupPattern matches, in one left-to-right pass, the inline markup
// worth coloring in free-form comment text: fenced (triple-backtick) or inline
// (single-backtick) code spans, markdown links "[text](url)", bare "http(s)://"
// URLs, file paths ("dir/.../name.ext" — at least one slash and an extension),
// and "@username" mentions. Matching them together in a single alternation
// (rather than chaining separate passes) means already-styled spans aren't
// re-scanned — so a URL or path inside a code span stays plain code, and the
// path inside a URL/markdown link isn't re-colored as a filename, since the
// earlier alternative already consumed it. The mention character class is
// broader than GitHub's own username rules (letters/digits/single hyphens)
// because some orgs' SSO-provisioned logins include underscores (e.g.
// "jsmith_example"). (?s) lets a fenced block span multiple lines.
var commentMarkupPattern = regexp.MustCompile("(?s)```.*?```|`[^`]*`|\\[[^\\]]*\\]\\([^)]*\\)|https?://[^\\s<>()\\[\\]\"']+|(?:[\\w.-]+/)+[\\w.-]+\\.[A-Za-z0-9]+|@[A-Za-z0-9_-]+")

// highlightCommentMarkup colors code spans orange, markdown links and bare
// URLs blue, file paths yellow, and "@username" mentions in that login's
// deterministic color (matching how the name reads everywhere else in the
// TUI). Anything not matched is left as-is.
func highlightCommentMarkup(s string) string {
	return commentMarkupPattern.ReplaceAllStringFunc(s, func(m string) string {
		switch {
		case m[0] == '`':
			return styleCode.Render(m)
		case m[0] == '[':
			return styleLink.Render(m)
		case m[0] == '@':
			login := strings.TrimPrefix(m, "@")
			return lipgloss.NewStyle().Foreground(usernameColor(displayLogin(login))).Render("@" + truncateLogin(login))
		case strings.Contains(m, "://"):
			// A bare URL — peel any trailing sentence punctuation back out so it
			// isn't colored as part of the link (e.g. "see https://x.com." —
			// the period stays plain). Closing brackets/parens are already
			// excluded by the pattern's character class.
			url := strings.TrimRight(m, ".,;:!?")
			return styleLink.Render(url) + m[len(url):]
		default: // file path (dir/.../name.ext)
			return styleYellow.Render(m)
		}
	})
}

// renderDetailContent builds the content-column text for one comment/review
// entry: just the colored username tag and the text (or, for a bare
// approval/changes-request, the username tag and badge) — no timestamp and
// no "(kind: state)" action-type tag, both dropped in favor of the plain
// two-column [timestamp | content] layout rendered by
// renderTimestampContentLine.
func renderDetailContent(d DetailLine) string {
	var b strings.Builder

	switch d.Simplified {
	case "approved":
		b.WriteString(usernameTag(d.Login))
		b.WriteString(styleApproved.Render("✓ Approved"))
		return b.String()
	case "changes_requested":
		b.WriteString(usernameTag(d.Login))
		b.WriteString(styleChangesRequested.Render("✗ Changes requested"))
		return b.String()
	}

	if login := usernameColored(d.Login); login != "" {
		b.WriteString(login)
		b.WriteString(": ")
	}
	b.WriteString(highlightCommentMarkup(sanitizeDetailText(d.Text)))

	return b.String()
}

// excessiveBlankLinesPattern matches 3 or more consecutive newlines (i.e. 2
// or more entirely blank lines in a row), collapsed down to a single blank
// line (2 newlines) by sanitizeDetailText — a comment with a wall of blank
// lines shouldn't get to blow up the detail pane's vertical space.
var excessiveBlankLinesPattern = regexp.MustCompile(`\n{3,}`)

// sanitizeDetailText normalizes "\r\n" to "\n" (rendered as real line breaks
// by renderTimestampContentLine's lipgloss-based wrapping, which splits on
// "\n" before word-wrapping each resulting line), collapses 3+ consecutive
// newlines down to 2, and truncates to maxDetailTextRunes runes with a
// trailing "…".
func sanitizeDetailText(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = excessiveBlankLinesPattern.ReplaceAllString(s, "\n\n")

	runes := []rune(s)
	if len(runes) > maxDetailTextRunes {
		return string(runes[:maxDetailTextRunes]) + "…"
	}
	return s
}

// truncateRunes truncates s to at most n runes, appending "…" if it had to
// cut anything.
func truncateRunes(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	if n <= 1 {
		return string(runes[:n])
	}
	return string(runes[:n-1]) + "…"
}

// reviewStateColor maps a ReviewState to its display color.
func reviewStateColor(state ReviewState) lipgloss.Color {
	switch state {
	case ReviewApproved:
		return colorGreen
	case ReviewChangesRequested:
		return colorRed
	case ReviewCommented:
		return colorCyan
	default:
		return colorGray
	}
}

// reviewStateLabel maps a ReviewState to its bracketed badge text.
func reviewStateLabel(state ReviewState) string {
	switch state {
	case ReviewApproved:
		return "[APPROVED]"
	case ReviewChangesRequested:
		return "[CHANGES REQUESTED]"
	case ReviewCommented:
		return "[COMMENTED]"
	default:
		return "[NOT REVIEWED]"
	}
}

// renderFooter renders the footer keymap hints as a subtle, backgroundless
// line (blending into the rest of the TUI rather than standing out as a
// distinct bar), right-aligned within width with a leftMargin-sized gap on
// the right edge (mirroring the gap used on the left elsewhere) — falling
// back to a plain leftMargin-indented (effectively left-aligned) line if the
// terminal is too narrow to fit the full right-aligned form.
func renderFooter(width int) string {
	// Only the essentials live in the footer now; the full keymap is in the
	// floating "?" help overlay (see renderHelpOverlay).
	hints := []struct{ label, key string }{
		{"Toggle Done", "Enter"},
		{"Toggle Ignore", "i"},
		{"Help", "?"},
		{"Quit", "q"},
	}

	parts := make([]string, len(hints))
	for i, h := range hints {
		parts[i] = styleFooterHint.Render(fmt.Sprintf("%s (%s)", h.label, h.key))
	}

	content := strings.Join(parts, styleFooterHint.Render("   "))

	pad := width - leftMargin - lipgloss.Width(content)
	if pad < leftMargin {
		pad = leftMargin
	}
	return strings.Repeat(" ", pad) + content
}
