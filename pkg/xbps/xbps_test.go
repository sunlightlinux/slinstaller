package xbps

import (
	"strings"
	"testing"
)

func TestCommands(t *testing.T) {
	o := Options{
		Root:     "/mnt",
		Repo:     "https://repo.sunlightlinux.org/current",
		Arch:     "x86_64",
		Packages: []string{"base-system", "slinit"},
	}
	cmds, err := o.Commands()
	if err != nil {
		t.Fatalf("Commands: %v", err)
	}
	if len(cmds) != 2 {
		t.Fatalf("got %d commands, want 2 (install, reconfigure)", len(cmds))
	}

	install := strings.Join(cmds[0], " ")
	want := "env XBPS_ARCH=x86_64 xbps-install -Sy -R https://repo.sunlightlinux.org/current -r /mnt base-system slinit"
	if install != want {
		t.Errorf("install command =\n  %s\nwant\n  %s", install, want)
	}

	reconf := strings.Join(cmds[1], " ")
	if reconf != "chroot /mnt xbps-reconfigure -a" {
		t.Errorf("reconfigure command = %q", reconf)
	}
}

func TestCommandsNoArch(t *testing.T) {
	// Without Arch, the install command does not set XBPS_ARCH.
	cmds, err := Options{Root: "/mnt", Repo: "r", Packages: []string{"base-system"}}.Commands()
	if err != nil {
		t.Fatalf("Commands: %v", err)
	}
	if cmds[0][0] != "xbps-install" {
		t.Errorf("first token = %q, want xbps-install (no env prefix)", cmds[0][0])
	}
}

func TestCommandsValidation(t *testing.T) {
	cases := []Options{
		{Repo: "r", Packages: []string{"p"}},    // no root
		{Root: "/mnt", Packages: []string{"p"}}, // no repo
		{Root: "/mnt", Repo: "r"},               // no packages
	}
	for i, o := range cases {
		if _, err := o.Commands(); err == nil {
			t.Errorf("case %d: Commands() = nil error, want failure", i)
		}
	}
}
