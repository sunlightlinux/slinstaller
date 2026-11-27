package tui

import (
	"testing"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
)

// TestListFilterEditsWholeCharacters: Backspace in a list's filter took off
// a byte, so after a non-ASCII letter the filter was half a character —
// shown as garbage, matching nothing — and a pasted name was ignored
// (only one-rune keys were taken).
func TestListFilterEditsWholeCharacters(t *testing.T) {
	m := withSize(newModel(twoTestDisks()))
	m.editing = secTimezone
	m = m.openList([]string{"America/Bogotá", "Europe/Bucharest", "Europe/Zürich"}, "")

	got := send(send(send(m, runes("ü")), runes("x")), backKey)
	if got.listFilter != "ü" {
		t.Errorf("Backspace after ü x left %q", got.listFilter)
	}
	got = send(send(m, runes("ü")), backKey)
	if got.listFilter != "" || !utf8.ValidString(got.listFilter) {
		t.Errorf("Backspace after ü left %q", got.listFilter)
	}

	paste := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("Zürich\n"), Paste: true} // copied with its line break
	got = send(send(m, paste), enter)
	if got.timezone != "Europe/Zürich" {
		t.Errorf("pasted filter chose %q", got.timezone)
	}
}
