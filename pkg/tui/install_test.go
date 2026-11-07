package tui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/progress"
	tea "github.com/charmbracelet/bubbletea"
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

// On a tall screen the command log fills the panel down to the rule above
// the footer. It used to be capped at 20 lines, which left a blank band
// between the log and the footer.
func TestRunningViewLogFillsPanel(t *testing.T) {
	for _, h := range []int{24, 40, 60} {
		m := installModel{width: 120, height: h, total: 12, stage: "base-install",
			bar: progress.New(progress.WithSolidFill("#00afaf"))}
		for i := 0; i < 200; i++ {
			m.logs = append(m.logs, fmt.Sprintf("log-line-%03d", i))
		}
		lines := strings.Split(stripANSIForTest(m.runningView()), "\n")
		last, count := -1, 0
		for i, l := range lines {
			if strings.Contains(l, "log-line-") {
				last, count = i, count+1
			}
		}
		if want := panelHeight(h) - 8; count != want {
			t.Errorf("height %d: %d log lines, want %d (all the panel has room for)", h, count, want)
		}
		if last < 0 || last+1 >= len(lines) || !strings.Contains(lines[last+1], "───") {
			t.Errorf("height %d: the line after the last log line is not the rule: %q", h, lines[last+1])
		}
		if !strings.Contains(lines[last], "log-line-199") {
			t.Errorf("height %d: the newest line is not shown last: %q", h, lines[last])
		}
	}
}
