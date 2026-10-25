package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/sunlightlinux/slinstaller/pkg/blockdev"
)

func send(m tea.Model, k tea.KeyMsg) model {
	nm, _ := m.Update(k)
	return nm.(model)
}

var (
	enter = tea.KeyMsg{Type: tea.KeyEnter}
	down  = tea.KeyMsg{Type: tea.KeyDown}
)

func runes(s string) tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)} }

func twoDisks() []blockdev.Device {
	return []blockdev.Device{{Name: "sda", Path: "/dev/sda"}, {Name: "vdb", Path: "/dev/vdb"}}
}

// moveTo returns a model with the hub cursor on section s.
func moveTo(m model, s section) model {
	for m.hubCursor < int(s) {
		m = send(m, down)
	}
	return m
}

func TestInstallDisabledUntilValid(t *testing.T) {
	m := newModel(twoDisks())
	if m.canInstall() {
		t.Fatal("fresh model should not be installable")
	}
	// Pressing Install (the section after the last) does nothing.
	m = moveTo(m, sectionCount) // Install button
	m = send(m, enter)
	if m.confirmed {
		t.Error("Install fired while disabled")
	}
}

func TestConfigureAndInstall(t *testing.T) {
	var m tea.Model = newModel(twoDisks())

	// Disk: open, pick the second, back to hub.
	m = send(m, enter) // hub cursor 0 = Disk -> editor
	m = send(m, down)  // choose /dev/vdb
	m = send(m, enter) // commit -> hub
	if !m.(model).diskSet || m.(model).diskIdx != 1 {
		t.Fatalf("disk not set: %+v", m.(model))
	}

	// Admin user: login then password.
	mm := moveTo(m.(model), secUser)
	var cur tea.Model = mm
	cur = send(cur, enter)       // open user editor (login, default "user")
	cur = send(cur, enter)       // accept login "user" -> password step
	cur = send(cur, runes("pw")) // type password
	cur = send(cur, enter)       // commit -> hub
	if !cur.(model).userSet || cur.(model).login != "user" || cur.(model).password != "pw" {
		t.Fatalf("user not set: %+v", cur.(model))
	}
	if !cur.(model).canInstall() {
		t.Fatal("should be installable now")
	}

	// Install.
	fin := send(moveTo(cur.(model), sectionCount), enter)
	if !fin.confirmed {
		t.Error("Install did not confirm")
	}
}

func TestEncryptEditor(t *testing.T) {
	m := moveTo(newModel(twoDisks()), secEncrypt)
	var cur tea.Model = m
	cur = send(cur, enter) // open encryption editor (off/on)
	cur = send(cur, down)  // -> "on"
	cur = send(cur, enter) // choose on -> passphrase step
	cur = send(cur, enter) // empty passphrase: stays
	if cur.(model).onHub {
		t.Fatal("empty passphrase returned to hub")
	}
	cur = send(cur, runes("secret"))
	cur = send(cur, enter) // commit -> hub
	if !cur.(model).encrypt || cur.(model).passphrase != "secret" || !cur.(model).onHub {
		t.Fatalf("encryption not set: %+v", cur.(model))
	}
}

func TestSwapNone(t *testing.T) {
	m := moveTo(newModel(twoDisks()), secSwap)
	var cur tea.Model = m
	cur = send(cur, enter) // open swap editor
	cur = send(cur, down)  // file
	cur = send(cur, down)  // none
	cur = send(cur, enter) // choose none -> straight back to hub (no size)
	if cur.(model).swap != "none" || !cur.(model).onHub {
		t.Fatalf("swap none not set: %+v", cur.(model))
	}
}
