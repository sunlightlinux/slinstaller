package grub

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const grubCfg = `set timeout=5
set default="1"
menuentry 'Sunlight OS GNU/Linux, with Linux 6.18.62_1 (recovery)' --class sunlight {
	linux	/boot/vmlinuz-6.18.62_1 root=UUID=abcd ro single
	initrd	/boot/initramfs-6.18.62_1.img
}
menuentry 'Sunlight OS GNU/Linux' --class sunlight {
	insmod gzio
	linux	/boot/vmlinuz-6.18.62_1 root=UUID=abcd ro quiet console=ttyS0,115200 init=/usr/bin/slinit
	initrd	/boot/initramfs-6.18.62_1.img
}
`

func target(t *testing.T, cfg string, files ...string) string {
	t.Helper()
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "boot", "grub"), 0o755)
	if cfg != "" {
		os.WriteFile(filepath.Join(root, "boot", "grub", "grub.cfg"), []byte(cfg), 0o644)
	}
	for _, f := range files {
		os.WriteFile(filepath.Join(root, f), nil, 0o644)
	}
	return root
}

func TestKexecCommand(t *testing.T) {
	root := target(t, grubCfg, "boot/vmlinuz-6.18.62_1", "boot/initramfs-6.18.62_1.img")
	cmd, err := KexecCommand(root)
	if err != nil {
		t.Fatal(err)
	}
	// The default entry's kernel, initramfs and command line — the second
	// entry here, not the recovery one before it.
	want := "kexec|-l|" + root + "/boot/vmlinuz-6.18.62_1|--initrd=" + root + "/boot/initramfs-6.18.62_1.img|" +
		"--append=root=UUID=abcd ro quiet console=ttyS0,115200 init=/usr/bin/slinit"
	if strings.Join(cmd, "|") != want {
		t.Errorf("kexec command:\n%s\nwant:\n%s", strings.Join(cmd, "|"), want)
	}

	// /boot on a partition of its own: grub.cfg names the files from that
	// filesystem's root, and they are found under the target's /boot.
	sep := strings.ReplaceAll(grubCfg, "/boot/", "/")
	root = target(t, sep, "boot/vmlinuz-6.18.62_1", "boot/initramfs-6.18.62_1.img")
	cmd, err = KexecCommand(root)
	if err != nil || cmd[2] != root+"/boot/vmlinuz-6.18.62_1" {
		t.Errorf("separate /boot: %v, %v", cmd, err)
	}
	// A default that is not a number, or past the last entry: the first.
	root = target(t, strings.Replace(grubCfg, `set default="1"`, `set default="${saved_entry}"`, 1), "boot/vmlinuz-6.18.62_1", "boot/initramfs-6.18.62_1.img")
	if cmd, err = KexecCommand(root); err != nil || !strings.HasSuffix(cmd[4], "ro single") {
		t.Errorf("saved default: %v, %v", cmd, err)
	}
	root = target(t, strings.Replace(grubCfg, `"1"`, `"7"`, 1), "boot/vmlinuz-6.18.62_1", "boot/initramfs-6.18.62_1.img")
	if cmd, err = KexecCommand(root); err != nil || !strings.HasSuffix(cmd[4], "ro single") {
		t.Errorf("default past the end: %v, %v", cmd, err)
	}
	// No initramfs line: none is passed.
	root = target(t, "menuentry 'x' {\n linuxefi /boot/vmlinuz root=/dev/sda2\n}\n", "boot/vmlinuz")
	if cmd, err = KexecCommand(root); err != nil || len(cmd) != 4 || cmd[3] != "--append=root=/dev/sda2" {
		t.Errorf("no initramfs: %v, %v", cmd, err)
	}

	for name, tc := range map[string]struct {
		root, want string
	}{
		"no grub.cfg":    {target(t, ""), "grub.cfg"},
		"no entry":       {target(t, "set timeout=5\n"), "has no menu entry"},
		"no kernel":      {target(t, "menuentry 'x' {\n echo hi\n}\n"), "loads no kernel"},
		"kernel missing": {target(t, grubCfg), "is not in the target"},
		"initrd missing": {target(t, grubCfg, "boot/vmlinuz-6.18.62_1"), "initramfs-6.18.62_1.img, named by"},
	} {
		if _, err := KexecCommand(tc.root); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: error %v, want %q", name, err, tc.want)
		}
	}
}
