package tui

import (
	"bytes"
	"fmt"
	"os/exec"
	"regexp"
	"strings"

	"github.com/charmbracelet/bubbles/progress"
	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/sunlightlinux/slinstaller/pkg/config"
	"github.com/sunlightlinux/slinstaller/pkg/controller"
	"github.com/sunlightlinux/slinstaller/pkg/storage"
)

// doneAction is what the operator chose on the final screen.
type doneAction int

const (
	actionExit doneAction = iota
	actionReboot
	actionPowerOff
	actionBack // return to the configuration wizard
)

// doneButtons are the choices on the final screen, in order.
var doneButtons = []struct {
	label  string
	action doneAction
}{
	{"Reboot", actionReboot},
	{"Power off", actionPowerOff},
	{"Back to installer", actionBack},
	{"Exit to shell", actionExit},
}

// RunInstall carries out the install inside the full-screen interface:
// a final confirmation, then a progress bar over the list of stages, each
// marked done, running (a spinner) or failed, with the full command output
// one Details button away, all on the same clr-installer-style screen
// as the configuration hub. It is used only for the interactive
// full-screen front-end; the file and serial paths install over plain text.
//
// opts carries everything the controller needs (repo, keys, passphrase, …);
// RunInstall supplies its own Runner and Progress to feed the view, so the
// caller leaves those unset. plan is used for the confirmation screen and the
// stage count.
// RunInstall returns back=true when the operator chose "Back to installer"
// on the final screen, so the caller can re-run the configuration wizard.
// Reboot and power-off are carried out here, after the interface is torn
// down; an error aborts as before.
func RunInstall(cfg *config.Config, opts controller.Options, plan *storage.Plan) (back bool, err error) {
	ch := make(chan tea.Msg, 1024)
	m := installModel{
		cfg: cfg, opts: opts, disk: plan.Disk, ch: ch,
		total: 12, // the controller's stage count; the first update corrects it
		// Solid amber, to match the menu's selection bar rather than the
		// default blue→pink gradient.
		bar: progress.New(progress.WithSolidFill("#ffc23d")),
		// ASCII frames: the console font has no braille or box spinner glyphs.
		spin: spinner.New(spinner.WithSpinner(spinner.Line), spinner.WithStyle(stepRun)),
	}
	final, runErr := tea.NewProgram(m, tea.WithAltScreen()).Run()
	if runErr != nil {
		return false, runErr
	}
	fm := final.(installModel)
	if fm.cancelled {
		return false, fmt.Errorf("aborted")
	}
	// The full-screen interface has been torn down by now, so a reboot or
	// power-off chosen on the final screen runs cleanly.
	switch fm.action {
	case actionReboot:
		if e := exec.Command("reboot").Run(); e != nil && fm.err == nil {
			return false, fmt.Errorf("reboot: %w", e)
		}
	case actionPowerOff:
		if e := exec.Command("poweroff").Run(); e != nil && fm.err == nil {
			return false, fmt.Errorf("poweroff: %w", e)
		}
	case actionBack:
		// Caller re-runs the wizard; the install itself may have succeeded or
		// failed, but the operator asked to go back regardless.
		return true, nil
	}
	return false, fm.err
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

// maxLogLines is how much command output the log view keeps.
const maxLogLines = 10000

// A stage on the progress screen and where it has got.
type installStep struct {
	name   string
	status int
}

const (
	stepPending = iota
	stepRunning
	stepOK
	stepFailed
)

// stageLabels are the controller's stages as the progress screen names them.
var stageLabels = map[string]string{
	"pre-install":  "Running pre-install hooks",
	"partition":    "Partitioning the disk",
	"mount":        "Mounting the target",
	"meta-fs":      "Mounting system filesystems",
	"base-install": "Installing the base system",
	"fstab":        "Writing fstab",
	"swapfile":     "Creating the swap file",
	"bootloader":   "Installing the bootloader",
	"first-boot":   "Configuring first boot",
	"network":      "Configuring the network",
	"save-config":  "Saving the install configuration",
	"post-install": "Running post-install hooks",
}

func stageLabel(name string) string {
	if l, ok := stageLabels[name]; ok {
		return l
	}
	return name
}

// Step marks are plain ASCII: the console font has no check or cross glyph
// (it shows an em dash as a filled box), so a tick would come out as one.
var (
	stepOKStyle   = lipgloss.NewStyle().Bold(true).Foreground(tone("#7dff6b", "119", "10")).Background(clrDialog)
	stepFailStyle = lipgloss.NewStyle().Bold(true).Foreground(clrWhite).Background(clrRed)
	stepRun       = lipgloss.NewStyle().Bold(true).Foreground(clrWhite).Background(clrDialog)
	stepWait      = lipgloss.NewStyle().Foreground(clrDim).Background(clrDialog)
)

// newSteps lists every stage, none started yet.
func newSteps() []installStep {
	var steps []installStep
	for _, n := range controller.StageNames() {
		steps = append(steps, installStep{name: n})
	}
	return steps
}

// markStage records that stage done (0-based) of total has begun: the ones
// before it are done, the rest wait. "complete" marks them all done.
func (m *installModel) markStage(done, total int, name string) {
	if total > 0 && total < len(m.steps) { // an install stopped --through a stage
		m.steps = m.steps[:total]
	}
	for i := range m.steps {
		switch {
		case i < done || name == "complete":
			m.steps[i].status = stepOK
		case i == done:
			m.steps[i].status = stepRunning
		default:
			m.steps[i].status = stepPending
		}
	}
}

// failRunning marks the stage that was running when the install failed.
func (m *installModel) failRunning() {
	for i := range m.steps {
		if m.steps[i].status == stepRunning {
			m.steps[i].status = stepFailed
		}
	}
}

func (m installModel) stepMark(status int) string {
	switch status {
	case stepOK:
		return stepOKStyle.Render("[ OK ]")
	case stepFailed:
		return stepFailStyle.Render("[FAIL]")
	case stepRunning:
		return stepRun.Render("[ ") + m.spin.View() + stepRun.Render("  ]")
	}
	return stepWait.Render("[    ]")
}

// logRows is how many log lines the log view shows: what the dialog has
// left under its two header rows and above its Back button.
func (m installModel) logRows() int {
	if r := panelHeight(m.height) - 3 - 2 - 1; r > 3 {
		return r
	}
	return 3
}

// scrollLog handles the log view's keys; it reports whether it used one.
func (m *installModel) scrollLog(key string) bool {
	rows := m.logRows()
	top := max(len(m.logs)-rows, 0) // furthest the view can scroll up
	switch key {
	case "up", "k":
		m.scroll++
	case "down", "j":
		m.scroll--
	case "pgup":
		m.scroll += rows
	case "pgdown":
		m.scroll -= rows
	case "home", "g":
		m.scroll = top
	case "end", "G":
		m.scroll = 0
	case "enter", "esc", "d", "q":
		m.details = false
		return true
	default:
		return false
	}
	m.scroll = min(max(m.scroll, 0), top)
	return true
}

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

	steps   []installStep // every stage, with how far it has got
	spin    spinner.Model // turns beside the running stage
	details bool          // the full command log is on screen
	scroll  int           // lines the log view is scrolled up from the end

	doneCursor int        // selected button on the final screen
	action     doneAction // what the operator chose there

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
		if m.details && msg.String() != "ctrl+c" {
			m.scrollLog(msg.String())
			return m, nil
		}
		switch m.phase {
		case phaseConfirm:
			switch msg.String() {
			case "enter":
				m.phase = phaseRunning
				m.steps = newSteps()
				return m, tea.Batch(m.start(), m.spin.Tick)
			case "esc", "q", "ctrl+c":
				m.cancelled = true
				return m, tea.Quit
			}
		case phaseRunning:
			// Keys do nothing to the install itself: a half-done install
			// must not be abandoned by a stray keypress. Enter (the Details
			// button) or d shows the full log. Ctrl+C still quits the
			// program (the goroutine is left to finish its current command).
			switch msg.String() {
			case "ctrl+c":
				m.cancelled = true
				return m, tea.Quit
			case "enter", "d":
				m.details, m.scroll = true, 0
			}
		case phaseDone:
			switch msg.String() {
			case "left", "h", "up", "k", "shift+tab":
				if m.doneCursor > 0 {
					m.doneCursor--
				} else {
					m.doneCursor = len(doneButtons) - 1
				}
			case "right", "l", "down", "j", "tab":
				if m.doneCursor < len(doneButtons)-1 {
					m.doneCursor++
				} else {
					m.doneCursor = 0
				}
			case "enter":
				m.action = doneButtons[m.doneCursor].action
				return m, tea.Quit
			case "q", "esc":
				m.action = actionExit
				return m, tea.Quit
			case "d":
				m.details, m.scroll = true, 0
			}
		}
		return m, nil

	case spinner.TickMsg:
		if m.phase != phaseRunning {
			return m, nil // stop turning once the install is over
		}
		var cmd tea.Cmd
		m.spin, cmd = m.spin.Update(msg)
		return m, cmd

	case stageMsg:
		m.done, m.total, m.stage = msg.done, msg.total, msg.name
		m.markStage(msg.done, msg.total, msg.name)
		return m, m.wait()

	case logMsg:
		m.logs = append(m.logs, cleanLogLine(string(msg)))
		if len(m.logs) > maxLogLines {
			m.logs = m.logs[len(m.logs)-maxLogLines:]
		} else if m.scroll > 0 {
			m.scroll++ // keep a scrolled-up view on the lines being read
		}
		return m, m.wait()

	case doneMsg:
		m.err = msg.err
		if m.err != nil {
			m.failRunning()
		} else {
			m.markStage(len(m.steps), len(m.steps), "complete")
		}
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
	sink := func(line string) { ch <- logMsg(line) }
	opts := m.opts
	opts.Log = nil
	opts.Progress = func(done, total int, stage string) {
		ch <- stageMsg{done: done, total: total, name: stage}
	}
	opts.Runner = storage.SinkRunner(sink)
	// cryptsetup is fed the passphrase on stdin, so it runs outside Runner;
	// point its output at the log too, or it would leak onto the terminal
	// and corrupt the full-screen view.
	opts.Output = &lineWriter{sink: sink}

	cfg := m.cfg
	go func() { ch <- doneMsg{err: controller.Install(cfg, opts)} }()
	return m.wait()
}

// lineWriter forwards whole lines written to it to a sink, buffering a
// partial last line until its newline arrives. It lets a sub-command that
// writes to an io.Writer (cryptsetup) feed the same log the Runner does.
type lineWriter struct {
	sink func(string)
	buf  []byte
}

func (w *lineWriter) Write(p []byte) (int, error) {
	w.buf = append(w.buf, p...)
	for {
		i := bytes.IndexByte(w.buf, '\n')
		if i < 0 {
			break
		}
		w.sink(strings.TrimRight(string(w.buf[:i]), "\r"))
		w.buf = w.buf[i+1:]
	}
	return len(p), nil
}

// wait reads the next event from the install goroutine.
func (m installModel) wait() tea.Cmd {
	ch := m.ch
	return func() tea.Msg { return <-ch }
}

func (m installModel) View() string {
	if m.details {
		return m.detailsView()
	}
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
	}
	btns := []string{lineBG.Width(w).Render("  " + btnSel.Render("[Enter] Install") + "   " + btnIdle.Render("[Esc] Cancel"))}
	return frameScreenButtons(m.width, m.height, body, btns, "[Enter] begin the install · [Esc] go back")
}

func (m installModel) runningView() string {
	w := contentWidth(m.width)
	n := m.done
	if n > m.total {
		n = m.total
	}
	label := fmt.Sprintf("Installing - step %d of %d", min(n+1, m.total), m.total)
	if m.stage == "complete" {
		label = "Finishing..."
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
	// The stages, one per line, each with its mark; the command output
	// itself is behind the Details button.
	for _, s := range m.steps {
		body = append(body, lineBG.Width(w).Render("  "+m.stepMark(s.status)+stepRun.Render("  ")+
			lineBG.Render(stageLabel(s.name))))
	}
	btns := []string{lineBG.Width(w).Render("  " + btnSel.Render("Details"))}
	return frameScreenButtons(m.width, m.height, body, btns, "[Enter] or [d] show the full log - please wait")
}

// detailsView is the full command log, scrollable, behind the Details
// button. It follows the end as lines arrive unless scrolled up.
func (m installModel) detailsView() string {
	w := contentWidth(m.width)
	rows := m.logRows()
	end := max(len(m.logs)-m.scroll, 0)
	start := max(end-rows, 0)
	head := fmt.Sprintf("Install log: lines %d-%d of %d", min(start+1, end), end, len(m.logs))
	body := []string{itemLabel.Width(w).Render("  " + head), rule(w)}
	for _, l := range m.logs[start:end] {
		body = append(body, footer.Width(w).Render("  "+clip(l, w-3)))
	}
	btns := []string{lineBG.Width(w).Render("  " + btnSel.Render("Back"))}
	return frameScreenButtons(m.width, m.height, body, btns, "[Up/Down/PgUp/PgDn/Home/End] scroll - [Enter] back")
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
		// Show the tail of the command output — the failing command's own
		// error is usually the last thing it printed.
		if n := len(m.logs); n > 0 {
			body = append(body, lineBG.Width(w).Render(""), rule(w),
				footer.Width(w).Render("  last output:"))
			start := n - 8
			if start < 0 {
				start = 0
			}
			for _, l := range m.logs[start:] {
				body = append(body, footer.Width(w).Render("  "+clip(l, w-3)))
			}
		}
	}

	// Action buttons: Reboot / Power off / Exit to shell.
	var btns []string
	for i, b := range doneButtons {
		style := btnIdle
		if i == m.doneCursor {
			style = btnSel
		}
		btns = append(btns, style.Render(b.label))
	}
	return frameScreenButtons(m.width, m.height, body,
		[]string{lineBG.Width(w).Render("  " + strings.Join(btns, "   "))}, "[Tab/Arrows] move · [Enter] select · [d] full log")
}

// ansiSeq matches the terminal escape sequences a command may print (colour,
// cursor movement) so they can be dropped from a log line.
var ansiSeq = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]|\x1b\][^\x07\x1b]*(\x07|\x1b\\)|\x1b[@-_]`)

// cleanLogLine turns a line of command output into what a terminal would
// have shown for it: "\r" returns to the start and what follows redraws the
// line, "\b" steps back one cell, a tab advances to the next multiple of 8,
// and escape sequences and other control characters are dropped.
//
// A "\r" followed by new text is a redraw: progress output clears what is
// left of the old line with an erase sequence, which is dropped here, so the
// line ends where the redraw ends rather than keeping the old tail.
//
// Progress output relies on those: mke2fs writes "Allocating group tables:
// 0/16" and then five "\b", blanks, five "\b" and "done"; xbps redraws its
// download line with "\r". Passed through raw, lipgloss counted the control
// characters as zero-width while the terminal still moved the cursor for
// them, so the line came out shorter than the panel, the rest of the row was
// never drawn over and showed black, and a half-redrawn xbps line read like
// "106KB [avg rate:_2149MB/s]...".
func cleanLogLine(s string) string {
	s = ansiSeq.ReplaceAllString(s, "")
	var line []rune
	col := 0
	redrawn, end := false, 0 // a "\r" came; how far the text after it reached
	put := func(r rune) {
		if redrawn {
			end = max(end, col+1)
		}
		if col < len(line) {
			line[col] = r
		} else {
			for len(line) < col {
				line = append(line, ' ')
			}
			line = append(line, r)
		}
		col++
	}
	for _, r := range s {
		switch {
		case r == '\r':
			col, redrawn, end = 0, true, 0
		case r == '\b':
			if col > 0 {
				col--
			}
		case r == '\t':
			for {
				put(' ')
				if col%8 == 0 {
					break
				}
			}
		case r < 0x20 || r == 0x7f:
			// other control characters print nothing
		default:
			put(r)
		}
	}
	if redrawn && end > 0 && end < len(line) {
		line = line[:end]
	}
	return strings.TrimRight(string(line), " ")
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
