package tui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/progress"
	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"

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
	// Tab steps through Power off, Back to installer, Exit to shell.
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
	m = key(m, tea.KeyMsg{Type: tea.KeyUp})
	if strings.Contains(stripANSIForTest(m.View()), "log-line-199") {
		t.Error("Up did not scroll the log")
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
