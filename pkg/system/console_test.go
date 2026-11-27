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
	// A console named twice is passed on once. (The repeat is not the last
	// console=: that one is the kernel's /dev/console.)
	os.WriteFile(f, []byte("BOOT_IMAGE=/vmlinuz root=UUID=x console=tty0 console=tty0 console=ttyS0,115200n8 quiet\n"), 0o644)
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

// The kernel makes the last console= its /dev/console, where the boot
// messages and a single-user shell go. A console named again last (the
// live medium's "console=tty0 console=ttyS0,115200n8 console=tty0") kept
// its first place, so the installed system came up with ttyS0 last: its
// primary console moved from the screen to the serial port.
func TestDetectConsolesKeepsTheLastConsoleLast(t *testing.T) {
	saved := procCmdline
	defer func() { procCmdline = saved }()
	f := filepath.Join(t.TempDir(), "cmdline")
	os.WriteFile(f, []byte("console=tty0 console=ttyS0,115200n8 console=tty0\n"), 0o644)
	procCmdline = f
	got := DetectConsoles()
	if len(got) != 2 || got[0] != "console=ttyS0,115200n8" || got[1] != "console=tty0" {
		t.Fatalf("consoles = %v, want ttyS0 then tty0 (the last, /dev/console)", got)
	}
}
