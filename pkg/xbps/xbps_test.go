package xbps

import (
	"fmt"
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
	cmds, err := Options{Root: "/mnt", Repo: "r", ProxyEnv: []string{"https_proxy=http://p:3128", "http_proxy=http://p:3128", "no_proxy=localhost"}, Packages: []string{"base-system"}}.Commands()
	if err != nil {
		t.Fatalf("Commands: %v", err)
	}
	install := strings.Join(cmds[0], " ")
	if !strings.Contains(install, "env") ||
		!strings.Contains(install, "https_proxy=http://p:3128") ||
		!strings.Contains(install, "http_proxy=http://p:3128") ||
		!strings.Contains(install, "no_proxy=localhost xbps-install") {
		t.Errorf("install command missing proxy env: %s", install)
	}
}

func TestCommandsLocalRepos(t *testing.T) {
	// The live medium's own repositories come first, so a package only
	// they carry (slinit-services) is found, and the remote one last.
	cmds, err := Options{
		Root:       "/mnt",
		Repo:       "https://repo-default.voidlinux.org/current",
		LocalRepos: []string{"/usr/share/slinstaller/repo", "/media/extra"},
		Packages:   []string{"base-system", "slinit-services"},
	}.Commands()
	if err != nil {
		t.Fatalf("Commands: %v", err)
	}
	want := "xbps-install -Sy -R /usr/share/slinstaller/repo -R /media/extra " +
		"-R https://repo-default.voidlinux.org/current -r /mnt base-system slinit-services"
	if got := strings.Join(cmds[0], " "); got != want {
		t.Errorf("install =\n  %s\nwant\n  %s", got, want)
	}
}

func TestCommandsExtraRepos(t *testing.T) {
	// Layering: the live medium first, then the Sunlight mirror, then Void.
	// A package the mirror carries at a higher revision wins because the
	// mirror is searched before the default remote.
	cmds, err := Options{
		Root:       "/mnt",
		Repo:       "https://repo-default.voidlinux.org/current",
		ExtraRepos: []string{"https://mirror.sunlightlinux.org/current"},
		LocalRepos: []string{"/usr/share/slinstaller/repo"},
		Packages:   []string{"base-system"},
	}.Commands()
	if err != nil {
		t.Fatalf("Commands: %v", err)
	}
	want := "xbps-install -Sy -R /usr/share/slinstaller/repo " +
		"-R https://mirror.sunlightlinux.org/current " +
		"-R https://repo-default.voidlinux.org/current -r /mnt base-system"
	if got := strings.Join(cmds[0], " "); got != want {
		t.Errorf("install =\n  %s\nwant\n  %s", got, want)
	}
}

// An extra repo alone (no Repo, no LocalRepos) is a valid install source.
func TestCommandsExtraRepoOnly(t *testing.T) {
	cmds, err := Options{
		Root:       "/mnt",
		ExtraRepos: []string{"https://mirror.sunlightlinux.org/current"},
		Packages:   []string{"base-system"},
	}.Commands()
	if err != nil {
		t.Fatalf("Commands: %v", err)
	}
	want := "xbps-install -Sy -R https://mirror.sunlightlinux.org/current -r /mnt base-system"
	if got := strings.Join(cmds[0], " "); got != want {
		t.Errorf("install =\n  %s\nwant\n  %s", got, want)
	}
}

func TestCommandsOfflineLocalOnly(t *testing.T) {
	// Offline: no remote Repo, only a local repository on the live medium.
	// The install command must carry the local -R and no empty remote -R.
	cmds, err := Options{
		Root:       "/mnt",
		LocalRepos: []string{"/usr/share/slinstaller/repo"},
		Packages:   []string{"base-system"},
	}.Commands()
	if err != nil {
		t.Fatalf("Commands: %v", err)
	}
	want := "xbps-install -Sy -R /usr/share/slinstaller/repo -r /mnt base-system"
	if got := strings.Join(cmds[0], " "); got != want {
		t.Errorf("offline install =\n  %s\nwant\n  %s", got, want)
	}
	if strings.Contains(want, "-R  ") || strings.Contains(want, "-R -r") {
		t.Error("offline install has an empty remote -R")
	}
}

// The dry run is the install's own command in a scratch root, with -n and
// no reconfigure; only a verdict on the package set is reported.
func TestResolve(t *testing.T) {
	o := Options{Root: "/mnt", Repo: "https://repo", ExtraRepos: []string{"https://mirror"}, KeysDir: "/keys", Packages: []string{"base", "linux"}}
	cmds, err := o.ResolveCommands("/tmp/scratch")
	if err != nil {
		t.Fatal(err)
	}
	got := fmt.Sprint(cmds)
	if !strings.Contains(got, "[xbps-install -S -R https://mirror -R https://repo -r /tmp/scratch] [xbps-install -n -R https://mirror -R https://repo -r /tmp/scratch base linux]") ||
		strings.Contains(got, "/mnt") || strings.Contains(got, "reconfigure") || !strings.Contains(got, "/tmp/scratch/var/db/xbps/keys") {
		t.Errorf("resolve commands: %s", got)
	}
	if _, err := (Options{Root: "/mnt"}).ResolveCommands("/s"); err == nil {
		t.Error("no repository accepted")
	}
	for out, want := range map[string]bool{
		"[*] Updating repository...\nPackage 'linx' not found in repository pool.\n":          true,
		"foo-1.0 (missing dep bar>=2)\nTransaction aborted due to unresolved dependencies.\n": true,
		"Transaction aborted due to conflicting packages.\n":                                  true,
		"ERROR: [reposync] failed to fetch file: Connection refused\n":                        false,
		"": false,
	} {
		if got := Unresolved(out); (len(got) > 0) != want {
			t.Errorf("%q: %v", out, got)
		}
	}
	long := strings.Repeat("x\n", 30) + "Package 'y' not found in repository pool.\n"
	if got := Unresolved(long); len(got) != 20 || !strings.Contains(got[19], "'y'") {
		t.Errorf("long output: %d lines", len(got))
	}
}
