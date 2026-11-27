package system

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The first-boot writers run against a freshly installed tree, after the
// disk has been wiped. If one of them cannot write, the install has to stop
// and say so — a swallowed error leaves a system that boots without a
// hostname, a sudoers entry or its services. These tests make each write fail
// and check the failure comes back, naming what it was doing.
//
// Three ways to make a write fail that work for root too:
//   - blockDir:  a regular file where a directory has to be made
//   - blockFile: a symlink into a directory that does not exist, where a
//     file has to be written
//   - blockLink: a non-empty directory where a symlink has to be made

func blockDir(t *testing.T, root string, rel ...string) {
	t.Helper()
	p := filepath.Join(append([]string{root}, rel...)...)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, nil, 0o644); err != nil {
		t.Fatal(err)
	}
}

func blockFile(t *testing.T, root string, rel ...string) {
	t.Helper()
	p := filepath.Join(append([]string{root}, rel...)...)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "no-such-dir", "x"), p); err != nil {
		t.Fatal(err)
	}
}

func blockLink(t *testing.T, root string, rel ...string) {
	t.Helper()
	p := filepath.Join(append([]string{root}, rel...)...)
	if err := os.MkdirAll(filepath.Join(p, "occupied"), 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestFirstBootWritersReportFailure(t *testing.T) {
	user := User{Login: "ion", SSHKeys: []string{"ssh-ed25519 AAAA"}}
	slinitSvc := func(t *testing.T, root string) { // the service to enable exists
		t.Helper()
		dir := filepath.Join(root, "usr", "lib", "slinit.d")
		os.MkdirAll(dir, 0o755)
		os.WriteFile(filepath.Join(dir, "sshd"), []byte("type = process\n"), 0o644)
	}
	cases := []struct {
		name  string
		setup func(t *testing.T, root string)
		run   func(root string) error
		want  string // part of the error
	}{
		{"hostname: /etc", func(t *testing.T, r string) { blockDir(t, r, "etc") },
			func(r string) error { return Hostname(r, "box") }, "system:"},
		{"hostname: file", func(t *testing.T, r string) { blockFile(t, r, "etc", "hostname") },
			func(r string) error { return Hostname(r, "box") }, "write hostname"},
		{"machine-id: /etc", func(t *testing.T, r string) { blockDir(t, r, "etc") },
			func(r string) error { return MachineID(r) }, "system:"},
		{"machine-id: file", func(t *testing.T, r string) { blockFile(t, r, "etc", "machine-id") },
			func(r string) error { return MachineID(r) }, "write machine-id"},
		{"timezone: /etc", func(t *testing.T, r string) { blockDir(t, r, "etc") },
			func(r string) error { return Timezone(r, "UTC") }, "system:"},
		{"timezone: link", func(t *testing.T, r string) { blockLink(t, r, "etc", "localtime") },
			func(r string) error { return Timezone(r, "UTC") }, "link localtime"},
		{"keymap: /etc", func(t *testing.T, r string) { blockDir(t, r, "etc") },
			func(r string) error { return Keymap(r, "us") }, "system:"},
		{"keymap: open", func(t *testing.T, r string) { blockLink(t, r, "etc", "rc.conf") },
			func(r string) error { return Keymap(r, "us") }, "open rc.conf"},
		{"locale: /etc", func(t *testing.T, r string) { blockDir(t, r, "etc") },
			func(r string) error { _, err := Locale(r, "en_US.UTF-8"); return err }, "system:"},
		{"locale: locale.conf", func(t *testing.T, r string) { blockFile(t, r, "etc", "locale.conf") },
			func(r string) error { _, err := Locale(r, "en_US.UTF-8"); return err }, "write locale.conf"},
		{"locale: libc-locales unreadable", func(t *testing.T, r string) { blockLink(t, r, "etc", "default", "libc-locales") },
			func(r string) error { _, err := Locale(r, "en_US.UTF-8"); return err }, "read libc-locales"},
		{"sudoers: dir", func(t *testing.T, r string) { blockDir(t, r, "etc", "sudoers.d") },
			func(r string) error { return WheelSudo(r) }, "system:"},
		// (An occupied directory: a stale file or link at the name is replaced.)
		{"sudoers: file", func(t *testing.T, r string) { blockLink(t, r, "etc", "sudoers.d", "wheel") },
			func(r string) error { return WheelSudo(r) }, "write sudoers wheel"},
		{"autoupdate: dir", func(t *testing.T, r string) { blockDir(t, r, "etc", "slinit.d") },
			func(r string) error { return AutoUpdate(r, true) }, "system:"},
		{"autoupdate: file", func(t *testing.T, r string) { blockFile(t, r, "etc", "slinit.d", AutoUpdateServiceName) },
			func(r string) error { return AutoUpdate(r, true) }, "write " + AutoUpdateServiceName},
		{"runit service: dir", func(t *testing.T, r string) { blockDir(t, r, "etc", "runit") },
			func(r string) error { return EnableService(r, "dhcpcd") }, "system:"},
		{"runit service: link", func(t *testing.T, r string) {
			blockLink(t, r, "etc", "runit", "runsvdir", "default", "dhcpcd")
		}, func(r string) error { return EnableService(r, "dhcpcd") }, "enable service dhcpcd"},
		{"slinit service: dir", func(t *testing.T, r string) { slinitSvc(t, r); blockDir(t, r, "etc", "slinit.d", "boot.d") },
			func(r string) error { return EnableSlinitService(r, "sshd") }, "system:"},
		{"slinit service: link", func(t *testing.T, r string) {
			slinitSvc(t, r)
			blockLink(t, r, "etc", "slinit.d", "boot.d", "sshd")
		}, func(r string) error { return EnableSlinitService(r, "sshd") }, "enable slinit service sshd"},
		{"ssh keys: dir", func(t *testing.T, r string) { blockDir(t, r, "home") },
			func(r string) error { _, err := user.WriteSSHKeys(r); return err }, "system:"},
		// A link there is replaced, not a failure; a directory in the way is.
		{"ssh keys: file", func(t *testing.T, r string) {
			os.MkdirAll(filepath.Join(r, "home", "ion", ".ssh", "authorized_keys", "x"), 0o755)
		},
			func(r string) error { _, err := user.WriteSSHKeys(r); return err }, "write authorized_keys"},
		{"printk: dir", func(t *testing.T, r string) { blockDir(t, r, "etc", "sysctl.d") },
			func(r string) error { return PrintkSysctl(r, 3) }, "sysctl.d"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root := t.TempDir()
			c.setup(t, root)
			err := c.run(root)
			if err == nil {
				t.Fatal("the failed write was not reported")
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("error %q does not say %q", err, c.want)
			}
		})
	}
}

func TestKeymapReportsAFailedWrite(t *testing.T) {
	if _, err := os.Stat("/dev/full"); err != nil {
		t.Skip("no /dev/full to fail a write with")
	}
	// rc.conf opens fine and every write to it fails, as on a full disk.
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "etc"), 0o755)
	if err := os.Symlink("/dev/full", filepath.Join(root, "etc", "rc.conf")); err != nil {
		t.Fatal(err)
	}
	if err := Keymap(root, "us"); err == nil || !strings.Contains(err.Error(), "write rc.conf") {
		t.Errorf("Keymap on a full disk: %v", err)
	}
}

func TestLocaleReportsAFailedLibcLocalesWrite(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes through a read-only file")
	}
	root := t.TempDir()
	dir := filepath.Join(root, "etc", "default")
	os.MkdirAll(dir, 0o755)
	if err := os.WriteFile(filepath.Join(dir, "libc-locales"), []byte("#en_US.UTF-8 UTF-8\n"), 0o444); err != nil {
		t.Fatal(err)
	}
	if _, err := Locale(root, "en_US.UTF-8"); err == nil || !strings.Contains(err.Error(), "write libc-locales") {
		t.Errorf("Locale with a read-only libc-locales: %v", err)
	}
}

func TestEmptyValuesAreLeftUnset(t *testing.T) {
	// A config that leaves these out gets nothing written, not an empty file.
	root := t.TempDir()
	if err := Timezone(root, ""); err != nil {
		t.Fatal(err)
	}
	if changed, err := Locale(root, ""); err != nil || changed {
		t.Fatalf("Locale(\"\") = %v, %v", changed, err)
	}
	if entries, _ := os.ReadDir(root); len(entries) != 0 {
		t.Errorf("empty values wrote %d entries", len(entries))
	}
	// A user's full name becomes the account's comment field.
	cmd := strings.Join(User{Login: "ion", FullName: "Ion N"}.AddCommand("/mnt"), " ")
	if !strings.Contains(cmd, "-c Ion N ion") {
		t.Errorf("AddCommand = %q", cmd)
	}
}

func TestKeymapAndZoneListsWithoutData(t *testing.T) {
	savedZ, savedK := zoneinfoDir, keymapsDir
	defer func() { zoneinfoDir, keymapsDir = savedZ, savedK }()

	// No data on the live system: empty lists, not an error.
	zoneinfoDir = filepath.Join(t.TempDir(), "none")
	keymapsDir = filepath.Join(t.TempDir(), "none")
	if got := ListTimezones(); len(got) != 0 {
		t.Errorf("zones without data: %v", got)
	}
	if got := ListKeymaps(); len(got) != 0 {
		t.Errorf("keymaps without data: %v", got)
	}

	// With data: files that are not keymaps are ignored, a path or a dotted
	// name is refused before the tree is searched, and the search stops at
	// the first match.
	keymapsDir = t.TempDir()
	os.MkdirAll(filepath.Join(keymapsDir, "i386", "qwerty"), 0o755)
	for _, f := range []string{"i386/qwerty/us.map.gz", "i386/qwerty/ro.map", "i386/qwerty/README", "i386/zz.map"} {
		os.WriteFile(filepath.Join(keymapsDir, f), nil, 0o644)
	}
	if got := strings.Join(ListKeymaps(), ","); got != "ro,us,zz" {
		t.Errorf("keymaps = %q", got)
	}
	for _, bad := range []string{"", "../us", "us.map", "i386/us"} {
		if ok, checked := ValidKeymap(bad); ok || !checked {
			t.Errorf("ValidKeymap(%q) = %v, %v; want refused and checked", bad, ok, checked)
		}
	}
	if ok, _ := ValidKeymap("ro"); !ok {
		t.Error("ro.map not found")
	}
}
