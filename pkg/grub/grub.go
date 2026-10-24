// Package grub installs the GRUB bootloader for a UEFI target.
//
// Sunlight Linux is UEFI-only, and GRUB is its bootloader — the same
// choice Void makes. clr-installer uses clr-boot-manager, which is
// specific to Clear Linux, so there is nothing to port there; what is
// shared is only that the boot entry ends up registered with efibootmgr,
// which grub-install does as part of a UEFI install.
//
// The work is two chrooted commands — grub-install to write the loader
// to the ESP and register the firmware boot entry, and grub-mkconfig to
// generate the menu by discovering the installed kernels. Both need the
// base system (the grub package) already installed and /proc, /sys and
// /dev bound into the target.
package grub

import (
	"fmt"
	"strings"
)

// Defaults for a Sunlight UEFI install.
const (
	DefaultID     = "Sunlight"
	DefaultTarget = "x86_64-efi"
	configPath    = "/boot/grub/grub.cfg"
)

// Options is a bootloader install: the target tree, where the ESP is
// mounted inside it, and how to label and target the install.
type Options struct {
	// Root is the target mount tree.
	Root string

	// ESPDir is the ESP mountpoint inside the target, e.g. /boot.
	ESPDir string

	// ID is the EFI bootloader id, which is also the efibootmgr label.
	// Empty means DefaultID.
	ID string

	// Target is GRUB's platform target. Empty means DefaultTarget.
	Target string

	// KernelArgs are appended to the kernel command line via
	// /etc/default/grub (see CmdlineConfig); the controller writes that
	// file, this package only renders the line.
	KernelArgs []string
}

// Commands returns the chrooted grub-install and grub-mkconfig commands.
func (o Options) Commands() ([][]string, error) {
	if o.Root == "" {
		return nil, fmt.Errorf("grub: no target root")
	}
	if o.ESPDir == "" {
		return nil, fmt.Errorf("grub: no ESP mountpoint")
	}

	id := o.ID
	if id == "" {
		id = DefaultID
	}
	target := o.Target
	if target == "" {
		target = DefaultTarget
	}

	return [][]string{
		{"chroot", o.Root, "grub-install",
			"--target=" + target, "--efi-directory=" + o.ESPDir, "--bootloader-id=" + id},
		{"chroot", o.Root, "grub-mkconfig", "-o", configPath},
	}, nil
}

// CmdlineConfig renders the /etc/default/grub line that sets the kernel
// command line, or "" when there are no arguments. Appended after the
// package default, it wins because the file is sourced as shell and the
// later assignment takes effect.
func (o Options) CmdlineConfig() string {
	if len(o.KernelArgs) == 0 {
		return ""
	}
	return fmt.Sprintf("GRUB_CMDLINE_LINUX_DEFAULT=%q\n", strings.Join(o.KernelArgs, " "))
}
