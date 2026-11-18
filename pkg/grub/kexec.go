package grub

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// KexecCommand returns the command that loads the installed system's kernel
// for kexec — the kernel, initramfs and command line of the default entry
// in the target's grub.cfg — so the machine can go straight into the new
// system without a trip through the firmware.
//
// The paths in grub.cfg are relative to the filesystem that holds /boot:
// "/boot/vmlinuz-…" when /boot is part of the root, "/vmlinuz-…" when it is
// a partition of its own. Both are looked for under the target.
func KexecCommand(root string) ([]string, error) {
	cfg := filepath.Join(root, "boot", "grub", "grub.cfg")
	data, err := os.ReadFile(cfg)
	if err != nil {
		return nil, fmt.Errorf("grub: %w", err)
	}
	def := 0
	var entries [][]string // each menu entry's lines
	for _, line := range strings.Split(string(data), "\n") {
		f := strings.Fields(line)
		if len(f) == 0 {
			continue
		}
		switch {
		case f[0] == "set" && len(f) > 1 && strings.HasPrefix(f[1], "default="):
			// Only a plain number picks an entry here; a saved or named
			// default is left at the first.
			n := 0
			if _, err := fmt.Sscanf(strings.Trim(strings.TrimPrefix(f[1], "default="), `"'`), "%d", &n); err == nil {
				def = n
			}
		case f[0] == "menuentry":
			entries = append(entries, nil)
		case len(entries) > 0:
			entries[len(entries)-1] = append(entries[len(entries)-1], line)
		}
	}
	if len(entries) == 0 {
		return nil, fmt.Errorf("grub: %s has no menu entry", cfg)
	}
	if def < 0 || def >= len(entries) {
		def = 0
	}
	find := func(rel string) (string, error) {
		for _, p := range []string{filepath.Join(root, rel), filepath.Join(root, "boot", rel)} {
			if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
				return p, nil
			}
		}
		return "", fmt.Errorf("grub: %s, named by %s, is not in the target", rel, cfg)
	}
	var kernel, initrd, cmdline string
	for _, line := range entries[def] {
		f := strings.Fields(line)
		switch {
		case len(f) >= 2 && (f[0] == "linux" || f[0] == "linuxefi") && kernel == "":
			if kernel, err = find(f[1]); err != nil {
				return nil, err
			}
			cmdline = strings.Join(f[2:], " ")
		case len(f) >= 2 && (f[0] == "initrd" || f[0] == "initrdefi") && initrd == "":
			if initrd, err = find(f[1]); err != nil {
				return nil, err
			}
		}
	}
	if kernel == "" {
		return nil, fmt.Errorf("grub: the default entry of %s loads no kernel", cfg)
	}
	cmd := []string{"kexec", "-l", kernel}
	if initrd != "" {
		cmd = append(cmd, "--initrd="+initrd)
	}
	return append(cmd, "--append="+cmdline), nil
}
