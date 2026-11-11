package system

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEPPDetection(t *testing.T) {
	saved := eppPrefPath
	defer func() { eppPrefPath = saved }()
	dir := t.TempDir()
	eppPrefPath = filepath.Join(dir, "pref")

	// Missing -> not capable.
	if EPPCapable() {
		t.Error("EPPCapable true without sysfs")
	}
	os.WriteFile(eppPrefPath, []byte("balance_power\n"), 0o644)
	if !EPPCapable() {
		t.Error("EPPCapable false with sysfs present")
	}
}
