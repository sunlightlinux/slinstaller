package system

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDetectConsolesFromCmdline(t *testing.T) {
	saved := procCmdline
	defer func() { procCmdline = saved }()
	f := filepath.Join(t.TempDir(), "cmdline")
	os.WriteFile(f, []byte("BOOT_IMAGE=/vmlinuz root=UUID=x console=tty0 console=ttyS0,115200n8 quiet\n"), 0o644)
	procCmdline = f
	got := DetectConsoles()
	if len(got) != 2 || got[0] != "console=tty0" || got[1] != "console=ttyS0,115200n8" {
		t.Fatalf("consoles = %v", got)
	}
}

func TestDetectConsolesFromSysfs(t *testing.T) {
	savedC, savedA := procCmdline, consoleActive
	defer func() { procCmdline, consoleActive = savedC, savedA }()
	procCmdline = filepath.Join(t.TempDir(), "none") // no console= in cmdline
	os.WriteFile(procCmdline, []byte("root=UUID=x ro quiet\n"), 0o644)
	a := filepath.Join(t.TempDir(), "active")
	os.WriteFile(a, []byte("ttyS0 tty0\n"), 0o644)
	consoleActive = a
	got := DetectConsoles()
	// Serial gets a default speed; tty0 stays bare.
	if len(got) != 2 || got[0] != "console=ttyS0,115200n8" || got[1] != "console=tty0" {
		t.Fatalf("consoles from sysfs = %v", got)
	}
}
