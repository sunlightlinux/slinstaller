package tui

import "testing"

// TestInstallAsksForAMissingPassphrase: the passphrase popup, left with Esc
// when an encrypted partition was added ("for later"), could not be opened
// again from anywhere — the hub has no passphrase section — so the layout
// could never be installed without deleting and re-adding the partition.
// Install, otherwise ready, asks for it.
func TestInstallAsksForAMissingPassphrase(t *testing.T) {
	m := readyModel()
	m.parts = autoLayout(true)
	m.hubCursor = len(m.visibleSections(0)) // Install
	m = send(m, enter)
	if m.popup != popupPassphrase {
		t.Fatalf("Install with no passphrase: popup %d %v", m.popup, m.popupMsg)
	}
	m = send(typeText(m, "a long passphrase"), enter)
	if m.popup != popupPassphrase {
		t.Fatalf("second entry not asked: popup %d", m.popup)
	}
	m = send(typeText(m, "a long passphrase"), enter)
	if m.passphrase != "a long passphrase" || !m.canInstall() || m.confirmed {
		t.Errorf("passphrase %q canInstall %v confirmed %v", m.passphrase, m.canInstall(), m.confirmed)
	}
}
