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

	// Distributor names the system in the boot menu. grub-mkconfig's
	// 10_linux turns it into "Sunlight OS GNU/Linux" and the menu class
	// "sunlight"; the grub package's own default still says "Void".
	Distributor = "Sunlight OS"
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

	// Removable also installs the loader at the removable-media path
	// (EFI/BOOT/BOOTX64.EFI), which firmware boots when it has no entry of
	// its own for this disk — after its settings are reset, or when the
	// disk is moved to another machine.
	Removable bool

	// NoNVRAM leaves the firmware's boot entries alone.
	NoNVRAM bool

	// SerialTerminal shows the boot menu on the serial console as well as
	// the screen. The port and speed come from KernelArgs' console=ttyS…,
	// or COM1 at 115200 when there is none.
	SerialTerminal bool

	// ProbeOtherOS lets grub-mkconfig add entries for other systems it
	// finds on the machine (needs os-prober in the target).
	ProbeOtherOS bool
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

	install := []string{"chroot", o.Root, "grub-install",
		"--target=" + target, "--efi-directory=" + o.ESPDir, "--bootloader-id=" + id}
	if o.NoNVRAM {
		install = append(install, "--no-nvram")
	}
	cmds := [][]string{install}
	if o.Removable {
		// A second copy at the fallback path; --removable never registers
		// a firmware entry, so it does not disturb the one made above.
		cmds = append(cmds, []string{"chroot", o.Root, "grub-install",
			"--target=" + target, "--efi-directory=" + o.ESPDir, "--removable"})
	}
	return append(cmds, []string{"chroot", o.Root, "grub-mkconfig", "-o", configPath}), nil
}

// TerminalConfig renders the /etc/default/grub lines for the options that
// are not the command line: the serial terminal and probing for other
// systems. "" when neither is asked for.
func (o Options) TerminalConfig() string {
	var b strings.Builder
	if o.SerialTerminal {
		unit, speed, word, parity := serialPort(o.KernelArgs)
		b.WriteString("GRUB_TERMINAL=\"console serial\"\n")
		fmt.Fprintf(&b, "GRUB_SERIAL_COMMAND=\"serial --unit=%s --speed=%s --word=%s --parity=%s --stop=1\"\n",
			unit, speed, word, parity)
	}
	if o.ProbeOtherOS {
		b.WriteString("GRUB_DISABLE_OS_PROBER=false\n")
	}
	return b.String()
}

// serialPort reads the last console=ttyS<unit>[,<speed>[<parity>[<bits>]]]
// among the kernel arguments, the console the kernel itself will use, with
// 0, 115200, 8 and no parity for whatever is not given.
func serialPort(kargs []string) (unit, speed, word, parity string) {
	unit, speed, word, parity = "0", "115200", "8", "no"
	for _, a := range kargs {
		rest, ok := strings.CutPrefix(a, "console=ttyS")
		if !ok {
			continue
		}
		u, mode, _ := strings.Cut(rest, ",")
		if u == "" || strings.Trim(u, "0123456789") != "" {
			continue
		}
		unit, speed, word, parity = u, "115200", "8", "no"
		i := 0
		for i < len(mode) && mode[i] >= '0' && mode[i] <= '9' {
			i++
		}
		if i > 0 {
			speed = mode[:i]
		}
		if i < len(mode) {
			switch mode[i] {
			case 'o':
				parity = "odd"
			case 'e':
				parity = "even"
			}
			if i+1 < len(mode) && (mode[i+1] == '7' || mode[i+1] == '8') {
				word = mode[i+1 : i+2]
			}
		}
	}
	return
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

// DistributorConfig renders the /etc/default/grub line that names the
// system in the boot menu. Like CmdlineConfig it is appended after the
// package default, so it wins.
func DistributorConfig() string {
	return fmt.Sprintf("GRUB_DISTRIBUTOR=%q\n", Distributor)
}
