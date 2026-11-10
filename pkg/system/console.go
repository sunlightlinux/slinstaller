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

func dedupStrings(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}
