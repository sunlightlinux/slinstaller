package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/progress"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/sunlightlinux/slinstaller/pkg/config"
	"github.com/sunlightlinux/slinstaller/pkg/controller"
	"github.com/sunlightlinux/slinstaller/pkg/storage"
)

// RunInstall carries out the install inside the full-screen interface:
// a final confirmation, then a progress bar with the current stage and a
// live tail of the command output, all on the same clr-installer-style blue
// screen as the configuration hub. It is used only for the interactive
// full-screen front-end; the file and serial paths install over plain text.
//
// opts carries everything the controller needs (repo, keys, passphrase, …);
// RunInstall supplies its own Runner and Progress to feed the view, so the
// caller leaves those unset. plan is used for the confirmation screen and the
// stage count.
func RunInstall(cfg *config.Config, opts controller.Options, plan *storage.Plan) error {
	ch := make(chan tea.Msg, 1024)
	m := installModel{
		cfg: cfg, opts: opts, disk: plan.Disk, ch: ch,
		total: 12, // the controller's stage count; the first update corrects it
		// Solid teal, to match the menu's selection bar rather than the
		// default blue→pink gradient.
		bar: progress.New(progress.WithSolidFill("#00afaf")),
	}
	final, err := tea.NewProgram(m, tea.WithAltScreen()).Run()
	if err != nil {
		return err
	}
	fm := final.(installModel)
	if fm.cancelled {
		return fmt.Errorf("aborted")
	}
	return fm.err
}

// Messages pushed from the install goroutine into the model.
type (
	logMsg   string
	stageMsg struct {
		done, total int
		name        string
	}
	doneMsg struct{ err error }
)

const (
	phaseConfirm = iota
	phaseRunning
	phaseDone
)

type installModel struct {
	cfg  *config.Config
	opts controller.Options
	disk string
	ch   chan tea.Msg

	phase       int
	bar         progress.Model
	done, total int
	stage       string
	logs        []string
	err         error
	cancelled   bool

	width, height int
}

func (m installModel) Init() tea.Cmd { return nil }

func (m installModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		// Leave room for the two-space indent and the percentage the bar
		// prints, so it does not wrap past the panel.
		m.bar.Width = contentWidth(m.width) - 6
		return m, nil

	case tea.KeyMsg:
		switch m.phase {
		case phaseConfirm:
			switch msg.String() {
			case "enter":
				m.phase = phaseRunning
				return m, m.start()
			case "esc", "q", "ctrl+c":
				m.cancelled = true
				return m, tea.Quit
			}
		case phaseRunning:
			// Keys are ignored while installing: a half-done install must
			// not be abandoned by a stray keypress. Ctrl+C still quits the
			// program (the goroutine is left to finish its current command).
			if msg.String() == "ctrl+c" {
				m.cancelled = true
				return m, tea.Quit
			}
		case phaseDone:
			return m, tea.Quit
		}
		return m, nil

	case stageMsg:
		m.done, m.total, m.stage = msg.done, msg.total, msg.name
		return m, m.wait()

	case logMsg:
		m.logs = append(m.logs, string(msg))
		if len(m.logs) > 500 {
			m.logs = m.logs[len(m.logs)-500:]
		}
		return m, m.wait()

	case doneMsg:
		m.err = msg.err
		m.phase = phaseDone
		return m, nil
	}
	return m, nil
}

// start launches the install in a goroutine, wiring the controller's
// progress and command output into the model's channel, and begins waiting
// on it.
func (m installModel) start() tea.Cmd {
	ch := m.ch
	opts := m.opts
	opts.Log = nil
	opts.Progress = func(done, total int, stage string) {
		ch <- stageMsg{done: done, total: total, name: stage}
	}
	opts.Runner = storage.SinkRunner(func(line string) { ch <- logMsg(line) })

	cfg := m.cfg
	go func() { ch <- doneMsg{err: controller.Install(cfg, opts)} }()
	return m.wait()
}

// wait reads the next event from the install goroutine.
func (m installModel) wait() tea.Cmd {
	ch := m.ch
	return func() tea.Msg { return <-ch }
}

func (m installModel) View() string {
	switch m.phase {
	case phaseConfirm:
		return m.confirmView()
	case phaseDone:
		return m.doneView()
	default:
		return m.runningView()
	}
}

func (m installModel) confirmView() string {
	w := contentWidth(m.width)
	body := []string{
		lineBG.Width(w).Render(""),
		warnText.Width(w).Render("  This will PARTITION and ERASE:"),
		itemLabel.Width(w).Render("    " + m.disk),
		lineBG.Width(w).Render(""),
		lineBG.Width(w).Render("  All data on it will be lost."),
		lineBG.Width(w).Render(""),
		lineBG.Width(w).Render("  " + btnSel.Render("[Enter] Install") + "   " + btnIdle.Render("[Esc] Cancel")),
	}
	return frameScreen(m.width, m.height, body, "[Enter] begin the install · [Esc] go back")
}

func (m installModel) runningView() string {
	w := contentWidth(m.width)
	n := m.done
	if n > m.total {
		n = m.total
	}
	label := fmt.Sprintf("Installing — step %d of %d: %s", min(n+1, m.total), m.total, m.stage)
	if m.stage == "complete" {
		label = "Finishing…"
	}

	var frac float64
	if m.total > 0 {
		frac = float64(m.done) / float64(m.total)
	}

	body := []string{
		itemLabel.Width(w).Render("  " + label),
		lineBG.Width(w).Render(""),
		lineBG.Width(w).Render("  " + m.bar.ViewAs(frac)),
		lineBG.Width(w).Render(""),
		rule(w),
	}
	body = append(body, m.logTail(w)...)
	return frameScreen(m.width, m.height, body, "installing — please wait")
}

// logTail renders the last few command-output lines, as many as the window
// height allows, each clipped to the panel width so nothing wraps.
func (m installModel) logTail(w int) []string {
	rows := m.height - 16 // leave room for the frame, bar and footer
	if rows < 6 {
		rows = 6
	}
	if rows > 20 {
		rows = 20
	}
	start := len(m.logs) - rows
	if start < 0 {
		start = 0
	}
	out := make([]string, 0, rows)
	for _, l := range m.logs[start:] {
		out = append(out, footer.Width(w).Render("  "+clip(l, w-3)))
	}
	for len(out) < rows {
		out = append(out, lineBG.Width(w).Render(""))
	}
	return out
}

func (m installModel) doneView() string {
	w := contentWidth(m.width)
	var body []string
	if m.err == nil {
		body = []string{
			lineBG.Width(w).Render(""),
			itemLabel.Width(w).Render("  Install complete."),
			lineBG.Width(w).Render(""),
			lineBG.Width(w).Render("  Sunlight Linux is installed on " + m.disk + "."),
			lineBG.Width(w).Render("  Remove the install medium and reboot."),
		}
	} else {
		body = []string{
			lineBG.Width(w).Render(""),
			warnText.Width(w).Render("  Install failed."),
			lineBG.Width(w).Render(""),
		}
		for _, l := range wrapErr(m.err.Error(), w-4) {
			body = append(body, lineBG.Width(w).Render("  "+l))
		}
	}
	return frameScreen(m.width, m.height, body, "[Enter] exit")
}

func clip(s string, n int) string {
	if n < 1 {
		n = 1
	}
	r := []rune(strings.TrimRight(s, "\r"))
	if len(r) <= n {
		return string(r)
	}
	if n == 1 {
		return "…"
	}
	return string(r[:n-1]) + "…"
}

func wrapErr(s string, n int) []string {
	if n < 10 {
		n = 10
	}
	var out []string
	for len(s) > n {
		out = append(out, s[:n])
		s = s[n:]
	}
	out = append(out, s)
	return out
}
