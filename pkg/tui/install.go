package tui

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/charmbracelet/bubbles/progress"
	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"github.com/sunlightlinux/slinstaller/pkg/blockdev"
	"github.com/sunlightlinux/slinstaller/pkg/config"
	"github.com/sunlightlinux/slinstaller/pkg/controller"
	"github.com/sunlightlinux/slinstaller/pkg/storage"
	"github.com/sunlightlinux/slinstaller/pkg/system"
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
	{"Back to menu", actionBack},
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
// RunInstall returns back=true when the operator chose "Back to menu"
// on the final screen, so the caller can re-run the configuration wizard.
// Reboot and power-off are carried out here, after the interface is torn
// down; an error aborts as before.
func RunInstall(cfg *config.Config, opts controller.Options, plan *storage.Plan) (next string, err error) {
	ch := make(chan tea.Msg, 1024)
	stop := make(chan struct{})
	opts.Stop = stop
	devs, _ := blockdev.List() // best effort: sizes and models for the confirmation
	result := make(chan error, 1)
	m := installModel{
		result:  result,
		confirm: confirmLines(plan, devs),
		cfg:     cfg, opts: opts, disk: plan.Disk, ch: ch, stop: stop,
		total: 12, // the controller's stage count; the first update corrects it
		bar:   progress.New(progress.WithSolidFill(barFill(termenv.ColorProfile()))),
		// ASCII frames: the console font has no braille or box spinner glyphs.
		spin: spinner.New(spinner.WithSpinner(spinner.Line), spinner.WithStyle(stepRun)),
	}
	// Without bubbletea's own signal handler: it reacts to SIGTERM with a
	// blocking send that the SIGTERM abandonChildren sends its own group
	// could leave stuck as the program quits — Run never returned and the
	// terminal stayed raw. A SIGTERM from outside is taken here instead,
	// as the Ctrl+C it stands for (stop after this step; again, leave).
	p := tea.NewProgram(m, tea.WithAltScreen(), tea.WithoutSignalHandler())
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGTERM)
	defer func() { signal.Stop(sigs); close(sigs) }()
	go func() {
		for range sigs {
			p.Send(tea.KeyMsg{Type: tea.KeyCtrlC})
		}
	}()
	final, runErr := p.Run()
	if runErr != nil {
		return "", runErr
	}
	// bubbletea recovers a panic in Update or View and returns no model:
	// the install may still be running, so its commands are ended rather
	// than left behind.
	fm, ok := final.(installModel)
	if !ok {
		abandonChildren()
		return "", fmt.Errorf("the installer's screen failed; the install was abandoned")
	}
	if fm.cancelled && fm.stopping {
		return "", awaitAbandoned(ch, result)
	}
	if fm.cancelled {
		return "", fmt.Errorf("aborted")
	}
	// What the operator chose on the final screen is the caller's to do,
	// after it has reported the result: reboot or power off ran here
	// before, racing the report and the diagnostics, and "Back to menu"
	// returned no error, so a failed install was reported a success and
	// left no diagnostics.
	switch fm.action {
	case actionReboot:
		return "reboot", fm.err
	case actionPowerOff:
		return "poweroff", fm.err
	case actionBack:
		return "back", fm.err
	}
	return "", fm.err
}

// abandonChildren ends the commands the install is running when the
// operator leaves mid-step. In the full-screen view Ctrl+C is a key, not
// a signal, so nothing else would: xbps, mkfs or dd went on writing to a
// disk still mounted after the program had exited. It signals the
// installer's process group, which its commands share — only when the
// installer leads that group, so nothing it did not start is touched —
// with the installer itself catching the signal.
var abandonChildren = func() {
	if syscall.Getpgrp() != os.Getpid() {
		return
	}
	// Caught, not ignored: an ignored signal stays ignored in every command
	// started afterwards (the next step's xbps-install would no longer stop
	// on SIGTERM), while a caught one is back to its default across exec.
	// The installer's own copy lands in sigSink and is left there.
	signal.Notify(sigSink, syscall.SIGTERM)
	_ = syscall.Kill(0, syscall.SIGTERM)
}

// sigSink takes the SIGTERM abandonChildren sends its own process group.
var sigSink = make(chan os.Signal, 1)

// awaitAbandoned waits, for a while, for an install the operator left
// mid-step to wind down — its commands were ended (abandonChildren), so
// the step fails and the controller unmounts — taking the messages it
// still sends so it is not blocked on them. Returning at once left that
// cleanup racing the program's exit, and the "aborted" of a cancelled
// confirmation skipped the diagnostics this failure deserves.
// abandonWait is how long awaitAbandoned waits (a variable for tests).
var abandonWait = 15 * time.Second

func awaitAbandoned(ch chan tea.Msg, result chan error) error {
	timeout := time.After(abandonWait)
	for {
		select {
		case err := <-result:
			if err == nil {
				return nil // it had finished, and well, before the operator left
			}
			return fmt.Errorf("left by the operator during the install: %w", err)
		case <-ch:
		case <-timeout:
			return fmt.Errorf("left by the operator during the install; it did not wind down within 15s, so the target may still be mounted")
		}
	}
}

// AbandonChildren is abandonChildren for the text flow, whose second
// SIGTERM (a service manager, timeout(1)) reaches the installer alone.
func AbandonChildren() { abandonChildren() }

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

// InstallLogPath is that path, for the text flow to write its log to.
func InstallLogPath() string { return installLogPath }

// OpenInstallLog opens the log at path for writing, empty, mode 0600. The
// default path is in /tmp, where any user can plant something there first
// for root to write through: a link, a hard link to another file, a file
// of their own they can read. A link is never followed, and whatever is at
// path that is not a regular file of this user's alone is removed and the
// log made afresh. (In a sticky /tmp, a file root owns stays root's.)
func OpenInstallLog(path string) (*os.File, error) {
	if fi, err := os.Lstat(path); err == nil {
		st, _ := fi.Sys().(*syscall.Stat_t)
		if fi.Mode().IsRegular() && st != nil && int(st.Uid) == os.Geteuid() && st.Nlink == 1 {
			f, err := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC|syscall.O_NOFOLLOW, 0o600)
			if err == nil {
				err = f.Chmod(0o600)
			}
			return f, err
		}
		if err := os.Remove(path); err != nil {
			return nil, err
		}
	}
	return os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY|syscall.O_NOFOLLOW, 0o600)
}

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
// failedOrStopped is how the final screen says the install ended without
// completing.
func failedOrStopped(err error) string {
	if errors.Is(err, controller.ErrStopped) {
		return "Install stopped"
	}
	return "Install failed"
}

// finishRunning marks the stages still running as done.
func (m *installModel) finishRunning() {
	now := m.now()
	for i := range m.steps {
		if m.steps[i].status == stepRunning {
			m.steps[i].status = stepOK
			m.steps[i].took = now.Sub(m.steps[i].began)
		}
	}
}

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
// The panel is never shorter than 18 rows, so there are always some.
func (m installModel) logRows() int {
	return panelHeight(m.height) - 3 - 2 - 2
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
	line int // the log line it comes from, from 0
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
	if len(m.lineRows) == len(m.logs) && m.lineWidth == n {
		for i, lr := range m.lineRows {
			for _, r := range lr {
				r.line = i
				rows = append(rows, r)
			}
		}
		return rows
	}
	for i, l := range m.logs {
		rows = append(rows, lineToRows(l, i, n)...)
	}
	return rows
}

// lineToRows is one log line as the view shows it: wrapped to width n,
// each piece carrying the line's kind.
func lineToRows(l string, i, n int) []logRow {
	k := logKind(l)
	var rows []logRow
	for _, piece := range wrapLog(l, n) {
		rows = append(rows, logRow{piece, k, i})
	}
	return rows
}

// rewrapLog makes lineRows anew for the current width.
func (m *installModel) rewrapLog() {
	n := m.logWidth()
	m.lineRows = make([][]logRow, len(m.logs))
	for i, l := range m.logs {
		m.lineRows[i] = lineToRows(l, i, n)
	}
	m.lineWidth = n
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
	// lineRows holds each line of logs wrapped and classified, made once
	// when the line arrives (at the width lineWidth), so the log view does
	// not run two regexps and a rune wrap over 10000 lines on every frame:
	// with the Details view open that cost 86 ms a frame, and the screen
	// could no longer take lines as fast as the install made them.
	lineRows  [][]logRow
	lineWidth int
	err       error
	cancelled bool

	// stop is closed by Ctrl+C while the install runs: the controller stops
	// before its next stage and cleans up, and the screen waits for that.
	stop     chan struct{}
	stopping bool

	// What the confirmation lists (confirmLines), and whether its Install
	// button is the one selected. Cancel is, to begin with: one stray
	// Enter must not be what erases a disk.
	confirm   []string
	onInstall bool

	// result gets the install's error when controller.Install returns,
	// for RunInstall to wait on after the operator left mid-step.
	result chan error

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
		m.rewrapLog()
		return m, nil

	case tea.KeyMsg:
		if m.details && msg.String() != "ctrl+c" {
			m.scrollLog(msg.String())
			return m, nil
		}
		switch m.phase {
		case phaseConfirm:
			switch msg.String() {
			case "left", "right", "tab", "shift+tab", "h", "l":
				m.onInstall = !m.onInstall
			case "enter":
				if !m.onInstall {
					m.cancelled = true
					return m, tea.Quit
				}
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
			// button) or d shows the full log. Ctrl+C asks the install to
			// stop once the step running is done; it then unmounts and the
			// final screen says where it stopped. Quitting here instead left
			// the command running orphaned, the target mounted and no
			// diagnostics written.
			switch msg.String() {
			case "ctrl+c":
				if !m.stopping && m.stop != nil {
					m.stopping = true
					close(m.stop)
					break
				}
				// Pressed again while stopping: the way out of a step that
				// hangs (a download that never ends), which the stop
				// between steps would wait on for ever. The step running
				// is abandoned.
				if m.stopping {
					// Once: the installer's own SIGTERM comes back through
					// the bridge as one more Ctrl+C, and a second round
					// would hit the cleanup's commands.
					if m.cancelled {
						return m, nil
					}
					abandonChildren()
					m.cancelled = true
					return m, tea.Quit
				}
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
			case "q", "esc", "ctrl+c": // ctrl+c: also a SIGTERM from outside
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
		if len(m.lineRows) != len(m.logs) || m.lineWidth != m.logWidth() {
			m.rewrapLog()
		}
		rows := lineToRows(l, len(m.logs), m.lineWidth)
		m.logs = append(m.logs, l)
		m.lineRows = append(m.lineRows, rows)
		if m.scroll > 0 {
			// keep a scrolled-up view on the rows being read
			m.scroll += len(rows)
		}
		if len(m.logs) > maxLogLines {
			m.logs = m.logs[len(m.logs)-maxLogLines:]
			m.lineRows = m.lineRows[len(m.lineRows)-maxLogLines:]
			// The oldest rows are gone: no further up than the top.
			if top := len(m.logView()) - m.logRows(); m.scroll > top {
				m.scroll = max(top, 0)
			}
		}
		return m, m.wait()

	case doneMsg:
		m.finished = m.now()
		m.err = msg.err
		switch {
		case errors.Is(m.err, controller.ErrStopped):
			// Stopped between stages: the one still marked running had
			// finished (the stop is checked before the next one is
			// announced), so it is done, not failed.
			m.finishRunning()
		case m.err != nil:
			m.failRunning()
		default:
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
	f, err := OpenInstallLog(installLogPath)
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
	// The caller's own progress hook (a report sent elsewhere) still runs.
	outer := opts.Progress
	opts.Progress = func(done, total int, stage string) {
		if outer != nil {
			outer(done, total, stage)
		}
		ch <- stageMsg{done: done, total: total, name: stage}
	}
	opts.Runner = storage.SinkRunner(sink)
	// cryptsetup is fed the passphrase on stdin, so it runs outside Runner;
	// point its output at the log too, or it would leak onto the terminal
	// and corrupt the full-screen view.
	opts.Output = &lineWriter{sink: sink}

	cfg, now, result := m.cfg, m.now, m.result
	go func() {
		err := controller.Install(cfg, opts)
		if result != nil {
			result <- err
		}
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

// LogWriter returns a writer that keeps w a readable log of what passes
// through it: each line reduced to what a terminal would have shown for it
// (cleanLogLine). The text flow writes its log file through it.
func LogWriter(w io.Writer) io.Writer {
	return &lineWriter{sink: func(line string) { fmt.Fprintln(w, cleanLogLine(line)) }}
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
	lines := m.confirm
	if len(lines) == 0 {
		lines = []string{"!This will PARTITION and ERASE:", "    " + m.disk, "", "All data on it will be lost."}
	}
	body := []string{lineBG.Width(w).Render("")}
	for _, l := range lines {
		if t, ok := strings.CutPrefix(l, "!"); ok {
			body = append(body, warnText.Width(w).Render("  "+t))
		} else {
			body = append(body, lineBG.Width(w).Render("  "+l))
		}
	}
	btns := []string{buttonLine(w, button{"Install", pick(m.onInstall)}, button{"Cancel", pick(!m.onInstall)})}
	return frameScreenButtons(m.width, m.height, body, btns, "[Tab] choose · [Enter] press the button · [Esc] cancel and leave")
}

// confirmLines is what the confirmation says will happen to the disks, as
// the text flow's retyped confirmation says it: each disk erased, with its
// size and model so two alike can be told apart, or — keeping the table —
// each partition shrunk, created, kept or formatted. A line starting "!"
// is a warning.
func confirmLines(plan *storage.Plan, devs []blockdev.Device) []string {
	lines := confirmDiskLines(plan, devs)
	if secureBoot() {
		lines = append(lines, "", "!Secure Boot is on: the firmware will not start the unsigned GRUB",
			"!this installs. Turn it off in the firmware setup before rebooting.")
	}
	return lines
}

// secureBoot is system.SecureBoot (a variable for tests).
var secureBoot = system.SecureBoot

func confirmDiskLines(plan *storage.Plan, devs []blockdev.Device) []string {
	describe := func(path string) string {
		if d := blockdev.FindByPath(devs, path); d != nil && d.Size > 0 {
			return strings.TrimSpace(fmt.Sprintf("%-14s %-8s %s", path, gib(d.Size), d.Model))
		}
		return path
	}
	if plan.Wipe {
		lines := []string{"!This will PARTITION and ERASE:"}
		for _, d := range plan.DiskList() {
			lines = append(lines, "    "+describe(d))
		}
		return append(lines, "", "All data on them will be lost.")
	}
	lines := []string{"!This keeps the partition table of " + describe(plan.Disk) + ", and:"}
	shrink := false
	for _, p := range plan.Parts {
		if p.LV || p.Raid {
			continue
		}
		mnt := p.Mountpoint
		if mnt == "" {
			mnt = "-"
		}
		if p.ResizeMiB > 0 {
			shrink = true
			lines = append(lines, fmt.Sprintf("    SHRINK %s to %s (its data is kept)", p.Device, gib(p.ResizeMiB<<20)))
		}
		switch {
		case p.Create:
			lines = append(lines, fmt.Sprintf("    CREATE %s as %s -> %s", p.Device, p.Filesystem, mnt))
		case p.Preserve:
			lines = append(lines, fmt.Sprintf("    KEEP   %s -> %s", p.Device, mnt))
		default:
			lines = append(lines, fmt.Sprintf("    FORMAT %s as %s -> %s", p.Device, p.Filesystem, mnt))
		}
	}
	lines = append(lines, "", "What is on the partitions marked FORMAT is lost.")
	if shrink {
		lines = append(lines, "!A shrink that is interrupted can lose the partition - have a backup.")
	}
	return lines
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
	btns := []string{buttonLine(w, button{"Details", btnSel})}
	hint := "[Enter] or [d] show the full log - [Ctrl+C] stop after this step"
	if m.stopping {
		hint = "Stopping after the current step - [Ctrl+C] again leaves now, abandoning it"
	}
	return frameScreenButtons(m.width, m.height, body, btns, hint)
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
	// Where the view is: at the end it follows the log as lines arrive;
	// scrolled up it holds still, at the line shown last.
	where := "following new lines"
	switch {
	case m.scroll > 0 && end > 0:
		where = fmt.Sprintf("line %d of %d", all[end-1].line+1, len(m.logs))
		if m.phase == phaseRunning {
			where = "paused at " + where
		}
	case m.phase != phaseRunning:
		where = "end of log"
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
	btns := []string{buttonLine(w, button{"Back", btnSel})}
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
			warnText.Width(w).Render("  " + failedOrStopped(m.err) + " after " + fmtDur(m.elapsed()) + "."),
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

	// Action buttons: Reboot / Power off / Back to menu / Exit to shell.
	var btns []button
	for i, b := range doneButtons {
		btns = append(btns, button{b.label, pick(i == m.doneCursor)})
	}
	return frameScreenButtons(m.width, m.height, body,
		[]string{buttonLine(w, btns...)}, "[Tab/Arrows] move · [Enter] select · [d] full log")
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
		} else { // col never passes the end: it only moves by put, \b and \r
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
	// By character, not byte: an error naming a path may not be ASCII, and
	// half a character draws as garbage.
	r := []rune(s)
	var out []string
	for len(r) > n {
		out = append(out, string(r[:n]))
		r = r[n:]
	}
	return append(out, string(r))
}
