package tui

import (
	"testing"

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
	// Tab -> Power off.
	if got := step(step(base, tab), ent).action; got != actionPowerOff {
		t.Errorf("after one Tab action=%v, want Power off", got)
	}
	// Tab x2 -> Exit to shell.
	if got := step(step(step(base, tab), tab), ent).action; got != actionExit {
		t.Errorf("after two Tabs action=%v, want Exit", got)
	}
	// Tab x3 wraps back to Reboot.
	m := step(step(step(base, tab), tab), tab)
	if m.doneCursor != 0 {
		t.Errorf("Tab did not wrap: cursor=%d", m.doneCursor)
	}
	// q exits.
	if got := step(base, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")}).action; got != actionExit {
		t.Errorf("q action=%v, want Exit", got)
	}
}
