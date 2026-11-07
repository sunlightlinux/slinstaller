package tui

import (
	"fmt"
	"github.com/sunlightlinux/slinstaller/pkg/config"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/progress"
	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"github.com/sunlightlinux/slinstaller/pkg/controller"
)

func TestDoneScreenButtons(t *testing.T) {
	step := func(m installModel, k tea.KeyMsg) installModel {
		nm, _ := m.Update(k)
		return nm.(installModel)
	}
	tab := tea.KeyMsg{Type: tea.KeyTab}
	ent := tea.KeyMsg{Type: tea.KeyEnter}

	base := installModel{phase: phaseDone}

	// Default button is Reboot; Enter selects it.
	if got := step(base, ent).action; got != actionReboot {
		t.Errorf("default Enter action=%v, want Reboot", got)
	}
	// Tab steps through Power off, Back to menu, Exit to shell.
	if got := step(step(base, tab), ent).action; got != actionPowerOff {
		t.Errorf("after one Tab action=%v, want Power off", got)
	}
	if got := step(step(step(base, tab), tab), ent).action; got != actionBack {
		t.Errorf("after two Tabs action=%v, want Back", got)
	}
	if got := step(step(step(step(base, tab), tab), tab), ent).action; got != actionExit {
		t.Errorf("after three Tabs action=%v, want Exit", got)
	}
	// Tab past the last button wraps back to Reboot.
	m := base
	for i := 0; i < len(doneButtons); i++ {
		m = step(m, tab)
	}
	if m.doneCursor != 0 {
		t.Errorf("Tab did not wrap: cursor=%d", m.doneCursor)
	}
	// q exits.
	if got := step(base, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")}).action; got != actionExit {
		t.Errorf("q action=%v, want Exit", got)
	}
}

// The progress screen lists every stage with its mark: done ones [ OK ],
// the running one with a turning spinner, the rest waiting, and a failed
// one [FAIL]; the command output is behind the Details button.
func TestRunningViewShowsSteps(t *testing.T) {
	m := installModel{width: 80, height: 24, phase: phaseRunning, ch: make(chan tea.Msg, 1),
		bar: progress.New(), spin: spinner.New(spinner.WithSpinner(spinner.Line)), steps: newSteps()}
	nm, _ := m.Update(stageMsg{done: 4, total: 12, name: "base-install"})
	m = nm.(installModel)
	v := stripANSIForTest(m.View())

	for i, n := range controller.StageNames() {
		line := lineWith(v, stageLabel(n))
		switch {
		case i < 4 && !strings.Contains(line, "[ OK ]"):
			t.Errorf("stage %s should be done: %q", n, line)
		case i == 4 && !strings.Contains(line, "[ |  ]"):
			t.Errorf("stage %s should show the spinner: %q", n, line)
		case i > 4 && !strings.Contains(line, "[    ]"):
			t.Errorf("stage %s should be waiting: %q", n, line)
		}
	}
	if strings.Contains(v, "xbps") || !strings.Contains(lineWith(v, "Details"), "Details") {
		t.Errorf("the log should be behind a Details button, not on the progress screen:\n%s", v)
	}
	if h := len(strings.Split(m.View(), "\n")); h != 24 {
		t.Errorf("progress screen is %d rows, want 24", h)
	}

	nm, _ = m.Update(doneMsg{err: fmt.Errorf("controller: base-install: boom")})
	m = nm.(installModel)
	if m.steps[4].status != stepFailed || m.steps[3].status != stepOK {
		t.Errorf("after a failure in base-install: statuses %v", m.steps)
	}
}

// Details shows the full log in the whole dialog, following the end; it
// scrolls, and Enter returns.
func TestDetailsView(t *testing.T) {
	m := installModel{width: 80, height: 24, phase: phaseRunning, ch: make(chan tea.Msg, 1),
		bar: progress.New(), spin: spinner.New(spinner.WithSpinner(spinner.Line)), steps: newSteps()}
	for i := 0; i < 200; i++ {
		m.logs = append(m.logs, fmt.Sprintf("log-line-%03d", i))
	}
	key := func(m installModel, k tea.KeyMsg) installModel {
		nm, _ := m.Update(k)
		return nm.(installModel)
	}
	m = key(m, tea.KeyMsg{Type: tea.KeyEnter}) // the Details button
	if !m.details {
		t.Fatal("Enter did not open the log")
	}
	lines := strings.Split(stripANSIForTest(m.View()), "\n")
	count, last := 0, ""
	for _, l := range lines {
		if strings.Contains(l, "log-line-") {
			count, last = count+1, l
		}
	}
	if count != m.logRows() || !strings.Contains(last, "log-line-199") {
		t.Errorf("log view shows %d lines ending %q, want %d ending with the newest", count, last, m.logRows())
	}
	if len(lines) != 24 {
		t.Errorf("log view is %d rows, want 24", len(lines))
	}
	if h := lineWith(stripANSIForTest(m.View()), "Install log"); !strings.Contains(h, "following new lines") {
		t.Errorf("at the end the header should say it follows: %q", h)
	}
	m = key(m, tea.KeyMsg{Type: tea.KeyUp})
	if strings.Contains(stripANSIForTest(m.View()), "log-line-199") {
		t.Error("Up did not scroll the log")
	}
	if h := lineWith(stripANSIForTest(m.View()), "Install log"); !strings.Contains(h, "paused at line 199 of 200") {
		t.Errorf("scrolled up, the header should say where it holds: %q", h)
	}
	m.phase = phaseDone
	if h := lineWith(stripANSIForTest(m.View()), "Install log"); !strings.Contains(h, "line 199 of 200") || strings.Contains(h, "paused") {
		t.Errorf("after the install, scrolled: %q", h)
	}
	m = key(m, tea.KeyMsg{Type: tea.KeyEnd})
	if h := lineWith(stripANSIForTest(m.View()), "Install log"); !strings.Contains(h, "end of log") {
		t.Errorf("after the install, at the end: %q", h)
	}
	m = key(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.details {
		t.Error("Enter in the log view did not go back")
	}
}

// Command output reaches the log as a terminal would have shown it. The
// mke2fs line is the one it really prints (captured from mkfs.ext4); raw,
// its backspaces made the row shorter than the panel and left black cells
// at the right of the install screen.
func TestCleanLogLine(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Allocating group tables:  0/16\b\b\b\b\b     \b\b\b\b\bdone                            ",
			"Allocating group tables: done"},
		{"Writing superblocks and filesystem accounting information:  0/16\b\b\b\b\b     \b\b\b\b\bdone",
			"Writing superblocks and filesystem accounting information: done"},
		{"libedit.xbps: [106KB 3%] 81MB/s ETA: 00m00s\rlibedit.xbps: 106KB [avg rate: 2149MB/s]",
			"libedit.xbps: 106KB [avg rate: 2149MB/s]"},
		{"\x1b[1;32mok\x1b[0m done", "ok done"},
		{"a\tb", "a       b"},
		{"plain line", "plain line"},
	}
	for _, c := range cases {
		got := cleanLogLine(c.in)
		if got != c.want {
			t.Errorf("cleanLogLine(%q) = %q, want %q", c.in, got, c.want)
		}
		if strings.ContainsAny(got, "\b\r\x1b\t") {
			t.Errorf("cleanLogLine(%q) left a control character: %q", c.in, got)
		}
	}
}

// Every line the install sends is cleaned on its way into the log.
func TestLogLinesAreCleaned(t *testing.T) {
	m := installModel{phase: phaseRunning, ch: make(chan tea.Msg, 1)}
	nm, _ := m.Update(logMsg("Creating journal (16384 blocks):  0/16\b\b\b\b\b     \b\b\b\b\bdone"))
	got := nm.(installModel).logs
	if len(got) != 1 || got[0] != "Creating journal (16384 blocks): done" {
		t.Errorf("log = %q, want the line as a terminal shows it", got)
	}
}

// On the console the bar is bright yellow, not the bright red its own
// nearest-colour match made of amber.
func TestBarFillOnTheConsole(t *testing.T) {
	bar := progress.New(progress.WithSolidFill(barFill(termenv.ANSI)), progress.WithColorProfile(termenv.ANSI))
	bar.Width = 20
	if v := bar.ViewAs(0.5); !strings.Contains(v, "\x1b[93m") {
		t.Errorf("console bar is not bright yellow: %q", v)
	}
	if barFill(termenv.TrueColor) != "#ffc23d" {
		t.Error("a full-colour terminal should get the amber")
	}
}

// The running stage is set apart: its name is drawn in the running style,
// the others' are not.
func TestRunningStageStandsOut(t *testing.T) {
	prev := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.ANSI)
	t.Cleanup(func() { lipgloss.SetColorProfile(prev) })
	m := installModel{width: 80, height: 24, phase: phaseRunning, ch: make(chan tea.Msg, 1),
		bar: progress.New(), spin: spinner.New(spinner.WithSpinner(spinner.Line)), steps: newSteps()}
	nm, _ := m.Update(stageMsg{done: 4, total: 12, name: "base-install"})
	v := nm.(installModel).View()
	if !strings.Contains(v, stepRun.Render(stageLabel("base-install"))) {
		t.Error("the running stage's name is not highlighted")
	}
	if strings.Contains(v, stepRun.Render(stageLabel("fstab"))) {
		t.Error("a waiting stage is highlighted")
	}
}

// The log view leaves out xbps's progress lines, colours stage markers,
// errors and warnings, and wraps a long line instead of clipping it.
func TestLogViewRows(t *testing.T) {
	m := installModel{width: 80, height: 24, phase: phaseRunning, ch: make(chan tea.Msg, 1)}
	long := "grub-install: error: " + strings.Repeat("x", 150)
	for _, l := range []string{
		"==> base-install",
		"inih-62_1.x86_64.xbps: [17KB 23%] 115MB/s ETA: 00m00s",
		"inih-62_1.x86_64.xbps.sig2: [512B 100%] 19MB/s ETA: 00m00s",
		"inih-62_1.x86_64.xbps: 17KB [avg rate: 489MB/s]",
		"WARNING: the clock is off",
		long,
	} {
		nm, _ := m.Update(logMsg(l))
		m = nm.(installModel)
	}
	if len(m.logs) != 4 {
		t.Fatalf("logs = %q, want the two xbps progress lines left out", m.logs)
	}
	rows := m.logView()
	kinds := []int{logStage, logPlain, logWarn, logError, logError, logError}
	if len(rows) != len(kinds) {
		t.Fatalf("%d rows, want %d (the long line in three): %q", len(rows), len(kinds), rows)
	}
	for i, r := range rows {
		if r.kind != kinds[i] {
			t.Errorf("row %d %q: kind %d, want %d", i, r.text, r.kind, kinds[i])
		}
		if len([]rune(r.text)) > m.logWidth() {
			t.Errorf("row %d is %d cells, wider than %d", i, len([]rune(r.text)), m.logWidth())
		}
	}
	var joined string
	for i, r := range rows[3:] {
		if i > 0 {
			if !strings.HasPrefix(r.text, "  ") {
				t.Errorf("continuation row %q is not indented", r.text)
			}
			r.text = r.text[2:]
		}
		joined += r.text
	}
	if joined != long {
		t.Errorf("wrapped rows do not rejoin to the line:\n%q\n%q", joined, long)
	}
}

// The progress screen shows how long the install has run and each stage's
// time; the final screen says how long it took.
func TestInstallTimer(t *testing.T) {
	at := time.Date(2026, 11, 7, 20, 0, 0, 0, time.UTC)
	m := installModel{width: 80, height: 24, phase: phaseConfirm, ch: make(chan tea.Msg, 1),
		bar: progress.New(), spin: spinner.New(spinner.WithSpinner(spinner.Line)),
		clock: func() time.Time { return at }}
	m.phase, m.steps, m.started = phaseRunning, newSteps(), at
	step := func(done int, name string, after time.Duration) {
		at = at.Add(after)
		nm, _ := m.Update(stageMsg{done: done, total: 12, name: name})
		m = nm.(installModel)
	}
	step(0, "pre-install", 0)
	step(1, "partition", 3*time.Second)
	step(2, "mount", 42*time.Second)
	at = at.Add(5 * time.Second)

	v := stripANSIForTest(m.View())
	if !strings.Contains(lineWith(v, "Installing - step 3 of 12"), "elapsed 0:50") {
		t.Errorf("header has no elapsed 0:50:\n%s", v)
	}
	for label, want := range map[string]string{
		"Running pre-install hooks": "0:03", "Partitioning the disk": "0:42", "Mounting the target": "0:05",
	} {
		if l := lineWith(v, label); !strings.HasSuffix(strings.TrimRight(l, " "), want) {
			t.Errorf("%s: %q, want it to end in %s", label, l, want)
		}
	}
	if l := lineWith(v, "Writing fstab"); strings.Contains(l, ":") {
		t.Errorf("a waiting stage shows a time: %q", l)
	}

	at = at.Add(time.Hour + 10*time.Minute)
	nm, _ := m.Update(doneMsg{})
	m = nm.(installModel)
	if v := stripANSIForTest(m.View()); !strings.Contains(v, "Install complete in 1:10:50.") {
		t.Errorf("final screen has no duration:\n%s", v)
	}
}

// The install writes its log to a file as it goes, with the stage markers,
// and passes the file to the controller to archive.
func TestInstallWritesLogFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "slinstaller.log")
	defer func(p string) { installLogPath = p }(installLogPath)
	installLogPath = path

	cfg := &config.Config{} // no media: Plan fails before any command runs
	if _, err := cfg.Plan(); err == nil {
		t.Fatal("an empty config plans; this test would install")
	}
	m := installModel{cfg: cfg, ch: make(chan tea.Msg, 16)}
	m.start()
	if m.logPath != path {
		t.Errorf("logPath = %q, want %q", m.logPath, path)
	}
	for msg := range m.ch {
		if _, ok := msg.(doneMsg); ok {
			break
		}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "install log, started") || !strings.Contains(string(data), "install log, ended") {
		t.Errorf("log file = %q", data)
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
		t.Errorf("log file mode %v, want 0600", fi.Mode().Perm())
	}
}
