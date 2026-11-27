package tui

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/charmbracelet/bubbles/progress"
	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/sunlightlinux/slinstaller/pkg/blockdev"
	"github.com/sunlightlinux/slinstaller/pkg/config"
	"github.com/sunlightlinux/slinstaller/pkg/controller"
	"github.com/sunlightlinux/slinstaller/pkg/storage"
)

func newInstallModel() installModel {
	return installModel{width: 80, height: 24, disk: "/dev/sda", ch: make(chan tea.Msg, 64),
		bar: progress.New(), spin: spinner.New(spinner.WithSpinner(spinner.Line))}
}

func stepInstall(m installModel, msg tea.Msg) (installModel, tea.Cmd) {
	nm, cmd := m.Update(msg)
	return nm.(installModel), cmd
}

func keyNamed(s string) tea.KeyMsg {
	switch s {
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "ctrl+c":
		return tea.KeyMsg{Type: tea.KeyCtrlC}
	case "up":
		return tea.KeyMsg{Type: tea.KeyUp}
	case "down":
		return tea.KeyMsg{Type: tea.KeyDown}
	case "left":
		return tea.KeyMsg{Type: tea.KeyLeft}
	case "right":
		return tea.KeyMsg{Type: tea.KeyRight}
	case "pgup":
		return tea.KeyMsg{Type: tea.KeyPgUp}
	case "pgdown":
		return tea.KeyMsg{Type: tea.KeyPgDown}
	case "home":
		return tea.KeyMsg{Type: tea.KeyHome}
	case "end":
		return tea.KeyMsg{Type: tea.KeyEnd}
	case "tab":
		return tea.KeyMsg{Type: tea.KeyTab}
	case "shift+tab":
		return tea.KeyMsg{Type: tea.KeyShiftTab}
	}
	return runes(s)
}

// The confirmation is the last stop before the disk is erased: it names the
// disk, and only Enter starts the install.
func TestInstallConfirmation(t *testing.T) {
	m := newInstallModel()
	if m.Init() != nil {
		t.Error("Init should start nothing: the install waits for confirmation")
	}
	view := stripANSIForTest(m.View())
	if !strings.Contains(view, "This will PARTITION and ERASE:") || !strings.Contains(view, "/dev/sda") {
		t.Errorf("the confirmation does not name what is erased:\n%s", view)
	}
	for _, k := range []string{"esc", "q", "ctrl+c"} {
		got, cmd := stepInstall(m, keyNamed(k))
		if !got.cancelled || cmd == nil || got.phase != phaseConfirm {
			t.Errorf("%s at the confirmation: cancelled=%v phase=%d", k, got.cancelled, got.phase)
		}
	}
	// Any other key neither starts nor cancels.
	if got, cmd := stepInstall(m, keyNamed("y")); got.phase != phaseConfirm || got.cancelled || cmd != nil {
		t.Error("a stray key acted on the confirmation")
	}
	// Cancel is selected to begin with: Enter alone leaves, erasing nothing.
	if got, cmd := stepInstall(m, keyNamed("enter")); !got.cancelled || cmd == nil || got.phase != phaseConfirm {
		t.Errorf("Enter on the default button: cancelled=%v phase=%d", got.cancelled, got.phase)
	}
	// Tab moves to Install and back.
	if got, _ := stepInstall(m, keyNamed("tab")); !got.onInstall {
		t.Error("Tab did not select Install")
	} else if back, _ := stepInstall(got, keyNamed("tab")); back.onInstall {
		t.Error("Tab did not go back to Cancel")
	}
	m, _ = stepInstall(m, keyNamed("tab"))

	// Enter on Install starts it. The config here has no layout, so the install fails
	// before any command: what is checked is that it was started, timed and
	// reported.
	defer func(p string) { installLogPath = p }(installLogPath)
	installLogPath = filepath.Join(t.TempDir(), "install.log")
	m.cfg = &config.Config{}
	m, cmd := stepInstall(m, keyNamed("enter"))
	if m.phase != phaseRunning || cmd == nil || m.started.IsZero() || len(m.steps) != len(controller.StageNames()) {
		t.Fatalf("Enter did not start the install: phase=%d steps=%d", m.phase, len(m.steps))
	}
	for m.phase != phaseDone {
		m, _ = stepInstall(m, m.wait()())
	}
	if m.err == nil || m.finished.IsZero() {
		t.Errorf("the failed install was not reported: err=%v", m.err)
	}
}

func TestInstallIgnoresKeysWhileRunning(t *testing.T) {
	m := newInstallModel()
	m.phase, m.steps = phaseRunning, newSteps()
	// A stray key must not abandon a half-done install.
	for _, k := range []string{"q", "esc", "x", "tab"} {
		if got, cmd := stepInstall(m, keyNamed(k)); got.cancelled || cmd != nil || got.phase != phaseRunning {
			t.Errorf("%s while installing changed the install", k)
		}
	}
	// Ctrl+C asks the install to stop after the step running, and stays
	// on screen until it has; pressed again it does no more (and does not
	// close the channel twice).
	m.stop = make(chan struct{})
	got, cmd := stepInstall(m, keyNamed("ctrl+c"))
	if got.cancelled || cmd != nil || !got.stopping || got.phase != phaseRunning {
		t.Errorf("Ctrl+C while installing: cancelled=%v stopping=%v cmd=%v", got.cancelled, got.stopping, cmd != nil)
	}
	select {
	case <-m.stop:
	default:
		t.Error("Ctrl+C did not ask the install to stop")
	}
	// Pressed again it leaves at once — the way out of a step that hangs —
	// ending the commands the step was running.
	saved := abandonChildren
	defer func() { abandonChildren = saved }()
	abandoned := false
	abandonChildren = func() { abandoned = true }
	if again, cmd := stepInstall(got, keyNamed("ctrl+c")); !again.cancelled || cmd == nil || !abandoned {
		t.Errorf("a second Ctrl+C: left=%v, commands ended=%v", again.cancelled, abandoned)
	}
	if !strings.Contains(got.runningView(), "Stopping after the current step") {
		t.Error("the screen does not say it is stopping")
	}
	if got, _ := stepInstall(m, keyNamed("d")); !got.details {
		t.Error("d did not open the log")
	}
	// The spinner turns while installing and stops once it is over.
	if _, cmd := stepInstall(m, m.spin.Tick()); cmd == nil {
		t.Error("the spinner stopped during the install")
	}
	m.phase = phaseDone
	if _, cmd := stepInstall(m, m.spin.Tick()); cmd != nil {
		t.Error("the spinner kept turning after the install")
	}
	// An unknown message changes nothing.
	if got, cmd := stepInstall(m, struct{}{}); cmd != nil || got.phase != phaseDone {
		t.Error("an unknown message was acted on")
	}
}

func TestInstallWindowSizeAndSmallScreens(t *testing.T) {
	m := newInstallModel()
	m, _ = stepInstall(m, tea.WindowSizeMsg{Width: 100, Height: 30})
	if m.width != 100 || m.height != 30 || m.bar.Width != contentWidth(100)-6 {
		t.Errorf("size not taken: %dx%d bar %d", m.width, m.height, m.bar.Width)
	}
	// On a very short screen the panel keeps its smallest size, and the log
	// its rows.
	m.height = 6
	if m.logRows() != 11 {
		t.Errorf("logRows on a short screen = %d, want 11", m.logRows())
	}
	// Two labels that do not fit a line keep a space between them.
	if got := stripANSIForTest(spread("left-label", "right-label", 5)); got != "left-label right-label" {
		t.Errorf("spread = %q", got)
	}
	if stageLabel("a-new-stage") != "a-new-stage" {
		t.Error("an unnamed stage should show under its own name")
	}
}

func TestInstallStagesAndFailure(t *testing.T) {
	now := time.Unix(1000, 0)
	m := newInstallModel()
	m.clock = func() time.Time { return now }
	m.phase, m.steps, m.started = phaseRunning, newSteps(), now

	// A staged install has fewer stages; the list shows only those.
	m, cmd := stepInstall(m, stageMsg{done: 0, total: 2, name: "pre-install"})
	if len(m.steps) != 2 || m.steps[0].status != stepRunning || cmd == nil {
		t.Fatalf("stages = %+v", m.steps)
	}
	now = now.Add(5 * time.Second)
	m, _ = stepInstall(m, stageMsg{done: 1, total: 2, name: "partition"})
	if m.steps[0].status != stepOK || m.steps[0].took != 5*time.Second || m.steps[1].status != stepRunning {
		t.Errorf("after the second stage began: %+v", m.steps)
	}
	if view := stripANSIForTest(m.View()); !strings.Contains(view, "step 2 of 2") {
		t.Errorf("running view:\n%s", view)
	}

	// The stage that was running when the install failed is the one marked.
	now = now.Add(3 * time.Second)
	failed, _ := stepInstall(m, doneMsg{err: errors.New("controller: partition: parted: exit status 1")})
	if failed.steps[0].status != stepOK || failed.steps[1].status != stepFailed || failed.steps[1].took != 3*time.Second {
		t.Errorf("after the failure: %+v", failed.steps)
	}
	if !strings.Contains(stripANSIForTest(failed.runningView()), "[FAIL]") {
		t.Error("the failed stage is not marked [FAIL]")
	}
	failed.logs = []string{"one", "two", "parted: unrecognised disk label " + strings.Repeat("x", 200)}
	failed.logPath = "/tmp/slinstaller.log"
	view := stripANSIForTest(failed.View())
	for _, want := range []string{"Install failed after 0:08.", "controller: partition: parted: exit status 1",
		"The full log is in /tmp/slinstaller.log on this live system.", "last output:", "parted: unrecognised disk label"} {
		if !strings.Contains(view, want) {
			t.Errorf("failure screen missing %q:\n%s", want, view)
		}
	}
	for _, l := range strings.Split(view, "\n") {
		if utf8.RuneCountInString(l) > 80 {
			t.Errorf("a failure-screen row is wider than the screen: %q", l)
		}
	}

	// Success: everything done, and where the log went.
	done, _ := stepInstall(m, stageMsg{done: 2, total: 2, name: "complete"})
	if !strings.Contains(stripANSIForTest(done.View()), "Finishing...") {
		t.Error("the last moments of the install should say it is finishing")
	}
	done, _ = stepInstall(done, doneMsg{})
	done.logPath = "/tmp/slinstaller.log"
	view = stripANSIForTest(done.View())
	if !strings.Contains(view, "Install complete in 0:08.") || !strings.Contains(view, "The install log is saved in /root/slinstaller.log.") {
		t.Errorf("success screen:\n%s", view)
	}
	// A staged install does not finish the target, so no log is promised.
	done.opts.Through = "partition"
	if strings.Contains(stripANSIForTest(done.View()), "/root/slinstaller.log") {
		t.Error("a staged install claims the log was saved on the target")
	}
}

func TestDoneScreenKeys(t *testing.T) {
	m := newInstallModel()
	m.phase = phaseDone
	// Left from the first button wraps to the last; then back.
	m, _ = stepInstall(m, keyNamed("left"))
	if m.doneCursor != len(doneButtons)-1 {
		t.Errorf("Left did not wrap: %d", m.doneCursor)
	}
	m, _ = stepInstall(m, keyNamed("shift+tab"))
	if m.doneCursor != len(doneButtons)-2 {
		t.Errorf("Shift+Tab did not move back: %d", m.doneCursor)
	}
	if got, cmd := stepInstall(m, keyNamed("esc")); got.action != actionExit || cmd == nil {
		t.Error("Esc did not exit")
	}
	m, _ = stepInstall(m, keyNamed("d"))
	if !m.details {
		t.Error("d did not open the log from the final screen")
	}
}

func TestLogViewScrollingAndColours(t *testing.T) {
	m := newInstallModel()
	m.phase, m.details = phaseRunning, true
	m.logPath = "/tmp/slinstaller.log"
	for i := 0; i < 100; i++ {
		m.logs = append(m.logs, fmt.Sprintf("line-%03d", i))
	}
	rows, top := m.logRows(), 100-m.logRows()
	scroll := func(k string) int {
		m, _ = stepInstall(m, keyNamed(k))
		return m.scroll
	}
	if scroll("pgup") != rows || scroll("up") != rows+1 || scroll("down") != rows || scroll("pgdown") != 0 {
		t.Errorf("paging: scroll = %d", m.scroll)
	}
	if scroll("down") != 0 {
		t.Error("scrolled past the end")
	}
	if scroll("home") != top || scroll("up") != top {
		t.Errorf("Home: scroll = %d, want %d and no further", m.scroll, top)
	}
	// A key the log does not use changes nothing and keeps it open.
	if scroll("x") != top || !m.details {
		t.Error("an unused key moved or closed the log")
	}
	// While scrolled up, the view holds on the rows being read as new lines
	// arrive, including a long line that takes several rows.
	long := strings.Repeat("y", m.logWidth()*2)
	m, _ = stepInstall(m, logMsg(long))
	if m.scroll != top+len(wrapLog(long, m.logWidth())) || len(wrapLog(long, m.logWidth())) < 2 {
		t.Errorf("a new line moved the scrolled view: scroll = %d", m.scroll)
	}
	// A download's progress redraws are left out of the view.
	n := len(m.logs)
	m, _ = stepInstall(m, logMsg("foo-1.0_1.x86_64.xbps: [1038KB 0%] 118MB/s ETA: 00m00s"))
	if len(m.logs) != n {
		t.Error("an xbps progress line was kept in the view")
	}
	if got, _ := stepInstall(m, keyNamed("q")); got.details {
		t.Error("q did not close the log")
	}
	m.stop = make(chan struct{})
	if got, cmd := stepInstall(m, keyNamed("ctrl+c")); got.cancelled || cmd != nil || !got.stopping {
		t.Error("Ctrl+C in the log did not ask the install to stop")
	}

	// The view keeps a bounded amount of output.
	m.logs = make([]string, maxLogLines)
	m, _ = stepInstall(m, logMsg("the newest line"))
	if len(m.logs) != maxLogLines || m.logs[maxLogLines-1] != "the newest line" {
		t.Errorf("log length %d, last %q", len(m.logs), m.logs[len(m.logs)-1])
	}

	// Stage headers, errors and warnings are each shown as such, and the
	// header names the file the log is also written to.
	m.logs, m.scroll = []string{"==> partition", "mkfs: cannot open /dev/sda1", "warning: slow disk", "plain"}, 0
	if logKind(m.logs[0]) != logStage || logKind(m.logs[1]) != logError || logKind(m.logs[2]) != logWarn || logKind(m.logs[3]) != logPlain {
		t.Error("log lines misclassified")
	}
	view := stripANSIForTest(m.View())
	for _, want := range append([]string{"Install log (/tmp/slinstaller.log)"}, m.logs...) {
		if !strings.Contains(view, want) {
			t.Errorf("log view missing %q:\n%s", want, view)
		}
	}
}

func TestCleanLogLineAndClipping(t *testing.T) {
	// A tab pads to the next stop even past the end of what was written,
	// and control characters print nothing.
	if got := cleanLogLine("ab\b\b\tx\x07\x7f"); got != "        x" {
		t.Errorf("cleanLogLine = %q", got)
	}
	for _, tc := range []struct {
		s    string
		n    int
		want string
	}{
		{"short\r", 10, "short"},
		{"a long line of output", 10, "a long ..."},
		{"abcdef", 3, "..."},
		{"abcdef", 0, "."},
	} {
		if got := clip(tc.s, tc.n); got != tc.want {
			t.Errorf("clip(%q, %d) = %q, want %q", tc.s, tc.n, got, tc.want)
		}
	}
	// An error is wrapped by character: one naming a non-ASCII path must not
	// be cut inside a character.
	msg := strings.Repeat("ă", 25)
	parts := wrapErr(msg, 10)
	if len(parts) != 3 || strings.Join(parts, "") != msg {
		t.Errorf("wrapErr = %q", parts)
	}
	for _, p := range parts {
		if !utf8.ValidString(p) {
			t.Errorf("wrapErr cut a character: %q", p)
		}
	}
	if got := wrapErr("0123456789ab", 2); len(got) != 2 || got[0] != "0123456789" {
		t.Errorf("wrapErr with a tiny width = %q", got)
	}
	if InstallLogPath() != installLogPath {
		t.Error("InstallLogPath does not name the log file")
	}
}

// A running install reports through the model's channel: stage changes,
// each line of output, and the result. Only the pre-install stage runs here
// (its hook is a harmless echo); nothing after it is reached.
func TestInstallReportsStagesAndOutput(t *testing.T) {
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("no shell to run a hook with")
	}
	defer func(p string) { installLogPath = p }(installLogPath)
	dir := t.TempDir()
	// With a log file, and where none can be made: the install runs the
	// same, and a file is claimed only when there is one.
	for _, path := range []string{filepath.Join(dir, "install.log"), filepath.Join(dir, "no-such-dir", "install.log")} {
		installReports(t, path, strings.Contains(path, "no-such-dir"))
	}
}

func installReports(t *testing.T, logPath string, noFile bool) {
	installLogPath = logPath
	cfg := &config.Config{
		TargetMedia: []*config.Media{{Name: "slitest0", Type: "disk", Children: []*config.Media{
			{Name: "slitest0p1", Type: "part", FsType: "vfat", Mountpoint: "/boot", Size: "512M"},
			{Name: "slitest0p2", Type: "part", FsType: "ext4", Mountpoint: "/", Size: "0"},
		}}},
		PreInstall: []*config.Hook{{Cmd: "echo hello from the hook"}},
	}
	m := newInstallModel()
	m.cfg, m.opts = cfg, controller.Options{Through: "pre-install", Root: t.TempDir()}
	m.phase, m.steps = phaseRunning, newSteps()
	// A progress hook the caller set (a report sent elsewhere) still runs.
	var hooked []string
	m.opts.Progress = func(done, total int, stage string) { hooked = append(hooked, stage) }
	defer func() {
		if strings.Join(hooked, ",") != "pre-install,complete" {
			t.Errorf("the caller's progress hook saw %v", hooked)
		}
	}()
	msg := m.start()()
	if (m.logPath == "") != noFile {
		t.Errorf("log file claimed = %q, but could one be made: %v", m.logPath, !noFile)
	}
	var stages []string
	for m.phase != phaseDone {
		if s, ok := msg.(stageMsg); ok {
			stages = append(stages, s.name)
		}
		var cmd tea.Cmd
		if m, cmd = stepInstall(m, msg); cmd != nil {
			msg = cmd()
		}
	}
	if m.err != nil {
		t.Fatalf("install through pre-install: %v", m.err)
	}
	if strings.Join(stages, ",") != "pre-install,complete" {
		t.Errorf("stages reported = %v", stages)
	}
	log := strings.Join(m.logs, "\n")
	if !strings.Contains(log, "==> pre-install") || !strings.Contains(log, "hello from the hook") {
		t.Errorf("the install's output did not reach the view:\n%s", log)
	}
	if noFile {
		return
	}
	file, _ := os.ReadFile(logPath)
	if !strings.Contains(string(file), "hello from the hook") || !strings.Contains(string(file), "ended") || !strings.Contains(string(file), ": ok") {
		t.Errorf("the log file does not carry the install:\n%s", file)
	}
	// More stages reported done than there are: the step count stays sane.
	m.phase, m.done, m.total, m.stage = phaseRunning, 5, 2, "x"
	if view := stripANSIForTest(m.View()); !strings.Contains(view, "step 2 of 2") {
		t.Errorf("running view with done past total:\n%s", view)
	}
}

// The confirmation names each disk with its size and model, and for an
// install that keeps the table, what happens to each partition.
func TestConfirmLines(t *testing.T) {
	devs := []blockdev.Device{
		{Path: "/dev/sda", Size: 500 << 30, Model: "Samsung SSD"},
		{Path: "/dev/sdb", Size: 500 << 30, Model: "Samsung SSD"},
	}
	wipe := &storage.Plan{Wipe: true, Disk: "/dev/sda", Disks: []string{"/dev/sda", "/dev/sdb"}}
	got := strings.Join(confirmLines(wipe, devs), "\n")
	for _, want := range []string{"PARTITION and ERASE", "/dev/sda       500GiB   Samsung SSD", "/dev/sdb"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	keep := &storage.Plan{Disk: "/dev/sda", Parts: []storage.Partition{
		{Device: "/dev/sda1", Preserve: true, Mountpoint: "/boot/efi"},
		{Device: "/dev/sda2", Filesystem: "ntfs", Preserve: true, ResizeMiB: 40960},
		{Device: "/dev/sda3", Filesystem: "ext4", Mountpoint: "/", Create: true},
	}}
	got = strings.Join(confirmLines(keep, devs), "\n")
	for _, want := range []string{"keeps the partition table", "KEEP   /dev/sda1", "SHRINK /dev/sda2 to 40GiB", "CREATE /dev/sda3 as ext4 -> /", "have a backup"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "ERASE:") {
		t.Errorf("an install keeping the table says the disk is erased:\n%s", got)
	}
	// Nothing shrunk: no warning about an interrupted shrink. A logical
	// volume or an array is no partition of the disk, and is not listed.
	reuse := &storage.Plan{Disk: "/dev/sda", Parts: []storage.Partition{
		{Device: "/dev/sda1", Preserve: true, Mountpoint: "/boot/efi"},
		{Device: "/dev/sda2", Filesystem: "ext4", Mountpoint: "/"},
		{Device: "/dev/vg0/home", Filesystem: "ext4", Mountpoint: "/home", LV: true},
		{Device: "/dev/md/data", Filesystem: "xfs", Mountpoint: "/srv", Raid: true},
	}}
	got = strings.Join(confirmLines(reuse, devs), "\n")
	if !strings.Contains(got, "FORMAT /dev/sda2 as ext4 -> /") || strings.Contains(got, "have a backup") || strings.Contains(got, "SHRINK") ||
		strings.Contains(got, "/dev/vg0/home") || strings.Contains(got, "/dev/md/data") {
		t.Errorf("a reuse with no shrink:\n%s", got)
	}
}

// With Secure Boot on, the confirmation says the installed GRUB will not
// start until it is turned off.
func TestConfirmWarnsOfSecureBoot(t *testing.T) {
	saved := secureBoot
	defer func() { secureBoot = saved }()
	plan := &storage.Plan{Wipe: true, Disk: "/dev/sda"}
	secureBoot = func() bool { return true }
	if got := strings.Join(confirmLines(plan, nil), "\n"); !strings.Contains(got, "Secure Boot is on") {
		t.Errorf("no warning:\n%s", got)
	}
	secureBoot = func() bool { return false }
	if got := strings.Join(confirmLines(plan, nil), "\n"); strings.Contains(got, "Secure Boot") {
		t.Errorf("a warning with Secure Boot off:\n%s", got)
	}
}

// Stopped between stages: the stage that had finished is done, not
// failed, and the screen says stopped. Ctrl+C (a SIGTERM from outside) on
// the final screen leaves.
func TestStoppedInstallScreen(t *testing.T) {
	m := newInstallModel()
	m.phase, m.steps = phaseRunning, newSteps()
	m, _ = stepInstall(m, stageMsg{done: 1, total: len(m.steps), name: m.steps[1].name})
	m, _ = stepInstall(m, doneMsg{err: fmt.Errorf("controller: %w before mount", controller.ErrStopped)})
	if m.steps[1].status != stepOK {
		t.Errorf("the finished stage is %d, want OK", m.steps[1].status)
	}
	if view := stripANSIForTest(m.View()); !strings.Contains(view, "Install stopped after") {
		t.Errorf("final screen:\n%s", view)
	}
	if got, cmd := stepInstall(m, keyNamed("ctrl+c")); got.action != actionExit || cmd == nil {
		t.Error("Ctrl+C on the final screen did not leave")
	}
}

// After leaving mid-step, the installer's own SIGTERM coming back as one
// more Ctrl+C does not end the commands a second time.
func TestAbandonOnce(t *testing.T) {
	saved := abandonChildren
	defer func() { abandonChildren = saved }()
	n := 0
	abandonChildren = func() { n++ }
	m := newInstallModel()
	m.phase, m.steps, m.stop = phaseRunning, newSteps(), make(chan struct{})
	for i := 0; i < 3; i++ {
		m, _ = stepInstall(m, keyNamed("ctrl+c"))
	}
	if n != 1 {
		t.Errorf("abandoned %d times", n)
	}
}

// A view scrolled up holds still past the log's bound too.
func TestScrolledLogHoldsPastTheBound(t *testing.T) {
	m := newInstallModel()
	m.phase, m.details = phaseRunning, true
	for i := 0; i < maxLogLines+50; i++ {
		m, _ = stepInstall(m, logMsg(fmt.Sprintf("line %d", i)))
	}
	m.scroll = 15
	before := stripANSIForTest(m.detailsView())
	for i := 0; i < 5; i++ {
		m, _ = stepInstall(m, logMsg(fmt.Sprintf("new %d", i)))
	}
	after := stripANSIForTest(m.detailsView())
	rows := func(v string) string {
		var out []string
		for _, l := range strings.Split(v, "\n") {
			if l = strings.TrimSpace(l); strings.HasPrefix(l, "line ") {
				out = append(out, l)
			}
		}
		return strings.Join(out, "|")
	}
	if rows(before) != rows(after) {
		t.Errorf("the paused view moved:\n%s\n---\n%s", rows(before), rows(after))
	}
}
