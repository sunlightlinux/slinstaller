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

// RunFullscreen drives the install as a clr-installer-style menu: a hub
// of sections, each showing its configured value, edited in any order,
// with an Install button that lights up once the required sections are
// set. It returns the config and (for an encrypted root) the passphrase;
// cmd uses it on a real terminal and falls back to Run otherwise.
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
	cfg := assemble(assembly{
		Disk: disks[m.diskIdx].Name, RootFS: m.fs, Encrypt: m.encrypt,
		Swap: m.swap, SwapSize: m.swapSize,
		Hostname: m.hostname, Login: m.login, PassHash: hash,
	})
	return cfg, m.passphrase, nil
}

type section int

const (
	secDisk section = iota
	secFS
	secEncrypt
	secSwap
	secHostname
	secUser
	sectionCount
)

var sectionLabels = [sectionCount]string{"Disk", "Filesystem", "Encryption", "Swap", "Hostname", "Admin user"}

type model struct {
	disks []blockdev.Device

	// configured values
	diskIdx                   int
	diskSet, encrypt, userSet bool
	fs, swap, swapSize        string
	passphrase                string
	hostname, login, password string

	// UI
	onHub     bool
	hubCursor int // 0..sectionCount-1 = sections; sectionCount = Install; +1 = Cancel
	editing   section
	edStep    int
	edCursor  int
	input     textinput.Model

	confirmed bool
}

func newModel(disks []blockdev.Device) model {
	return model{
		disks: disks, onHub: true,
		fs: "ext4", swap: "partition", swapSize: "2G", hostname: "sunlight", login: "user",
	}
}

var (
	appStyle    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("15")).Background(lipgloss.Color("27")).Padding(0, 1)
	rowCursor   = lipgloss.NewStyle().Foreground(lipgloss.Color("45")).Bold(true)
	valStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("250"))
	needStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("203"))
	okStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("78"))
	btnOn       = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("0")).Background(lipgloss.Color("45")).Padding(0, 2)
	btnOff      = lipgloss.NewStyle().Foreground(lipgloss.Color("0")).Background(lipgloss.Color("240")).Padding(0, 2)
	btnDisabled = lipgloss.NewStyle().Foreground(lipgloss.Color("240")).Padding(0, 2)
	pick        = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("0")).Background(lipgloss.Color("45")).Padding(0, 1)
	plain       = lipgloss.NewStyle().Padding(0, 1)
	help        = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
	dangerStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("203"))
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
		m.confirmed = false
		return m, tea.Quit
	}
	if m.onHub {
		return m.updateHub(key)
	}
	return m.updateEditor(key)
}

// --- hub ---

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
		m.confirmed = false
		return m, tea.Quit
	case "enter":
		switch {
		case m.hubCursor < int(sectionCount):
			return m.openEditor(section(m.hubCursor))
		case m.hubCursor == int(sectionCount): // Install
			if m.canInstall() {
				m.confirmed = true
				return m, tea.Quit
			}
		default: // Cancel
			m.confirmed = false
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m model) canInstall() bool { return m.diskSet && m.userSet }

func (m model) sectionValue(s section) string {
	switch s {
	case secDisk:
		if m.diskSet {
			return m.disks[m.diskIdx].Path
		}
		return "— choose —"
	case secFS:
		return m.fs
	case secEncrypt:
		if m.encrypt {
			return "on (LUKS)"
		}
		return "off"
	case secSwap:
		if m.swap == "none" {
			return "none"
		}
		return m.swapSize + " " + m.swap
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
	case secUser:
		return m.userSet
	default:
		return true
	}
}

// --- editor ---

func (m model) openEditor(s section) (tea.Model, tea.Cmd) {
	m.onHub = false
	m.editing = s
	m.edStep = 0
	m.edCursor = 0
	if ch := m.editorChoices(); ch == nil {
		m.initInput()
	}
	return m, textinput.Blink
}

// editorChoices returns the options for the current editor step, or nil
// when the step is a text field.
func (m model) editorChoices() []string {
	switch {
	case m.editing == secDisk:
		out := make([]string, len(m.disks))
		for i, d := range m.disks {
			out[i] = fmt.Sprintf("%-14s %s", d.Path, d.Model)
		}
		return out
	case m.editing == secFS:
		return rootFilesystems
	case m.editing == secEncrypt && m.edStep == 0:
		return []string{"off", "on"}
	case m.editing == secSwap && m.edStep == 0:
		return []string{"partition", "file", "none"}
	default:
		return nil
	}
}

func (m *model) initInput() {
	ti := textinput.New()
	ti.Focus()
	switch {
	case m.editing == secEncrypt: // passphrase
		ti.EchoMode = textinput.EchoPassword
	case m.editing == secSwap: // size
		ti.SetValue(m.swapSize)
	case m.editing == secHostname:
		ti.SetValue(m.hostname)
	case m.editing == secUser && m.edStep == 0:
		ti.SetValue(m.login)
	case m.editing == secUser && m.edStep == 1:
		ti.EchoMode = textinput.EchoPassword
	}
	m.input = ti
}

func (m model) updateEditor(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	if key.String() == "esc" {
		m.onHub = true // cancel this edit, back to the hub
		return m, nil
	}
	if m.editorChoices() != nil {
		return m.updateEditorChoice(key)
	}
	return m.updateEditorText(key)
}

func (m model) updateEditorChoice(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	n := len(m.editorChoices())
	switch key.String() {
	case "up", "k":
		if m.edCursor > 0 {
			m.edCursor--
		}
	case "down", "j":
		if m.edCursor < n-1 {
			m.edCursor++
		}
	case "enter":
		return m.commitChoice()
	}
	return m, nil
}

func (m model) commitChoice() (tea.Model, tea.Cmd) {
	switch m.editing {
	case secDisk:
		m.diskIdx = m.edCursor
		m.diskSet = true
		return m.backToHub()
	case secFS:
		m.fs = rootFilesystems[m.edCursor]
		return m.backToHub()
	case secEncrypt:
		m.encrypt = m.edCursor == 1
		if m.encrypt {
			m.edStep = 1 // ask for the passphrase next
			m.initInput()
			return m, textinput.Blink
		}
		m.passphrase = ""
		return m.backToHub()
	case secSwap:
		m.swap = []string{"partition", "file", "none"}[m.edCursor]
		if m.swap == "none" {
			return m.backToHub()
		}
		m.edStep = 1 // ask for the size next
		m.initInput()
		return m, textinput.Blink
	}
	return m, nil
}

func (m model) updateEditorText(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	if key.String() == "enter" {
		v := m.input.Value()
		switch {
		case m.editing == secEncrypt: // passphrase
			if v == "" {
				return m, nil // required
			}
			m.passphrase = v
			return m.backToHub()
		case m.editing == secSwap: // size
			if v != "" {
				m.swapSize = v
			}
			return m.backToHub()
		case m.editing == secHostname:
			if v != "" {
				m.hostname = v
			}
			return m.backToHub()
		case m.editing == secUser && m.edStep == 0:
			if v == "" {
				return m, nil
			}
			m.login = v
			m.edStep = 1 // password next
			m.initInput()
			return m, textinput.Blink
		case m.editing == secUser && m.edStep == 1:
			m.password = v
			m.userSet = true
			return m.backToHub()
		}
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(key)
	return m, cmd
}

func (m model) backToHub() (tea.Model, tea.Cmd) {
	m.onHub = true
	return m, nil
}

// --- views ---

func (m model) View() string {
	header := "\n" + appStyle.Render(" slinstaller ") + "  Install Sunlight Linux\n\n"
	if m.onHub {
		return header + m.hubView()
	}
	return header + m.editorView()
}

func (m model) hubView() string {
	var b strings.Builder
	for s := section(0); s < sectionCount; s++ {
		mark := okStyle.Render("✓")
		if !m.sectionOK(s) {
			mark = needStyle.Render("✗")
		}
		label := fmt.Sprintf("  %-12s %s", sectionLabels[s], valStyle.Render(m.sectionValue(s)))
		if m.hubCursor == int(s) {
			label = rowCursor.Render("▸ "+fmt.Sprintf("%-12s", sectionLabels[s])) + " " + valStyle.Render(m.sectionValue(s))
		}
		fmt.Fprintf(&b, "%s %s\n", mark, label)
	}

	install := btnDisabled.Render("Install")
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
		b.WriteString("\n  " + needStyle.Render("Set the disk and an admin user to enable Install.") + "\n")
	}
	b.WriteString("\n" + help.Render("↑/↓ move · enter select/edit · esc cancel"))
	return b.String()
}

func (m model) editorView() string {
	title := sectionLabels[m.editing]
	var body string
	if ch := m.editorChoices(); ch != nil {
		for i, c := range ch {
			if i == m.edCursor {
				body += pick.Render("▸ "+c) + "\n"
			} else {
				body += plain.Render("  "+c) + "\n"
			}
		}
		if m.editing == secDisk {
			body = dangerStyle.Render("  the chosen disk will be ERASED") + "\n\n" + body
		}
	} else {
		switch {
		case m.editing == secEncrypt:
			title = "LUKS passphrase"
		case m.editing == secSwap:
			title = "Swap size"
		case m.editing == secUser && m.edStep == 0:
			title = "Admin username"
		case m.editing == secUser && m.edStep == 1:
			title = "Password for " + m.login
		}
		body = m.input.View()
	}
	h := "↑/↓ move · enter confirm · esc back"
	if m.editorChoices() == nil {
		h = "type · enter confirm · esc back"
	}
	return "  " + lipgloss.NewStyle().Bold(true).Render(title) + "\n\n" + body + "\n" + help.Render(h)
}
