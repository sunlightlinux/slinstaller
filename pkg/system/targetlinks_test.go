package system

import (
	"os"
	"path/filepath"
	"testing"
)

// A directory of an image or a reused root may be an absolute link: it
// means a place in the installed system, and from the live system, where
// the installer runs, it names the live system's own. Every file written
// into the target lands where the installed system would see it, and
// nothing lands on (or changes the mode of) the live system.
func TestWritesResolveLinksInsideTheTarget(t *testing.T) {
	type tc struct {
		name string
		link string // a directory in the target, made an absolute link
		do   func(root string) error
		want string // where the file must land, relative to the link's target
	}
	cases := []tc{
		{"ssh keys", "home", func(root string) error {
			_, err := User{Login: "ion", SSHKeys: []string{"ssh-ed25519 AAAA"}}.WriteSSHKeys(root)
			return err
		}, "ion/.ssh/authorized_keys"},
		{"seed", "var/lib/seedrng", SeedRNG, seedName},
		{"sudoers", "etc/sudoers.d", WheelSudo, "wheel"},
		{"printk", "etc/sysctl.d", func(root string) error { return PrintkSysctl(root, 3) }, "10-printk.conf"},
		{"autoupdate", "etc/slinit.d", func(root string) error { return AutoUpdate(root, true) }, AutoUpdateServiceName},
		{"netlinks", "etc/slinit.d", func(root string) error { return NetLinks(root, "ip link add bond0 type bond\n") }, "sunlight-netlinks.sh"},
		{"runit service", "etc/runit/runsvdir/default", func(root string) error { return EnableService(root, "dhcpcd") }, "dhcpcd"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root := t.TempDir()
			os.MkdirAll(filepath.Join(root, "usr", "lib", "slinit.d", "boot"), 0o755)
			os.MkdirAll(filepath.Join(root, "etc"), 0o755)
			// The live system's directory of that name: absolute, so it
			// is here, outside root.
			live := t.TempDir()
			os.Chmod(live, 0o755)
			// Inside the target it means <root>/<live>.
			inside := filepath.Join(root, live)
			os.MkdirAll(inside, 0o755)
			os.MkdirAll(filepath.Dir(filepath.Join(root, c.link)), 0o755)
			if err := os.Symlink(live, filepath.Join(root, c.link)); err != nil {
				t.Fatal(err)
			}
			if err := c.do(root); err != nil {
				t.Fatal(err)
			}
			ents, _ := os.ReadDir(live)
			if len(ents) > 0 {
				t.Errorf("written into the live system's %s: %v", live, ents)
			}
			if fi, _ := os.Stat(live); fi.Mode().Perm() != 0o755 {
				t.Errorf("the live directory's mode changed to %v", fi.Mode().Perm())
			}
			if _, err := os.Lstat(filepath.Join(inside, c.want)); err != nil {
				t.Errorf("not written where the installed system looks: %v", err)
			}
		})
	}
}

func TestTargetPath(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "var", "home"), 0o755)
	os.MkdirAll(filepath.Join(root, "etc"), 0o755)
	os.Symlink("/var/home", filepath.Join(root, "home"))              // absolute
	os.Symlink("../../../../../..", filepath.Join(root, "etc", "up")) // climbs past root
	os.Symlink("loop", filepath.Join(root, "loop"))
	os.Symlink("var/home", filepath.Join(root, "rel"))
	for rel, want := range map[string]string{
		"home/ion/.ssh": "var/home/ion/.ssh",
		"/home":         "var/home",
		"etc/up/etc":    "etc",
		"../../etc":     "etc",
		"rel/x":         "var/home/x",
		"etc/new/dir":   "etc/new/dir",
	} {
		got, err := TargetPath(root, rel)
		if err != nil || got != filepath.Join(root, want) {
			t.Errorf("TargetPath(%q) = %q, %v; want %q", rel, got, err, filepath.Join(root, want))
		}
	}
	if _, err := TargetPath(root, "loop/x"); err == nil {
		t.Error("a link cycle was resolved")
	}
}
