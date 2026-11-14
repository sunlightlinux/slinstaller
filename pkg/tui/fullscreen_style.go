package tui

import (
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// The Sunlight Linux palette, laid out as clr-installer lays out its own: a
// dialog centred on a black screen, a black tab strip with an accent-filled
// active tab, white labels over light "» value" lines, and an accent bar on
// the selected row. Sunlight's colours run from orange to red: a burnt
// orange dialog with an amber accent and dark red for what is selected or
// failed. Every content line is rendered at the panel width so its
// background spans the dialog.
//
// Each colour names its 16-colour form as well: the Linux console has no
// orange in its 16 colours, and lipgloss's nearest match sends every orange
// to bright red, while the console's colour 3 is the VGA brown (#aa5500), a
// dark orange. Terminals with 256 colours or more get the real shade.
//
// The console also has only 8 background colours: a bright one (SGR
// 100-107) shows as black. So there the accent is red with light text, not
// amber with black, and every background's 16-colour form is 0-7.
func tone(hex, c256, c16 string) lipgloss.CompleteColor {
	return lipgloss.CompleteColor{TrueColor: hex, ANSI256: c256, ANSI: c16}
}

var (
	clrDialog  = tone("#c4410f", "166", "3")  // dialog background (burnt orange)
	clrBlack   = lipgloss.Color("0")          // the screen around the dialog, and the tab strip
	clrAccent  = tone("#ffc23d", "214", "1")  // selection / active tab / buttons (amber; red on the console)
	clrWhite   = tone("#ffffff", "231", "15") // primary text
	clrValue   = tone("#ffe9a8", "229", "11") // "» value" text (light yellow)
	clrDim     = tone("#ffd9bd", "223", "15") // footer / log text
	clrRule    = tone("#f2955a", "209", "7")  // separator rules
	clrInk     = tone("#000000", "16", "0")   // dark text on a light button
	clrOnAcc   = tone("#000000", "16", "15")  // text on the accent bar (white on the console's red)
	clrSelText = tone("#8c1407", "88", "11")  // selected label (dark red; yellow on the console's red)
	clrRed     = tone("#a3150a", "124", "1")  // failure badges

	// The dialog: a filled block, no border, centred on the black screen —
	// the shape clr-installer draws.
	boxStyle = lipgloss.NewStyle().Background(clrDialog).Padding(1, 2)

	// Full-width line backgrounds.
	lineBG = lipgloss.NewStyle().Foreground(clrWhite).Background(clrDialog)
	selBG  = lipgloss.NewStyle().Foreground(clrOnAcc).Background(clrAccent)

	titleBar = lipgloss.NewStyle().Bold(true).Foreground(clrWhite).Background(clrDialog)
	footer   = lipgloss.NewStyle().Foreground(clrDim).Background(clrDialog)
	ruleBG   = lipgloss.NewStyle().Foreground(clrRule).Background(clrDialog)

	// Tabs sit on a black strip; the active one is an accent block.
	tabStrip    = lipgloss.NewStyle().Foreground(clrWhite).Background(clrBlack)
	tabActive   = lipgloss.NewStyle().Bold(true).Foreground(clrOnAcc).Background(clrAccent)
	tabInactive = lipgloss.NewStyle().Foreground(clrWhite).Background(clrBlack)

	// Hub items: a bold label then a "» value" line, highlighted when selected.
	itemLabel    = lipgloss.NewStyle().Bold(true).Foreground(clrWhite).Background(clrDialog)
	itemValue    = lipgloss.NewStyle().Foreground(clrValue).Background(clrDialog)
	itemSelLabel = lipgloss.NewStyle().Bold(true).Foreground(clrSelText).Background(clrAccent)
	itemSelValue = lipgloss.NewStyle().Foreground(clrOnAcc).Background(clrAccent)

	// Buttons.
	btnSel   = lipgloss.NewStyle().Bold(true).Foreground(clrOnAcc).Background(clrAccent).Padding(0, 2)
	btnIdle  = lipgloss.NewStyle().Foreground(clrInk).Background(tone("#bcbcbc", "250", "7")).Padding(0, 2)
	btnOff   = lipgloss.NewStyle().Foreground(tone("#808080", "244", "8")).Background(tone("#444444", "238", "0")).Padding(0, 2)
	warnText = lipgloss.NewStyle().Bold(true).Foreground(tone("#fff35c", "227", "11")).Background(clrDialog)

	// Popup: a bordered window floated over the hub. A dark red fill and a
	// white border set it apart from the dialog behind it.
	clrPopup      = tone("#7d1a0c", "88", "1")
	popupBoxStyle = lipgloss.NewStyle().Background(clrPopup).Foreground(clrWhite).
			Border(lipgloss.RoundedBorder()).BorderForeground(clrWhite).
			BorderBackground(clrDialog).Padding(1, 3)
	popupTitle = lipgloss.NewStyle().Bold(true).Foreground(clrWhite).Background(clrPopup)
	popupHint  = lipgloss.NewStyle().Foreground(clrDim).Background(clrPopup)
	popupInput = lipgloss.NewStyle().Foreground(clrWhite).Background(tone("#3a0a04", "52", "0")).Padding(0, 1)

	// Scrollbar on the right of the hub's section list.
	scrollStyle = lipgloss.NewStyle().Foreground(clrDim).Background(clrDialog)
)

// overlayCenter composites a popup box centred over a base screen. Only the
// cells the box covers are replaced: what lies left and right of it stays,
// with its colours, so the popup reads as a window floating on the hub. It
// used to replace whole rows, which cut a black band across the dialog.
func overlayCenter(base, popup string, width, height int) string {
	if width <= 0 {
		width = 80
	}
	if height <= 0 {
		height = 24
	}
	baseLines := strings.Split(base, "\n")
	popLines := strings.Split(popup, "\n")
	start := (len(baseLines) - len(popLines)) / 2
	if start < 0 {
		start = 0
	}
	for i, pl := range popLines {
		row := start + i
		if row < 0 || row >= len(baseLines) {
			continue
		}
		baseLines[row] = overlayLine(baseLines[row], pl, width)
	}
	return strings.Join(baseLines, "\n")
}

// overlayLine puts pop in the middle of a width-wide styled line.
func overlayLine(base, pop string, width int) string {
	left := max((width-lipgloss.Width(pop))/2, 0)
	return ansi.Truncate(base, left, "") + "\x1b[0m" + pop + "\x1b[0m" +
		cellsFrom(base, left+lipgloss.Width(pop))
}

// sgrSeq matches a colour or style escape sequence.
var sgrSeq = regexp.MustCompile(`^\x1b\[[0-9;]*m`)

// cellsFrom returns what is left of a styled line after its first n cells,
// starting with the styles in force at that point, so it keeps its colours.
func cellsFrom(s string, n int) string {
	var active strings.Builder
	cells := 0
	for i := 0; i < len(s); {
		if seq := sgrSeq.FindString(s[i:]); seq != "" {
			if seq == "\x1b[0m" || seq == "\x1b[m" {
				active.Reset()
			} else {
				active.WriteString(seq)
			}
			i += len(seq)
			continue
		}
		if cells >= n {
			return active.String() + s[i:]
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		cells += ansi.StringWidth(string(r))
		i += size
	}
	return ""
}

// gap is the space between two buttons. A button's style ends with a reset,
// so plain spaces after it fall back to the terminal's own background, which
// showed as a black block between buttons; gap keeps the dialog's colour.
func gap(n int) string { return lineBG.Render(strings.Repeat(" ", n)) }

// A button in a row: its label and how it is drawn (btnSel, btnIdle, btnOff).
type button struct {
	label string
	style lipgloss.Style
}

// buttonRow draws a row of buttons all as wide as the widest, labels
// centred, so a row reads as one set rather than boxes of every size.
func buttonRow(btns ...button) string { return sizedButtonRow(4, btns...) }

// sizedButtonRow is buttonRow with pad cells of space around the widest
// label. The styles' own padding is replaced: Width counts padding, and a
// compact row has less room than they ask for.
func sizedButtonRow(pad int, btns ...button) string {
	wide := 0
	for _, b := range btns {
		wide = max(wide, lipgloss.Width(b.label))
	}
	parts := make([]string, len(btns))
	for k, b := range btns {
		parts[k] = b.style.Padding(0).Width(wide + pad).Align(lipgloss.Center).Render(b.label)
	}
	return strings.Join(parts, gap(2))
}

// buttonLine is a full-width line with a row of buttons centred on it. A
// row too wide to leave a margin on both sides draws its buttons tighter:
// the final screen's four filled the dialog and touched its right edge.
func buttonLine(w int, btns ...button) string {
	row := buttonRow(btns...)
	if lipgloss.Width(row) > w-4 {
		row = sizedButtonRow(2, btns...)
	}
	margin := max(w-lipgloss.Width(row), 0)
	return gap(margin/2) + row + gap(margin-margin/2)
}

// pick is btnSel for the selected button, btnIdle for the rest.
func pick(sel bool) lipgloss.Style {
	if sel {
		return btnSel
	}
	return btnIdle
}

// --- views ---

// The dialog is clr-installer's window: 80 columns by 24 rows, centred,
// whatever the size of the terminal (its tui package opens every page as an
// 80x24 window). boxStyle pads it by 2 columns and 1 row on each side, so
// the content inside is 76x22. A terminal smaller than 80x24 shrinks it, to
// the same lower bounds as before.
const (
	dialogWidth  = 80
	dialogHeight = 24
)

// contentWidth is the width every content line is rendered to: the inner
// width of the centred dialog, not the terminal.
func contentWidth(width int) int {
	if width <= 0 {
		width = dialogWidth
	}
	cw := min(width, dialogWidth) - 4 // boxStyle's horizontal padding
	if cw < 40 {
		cw = 40
	}
	return cw
}

// panelHeight is the dialog's fixed inner height (title + body + rule +
// footer), so the box stays the same size moving between the menu and
// a section instead of shrinking to each view's content.
func panelHeight(height int) int {
	if height <= 0 {
		height = dialogHeight
	}
	h := min(height, dialogHeight) - 2 // boxStyle's vertical padding
	if h < 18 {
		h = 18
	}
	return h
}

// frameScreen frames a view the way clr-installer does: a filled dialog
// — bracketed title, the body, a rule and the footer hint — centred on a
// black screen. Both front-ends (the hub and the install progress view)
// render through it, so they match. The body is padded to a stable height so
// the box does not resize as views change.
func frameScreen(width, height int, body []string, foot string) string {
	return frameScreenButtons(width, height, body, nil, foot)
}

// frameScreenButtons is frameScreen with a row of buttons kept at the
// bottom of the dialog, just above the rule and footer, whatever the height
// of the body above it: clr-installer gives every page a control frame
// below its fixed-height content frame, so its buttons sit on the same row
// on every page instead of following the content up the screen.
func frameScreenButtons(width, height int, body, buttons []string, foot string) string {
	if width <= 0 {
		width = 80
	}
	if height <= 0 {
		height = 24
	}
	cw := contentWidth(width)
	lines := []string{
		titleBar.Width(cw).Render("[Sunlight Linux Installer]"),
	}
	lines = append(lines, body...)
	// Pad with blank full-width lines so title+body fills the panel, leaving
	// the last two rows for the rule and footer. A view taller than this
	// (e.g. the Advanced menu on a short terminal) is left as-is.
	for len(lines) < panelHeight(height)-2-len(buttons) {
		lines = append(lines, lineBG.Width(cw).Render(""))
	}
	lines = append(lines, buttons...)
	lines = append(lines, rule(cw), footer.Width(cw).Render(foot))
	box := boxStyle.Render(strings.Join(lines, "\n"))
	return lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, box,
		lipgloss.WithWhitespaceBackground(clrBlack))
}

func (m model) panelWidth() int { return contentWidth(m.width) }

func (m model) screen(body []string, foot string) string {
	return frameScreen(m.width, m.height, body, foot)
}

// screenButtons is screen with the view's buttons held at the bottom.
func (m model) screenButtons(body, buttons []string, foot string) string {
	return frameScreenButtons(m.width, m.height, body, buttons, foot)
}

func rule(w int) string { return ruleBG.Width(w).Render(strings.Repeat("─", w)) }

// row renders a plain full-width line, highlighted as an accent bar when selected.
func row(w int, selected bool, text string) string {
	if selected {
		return selBG.Width(w).Render("  " + text)
	}
	return lineBG.Width(w).Render("  " + text)
}

// item renders a hub entry: a bold label line and a "» value" line. When
// selected it is drawn in the accent selection colours. The height is fixed
// at two lines so the hub's section list can be windowed and scrolled
// cleanly — every section occupies the same space.
func item(w int, selected bool, label, value string) []string {
	if selected {
		return []string{
			itemSelLabel.Width(w).Render("  " + label),
			itemSelValue.Width(w).Render("    » " + value),
		}
	}
	return []string{
		itemLabel.Width(w).Render("  " + label),
		itemValue.Width(w).Render("    » " + value),
	}
}

// scrollbarCol builds the right-hand scrollbar: rows lines, each a space
// plus a glyph. The thumb covers the visible window [start, start+win) of a
// list of total items; ▲/▼ cap the ends. When everything fits (total<=win)
// it is a blank column, so the section content keeps the same width either
// way.
func scrollbarCol(rows, start, win, total int) []string {
	out := make([]string, rows)
	if total <= win || rows < 2 {
		for i := range out {
			out[i] = scrollStyle.Render("  ")
		}
		return out
	}
	for i := 0; i < rows; i++ {
		sec := i * total / rows
		glyph := "░"
		if sec >= start && sec < start+win {
			glyph = "█"
		}
		out[i] = scrollStyle.Render(" " + glyph)
	}
	out[0] = scrollStyle.Render(" ▲")
	out[rows-1] = scrollStyle.Render(" ▼")
	return out
}

// wrapWords wraps s to width on word boundaries, at most maxLines lines; a
// truncated last line ends with " ...".
func wrapWords(s string, width, maxLines int) []string {
	var lines []string
	cur := ""
	for _, wd := range strings.Fields(s) {
		switch {
		case cur == "":
			cur = wd
		case len(cur)+1+len(wd) <= width:
			cur += " " + wd
		default:
			lines = append(lines, cur)
			cur = wd
		}
	}
	if cur != "" {
		lines = append(lines, cur)
	}
	if len(lines) > maxLines {
		lines = lines[:maxLines]
		lines[maxLines-1] = clip(lines[maxLines-1]+" ...", width)
	}
	return lines
}
