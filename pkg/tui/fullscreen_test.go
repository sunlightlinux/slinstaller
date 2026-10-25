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

// A no-encryption, swap-partition run: the passphrase step is skipped,
// the swap-size step is kept, and the answers end up where expected.
func TestWizardPlainRun(t *testing.T) {
	disks := []blockdev.Device{
		{Name: "sda", Path: "/dev/sda"},
		{Name: "vdb", Path: "/dev/vdb"},
	}
	var m tea.Model = newModel(disks)

	m = send(m, down)  // pick the second disk
	m = send(m, enter) // disk -> fs
	m = send(m, enter) // fs = ext4 -> encrypt
	m = send(m, enter) // encrypt = "no" -> swap (passphrase skipped)
	m = send(m, enter) // swap = "partition" -> swap size
	m = send(m, enter) // swap size default 2G -> hostname
	m = send(m, enter) // hostname default -> username
	m = send(m, enter) // username default -> password
	m = send(m, runes("pw"))
	m = send(m, enter) // password -> confirm

	got := m.(model)
	if got.step != stepConfirm {
		t.Fatalf("ended at step %d, want stepConfirm", got.step)
	}
	if got.diskIdx != 1 || got.fs != "ext4" || got.encrypt || got.swap != "partition" ||
		got.swapSize != "2G" || got.hostname != "sunlight" || got.login != "user" || got.password != "pw" {
		t.Fatalf("answers = %+v", got)
	}

	// Install.
	final := send(m, enter)
	if !final.confirmed {
		t.Error("Install not confirmed")
	}
}

// Encryption on and swap none: the passphrase step is required and the
// swap-size step is skipped.
func TestWizardEncryptNoSwap(t *testing.T) {
	var m tea.Model = newModel([]blockdev.Device{{Name: "sda", Path: "/dev/sda"}})

	m = send(m, enter) // disk -> fs
	m = send(m, enter) // fs ext4 -> encrypt
	m = send(m, down)  // encrypt cursor -> "yes"
	m = send(m, enter) // encrypt = yes -> passphrase
	if m.(model).step != stepPassphrase {
		t.Fatalf("encrypt=yes did not reach passphrase, at %d", m.(model).step)
	}
	m = send(m, enter) // empty passphrase: stays put
	if m.(model).step != stepPassphrase {
		t.Fatal("empty passphrase advanced")
	}
	m = send(m, runes("secret"))
	m = send(m, enter) // passphrase -> swap
	m = send(m, down)  // swap -> "file"
	m = send(m, down)  // swap -> "none"
	m = send(m, enter) // swap = none -> hostname (swap size skipped)

	got := m.(model)
	if got.step != stepHostname {
		t.Fatalf("swap=none did not skip swap size, at step %d", got.step)
	}
	if !got.encrypt || got.passphrase != "secret" || got.swap != "none" {
		t.Fatalf("answers = %+v", got)
	}
}
