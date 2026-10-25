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

// RunFullscreen drives the install as a full-screen, clr-installer-style
// menu: a hub of sections configured in any order, including a manual
// partition editor (plain, encrypted and LVM), with an Install button
// that lights up once the layout is complete. It returns the config and
// the LUKS passphrase; cmd uses it on a real terminal, the prompt flow
// otherwise.
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
	Type  string // "part", "crypt", "lvm"
	FS    string
	Mount string
	Size  string
	VG    string // for lvm
	LVs   []lvEntry
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
		if p.Type == "crypt" {
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

	view      view
	hubCursor int

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

var (
	appStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("15")).Background(lipgloss.Color("27")).Padding(0, 1)
	rowCursor = lipgloss.NewStyle().Foreground(lipgloss.Color("45")).Bold(true)
	valStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("250"))
	needStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("203"))
	okStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("78"))
	btnOn     = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("0")).Background(lipgloss.Color("45")).Padding(0, 2)
	btnOff    = lipgloss.NewStyle().Foreground(lipgloss.Color("0")).Background(lipgloss.Color("240")).Padding(0, 2)
	btnGrey   = lipgloss.NewStyle().Foreground(lipgloss.Color("240")).Padding(0, 2)
	pick      = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("0")).Background(lipgloss.Color("45")).Padding(0, 1)
	plain     = lipgloss.NewStyle().Padding(0, 1)
	help      = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
	danger    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("203"))
)

func (m model) Init() tea.Cmd { return textinput.Blink }

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
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

func (m model) updateHub(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	last := int(sectionCount) + 1 // Install, Cancel
	switch key.String() {
	case "up", "k":
		if m.hubCursor > 0 {
			m.hubCursor--
		}
	case "down", "j":
		if m.hubCursor < last {
			m.hubCursor++
		}
	case "esc":
		return m, tea.Quit
	case "enter":
		switch {
		case m.hubCursor < int(sectionCount):
			return m.openSection(section(m.hubCursor))
		case m.hubCursor == int(sectionCount):
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

func (m model) sectionOK(s section) bool {
	switch s {
	case secDisk:
		return m.diskSet
	case secParts:
		return hasRoot(m.parts) && hasESP(m.parts)
	case secPassphrase:
		return !hasCrypt(m.parts) || m.passphrase != ""
	case secUser:
		return m.userSet
	default:
		return true
	}
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
		return []string{"part", "crypt", "lvm"}
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
		m.form.Type = v
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

func (m model) View() string {
	header := "\n" + appStyle.Render(" slinstaller ") + "  Install Sunlight Linux\n\n"
	switch m.view {
	case viewHub:
		return header + m.hubView()
	case viewSectionChoice:
		return header + m.diskChoiceView()
	case viewSectionText:
		return header + m.textView()
	case viewPartList:
		return header + m.partListView()
	case viewPartForm:
		return header + m.partFormView()
	}
	return header
}

func (m model) hubView() string {
	var b strings.Builder
	for s := section(0); s < sectionCount; s++ {
		mark := okStyle.Render("✓")
		if !m.sectionOK(s) {
			mark = needStyle.Render("✗")
		}
		label := fmt.Sprintf("%-12s", sectionLabels[s])
		if m.hubCursor == int(s) {
			label = rowCursor.Render("▸ " + label)
		} else {
			label = "  " + label
		}
		fmt.Fprintf(&b, "%s %s  %s\n", mark, label, valStyle.Render(m.sectionValue(s)))
	}

	install := btnGrey.Render("Install")
	if m.canInstall() {
		install = btnOff.Render("Install")
		if m.hubCursor == int(sectionCount) {
			install = btnOn.Render("Install")
		}
	}
	cancel := btnOff.Render("Cancel")
	if m.hubCursor == int(sectionCount)+1 {
		cancel = btnOn.Render("Cancel")
	}
	b.WriteString("\n    " + install + "   " + cancel + "\n")
	if !m.canInstall() {
		b.WriteString("\n  " + needStyle.Render("Need: a disk, a / and an ESP, an admin user (and a passphrase if encrypting).") + "\n")
	}
	b.WriteString("\n" + help.Render("↑/↓ move · enter select · esc cancel"))
	return b.String()
}

func (m model) diskChoiceView() string {
	var b strings.Builder
	b.WriteString(danger.Render("  the chosen disk will be ERASED") + "\n\n")
	for i, d := range m.disks {
		line := fmt.Sprintf("%-14s %s", d.Path, d.Model)
		if i == m.edCursor {
			b.WriteString(pick.Render("▸ "+line) + "\n")
		} else {
			b.WriteString(plain.Render("  "+line) + "\n")
		}
	}
	return b.String() + "\n" + help.Render("↑/↓ move · enter select · esc back")
}

func (m model) textView() string {
	title := sectionLabels[m.editing]
	if m.editing == secUser {
		if m.formStep == 0 {
			title = "Admin username"
		} else {
			title = "Password for " + m.login
		}
	}
	return "  " + lipgloss.NewStyle().Bold(true).Render(title) + "\n\n" + m.input.View() +
		"\n\n" + help.Render("type · enter confirm · esc back")
}

func (m model) partListView() string {
	var b strings.Builder
	b.WriteString("  " + lipgloss.NewStyle().Bold(true).Render("Partitions") + "\n\n")
	if len(m.parts) == 0 {
		b.WriteString("  " + help.Render("none yet — press 'a' to add") + "\n")
	}
	for i, p := range m.parts {
		line := partSummary(p)
		if i == m.partCursor {
			b.WriteString(pick.Render("▸ "+line) + "\n")
		} else {
			b.WriteString(plain.Render("  "+line) + "\n")
		}
		for _, lv := range p.LVs {
			b.WriteString(plain.Render("      lv "+lvSummary(lv)) + "\n")
		}
	}
	b.WriteString("\n" + help.Render("a add · l add LV (on lvm) · d delete · esc back"))
	return b.String()
}

func partSummary(p partEntry) string {
	switch p.Type {
	case "lvm":
		return fmt.Sprintf("lvm  group=%s  %s", p.VG, orFill(p.Size))
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
	var title, body string
	if m.formIsLV {
		switch m.formStep {
		case 0:
			title, body = "LV name", m.input.View()
		case 1:
			title, body = "LV filesystem", m.choiceBody()
		case 2:
			title, body = "LV mountpoint (blank for none)", m.input.View()
		case 3:
			title, body = "LV size (blank = fill the group)", m.input.View()
		}
	} else {
		switch m.formStep {
		case 0:
			title, body = "Partition type", m.choiceBody()
		case 1:
			if m.form.Type == "lvm" {
				title, body = "Volume group name", m.input.View()
			} else {
				title, body = "Filesystem", m.choiceBody()
			}
		case 2:
			title, body = "Mountpoint (e.g. / or /boot)", m.input.View()
		case 3:
			title, body = "Size (e.g. 512M, 30G; blank = fill)", m.input.View()
		}
	}
	h := "type · enter next · esc cancel"
	if m.formChoices() != nil {
		h = "↑/↓ move · enter next · esc cancel"
	}
	return "  " + lipgloss.NewStyle().Bold(true).Render(title) + "\n\n" + body + "\n" + help.Render(h)
}

func (m model) choiceBody() string {
	var b strings.Builder
	for i, c := range m.formChoices() {
		if i == m.edCursor {
			b.WriteString(pick.Render("▸ "+c) + "\n")
		} else {
			b.WriteString(plain.Render("  "+c) + "\n")
		}
	}
	return b.String()
}
