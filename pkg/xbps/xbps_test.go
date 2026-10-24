package xbps

import (
	"strings"
	"testing"
)

func TestCommands(t *testing.T) {
	o := Options{
		Root:     "/mnt",
		Repo:     "https://repo.sunlightlinux.org/current",
		KeysDir:  "/var/db/xbps/keys",
		Arch:     "aarch64",
		Packages: []string{"base-system", "slinit"},
	}
	cmds, err := o.Commands()
	if err != nil {
		t.Fatalf("Commands: %v", err)
	}
	if len(cmds) != 4 {
		t.Fatalf("got %d commands, want 4 (mkdir keys, cp keys, install, reconfigure)", len(cmds))
	}

	want := []string{
		"mkdir -p /mnt/var/db/xbps/keys",
		"cp -a /var/db/xbps/keys/. /mnt/var/db/xbps/keys/",
		"env XBPS_TARGET_ARCH=aarch64 xbps-install -Sy -R https://repo.sunlightlinux.org/current -r /mnt base-system slinit",
		"chroot /mnt xbps-reconfigure -fa",
	}
	for i, w := range want {
		if got := strings.Join(cmds[i], " "); got != w {
			t.Errorf("command %d =\n  %s\nwant\n  %s", i, got, w)
		}
	}
}

func TestCommandsNoKeysNoArch(t *testing.T) {
	// Without keys or arch: just install and reconfigure, and the
	// install command carries no env prefix.
	cmds, err := Options{Root: "/mnt", Repo: "r", Packages: []string{"base-system"}}.Commands()
	if err != nil {
		t.Fatalf("Commands: %v", err)
	}
	if len(cmds) != 2 {
		t.Fatalf("got %d commands, want 2", len(cmds))
	}
	if cmds[0][0] != "xbps-install" {
		t.Errorf("first token = %q, want xbps-install (no keys, no env prefix)", cmds[0][0])
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

func TestCommandsProxy(t *testing.T) {
	cmds, err := Options{Root: "/mnt", Repo: "r", Proxy: "http://p:3128", Packages: []string{"base-system"}}.Commands()
	if err != nil {
		t.Fatalf("Commands: %v", err)
	}
	install := strings.Join(cmds[0], " ")
	if !strings.Contains(install, "env") ||
		!strings.Contains(install, "https_proxy=http://p:3128") ||
		!strings.Contains(install, "http_proxy=http://p:3128") {
		t.Errorf("install command missing proxy env: %s", install)
	}
}
