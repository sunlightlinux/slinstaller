package system

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEPPDetection(t *testing.T) {
	savedP, savedA := eppPrefPath, eppAvailPath
	defer func() { eppPrefPath, eppAvailPath = savedP, savedA }()
	dir := t.TempDir()
	eppPrefPath = filepath.Join(dir, "pref")
	eppAvailPath = filepath.Join(dir, "avail")

	// Missing -> not capable.
	if EPPCapable() {
		t.Error("EPPCapable true without sysfs")
	}
	os.WriteFile(eppPrefPath, []byte("balance_power\n"), 0o644)
	os.WriteFile(eppAvailPath, []byte("default performance balance_performance balance_power power custom dynamic\n"), 0o644)
	if !EPPCapable() {
		t.Error("EPPCapable false with sysfs present")
	}
	got := EPPPreferences()
	// "custom" is dropped.
	want := []string{"default", "performance", "balance_performance", "balance_power", "power", "dynamic"}
	if len(got) != len(want) {
		t.Fatalf("prefs = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("prefs = %v, want %v", got, want)
		}
	}
}
