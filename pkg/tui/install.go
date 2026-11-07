package tui

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/bubbles/progress"
	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

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
		bar:   progress.New(progress.WithSolidFill(barFill(termenv.ColorProfile()))),
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

// A stage on the progress screen, where it has got, and its timing.
type installStep struct {
	name   string
	status int
	began  time.Time
	took   time.Duration // set once the stage has finished
}

// installLogPath is where the install log is written on the live system
// (a variable so tests can move it); a full run copies it to targetLogPath
// on the installed one.
var installLogPath = "/tmp/slinstaller.log"

const targetLogPath = "/root/slinstaller.log"

func (m installModel) now() time.Time {
	if m.clock != nil {
		return m.clock()
	}
	return time.Now()
}

// elapsed is how long the install has run, or ran.
func (m installModel) elapsed() time.Duration {
	if m.started.IsZero() {
		return 0
	}
	if !m.finished.IsZero() {
		return m.finished.Sub(m.started)
	}
	return m.now().Sub(m.started)
}

// fmtDur shows a duration as m:ss, or h:mm:ss past the hour.
func fmtDur(d time.Duration) string {
	sec := int(d.Round(time.Second) / time.Second)
	if sec >= 3600 {
		return fmt.Sprintf("%d:%02d:%02d", sec/3600, sec/60%60, sec%60)
	}
	return fmt.Sprintf("%d:%02d", sec/60, sec%60)
}

// spread puts left at the start of a w-wide line and right at its end.
func spread(left, right string, w int) string {
	gap := w - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 1 {
		gap = 1
	}
	return left + lineBG.Render(strings.Repeat(" ", gap)) + right
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

// barFill is the progress bar's colour: solid amber rather than the default
// blue→pink gradient. The bar picks its own colours for the terminal it
// finds (termenv's profile), and on the 16-colour console the nearest to
// amber is bright red, so there it is named outright: bright yellow, which
// the console has as a text colour (the bar is drawn in text, not
// background).
func barFill(p termenv.Profile) string {
	if p == termenv.ANSI {
		return "11"
	}
	return "#ffc23d"
}

// Step marks are plain ASCII: the console font has no check or cross glyph
// (it shows an em dash as a filled box), so a tick would come out as one.
var (
	stepOKStyle   = lipgloss.NewStyle().Bold(true).Foreground(tone("#7dff6b", "119", "10")).Background(clrDialog)
	stepFailStyle = lipgloss.NewStyle().Bold(true).Foreground(clrWhite).Background(clrRed)
	stepRun       = lipgloss.NewStyle().Bold(true).Foreground(tone("#fff35c", "227", "11")).Background(clrDialog)
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
	now := m.now()
	for i := range m.steps {
		st := &m.steps[i]
		switch {
		case i < done || name == "complete":
			if st.status == stepRunning {
				st.took = now.Sub(st.began)
			}
			st.status = stepOK
		case i == done:
			if st.status != stepRunning {
				st.began = now
			}
			st.status = stepRunning
		default:
			st.status = stepPending
		}
	}
}

// failRunning marks the stage that was running when the install failed.
func (m *installModel) failRunning() {
	now := m.now()
	for i := range m.steps {
		if m.steps[i].status == stepRunning {
			m.steps[i].status = stepFailed
			m.steps[i].took = now.Sub(m.steps[i].began)
		}
	}
}

// stepTime is the time shown beside a stage: how long it took, or how long
// it has been running.
func (m installModel) stepTime(st installStep) string {
	switch st.status {
	case stepOK, stepFailed:
		return fmtDur(st.took)
	case stepRunning:
		return fmtDur(m.now().Sub(st.began))
	}
	return ""
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

// logRows is how many rows the log view shows: what the dialog has left
// under its two header rows and above the blank row and its Back button.
func (m installModel) logRows() int {
	if r := panelHeight(m.height) - 3 - 2 - 2; r > 3 {
		return r
	}
	return 3
}

// What a log line is, for its colour in the log view.
const (
	logPlain = iota
	logStage // "==> partition", where a stage begins
	logWarn
	logError
)

var (
	logErrorRe = regexp.MustCompile(`(?i)\b(error|errors|failed|failure|fatal|unable to|cannot|can't|denied)\b`)
	logWarnRe  = regexp.MustCompile(`(?i)\bwarn(ing)?\b`)

	// xbpsProgressRe is a download progress line from xbps-install,
	// "foo-1.0_1.x86_64.xbps: [1038KB 0%] 118MB/s ETA: 00m00s". Each is
	// followed by the download's final line ("1038KB [avg rate: ...]"),
	// so the log view leaves them out; the log file keeps them.
	xbpsProgressRe = regexp.MustCompile(`^\S+: \[[0-9.]+[KMGT]?B +[0-9]+%\] .*ETA: \S+$`)

	logStageStyle = lipgloss.NewStyle().Bold(true).Foreground(clrInk).Background(tone("#bcbcbc", "250", "7"))
)

func logKind(l string) int {
	switch {
	case strings.HasPrefix(l, "==> "):
		return logStage
	case logErrorRe.MatchString(l):
		return logError
	case logWarnRe.MatchString(l):
		return logWarn
	}
	return logPlain
}

// A row of the log view: one line, or a piece of one too long for the panel.
type logRow struct {
	text string
	kind int
}

// logWidth is how much text a log row holds.
func (m installModel) logWidth() int { return contentWidth(m.width) - 3 }

// wrapLog splits a line into rows of at most n cells; the rows after the
// first are indented so a continued line reads as one.
func wrapLog(l string, n int) []string {
	r := []rune(l)
	if len(r) <= n || n < 8 {
		return []string{l}
	}
	out := []string{string(r[:n])}
	for r = r[n:]; len(r) > 0; {
		k := min(len(r), n-2)
		out = append(out, "  "+string(r[:k]))
		r = r[k:]
	}
	return out
}

// logView is the whole log as the view's rows.
func (m installModel) logView() []logRow {
	n := m.logWidth()
	var rows []logRow
	for _, l := range m.logs {
		k := logKind(l)
		for _, piece := range wrapLog(l, n) {
			rows = append(rows, logRow{piece, k})
		}
	}
	return rows
}

// scrollLog handles the log view's keys; it reports whether it used one.
func (m *installModel) scrollLog(key string) bool {
	rows := m.logRows()
	top := max(len(m.logView())-rows, 0) // furthest the view can scroll up
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
	scroll  int           // rows the log view is scrolled up from the end
	logPath string        // the log file on the live system; "" if none

	started, finished time.Time        // the install's start and end
	clock             func() time.Time // time.Now; tests fix it

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
				m.started = m.now()
				cmd := m.start()
				return m, tea.Batch(cmd, m.spin.Tick)
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
		l := cleanLogLine(string(msg))
		if xbpsProgressRe.MatchString(l) {
			return m, m.wait()
		}
		m.logs = append(m.logs, l)
		if len(m.logs) > maxLogLines {
			m.logs = m.logs[len(m.logs)-maxLogLines:]
		} else if m.scroll > 0 {
			// keep a scrolled-up view on the rows being read
			m.scroll += len(wrapLog(l, m.logWidth()))
		}
		return m, m.wait()

	case doneMsg:
		m.finished = m.now()
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
func (m *installModel) start() tea.Cmd {
	ch := m.ch
	// Every line also goes to the log file, written from the install's own
	// goroutine so it is complete when the controller copies it to the
	// target. Without a file the install still runs; the view has the log.
	var mu sync.Mutex
	f, err := os.OpenFile(installLogPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err == nil {
		m.logPath = installLogPath
		fmt.Fprintf(f, "slinstaller install log, started %s\n", m.now().Format(time.RFC1123))
	}
	sink := func(line string) {
		if f != nil {
			mu.Lock()
			fmt.Fprintln(f, cleanLogLine(line))
			mu.Unlock()
		}
		ch <- logMsg(line)
	}
	opts := m.opts
	if f != nil {
		opts.LogFile = installLogPath
	}
	// The stage markers ("==> partition") go into the log as well.
	opts.Log = &lineWriter{sink: sink}
	opts.Progress = func(done, total int, stage string) {
		ch <- stageMsg{done: done, total: total, name: stage}
	}
	opts.Runner = storage.SinkRunner(sink)
	// cryptsetup is fed the passphrase on stdin, so it runs outside Runner;
	// point its output at the log too, or it would leak onto the terminal
	// and corrupt the full-screen view.
	opts.Output = &lineWriter{sink: sink}

	cfg, now := m.cfg, m.now
	go func() {
		err := controller.Install(cfg, opts)
		if f != nil {
			mu.Lock()
			fmt.Fprintf(f, "slinstaller install log, ended %s: %v\n", now().Format(time.RFC1123), errOrOK(err))
			f.Close()
			f = nil
			mu.Unlock()
		}
		ch <- doneMsg{err: err}
	}()
	return m.wait()
}

func errOrOK(err error) any {
	if err == nil {
		return "ok"
	}
	return err
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
	btns := []string{lineBG.Width(w).Render("  " + btnSel.Render("[Enter] Install") + gap(3) + btnIdle.Render("[Esc] Cancel"))}
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
		lineBG.Width(w).Render(spread(itemLabel.Render("  "+label), stepWait.Render("elapsed "+fmtDur(m.elapsed())), w)),
		lineBG.Width(w).Render(""),
		lineBG.Width(w).Render("  " + m.bar.ViewAs(frac)),
		lineBG.Width(w).Render(""),
		rule(w),
	}
	// The stages, one per line, each with its mark; the command output
	// itself is behind the Details button.
	for _, s := range m.steps {
		// The running stage stands out: its mark, spinner and name are
		// bright yellow and bold, the rest plain.
		label := lineBG.Render(stageLabel(s.name))
		if s.status == stepRunning {
			label = stepRun.Render(stageLabel(s.name))
		}
		left := lineBG.Render("  ") + m.stepMark(s.status) + lineBG.Render("  ") + label
		body = append(body, lineBG.Width(w).Render(spread(left, stepWait.Render(m.stepTime(s)), w)))
	}
	btns := []string{lineBG.Width(w).Render("  " + btnSel.Render("Details"))}
	return frameScreenButtons(m.width, m.height, body, btns, "[Enter] or [d] show the full log - please wait")
}

// detailsView is the full command log, scrollable, behind the Details
// button. It follows the end as lines arrive unless scrolled up.
func (m installModel) detailsView() string {
	w := contentWidth(m.width)
	rows := m.logRows()
	all := m.logView()
	end := max(len(all)-m.scroll, 0)
	start := max(end-rows, 0)

	head := "Install log"
	if m.logPath != "" {
		head += " (" + m.logPath + ")"
	}
	where := "end"
	if m.scroll > 0 {
		where = fmt.Sprintf("%d%%", end*100/max(len(all), 1))
	}
	body := []string{
		lineBG.Width(w).Render(spread(itemLabel.Render("  "+head), stepWait.Render(where), w)),
		rule(w),
	}
	for _, r := range all[start:end] {
		text := "  " + r.text
		switch r.kind {
		case logStage:
			body = append(body, lineBG.Width(w).Render(logStageStyle.Width(w-1).Render(text)))
		case logError:
			body = append(body, lineBG.Width(w).Render(stepFailStyle.Width(w-1).Render(text)))
		case logWarn:
			body = append(body, warnText.Width(w).Render(text))
		default:
			body = append(body, footer.Width(w).Render(text))
		}
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
			itemLabel.Width(w).Render("  Install complete in " + fmtDur(m.elapsed()) + "."),
			lineBG.Width(w).Render(""),
			lineBG.Width(w).Render("  Sunlight Linux is installed on " + m.disk + "."),
			lineBG.Width(w).Render("  Remove the install medium and reboot."),
		}
		if m.logPath != "" && m.opts.Through == "" {
			body = append(body, lineBG.Width(w).Render(""),
				footer.Width(w).Render("  The install log is saved in "+targetLogPath+"."))
		}
	} else {
		body = []string{
			lineBG.Width(w).Render(""),
			warnText.Width(w).Render("  Install failed after " + fmtDur(m.elapsed()) + "."),
			lineBG.Width(w).Render(""),
		}
		for _, l := range wrapErr(m.err.Error(), w-4) {
			body = append(body, lineBG.Width(w).Render("  "+l))
		}
		if m.logPath != "" {
			body = append(body, footer.Width(w).Render("  The full log is in "+m.logPath+" on this live system."))
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
		[]string{lineBG.Width(w).Render("  " + strings.Join(btns, gap(3)))}, "[Tab/Arrows] move · [Enter] select · [d] full log")
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
	// ASCII dots: the console font has no ellipsis glyph.
	if n <= 3 {
		return strings.Repeat(".", n)
	}
	return string(r[:n-3]) + "..."
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
