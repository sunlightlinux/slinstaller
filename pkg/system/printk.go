package system

import (
	"fmt"
	"os"
	"path/filepath"
)

// PrintkSysctl writes /etc/sysctl.d/10-printk.conf on the target so the
// running system's console log verbosity matches the loglevel= kernel
// argument. kernel.printk has four fields — console, default-message,
// minimum-console and default-console log levels; only the first (the console
// level) is made configurable here, the rest keep the common "4 1 7".
// level is the console log level, 0 (emergency) to 7 (debug).
func PrintkSysctl(root string, level int) error {
	dir := filepath.Join(root, "etc", "sysctl.d")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	content := fmt.Sprintf("# console log verbosity set by slinstaller\nkernel.printk = %d 4 1 7\n", level)
	return os.WriteFile(filepath.Join(dir, "10-printk.conf"), []byte(content), 0o644)
}
