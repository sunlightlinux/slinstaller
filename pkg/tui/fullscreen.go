package tui

import (
	"fmt"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/sunlightlinux/slinstaller/pkg/blockdev"
	"github.com/sunlightlinux/slinstaller/pkg/config"
)

// RunFullscreen drives the install questions as a full-screen wizard,
// returning the config and (for an encrypted root) the passphrase. It
// builds the same config.Config the prompt flow does; cmd uses it when
// stdin and stdout are a real terminal, and falls back to Run otherwise.
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
	if m.err != nil {
		return nil, "", m.err
	}
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

type step int

const (
	stepDisk step = iota
	stepFS
	stepEncrypt
	stepPassphrase
	stepSwap
	stepSwapSize
	stepHostname
	stepUsername
	stepPassword
	stepConfirm
	stepDone
)

type model struct {
	step  step
	disks []blockdev.Device

	// answers
	diskIdx                   int
	fs, swap, swapSize        string
	encrypt                   bool
	passphrase                string
	hostname, login, password string

	cursor int             // for choice steps
	input  textinput.Model // for text/secret steps

	confirmed bool
	err       error
}

func newModel(disks []blockdev.Device) model {
	m := model{step: stepDisk, disks: disks, swap: "partition", swapSize: "2G"}
	m.enterStep()
	return m
}

var (
	titleStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("15")).Background(lipgloss.Color("27")).Padding(0, 1)
	pickedStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("0")).Background(lipgloss.Color("45")).Padding(0, 1)
	plainStyle  = lipgloss.NewStyle().Padding(0, 1)
	helpStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
	dangerStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("196"))
)

// choices returns the options for the current choice step, or nil for a
// text step.
func (m model) choices() []string {
	switch m.step {
	case stepDisk:
		out := make([]string, len(m.disks))
		for i, d := range m.disks {
			out[i] = fmt.Sprintf("%-14s %s", d.Path, d.Model)
		}
		return out
	case stepFS:
		return rootFilesystems
	case stepEncrypt:
		return []string{"no", "yes"}
	case stepSwap:
		return []string{"partition", "file", "none"}
	case stepConfirm:
		return []string{"Install", "Cancel"}
	default:
		return nil
	}
}

// enterStep prepares the widget and cursor for the step just entered.
func (m *model) enterStep() {
	if ch := m.choices(); ch != nil {
		m.cursor = 0
		return
	}
	ti := textinput.New()
	ti.Focus()
	switch m.step {
	case stepPassphrase, stepPassword:
		ti.EchoMode = textinput.EchoPassword
	case stepSwapSize:
		ti.SetValue(m.swapSize)
	case stepHostname:
		ti.SetValue("sunlight")
	case stepUsername:
		ti.SetValue("user")
	}
	m.input = ti
}

func (m model) Init() tea.Cmd { return textinput.Blink }

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		return m, cmd
	}

	switch key.String() {
	case "ctrl+c", "esc":
		m.confirmed = false
		return m, tea.Quit
	}

	if m.choices() != nil {
		return m.updateChoice(key)
	}
	return m.updateText(key)
}

func (m model) updateChoice(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	n := len(m.choices())
	switch key.String() {
	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
		}
	case "down", "j":
		if m.cursor < n-1 {
			m.cursor++
		}
	case "enter":
		return m.selectChoice()
	}
	return m, nil
}

func (m model) selectChoice() (tea.Model, tea.Cmd) {
	switch m.step {
	case stepDisk:
		m.diskIdx = m.cursor
	case stepFS:
		m.fs = rootFilesystems[m.cursor]
	case stepEncrypt:
		m.encrypt = m.cursor == 1
	case stepSwap:
		m.swap = []string{"partition", "file", "none"}[m.cursor]
	case stepConfirm:
		m.confirmed = m.cursor == 0
		return m, tea.Quit
	}
	return m.advance()
}

func (m model) updateText(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	if key.String() == "enter" {
		v := m.input.Value()
		switch m.step {
		case stepPassphrase:
			if v == "" {
				return m, nil // an encrypted root needs a passphrase
			}
			m.passphrase = v
		case stepSwapSize:
			m.swapSize = v
		case stepHostname:
			m.hostname = v
		case stepUsername:
			m.login = v
		case stepPassword:
			m.password = v
		}
		return m.advance()
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(key)
	return m, cmd
}

// advance moves to the next step, skipping the ones the current answers
// make irrelevant (no passphrase unless encrypting, no swap size without
// swap).
func (m model) advance() (tea.Model, tea.Cmd) {
	m.step++
	for {
		switch {
		case m.step == stepPassphrase && !m.encrypt:
			m.step++
			continue
		case m.step == stepSwapSize && m.swap == "none":
			m.step++
			continue
		}
		break
	}
	if m.step == stepDone {
		return m, tea.Quit
	}
	m.enterStep()
	return m, textinput.Blink
}

func (m model) View() string {
	if m.step == stepDone {
		return ""
	}

	var title, body string
	switch m.step {
	case stepDisk:
		title = "Install to which disk? — it will be ERASED"
		body = m.choiceView()
	case stepFS:
		title = "Root filesystem"
		body = m.choiceView()
	case stepEncrypt:
		title = "Encrypt the root partition?"
		body = m.choiceView()
	case stepPassphrase:
		title = "LUKS passphrase"
		body = m.input.View()
	case stepSwap:
		title = "Swap"
		body = m.choiceView()
	case stepSwapSize:
		title = "Swap size"
		body = m.input.View()
	case stepHostname:
		title = "Hostname"
		body = m.input.View()
	case stepUsername:
		title = "Admin username"
		body = m.input.View()
	case stepPassword:
		title = "Password for " + m.login
		body = m.input.View()
	case stepConfirm:
		title = "Review"
		body = m.summaryView() + "\n" + m.choiceView()
	}

	help := "↑/↓ move · enter select · esc cancel"
	if m.choices() == nil {
		help = "type · enter next · esc cancel"
	}
	return "\n" + titleStyle.Render(" slinstaller ") + "  " + title + "\n\n" +
		body + "\n\n" + helpStyle.Render(help) + "\n"
}

func (m model) choiceView() string {
	var s string
	for i, c := range m.choices() {
		if i == m.cursor {
			s += pickedStyle.Render("▸ "+c) + "\n"
		} else {
			s += plainStyle.Render("  "+c) + "\n"
		}
	}
	return s
}

func (m model) summaryView() string {
	enc := "no"
	if m.encrypt {
		enc = "yes (LUKS)"
	}
	sw := "none"
	switch m.swap {
	case "partition":
		sw = m.swapSize + " partition"
	case "file":
		sw = m.swapSize + " file"
	}
	return fmt.Sprintf("  %s\n  root %s, fills disk · encryption %s · swap %s\n  hostname %s · admin %s\n",
		dangerStyle.Render(m.disks[m.diskIdx].Path+" will be ERASED"),
		m.fs, enc, sw, m.hostname, m.login)
}
