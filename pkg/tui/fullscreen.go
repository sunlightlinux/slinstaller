package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/sunlightlinux/slinstaller/pkg/blockdev"
	"github.com/sunlightlinux/slinstaller/pkg/config"
)

// RunFullscreen drives the install as a full-screen menu styled after the
// Clear Linux minimal-ISO installer: a blue panel with [R] Required / [A]
// Advanced tabs, each section a label over a "» value" line with a teal
// selection bar, and Cancel/Install buttons at the foot. Sections are
// configured in any order and include a manual partition editor (plain,
// encrypted and LVM); Install lights up once the layout is complete. It
// returns the config and the LUKS passphrase; cmd uses it on a real
// terminal, the prompt flow otherwise.
func RunFullscreen() (*config.Config, string, error) {
	devs, err := blockdev.List()
	if err != nil {
		return nil, "", err
	}
	disks := blockdev.Disks(devs)
	if len(disks) == 0 {
		return nil, "", fmt.Errorf("tui: no disks found")
	}

	final, err := tea.NewProgram(newModel(disks), tea.WithAltScreen()).Run()
	if err != nil {
		return nil, "", err
	}
	m := final.(model)
	if !m.confirmed {
		return nil, "", fmt.Errorf("cancelled")
	}

	hash, err := cryptHash(m.password)
	if err != nil {
		return nil, "", err
	}
	cfg := &config.Config{
		TargetMedia: []*config.Media{{
			Name: m.disks[m.diskIdx].Name, Type: "disk",
			Children: layoutToMedia(m.disks[m.diskIdx].Name, m.parts),
		}},
		Hostname: m.hostname, Keyboard: m.keyboard, Language: m.language, Timezone: m.timezone,
		Users: []*config.User{{Login: m.login, Password: hash, Admin: true}},
	}
	return cfg, m.passphrase, nil
}

// --- the layout data model (separate from the UI, so it is testable) ---

type lvEntry struct {
	Name, FS, Mount, Size string
}

type partEntry struct {
	Type      string // "part", "crypt", "lvm"
	FS        string
	Mount     string
	Size      string
	VG        string // for lvm
	Encrypted bool   // for lvm: the PV is a LUKS container (LVM-on-LUKS)
	LVs       []lvEntry
}

// layoutToMedia turns the editor's partition list into config children,
// numbering the physical partitions (sda1, sda2, ...) and nesting an
// lvm partition's logical volumes.
func layoutToMedia(disk string, parts []partEntry) []*config.Media {
	var out []*config.Media
	for i, p := range parts {
		name := partDevName(disk, i+1)
		m := &config.Media{Name: name, Type: p.Type, FsType: p.FS, Mountpoint: p.Mount, Size: p.Size}
		if p.Type == "lvm" {
			m.FsType, m.Mountpoint = "", ""
			m.VolumeGroup = p.VG
			m.Encrypted = p.Encrypted
			for _, lv := range p.LVs {
				m.Children = append(m.Children, &config.Media{
					Name: lv.Name, FsType: lv.FS, Mountpoint: lv.Mount, Size: lv.Size,
				})
			}
		}
		out = append(out, m)
	}
	return out
}

func hasRoot(parts []partEntry) bool {
	for _, p := range parts {
		if p.Mount == "/" {
			return true
		}
		for _, lv := range p.LVs {
			if lv.Mount == "/" {
				return true
			}
		}
	}
	return false
}

func hasESP(parts []partEntry) bool {
	for _, p := range parts {
		if p.FS == "vfat" && (p.Mount == "/boot" || p.Mount == "/boot/efi") {
			return true
		}
	}
	return false
}

func hasCrypt(parts []partEntry) bool {
	for _, p := range parts {
		if p.Type == "crypt" || p.Encrypted {
			return true
		}
	}
	return false
}

// --- the model ---

type section int

const (
	secDisk section = iota
	secParts
	secPassphrase
	secKeyboard
	secLanguage
	secTimezone
	secHostname
	secUser
	sectionCount
)

var sectionLabels = [sectionCount]string{
	"Disk", "Partitions", "Passphrase", "Keyboard", "Language", "Timezone", "Hostname", "Admin user",
}

type view int

const (
	viewHub view = iota
	viewSectionChoice
	viewSectionText
	viewPartList
	viewPartForm
)

type model struct {
	disks []blockdev.Device

	diskIdx                     int
	diskSet, userSet            bool
	keyboard, language          string
	timezone, hostname          string
	login, password, passphrase string
	parts                       []partEntry

	view          view
	tab           int // 0 = Required options, 1 = Advanced options
	hubCursor     int
	width, height int

	// simple section editors
	editing  section
	input    textinput.Model
	edCursor int

	// partition editor
	partCursor int       // index into parts (and -1.. for buttons)
	form       partEntry // the entry being built
	formLV     lvEntry
	formStep   int
	formIsLV   bool

	confirmed bool
}

func newModel(disks []blockdev.Device) model {
	return model{
		disks: disks, view: viewHub,
		keyboard: "us", language: "en_US.UTF-8", timezone: "UTC", hostname: "sunlight", login: "user",
	}
}

// The Clear Linux installer palette: a royal-blue dialog centred on a black
// screen, a black tab strip with a teal active tab, white labels over cyan
// "» value" lines, and a tall teal bar — with a magenta label — on the
// selected row. Every content line is rendered at the panel width so its
// background spans the dialog.
var (
	clrBlue    = lipgloss.Color("20")  // dialog background (royal blue)
	clrBlack   = lipgloss.Color("0")   // the screen around the dialog, and the tab strip
	clrTeal    = lipgloss.Color("37")  // selection / active tab
	clrWhite   = lipgloss.Color("231") // primary text
	clrValue   = lipgloss.Color("51")  // "» value" text (cyan)
	clrDim     = lipgloss.Color("152") // footer / log text
	clrRule    = lipgloss.Color("66")  // separator rules
	clrInk     = lipgloss.Color("16")  // dark text on a teal bar
	clrSelText = lipgloss.Color("201") // selected label (magenta), as clr renders it

	// The dialog: a filled blue block, no border, centred on the black
	// screen — the shape clr-installer draws.
	boxStyle = lipgloss.NewStyle().Background(clrBlue).Padding(1, 2)

	// Full-width line backgrounds.
	lineBG = lipgloss.NewStyle().Foreground(clrWhite).Background(clrBlue)
	selBG  = lipgloss.NewStyle().Foreground(clrInk).Background(clrTeal)

	titleBar = lipgloss.NewStyle().Bold(true).Foreground(clrWhite).Background(clrBlue)
	footer   = lipgloss.NewStyle().Foreground(clrDim).Background(clrBlue)
	ruleBG   = lipgloss.NewStyle().Foreground(clrRule).Background(clrBlue)

	// Tabs sit on a black strip; the active one is a teal block.
	tabStrip    = lipgloss.NewStyle().Foreground(clrWhite).Background(clrBlack)
	tabActive   = lipgloss.NewStyle().Bold(true).Foreground(clrInk).Background(clrTeal)
	tabInactive = lipgloss.NewStyle().Foreground(clrWhite).Background(clrBlack)

	// Hub items: a bold label then a "» value" line, highlighted when selected.
	itemLabel    = lipgloss.NewStyle().Bold(true).Foreground(clrWhite).Background(clrBlue)
	itemValue    = lipgloss.NewStyle().Foreground(clrValue).Background(clrBlue)
	itemSelLabel = lipgloss.NewStyle().Bold(true).Foreground(clrSelText).Background(clrTeal)
	itemSelValue = lipgloss.NewStyle().Foreground(clrInk).Background(clrTeal)

	// Buttons.
	btnSel   = lipgloss.NewStyle().Bold(true).Foreground(clrInk).Background(clrTeal).Padding(0, 2)
	btnIdle  = lipgloss.NewStyle().Foreground(clrInk).Background(lipgloss.Color("250")).Padding(0, 2)
	btnOff   = lipgloss.NewStyle().Foreground(lipgloss.Color("244")).Background(lipgloss.Color("238")).Padding(0, 2)
	warnText = lipgloss.NewStyle().Foreground(lipgloss.Color("221")).Background(clrBlue)
)

func (m model) Init() tea.Cmd { return textinput.Blink }

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if ws, ok := msg.(tea.WindowSizeMsg); ok {
		m.width, m.height = ws.Width, ws.Height
		return m, nil
	}
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		return m, cmd
	}
	if key.String() == "ctrl+c" {
		return m, tea.Quit
	}
	switch m.view {
	case viewHub:
		return m.updateHub(key)
	case viewSectionChoice:
		return m.updateSectionChoice(key)
	case viewSectionText:
		return m.updateSectionText(key)
	case viewPartList:
		return m.updatePartList(key)
	case viewPartForm:
		return m.updatePartForm(key)
	}
	return m, nil
}

// --- hub ---

func (m model) canInstall() bool {
	return m.diskSet && m.userSet && hasRoot(m.parts) && hasESP(m.parts) &&
		(!hasCrypt(m.parts) || m.passphrase != "")
}

// visibleSections lists the sections shown under a tab. Required holds the
// everyday choices; Advanced the ones most installs leave at their defaults —
// mirroring clr-installer's [R] Required / [A] Advanced split.
func visibleSections(tab int) []section {
	if tab == 1 {
		return []section{secPassphrase, secHostname}
	}
	return []section{secDisk, secParts, secKeyboard, secLanguage, secTimezone, secUser}
}

func (m model) updateHub(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	vis := visibleSections(m.tab)
	last := len(vis) + 1 // Install at len(vis), Cancel at len(vis)+1
	switch key.String() {
	case "up", "k":
		if m.hubCursor > 0 {
			m.hubCursor--
		}
	case "down", "j":
		if m.hubCursor < last {
			m.hubCursor++
		}
	case "tab", "right", "shift+tab", "left", "h", "l":
		m.tab = (m.tab + 1) % 2
		m.hubCursor = 0
	case "r":
		m.tab, m.hubCursor = 0, 0
	case "A":
		m.tab, m.hubCursor = 1, 0
	case "esc":
		return m, tea.Quit
	case "enter":
		switch {
		case m.hubCursor < len(vis):
			return m.openSection(vis[m.hubCursor])
		case m.hubCursor == len(vis):
			if m.canInstall() {
				m.confirmed = true
				return m, tea.Quit
			}
		default:
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m model) openSection(s section) (tea.Model, tea.Cmd) {
	m.editing = s
	switch s {
	case secParts:
		m.view = viewPartList
		m.partCursor = 0
		return m, nil
	case secDisk:
		m.view = viewSectionChoice
		m.edCursor = m.diskIdx
		return m, nil
	default: // text sections
		m.view = viewSectionText
		ti := textinput.New()
		ti.Focus()
		switch s {
		case secKeyboard:
			ti.SetValue(m.keyboard)
		case secLanguage:
			ti.SetValue(m.language)
		case secTimezone:
			ti.SetValue(m.timezone)
		case secHostname:
			ti.SetValue(m.hostname)
		case secPassphrase, secUser:
			if s == secPassphrase {
				ti.EchoMode = textinput.EchoPassword
			}
		}
		m.input = ti
		m.formStep = 0 // for the two-field user editor
		return m, textinput.Blink
	}
}

func (m model) sectionValue(s section) string {
	switch s {
	case secDisk:
		if m.diskSet {
			return m.disks[m.diskIdx].Path
		}
		return "— choose —"
	case secParts:
		if len(m.parts) == 0 {
			return "— none —"
		}
		return fmt.Sprintf("%d defined", len(m.parts))
	case secPassphrase:
		if m.passphrase != "" {
			return "set"
		}
		if hasCrypt(m.parts) {
			return "— required —"
		}
		return "—"
	case secKeyboard:
		return m.keyboard
	case secLanguage:
		return m.language
	case secTimezone:
		return m.timezone
	case secHostname:
		return m.hostname
	case secUser:
		if m.userSet {
			return m.login + " (admin)"
		}
		return "— not set —"
	}
	return ""
}

// --- simple section editors ---

func (m model) updateSectionChoice(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch key.String() {
	case "esc":
		m.view = viewHub
	case "up", "k":
		if m.edCursor > 0 {
			m.edCursor--
		}
	case "down", "j":
		if m.edCursor < len(m.disks)-1 {
			m.edCursor++
		}
	case "enter":
		m.diskIdx = m.edCursor
		m.diskSet = true
		m.view = viewHub
	}
	return m, nil
}

func (m model) updateSectionText(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch key.String() {
	case "esc":
		m.view = viewHub
		return m, nil
	case "enter":
		v := strings.TrimSpace(m.input.Value())
		switch m.editing {
		case secKeyboard:
			if v != "" {
				m.keyboard = v
			}
		case secLanguage:
			if v != "" {
				m.language = v
			}
		case secTimezone:
			if v != "" {
				m.timezone = v
			}
		case secHostname:
			if v != "" {
				m.hostname = v
			}
		case secPassphrase:
			m.passphrase = v
		case secUser:
			if m.formStep == 0 {
				if v == "" {
					return m, nil
				}
				m.login = v
				m.formStep = 1
				ti := textinput.New()
				ti.Focus()
				ti.EchoMode = textinput.EchoPassword
				m.input = ti
				return m, textinput.Blink
			}
			m.password = m.input.Value()
			m.userSet = true
		}
		m.view = viewHub
		return m, nil
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(key)
	return m, cmd
}

// --- partition editor ---

func (m model) updatePartList(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch key.String() {
	case "esc":
		m.view = viewHub
	case "up", "k":
		if m.partCursor > 0 {
			m.partCursor--
		}
	case "down", "j":
		if m.partCursor < len(m.parts)-1 {
			m.partCursor++
		}
	case "a":
		return m.startForm(false)
	case "l": // add a logical volume to the selected lvm partition
		if m.partCursor < len(m.parts) && m.parts[m.partCursor].Type == "lvm" {
			return m.startForm(true)
		}
	case "d":
		if len(m.parts) > 0 {
			m.parts = append(m.parts[:m.partCursor], m.parts[m.partCursor+1:]...)
			if m.partCursor > 0 {
				m.partCursor--
			}
		}
	}
	return m, nil
}

func (m model) startForm(isLV bool) (tea.Model, tea.Cmd) {
	m.view = viewPartForm
	m.formStep = 0
	m.formIsLV = isLV
	m.edCursor = 0
	if isLV {
		m.formLV = lvEntry{}
		m.initFormInput()
	} else {
		m.form = partEntry{Type: "part"}
	}
	return m, textinput.Blink
}

// formChoices returns the options for the current form step, or nil for
// a text step.
func (m model) formChoices() []string {
	if m.formIsLV {
		if m.formStep == 1 {
			return rootFilesystems
		}
		return nil
	}
	switch m.formStep {
	case 0:
		return []string{"part", "crypt", "lvm", "lvm-encrypted"}
	case 1:
		if m.form.Type == "lvm" {
			return nil // volumeGroup is text
		}
		return append(append([]string{}, rootFilesystems...), "vfat", "swap")
	default:
		return nil
	}
}

func (m *model) initFormInput() {
	ti := textinput.New()
	ti.Focus()
	m.input = ti
}

func (m model) updatePartForm(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	if key.String() == "esc" {
		m.view = viewPartList
		return m, nil
	}
	if m.formChoices() != nil {
		return m.updateFormChoice(key)
	}
	return m.updateFormText(key)
}

func (m model) updateFormChoice(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	ch := m.formChoices()
	switch key.String() {
	case "up", "k":
		if m.edCursor > 0 {
			m.edCursor--
		}
	case "down", "j":
		if m.edCursor < len(ch)-1 {
			m.edCursor++
		}
	case "enter":
		return m.commitFormField(ch[m.edCursor])
	}
	return m, nil
}

func (m model) updateFormText(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	if key.String() == "enter" {
		return m.commitFormField(strings.TrimSpace(m.input.Value()))
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(key)
	return m, cmd
}

// commitFormField records the current field and moves to the next, or
// finishes the form.
func (m model) commitFormField(v string) (tea.Model, tea.Cmd) {
	if m.formIsLV {
		switch m.formStep {
		case 0: // name
			if v == "" {
				return m, nil
			}
			m.formLV.Name = v
		case 1: // fs
			m.formLV.FS = v
		case 2: // mount
			m.formLV.Mount = v
		case 3: // size
			m.formLV.Size = v
			p := &m.parts[m.partCursor]
			p.LVs = append(p.LVs, m.formLV)
			m.view = viewPartList
			return m, nil
		}
		m.formStep++
		m.edCursor = 0
		m.initFormInput()
		return m, textinput.Blink
	}

	switch m.formStep {
	case 0: // type
		if v == "lvm-encrypted" {
			m.form.Type, m.form.Encrypted = "lvm", true
		} else {
			m.form.Type = v
		}
	case 1: // fs or vg
		if m.form.Type == "lvm" {
			if v == "" {
				v = "vg0"
			}
			m.form.VG = v
		} else {
			m.form.FS = v
		}
	case 2: // mountpoint (skipped for lvm/swap below)
		m.form.Mount = v
	case 3: // size
		m.form.Size = v
		m.parts = append(m.parts, m.form)
		m.view = viewPartList
		m.partCursor = len(m.parts) - 1
		return m, nil
	}

	m.formStep++
	// Skip the mountpoint for an LVM PV and for swap.
	if m.formStep == 2 && (m.form.Type == "lvm" || m.form.FS == "swap") {
		m.formStep = 3
	}
	m.edCursor = 0
	if m.formChoices() == nil {
		m.initFormInput()
	}
	return m, textinput.Blink
}

// --- views ---

// contentWidth is the width every content line is rendered to: the inner
// width of the centred dialog, not the terminal. Capped so the UI stays
// readable on a wide screen and bounded so it fits a small one.
func contentWidth(width int) int {
	if width <= 0 {
		width = 80
	}
	cw := width - 10 // leave room for the border, padding and margins
	if cw > 86 {
		cw = 86
	}
	if cw < 40 {
		cw = 40
	}
	return cw
}

// frameScreen frames a view the way clr-installer does: a filled blue dialog
// — bracketed title, the body, a rule and the footer hint — centred on a
// black screen. Both front-ends (the hub and the install progress view)
// render through it, so they match.
func frameScreen(width, height int, body []string, foot string) string {
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
	lines = append(lines, rule(cw), footer.Width(cw).Render(foot))
	box := boxStyle.Render(strings.Join(lines, "\n"))
	return lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, box,
		lipgloss.WithWhitespaceBackground(clrBlack))
}

func (m model) panelWidth() int { return contentWidth(m.width) }

func (m model) screen(body []string, foot string) string {
	return frameScreen(m.width, m.height, body, foot)
}

func rule(w int) string { return ruleBG.Width(w).Render(strings.Repeat("─", w)) }

// row renders a plain full-width line, highlighted as a teal bar when selected.
func row(w int, selected bool, text string) string {
	if selected {
		return selBG.Width(w).Render("  " + text)
	}
	return lineBG.Width(w).Render("  " + text)
}

// item renders a hub entry: a bold label line and a "» value" line. When
// selected it becomes a taller teal bar — a blank teal line above and below
// the two lines — the way clr-installer highlights the focused row.
func item(w int, selected bool, label, value string) []string {
	if selected {
		blank := selBG.Width(w).Render("")
		return []string{
			blank,
			itemSelLabel.Width(w).Render("  " + label),
			itemSelValue.Width(w).Render("    » " + value),
			blank,
		}
	}
	return []string{
		itemLabel.Width(w).Render("  " + label),
		itemValue.Width(w).Render("    » " + value),
	}
}

func (m model) View() string {
	switch m.view {
	case viewHub:
		return m.hubView()
	case viewSectionChoice:
		return m.diskChoiceView()
	case viewSectionText:
		return m.textView()
	case viewPartList:
		return m.partListView()
	case viewPartForm:
		return m.partFormView()
	}
	return ""
}

func (m model) hubView() string {
	w := m.panelWidth()
	var body []string

	// Tab row: [R] Required options   [A] Advanced options.
	req, adv := tabInactive, tabInactive
	if m.tab == 0 {
		req = tabActive
	} else {
		adv = tabActive
	}
	tabs := req.Render(" [R] Required options ") + tabInactive.Render("  ") + adv.Render(" [A] Advanced options ")
	body = append(body, tabStrip.Width(w).Render("  "+tabs), rule(w))

	vis := visibleSections(m.tab)
	for i, s := range vis {
		body = append(body, item(w, m.hubCursor == i, sectionLabels[s], m.sectionValue(s))...)
		body = append(body, rule(w))
	}

	body = append(body, lineBG.Width(w).Render(""))

	// Cancel / Install buttons.
	cancel := btnIdle.Render("Cancel")
	if m.hubCursor == len(vis)+1 {
		cancel = btnSel.Render("Cancel")
	}
	install := btnOff.Render("Install")
	if m.canInstall() {
		install = btnIdle.Render("Install")
		if m.hubCursor == len(vis) {
			install = btnSel.Render("Install")
		}
	}
	body = append(body, lineBG.Width(w).Render("  "+cancel+"   "+install))
	if !m.canInstall() {
		body = append(body, warnText.Width(w).Render("  Need: a disk, a /, an ESP, an admin user (+ passphrase if encrypting)."))
	}

	return m.screen(body, "Use [Tab] or the arrow keys [Up and Down] to navigate")
}

func (m model) diskChoiceView() string {
	w := m.panelWidth()
	body := []string{
		warnText.Width(w).Render("  Select a target disk — it will be ERASED."),
		lineBG.Width(w).Render(""),
	}
	for i, d := range m.disks {
		body = append(body, row(w, i == m.edCursor, fmt.Sprintf("%-14s %s", d.Path, d.Model)))
	}
	return m.screen(body, "[Up/Down] move · [Enter] select · [Esc] back")
}

func (m model) textView() string {
	w := m.panelWidth()
	title := sectionLabels[m.editing]
	if m.editing == secUser {
		if m.formStep == 0 {
			title = "Admin username"
		} else {
			title = "Password for " + m.login
		}
	}
	body := []string{
		itemLabel.Width(w).Render("  " + title),
		lineBG.Width(w).Render(""),
		lineBG.Width(w).Render("  " + m.input.View()),
	}
	return m.screen(body, "type · [Enter] confirm · [Esc] back")
}

func (m model) partListView() string {
	w := m.panelWidth()
	body := []string{
		itemLabel.Width(w).Render("  Partitions"),
		rule(w),
	}
	if len(m.parts) == 0 {
		body = append(body, footer.Width(w).Render("  none yet — press [a] to add"))
	}
	for i, p := range m.parts {
		body = append(body, row(w, i == m.partCursor, partSummary(p)))
		for _, lv := range p.LVs {
			body = append(body, lineBG.Width(w).Render("      lv "+lvSummary(lv)))
		}
	}
	return m.screen(body, "[a] add · [l] add LV (on lvm) · [d] delete · [Esc] back")
}

func partSummary(p partEntry) string {
	switch p.Type {
	case "lvm":
		enc := ""
		if p.Encrypted {
			enc = " (encrypted)"
		}
		return fmt.Sprintf("lvm  group=%s%s  %s", p.VG, enc, orFill(p.Size))
	case "crypt":
		return fmt.Sprintf("crypt %s  %s  %s", p.FS, orNone(p.Mount), orFill(p.Size))
	default:
		return fmt.Sprintf("part  %s  %s  %s", p.FS, orNone(p.Mount), orFill(p.Size))
	}
}

func lvSummary(lv lvEntry) string {
	return fmt.Sprintf("%-6s %s  %s  %s", lv.Name, lv.FS, orNone(lv.Mount), orFill(lv.Size))
}

func orNone(s string) string {
	if s == "" {
		return "(no mount)"
	}
	return s
}
func orFill(s string) string {
	if s == "" || strings.EqualFold(s, "rest") {
		return "fill"
	}
	return s
}

func (m model) partFormView() string {
	w := m.panelWidth()
	var title string
	var isChoice bool
	if m.formIsLV {
		switch m.formStep {
		case 0:
			title = "LV name"
		case 1:
			title, isChoice = "LV filesystem", true
		case 2:
			title = "LV mountpoint (blank for none)"
		case 3:
			title = "LV size (blank = fill the group)"
		}
	} else {
		switch m.formStep {
		case 0:
			title, isChoice = "Partition type", true
		case 1:
			if m.form.Type == "lvm" {
				title = "Volume group name"
			} else {
				title, isChoice = "Filesystem", true
			}
		case 2:
			title = "Mountpoint (e.g. / or /boot)"
		case 3:
			title = "Size (e.g. 512M, 30G; blank = fill)"
		}
	}

	body := []string{
		itemLabel.Width(w).Render("  " + title),
		lineBG.Width(w).Render(""),
	}
	foot := "type · [Enter] next · [Esc] cancel"
	if isChoice {
		for i, c := range m.formChoices() {
			body = append(body, row(w, i == m.edCursor, c))
		}
		foot = "[Up/Down] move · [Enter] next · [Esc] cancel"
	} else {
		body = append(body, lineBG.Width(w).Render("  "+m.input.View()))
	}
	return m.screen(body, foot)
}
