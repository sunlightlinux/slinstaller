package system

import (
	"os"
	"strings"
)

// procCmdline and consoleActive are the live sources for console detection;
// variables so tests can point them elsewhere.
var (
	procCmdline   = "/proc/cmdline"
	consoleActive = "/sys/class/tty/console/active"
)

// DetectConsoles returns the console= kernel arguments the installed system
// should use, mirrored from the live system so a serial-console install comes
// up on the same serial port and a graphical install on the VGA console. It
// prefers the live kernel's own console= tokens (which already carry the baud
// and format, e.g. console=ttyS0,115200n8); failing that, it reads the active
// consoles from sysfs and gives a serial one a sane default speed.
func DetectConsoles() []string {
	if data, err := os.ReadFile(procCmdline); err == nil {
		var out []string
		for _, tok := range strings.Fields(string(data)) {
			if strings.HasPrefix(tok, "console=") {
				out = append(out, tok)
			}
		}
		if len(out) > 0 {
			return dedupStrings(out)
		}
	}
	if data, err := os.ReadFile(consoleActive); err == nil {
		var out []string
		for _, dev := range strings.Fields(string(data)) {
			tok := "console=" + dev
			if isSerialConsole(dev) {
				tok += ",115200n8"
			}
			out = append(out, tok)
		}
		return dedupStrings(out)
	}
	return nil
}

// isSerialConsole reports whether a tty device name is a serial-type console,
// which needs a speed/format appended.
func isSerialConsole(dev string) bool {
	for _, p := range []string{"ttyS", "ttyUSB", "ttyAMA", "ttyMFD", "ttyO", "hvc"} {
		if strings.HasPrefix(dev, p) {
			return true
		}
	}
	return false
}

// dedupStrings drops repeats, keeping each at its last place: the kernel
// makes the last console= /dev/console, and a console named again last
// must stay last.
func dedupStrings(in []string) []string {
	last := map[string]int{}
	for i, s := range in {
		last[s] = i
	}
	var out []string
	for i, s := range in {
		if last[s] == i {
			out = append(out, s)
		}
	}
	return out
}
