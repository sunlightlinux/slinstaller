package grub

import (
	"strings"
	"testing"
)

func flat(cmds [][]string) string {
	var b strings.Builder
	for _, c := range cmds {
		b.WriteString(strings.Join(c, " ") + "\n")
	}
	return b.String()
}

func TestRemovableAndNoNVRAM(t *testing.T) {
	o := Options{Root: "/mnt", ESPDir: "/boot"}
	cmds, _ := o.Commands()
	if got := flat(cmds); strings.Contains(got, "--removable") || strings.Contains(got, "--no-nvram") || len(cmds) != 2 {
		t.Errorf("default install changed:\n%s", got)
	}

	// Removable is a second copy: the firmware entry is still made by the
	// first, and the menu is generated last.
	o.Removable = true
	cmds, _ = o.Commands()
	if len(cmds) != 3 {
		t.Fatalf("commands:\n%s", flat(cmds))
	}
	first, second := strings.Join(cmds[0], " "), strings.Join(cmds[1], " ")
	if !strings.Contains(first, "--bootloader-id=Sunlight") || strings.Contains(first, "--removable") {
		t.Errorf("first install = %s", first)
	}
	if !strings.Contains(second, "--removable") || !strings.Contains(second, "--efi-directory=/boot") || strings.Contains(second, "--bootloader-id") {
		t.Errorf("removable install = %s", second)
	}
	if cmds[2][2] != "grub-mkconfig" {
		t.Errorf("last command = %v", cmds[2])
	}

	// Without NVRAM the firmware's entries are not touched by either.
	o.NoNVRAM = true
	cmds, _ = o.Commands()
	if !strings.Contains(strings.Join(cmds[0], " "), "--no-nvram") {
		t.Errorf("first install = %v", cmds[0])
	}
}

func TestSerialTerminalFollowsTheKernelConsole(t *testing.T) {
	if got := (Options{}).TerminalConfig(); got != "" {
		t.Errorf("nothing asked for, but %q", got)
	}
	for _, tc := range []struct {
		args string
		want string
	}{
		{"", "--unit=0 --speed=115200 --word=8 --parity=no --stop=1"},
		{"quiet console=tty0 console=ttyS1,57600n8", "--unit=1 --speed=57600 --word=8 --parity=no --stop=1"},
		{"console=ttyS0", "--unit=0 --speed=115200 --word=8 --parity=no --stop=1"},
		{"console=ttyS2,9600e7", "--unit=2 --speed=9600 --word=7 --parity=even --stop=1"},
		{"console=ttyS0,19200o", "--unit=0 --speed=19200 --word=8 --parity=odd --stop=1"},
		// The kernel uses the last console= for /dev/console; so does GRUB.
		{"console=ttyS0,9600 console=ttyS3,115200n8", "--unit=3 --speed=115200"},
		// Not a COM port GRUB can name: the default.
		{"console=ttyUSB0,115200 console=ttySx", "--unit=0 --speed=115200"},
	} {
		got := Options{SerialTerminal: true, KernelArgs: strings.Fields(tc.args)}.TerminalConfig()
		if !strings.Contains(got, `GRUB_TERMINAL="console serial"`) || !strings.Contains(got, `GRUB_SERIAL_COMMAND="serial `+tc.want) {
			t.Errorf("args %q:\n%s", tc.args, got)
		}
	}
	if got := (Options{ProbeOtherOS: true}).TerminalConfig(); got != "GRUB_DISABLE_OS_PROBER=false\n" {
		t.Errorf("probe-other-os = %q", got)
	}
}
