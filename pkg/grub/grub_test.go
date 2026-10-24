package grub

import (
	"strings"
	"testing"
)

func TestCommands(t *testing.T) {
	cmds, err := Options{Root: "/mnt", ESPDir: "/boot"}.Commands()
	if err != nil {
		t.Fatalf("Commands: %v", err)
	}
	if len(cmds) != 2 {
		t.Fatalf("got %d commands, want 2", len(cmds))
	}
	want := []string{
		"chroot /mnt grub-install --target=x86_64-efi --efi-directory=/boot --bootloader-id=Sunlight",
		"chroot /mnt grub-mkconfig -o /boot/grub/grub.cfg",
	}
	for i, w := range want {
		if got := strings.Join(cmds[i], " "); got != w {
			t.Errorf("command %d =\n  %s\nwant\n  %s", i, got, w)
		}
	}
}

func TestCommandsOverrides(t *testing.T) {
	cmds, _ := Options{Root: "/mnt", ESPDir: "/boot/efi", ID: "Custom", Target: "arm64-efi"}.Commands()
	got := strings.Join(cmds[0], " ")
	for _, frag := range []string{"--target=arm64-efi", "--efi-directory=/boot/efi", "--bootloader-id=Custom"} {
		if !strings.Contains(got, frag) {
			t.Errorf("grub-install missing %q: %s", frag, got)
		}
	}
}

func TestCommandsValidation(t *testing.T) {
	if _, err := (Options{ESPDir: "/boot"}).Commands(); err == nil {
		t.Error("no root = nil error, want failure")
	}
	if _, err := (Options{Root: "/mnt"}).Commands(); err == nil {
		t.Error("no ESP = nil error, want failure")
	}
}

func TestCmdlineConfig(t *testing.T) {
	if got := (Options{}).CmdlineConfig(); got != "" {
		t.Errorf("no args should render nothing, got %q", got)
	}
	got := Options{KernelArgs: []string{"quiet", "loglevel=3"}}.CmdlineConfig()
	want := "GRUB_CMDLINE_LINUX_DEFAULT=\"quiet loglevel=3\"\n"
	if got != want {
		t.Errorf("CmdlineConfig = %q, want %q", got, want)
	}
}
