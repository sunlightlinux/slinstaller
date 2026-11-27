package system

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestHostname(t *testing.T) {
	root := t.TempDir()
	if err := Hostname(root, "sunlight"); err != nil {
		t.Fatalf("Hostname: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(root, "etc", "hostname"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != "sunlight\n" {
		t.Errorf("hostname = %q, want %q", got, "sunlight\n")
	}

	// Empty name writes nothing.
	root2 := t.TempDir()
	if err := Hostname(root2, ""); err != nil {
		t.Fatalf("Hostname empty: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root2, "etc", "hostname")); !os.IsNotExist(err) {
		t.Error("empty hostname should not write a file")
	}
}

func TestTimezone(t *testing.T) {
	root := t.TempDir()
	if err := Timezone(root, "Europe/Bucharest"); err != nil {
		t.Fatalf("Timezone: %v", err)
	}
	target, err := os.Readlink(filepath.Join(root, "etc", "localtime"))
	if err != nil {
		t.Fatalf("readlink: %v", err)
	}
	if target != "/usr/share/zoneinfo/Europe/Bucharest" {
		t.Errorf("localtime -> %q", target)
	}
}

func TestUserAddCommand(t *testing.T) {
	// Admin user with password and shell.
	u := User{Login: "ion", PasswordHash: "$6$abc$def", Shell: "/bin/bash", Admin: true}
	got := strings.Join(u.AddCommand("/mnt"), " ")
	want := "chroot /mnt useradd -m -G wheel -s /bin/bash -p $6$abc$def ion"
	if got != want {
		t.Errorf("AddCommand =\n  %s\nwant\n  %s", got, want)
	}

	// Plain user, no password: no -p, no wheel.
	plain := strings.Join(User{Login: "guest"}.AddCommand("/mnt"), " ")
	if strings.Contains(plain, "-p") || strings.Contains(plain, "wheel") {
		t.Errorf("plain user command has -p or wheel: %s", plain)
	}
	if !strings.HasSuffix(plain, " guest") {
		t.Errorf("login not last: %s", plain)
	}
}

func TestWriteSSHKeys(t *testing.T) {
	root := t.TempDir()
	u := User{Login: "ion", SSHKeys: []string{"ssh-ed25519 AAAA", "ssh-rsa BBBB"}}
	chown, err := u.WriteSSHKeys(root)
	if err != nil {
		t.Fatalf("WriteSSHKeys: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(root, "home", "ion", ".ssh", "authorized_keys"))
	if err != nil {
		t.Fatalf("read keys: %v", err)
	}
	if string(got) != "ssh-ed25519 AAAA\nssh-rsa BBBB\n" {
		t.Errorf("authorized_keys = %q", got)
	}
	if strings.Join(chown, " ") != "chroot "+root+" chown ion:ion /home/ion/.ssh /home/ion/.ssh/authorized_keys" {
		t.Errorf("chown command = %v", chown)
	}

	// No keys: nothing written, nil command.
	if cmd, err := (User{Login: "x"}).WriteSSHKeys(t.TempDir()); err != nil || cmd != nil {
		t.Errorf("no keys should be a no-op, got %v / %v", cmd, err)
	}

	// A file already there (a preserved home) is rewritten, and made 0600
	// whatever mode it had: sshd ignores keys others can write.
	file := filepath.Join(root, "home", "ion", ".ssh", "authorized_keys")
	os.WriteFile(file, []byte("old key\n"), 0o666)
	os.Chmod(file, 0o666)
	if _, err := u.WriteSSHKeys(root); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(file); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("a kept authorized_keys: %v, %v", fi.Mode().Perm(), err)
	}
	if got, _ := os.ReadFile(file); string(got) != "ssh-ed25519 AAAA\nssh-rsa BBBB\n" {
		t.Errorf("a kept authorized_keys holds %q", got)
	}
}

// A preserved home belongs to its previous owner: links planted in it are
// replaced or refused, never followed.
func TestWriteSSHKeysFollowsNoLink(t *testing.T) {
	u := User{Login: "ion", SSHKeys: []string{"ssh-ed25519 AAAA"}}
	setup := func(t *testing.T) (root, shadow string) {
		root = t.TempDir()
		os.MkdirAll(filepath.Join(root, "etc"), 0o755)
		shadow = filepath.Join(root, "etc", "shadow")
		os.WriteFile(shadow, []byte("root:!:1::::::\n"), 0o600)
		return root, shadow
	}
	intact := func(t *testing.T, shadow string) {
		if got, _ := os.ReadFile(shadow); string(got) != "root:!:1::::::\n" {
			t.Errorf("shadow was written through a link: %q", got)
		}
	}

	// authorized_keys -> ../../../etc/shadow
	root, shadow := setup(t)
	ssh := filepath.Join(root, "home", "ion", ".ssh")
	os.MkdirAll(ssh, 0o700)
	os.Symlink("../../../etc/shadow", filepath.Join(ssh, "authorized_keys"))
	if _, err := u.WriteSSHKeys(root); err != nil {
		t.Fatal(err)
	}
	intact(t, shadow)
	if fi, err := os.Lstat(filepath.Join(ssh, "authorized_keys")); err != nil || !fi.Mode().IsRegular() || fi.Mode().Perm() != 0o600 {
		t.Errorf("authorized_keys = %v, %v", fi, err)
	}

	// a hard link to the shadow file
	root, shadow = setup(t)
	ssh = filepath.Join(root, "home", "ion", ".ssh")
	os.MkdirAll(ssh, 0o700)
	if err := os.Link(shadow, filepath.Join(ssh, "authorized_keys")); err == nil {
		if _, err := u.WriteSSHKeys(root); err != nil {
			t.Fatal(err)
		}
		intact(t, shadow)
	}

	// .ssh -> ../../etc
	root, shadow = setup(t)
	os.MkdirAll(filepath.Join(root, "home", "ion"), 0o755)
	os.Symlink("../../etc", filepath.Join(root, "home", "ion", ".ssh"))
	if _, err := u.WriteSSHKeys(root); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "etc", "authorized_keys")); err == nil {
		t.Error("keys written into /etc through a .ssh link")
	}
	intact(t, shadow)

	// the home itself a link: refused
	root, _ = setup(t)
	os.MkdirAll(filepath.Join(root, "home"), 0o755)
	os.Symlink("/etc", filepath.Join(root, "home", "ion"))
	if _, err := u.WriteSSHKeys(root); err == nil || !strings.Contains(err.Error(), "symbolic link") {
		t.Errorf("a linked home: %v", err)
	}
}

func TestKeymap(t *testing.T) {
	root := t.TempDir()
	if err := Keymap(root, "us"); err != nil {
		t.Fatalf("Keymap: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(root, "etc", "rc.conf"))
	if err != nil {
		t.Fatalf("read rc.conf: %v", err)
	}
	if !strings.Contains(string(got), "KEYMAP=\"us\"") {
		t.Errorf("rc.conf = %q, want it to set KEYMAP", got)
	}
	// Letters, digits and ._-+@ are written as they are; anything else is
	// single-quoted, the ends of each range included.
	for v, want := range map[string]string{"azAZ09._-+@": "azAZ09._-+@", "a b": "'a b'", "a`b": "'a`b'", "a'b": `'a'\''b'`, "": "''"} {
		if got := shellValue(v, false); got != want {
			t.Errorf("shellValue(%q) = %s, want %s", v, got, want)
		}
	}

	// Empty code writes nothing.
	root2 := t.TempDir()
	if err := Keymap(root2, ""); err != nil {
		t.Fatalf("Keymap empty: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root2, "etc", "rc.conf")); !os.IsNotExist(err) {
		t.Error("empty keymap should not write rc.conf")
	}
}

func TestLocaleGlibc(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "etc", "default"), 0o755)
	// A libc-locales file with our locale commented out, plus noise.
	libc := "#aa_DJ.UTF-8 UTF-8\n#en_US.UTF-8 UTF-8\n#de_DE.UTF-8 UTF-8\n"
	os.WriteFile(filepath.Join(root, "etc", "default", "libc-locales"), []byte(libc), 0o644)

	generated, err := Locale(root, "en_US.UTF-8")
	if err != nil {
		t.Fatalf("Locale: %v", err)
	}
	if !generated {
		t.Error("glibc locale should report generated=true")
	}

	conf, _ := os.ReadFile(filepath.Join(root, "etc", "locale.conf"))
	if string(conf) != "LANG=en_US.UTF-8\n" {
		t.Errorf("locale.conf = %q", conf)
	}
	out, _ := os.ReadFile(filepath.Join(root, "etc", "default", "libc-locales"))
	if !strings.Contains(string(out), "\nen_US.UTF-8 UTF-8\n") {
		t.Errorf("en_US line not uncommented:\n%s", out)
	}
	// Other locales stay commented.
	if !strings.Contains(string(out), "#de_DE.UTF-8 UTF-8") {
		t.Error("unrelated locale was changed")
	}

	// Void's file opens with bare "#" lines; a locale it does not list is
	// no change, and the file is left as it was.
	libc = "#\n# Comment out locales you do not need\n#\n#de_DE.UTF-8 UTF-8\n"
	os.WriteFile(filepath.Join(root, "etc", "default", "libc-locales"), []byte(libc), 0o644)
	if generated, err := Locale(root, "xx_XX.UTF-8"); err != nil || generated {
		t.Errorf("a locale not listed: generated %v, %v", generated, err)
	}
	if out, _ := os.ReadFile(filepath.Join(root, "etc", "default", "libc-locales")); string(out) != libc {
		t.Errorf("libc-locales changed for a locale it does not list:\n%s", out)
	}
}

func TestLocaleMusl(t *testing.T) {
	// No libc-locales file (musl): writes locale.conf, reports no generation.
	root := t.TempDir()
	generated, err := Locale(root, "en_US.UTF-8")
	if err != nil {
		t.Fatalf("Locale: %v", err)
	}
	if generated {
		t.Error("without libc-locales, generated should be false")
	}
	if _, err := os.Stat(filepath.Join(root, "etc", "locale.conf")); err != nil {
		t.Error("locale.conf should still be written")
	}
}

func TestEnableService(t *testing.T) {
	root := t.TempDir()
	if err := EnableService(root, "dhcpcd"); err != nil {
		t.Fatalf("EnableService: %v", err)
	}
	link := filepath.Join(root, "etc", "runit", "runsvdir", "default", "dhcpcd")
	target, err := os.Readlink(link)
	if err != nil {
		t.Fatalf("readlink: %v", err)
	}
	if target != "/etc/sv/dhcpcd" {
		t.Errorf("service link -> %q, want /etc/sv/dhcpcd", target)
	}
}

func TestWheelSudo(t *testing.T) {
	root := t.TempDir()
	if err := WheelSudo(root); err != nil {
		t.Fatalf("WheelSudo: %v", err)
	}
	path := filepath.Join(root, "etc", "sudoers.d", "wheel")
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(string(got), "%wheel ALL=(ALL:ALL) ALL") {
		t.Errorf("sudoers.d/wheel = %q", got)
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0o440 {
		t.Errorf("mode = %o, want 440", info.Mode().Perm())
	}
}

func TestMachineID(t *testing.T) {
	root := t.TempDir()
	if err := MachineID(root); err != nil {
		t.Fatalf("MachineID: %v", err)
	}
	path := filepath.Join(root, "etc", "machine-id")
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	id := strings.TrimSuffix(string(got), "\n")
	if len(id) != 32 || strings.Trim(id, "0123456789abcdef") != "" {
		t.Errorf("machine-id = %q, want 32 lowercase hex digits", id)
	}

	// An existing ID is kept, not regenerated.
	if err := MachineID(root); err != nil {
		t.Fatalf("MachineID again: %v", err)
	}
	again, _ := os.ReadFile(path)
	if string(again) != string(got) {
		t.Errorf("machine-id changed from %q to %q; it must stay with the machine", got, again)
	}
	// An empty one (a live medium's placeholder) is no ID: it is filled.
	os.Remove(path) // written 0444; the installer, as root, writes it anyway
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := MachineID(root); err != nil {
		t.Fatalf("MachineID over an empty file: %v", err)
	}
	if got, _ := os.ReadFile(path); len(strings.TrimSpace(string(got))) != 32 {
		t.Errorf("an empty machine-id was kept: %q", got)
	}
}

func TestEnableSlinitService(t *testing.T) {
	root := t.TempDir()
	svcDir := filepath.Join(root, "usr", "lib", "slinit.d")
	if err := os.MkdirAll(svcDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if IsSlinitSystem(root) {
		t.Error("IsSlinitSystem with no boot service = true, want false")
	}
	for _, f := range []string{"boot", "sshd"} {
		if err := os.WriteFile(filepath.Join(svcDir, f), []byte("type = internal\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if !IsSlinitSystem(root) {
		t.Error("IsSlinitSystem with slinit-services installed = false, want true")
	}

	if err := EnableSlinitService(root, "sshd"); err != nil {
		t.Fatalf("EnableSlinitService: %v", err)
	}
	link := filepath.Join(root, "etc", "slinit.d", "boot.d", "sshd")
	got, err := os.Readlink(link)
	if err != nil {
		t.Fatalf("readlink: %v", err)
	}
	// An absolute path inside the installed system, as slinitctl enable
	// writes it, so it resolves once the target is booted.
	if got != "/usr/lib/slinit.d/sshd" {
		t.Errorf("link -> %q, want /usr/lib/slinit.d/sshd", got)
	}

	// A service that does not exist is an error, not a dangling link.
	if err := EnableSlinitService(root, "no-such-service"); err == nil {
		t.Error("EnableSlinitService of a missing service: got nil, want an error")
	}
	if _, err := os.Lstat(filepath.Join(root, "etc", "slinit.d", "boot.d", "no-such-service")); !os.IsNotExist(err) {
		t.Error("a link was left for a missing service")
	}
}

func TestIsUEFI(t *testing.T) {
	saved := efiFirmwarePath
	defer func() { efiFirmwarePath = saved }()

	// Absent path: not UEFI.
	efiFirmwarePath = filepath.Join(t.TempDir(), "nope")
	if IsUEFI() {
		t.Error("IsUEFI() = true for a missing firmware path")
	}

	// A regular file is not the firmware dir.
	f := filepath.Join(t.TempDir(), "efi")
	if err := os.WriteFile(f, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	efiFirmwarePath = f
	if IsUEFI() {
		t.Error("IsUEFI() = true for a regular file")
	}

	// A directory (as sysfs presents it) means UEFI.
	d := filepath.Join(t.TempDir(), "efi")
	if err := os.Mkdir(d, 0o755); err != nil {
		t.Fatal(err)
	}
	efiFirmwarePath = d
	if !IsUEFI() {
		t.Error("IsUEFI() = false for an existing firmware directory")
	}
}

func TestValidTimezone(t *testing.T) {
	saved := zoneinfoDir
	defer func() { zoneinfoDir = saved }()

	// No data: checked=false, never rejects.
	zoneinfoDir = filepath.Join(t.TempDir(), "absent")
	if ok, checked := ValidTimezone("Whatever"); checked {
		t.Errorf("missing zoneinfo should be unchecked, got ok=%v checked=%v", ok, checked)
	}

	// With data: a real zone passes, a bad one and traversal fail.
	dir := filepath.Join(t.TempDir(), "zoneinfo")
	zoneinfoDir = dir
	os.WriteFile(filepath.Join(filepath.Dir(dir), "outside"), []byte("tz"), 0o644)
	if err := os.MkdirAll(filepath.Join(dir, "Europe"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "Europe", "Bucharest"), []byte("tz"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "UTC"), []byte("tz"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		tz   string
		want bool
	}{
		{"UTC", true}, {"Europe/Bucharest", true},
		{"Europe/Nope", false}, {"../etc/passwd", false}, {"Europe", false}, // a dir is not a zone
		// Names that would reach a file, refused by their form: one up and
		// out of the zoneinfo tree, and an absolute path (which Join would
		// otherwise take as under it).
		{"../outside", false}, {"Europe/../../outside", false}, {"/UTC", false},
	} {
		ok, checked := ValidTimezone(tc.tz)
		if !checked {
			t.Errorf("%q: expected checked", tc.tz)
		}
		if ok != tc.want {
			t.Errorf("ValidTimezone(%q) = %v, want %v", tc.tz, ok, tc.want)
		}
	}
}

func TestValidKeymap(t *testing.T) {
	saved := keymapsDir
	defer func() { keymapsDir = saved }()

	keymapsDir = filepath.Join(t.TempDir(), "absent")
	if _, checked := ValidKeymap("us"); checked {
		t.Error("missing keymaps dir should be unchecked")
	}

	dir := t.TempDir()
	keymapsDir = dir
	sub := filepath.Join(dir, "i386", "qwerty")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "us.map.gz"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if ok, checked := ValidKeymap("us"); !ok || !checked {
		t.Errorf("us should be found: ok=%v checked=%v", ok, checked)
	}
	if ok, _ := ValidKeymap("zz"); ok {
		t.Error("zz should not be found")
	}
}

func TestValidLocale(t *testing.T) {
	savedL, savedV := localesFile, libcLocalesFile
	defer func() { localesFile, libcLocalesFile = savedL, savedV }()

	localesFile = filepath.Join(t.TempDir(), "absent")
	libcLocalesFile = filepath.Join(t.TempDir(), "absent")
	if _, checked := ValidLocale("xx_YY.garbage"); checked {
		t.Error("no locale list should be unchecked")
	}

	// Void's libc-locales: every line commented until it is enabled.
	libcLocalesFile = filepath.Join(t.TempDir(), "libc-locales")
	os.WriteFile(libcLocalesFile, []byte("#en_US.UTF-8 UTF-8\nro_RO.UTF-8 UTF-8\n#de_DE@euro ISO-8859-15\n"), 0o644)
	for lang, want := range map[string]bool{
		"en_US.UTF-8": true, "en_US.utf8": true, "ro_RO.UTF-8": true, "de_DE@euro": true,
		"C": true, "C.UTF-8": true, "POSIX": true,
		"xx_YY.garbage": false, "en_US.UTF-8 x": false, "en_US": false, "en_GB.UTF-8": false,
	} {
		if ok, checked := ValidLocale(lang); ok != want || !checked {
			t.Errorf("ValidLocale(%q) = %v, %v; want %v, true", lang, ok, checked, want)
		}
	}

	// glibc's SUPPORTED is read first, in either of its spellings.
	localesFile = filepath.Join(t.TempDir(), "SUPPORTED")
	os.WriteFile(localesFile, []byte("fr_FR.UTF-8 UTF-8\nja_JP.EUC-JP/EUC-JP \\\n"), 0o644)
	for _, lang := range []string{"fr_FR.UTF-8", "ja_JP.eucjp", "ro_RO.UTF-8"} {
		if ok, _ := ValidLocale(lang); !ok {
			t.Errorf("ValidLocale(%q) refused", lang)
		}
	}
}

func TestListTimezonesKeymapsLocales(t *testing.T) {
	savedZ, savedK, savedL := zoneinfoDir, keymapsDir, localesFile
	defer func() { zoneinfoDir, keymapsDir, localesFile = savedZ, savedK, savedL }()

	// Timezones: skip the tab/metadata files and the posix/right trees.
	z := t.TempDir()
	zoneinfoDir = z
	os.MkdirAll(filepath.Join(z, "Europe"), 0o755)
	os.MkdirAll(filepath.Join(z, "posix", "Europe"), 0o755)
	os.WriteFile(filepath.Join(z, "UTC"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(z, "Europe", "Bucharest"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(z, "zone.tab"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(z, "posix", "Europe", "Paris"), []byte("x"), 0o644)
	os.MkdirAll(filepath.Join(z, "right"), 0o755)
	for _, f := range []string{"right/UTC", "zone1970.tab", "iso3166.tab", "leapseconds", "tzdata.zi", "leap-seconds.list"} {
		os.WriteFile(filepath.Join(z, f), []byte("x"), 0o644)
	}
	if tz := strings.Join(ListTimezones(), ","); tz != "Europe/Bucharest,UTC" {
		t.Errorf("timezones = %s, want only the zones, sorted", tz)
	}

	// Keymaps: basename without .map/.map.gz, de-duplicated.
	k := t.TempDir()
	keymapsDir = k
	os.MkdirAll(filepath.Join(k, "i386", "qwerty"), 0o755)
	os.WriteFile(filepath.Join(k, "i386", "qwerty", "us.map.gz"), nil, 0o644)
	os.WriteFile(filepath.Join(k, "ro.map"), nil, 0o644)
	km := ListKeymaps()
	if !contains(km, "us") || !contains(km, "ro") {
		t.Errorf("keymaps wrong: %v", km)
	}

	// Locales: parse SUPPORTED, keep UTF-8, once each, sorted. (None of
	// these is in the fallback set, so it cannot stand in for the file.)
	savedV := libcLocalesFile
	defer func() { libcLocalesFile = savedV }()
	lf := filepath.Join(t.TempDir(), "SUPPORTED")
	os.WriteFile(lf, []byte("sv_SE.UTF-8 UTF-8\nen_US.ISO-8859-1 ISO-8859-1\n# comment\nnl_NL.UTF-8 UTF-8\nsv_SE.UTF-8 UTF-8\n"), 0o644)
	localesFile = lf
	if lc := strings.Join(ListLocales(), ","); lc != "nl_NL.UTF-8,sv_SE.UTF-8" {
		t.Errorf("locales = %s", lc)
	}
	// A SUPPORTED with no UTF-8 locale gives way to libc-locales.
	os.WriteFile(lf, []byte("en_US.ISO-8859-1 ISO-8859-1\n"), 0o644)
	libcLocalesFile = filepath.Join(t.TempDir(), "libc-locales")
	os.WriteFile(libcLocalesFile, []byte("#fi_FI.UTF-8 UTF-8\n#fi_FI ISO-8859-1\n"), 0o644)
	if lc := strings.Join(ListLocales(), ","); lc != "fi_FI.UTF-8" {
		t.Errorf("locales from libc-locales = %s", lc)
	}
	libcLocalesFile = filepath.Join(t.TempDir(), "nope")

	// Missing locale sources -> the built-in fallback (never empty).
	localesFile = filepath.Join(t.TempDir(), "nope")
	if lc := ListLocales(); len(lc) == 0 {
		t.Error("ListLocales fallback is empty")
	}
}

func contains(ss []string, v string) bool {
	for _, s := range ss {
		if s == v {
			return true
		}
	}
	return false
}

func TestLockRootCommands(t *testing.T) {
	got := LockRootCommands("/mnt", time.Date(2026, 11, 7, 21, 0, 0, 0, time.UTC))
	want := "[[chroot /mnt usermod --lock root] [chroot /mnt chage --lastday 20764 root]]"
	if fmt.Sprint(got) != want {
		t.Errorf("LockRootCommands = %v, want %s", got, want)
	}
}

// The update service is written to /etc/slinit.d either way and enabled
// (linked from boot.d) only when automatic updates are on.
func TestAutoUpdate(t *testing.T) {
	for _, on := range []bool{true, false} {
		root := t.TempDir()
		if err := AutoUpdate(root, on); err != nil {
			t.Fatal(err)
		}
		svc, err := os.ReadFile(filepath.Join(root, "etc", "slinit.d", AutoUpdateServiceName))
		if err != nil || !strings.Contains(string(svc), "cron-calendar = daily") {
			t.Fatalf("on=%v: service file %q, %v", on, svc, err)
		}
		target, err := os.Readlink(filepath.Join(root, "etc", "slinit.d", "boot.d", AutoUpdateServiceName))
		if on && target != "/etc/slinit.d/"+AutoUpdateServiceName {
			t.Errorf("on: boot.d link = %q, %v", target, err)
		}
		if !on && err == nil {
			t.Errorf("off: service is enabled (link to %q)", target)
		}
	}
}

// An IOMMU turned off stays off on the other vendor: it is set to get past
// broken firmware, and turned on it could keep the machine from booting.
func TestVendorKeepsIOMMUOff(t *testing.T) {
	if got := VendorKernelArgs("intel_iommu=off quiet", "amd"); got != "amd_iommu=off quiet" {
		t.Errorf("intel off on AMD: %q", got)
	}
	if got := RefitVendor([]string{"amd_iommu=off"}, "intel"); strings.Join(got, " ") != "intel_iommu=off" {
		t.Errorf("amd off on Intel: %q", got)
	}
	if got := VendorKernelArgs("intel_iommu=on,igfx_off kvm_intel.nested=1", "amd"); got != "amd_iommu=on kvm-amd.nested=1" {
		t.Errorf("on, and the underscore spelling: %q", got)
	}
}

// A locale spelled as glibc also accepts it (en_US.utf8) is generated.
func TestLocaleMatchesTheCodesetAsGlibcDoes(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "etc", "default"), 0o755)
	os.WriteFile(filepath.Join(root, "etc", "default", "libc-locales"), []byte("#en_US.UTF-8 UTF-8\n#ro_RO.UTF-8 UTF-8\n"), 0o644)
	changed, err := Locale(root, "en_US.utf8")
	if err != nil || !changed {
		t.Fatalf("Locale: %v, changed %v", err, changed)
	}
	got, _ := os.ReadFile(filepath.Join(root, "etc", "default", "libc-locales"))
	if string(got) != "en_US.UTF-8 UTF-8\n#ro_RO.UTF-8 UTF-8\n" {
		t.Errorf("libc-locales:\n%s", got)
	}
}

func TestSecureBoot(t *testing.T) {
	saved := secureBootVar
	defer func() { secureBootVar = saved }()
	secureBootVar = filepath.Join(t.TempDir(), "SecureBoot")
	if SecureBoot() {
		t.Error("no variable: on")
	}
	os.WriteFile(secureBootVar, []byte{6, 0, 0, 0, 1}, 0o644)
	if !SecureBoot() {
		t.Error("enforced: off")
	}
	os.WriteFile(secureBootVar, []byte{6, 0, 0, 0, 0}, 0o644)
	if SecureBoot() {
		t.Error("not enforced: on")
	}
}

func TestLocaleEnabled(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "etc", "default"), 0o755)
	os.WriteFile(filepath.Join(root, "etc", "default", "libc-locales"), []byte("#de_DE.UTF-8 UTF-8\nen_US.UTF-8 UTF-8\n"), 0o644)
	if !LocaleEnabled(root, "en_US.utf8") || LocaleEnabled(root, "de_DE.UTF-8") || LocaleEnabled(t.TempDir(), "en_US.UTF-8") {
		t.Error("LocaleEnabled")
	}
}

// CheckLocale refuses a zone, keymap or language the live system's data
// does not have, accepts one it has, and accepts anything where there is no
// data to check against (a musl system has no locale list). The data is the
// test's own: the host's may lack any of it (Ubuntu ships no kbd keymaps),
// and a check that runs only where it is present checked nothing there.
func TestCheckLocale(t *testing.T) {
	savedZ, savedK, savedL, savedV := zoneinfoDir, keymapsDir, localesFile, libcLocalesFile
	defer func() { zoneinfoDir, keymapsDir, localesFile, libcLocalesFile = savedZ, savedK, savedL, savedV }()
	none := filepath.Join(t.TempDir(), "none")
	zoneinfoDir, keymapsDir, localesFile, libcLocalesFile = none, none, none, none
	if err := CheckLocale("No/Such_Zone", "no-such-map", "xx_XX.NOPE"); err != nil {
		t.Errorf("no data to check against: %v", err)
	}

	zoneinfoDir, keymapsDir = t.TempDir(), t.TempDir()
	os.WriteFile(filepath.Join(zoneinfoDir, "UTC"), []byte("tz"), 0o644)
	os.WriteFile(filepath.Join(keymapsDir, "us.map.gz"), nil, 0o644)
	localesFile = filepath.Join(t.TempDir(), "SUPPORTED")
	os.WriteFile(localesFile, []byte("en_US.UTF-8 UTF-8\n"), 0o644)
	if err := CheckLocale("", "", ""); err != nil {
		t.Errorf("empty: %v", err)
	}
	if err := CheckLocale("UTC", "us", "en_US.UTF-8"); err != nil {
		t.Errorf("all known: %v", err)
	}
	for _, c := range []struct{ tz, keymap, lang, want string }{
		{"No/Such_Zone", "us", "en_US.UTF-8", "timezone"},
		{"UTC", "no-such-map", "en_US.UTF-8", "keymap"},
		{"UTC", "us", "xx_XX.NOPE", "language"},
	} {
		if err := CheckLocale(c.tz, c.keymap, c.lang); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("CheckLocale(%q, %q, %q) = %v, want a bad %s", c.tz, c.keymap, c.lang, err, c.want)
		}
	}
}

// NetLinks writes its script and, on a runit system, runs it from rc.local
// once; with no script it does nothing.
func TestNetLinksRunit(t *testing.T) {
	root := t.TempDir()
	if err := NetLinks(root, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "etc")); err == nil {
		t.Error("wrote something with no script")
	}
	for i := 0; i < 2; i++ {
		if err := NetLinks(root, "#!/bin/sh\nip link add bond0 type bond\n"); err != nil {
			t.Fatal(err)
		}
	}
	rc, _ := os.ReadFile(filepath.Join(root, "etc", "rc.local"))
	if strings.Count(string(rc), netLinksScript) != 1 {
		t.Errorf("rc.local:\n%s", rc)
	}
	// An rc.local of the system's own is added to, once.
	own := t.TempDir()
	os.MkdirAll(filepath.Join(own, "etc"), 0o755)
	os.WriteFile(filepath.Join(own, "etc", "rc.local"), []byte("#!/bin/sh\necho mine\n"), 0o755)
	for i := 0; i < 2; i++ {
		if err := NetLinks(own, "#!/bin/sh\n"); err != nil {
			t.Fatal(err)
		}
	}
	if rc, _ := os.ReadFile(filepath.Join(own, "etc", "rc.local")); !strings.HasPrefix(string(rc), "#!/bin/sh\necho mine\n") || strings.Count(string(rc), netLinksScript+"\n") != 1 {
		t.Errorf("rc.local:\n%s", rc)
	}
	if fi, err := os.Stat(filepath.Join(root, netLinksScript)); err != nil || fi.Mode().Perm()&0o100 == 0 {
		t.Errorf("script: %v %v", fi, err)
	}
	if MemTotalBytes() == 0 {
		t.Log("no /proc/meminfo here")
	}
}

// On a slinit system NetLinks makes the script a scripted service of its
// own, enabled at boot, and leaves rc.local alone; the script is written as
// given, executable.
func TestNetLinksSlinit(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, SlinitServicesDir, "boot"), 0o755)
	script := "#!/bin/sh\nip link add bond0 type bond\n"
	if err := NetLinks(root, script); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(filepath.Join(root, netLinksScript)); err != nil || string(got) != script {
		t.Errorf("script: %q, %v", got, err)
	}
	if fi, err := os.Stat(filepath.Join(root, netLinksScript)); err != nil || fi.Mode().Perm() != 0o755 {
		t.Errorf("script mode: %v", err)
	}
	svc, err := os.ReadFile(filepath.Join(root, "etc", "slinit.d", NetLinksServiceName))
	if err != nil || !strings.Contains(string(svc), "type = scripted\n") || !strings.Contains(string(svc), "command = "+netLinksScript+"\n") {
		t.Errorf("service file: %v\n%s", err, svc)
	}
	link, err := os.Readlink(filepath.Join(root, "etc", "slinit.d", "boot.d", NetLinksServiceName))
	if err != nil || link != filepath.Join("/etc/slinit.d", NetLinksServiceName) {
		t.Errorf("not enabled: %q, %v", link, err)
	}
	if _, err := os.Stat(filepath.Join(root, "etc", "rc.local")); err == nil {
		t.Error("rc.local written on a slinit system")
	}
}

// Where the script or rc.local cannot be written, NetLinks says so.
func TestNetLinksReportsFailures(t *testing.T) {
	// /etc is a file: no directory for the script.
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "etc"), nil, 0o644)
	if err := NetLinks(root, "x"); err == nil {
		t.Error("no /etc/slinit.d, and no error")
	}
	// The script's name is taken by a directory.
	root = t.TempDir()
	os.MkdirAll(filepath.Join(root, netLinksScript), 0o755)
	if err := NetLinks(root, "x"); err == nil || !strings.Contains(err.Error(), "write "+netLinksScript) {
		t.Errorf("script path a directory: %v", err)
	}
	// rc.local is a directory.
	root = t.TempDir()
	os.MkdirAll(filepath.Join(root, "etc", "rc.local"), 0o755)
	if err := NetLinks(root, "x"); err == nil {
		t.Error("rc.local a directory, and no error")
	}
	// rc.local takes no write, as on a full disk.
	if _, err := os.Stat("/dev/full"); err == nil {
		root = t.TempDir()
		os.MkdirAll(filepath.Join(root, "etc"), 0o755)
		os.Symlink("/dev/full", filepath.Join(root, "etc", "rc.local"))
		if err := NetLinks(root, "x"); err == nil || !strings.Contains(err.Error(), "write rc.local") {
			t.Errorf("rc.local on a full disk: %v", err)
		}
	}
	// On a slinit system, the service file's name is taken by a directory.
	root = t.TempDir()
	os.MkdirAll(filepath.Join(root, SlinitServicesDir, "boot"), 0o755)
	os.MkdirAll(filepath.Join(root, "etc", "slinit.d", NetLinksServiceName), 0o755)
	if err := NetLinks(root, "x"); err == nil || !strings.Contains(err.Error(), "write "+NetLinksServiceName) {
		t.Errorf("service path a directory: %v", err)
	}
}

// What a keymap or language holds stays text in the files sh sources as
// root: with the checks switched off, a value with $(...) was written in
// Go's quoting, which sh expands.
func TestKeymapLocaleShellSafe(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh")
	}
	for _, v := range []string{"us", "de-latin1-nodeadkeys", "sr_RS@latin", "us$(touch${IFS}pwned)", "a'b`id`\"c\\", "x;y"} {
		root := t.TempDir()
		if err := Keymap(root, v); err != nil {
			t.Fatal(err)
		}
		if _, err := Locale(root, v); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(sh, "-c", `. ./etc/rc.conf && . ./etc/locale.conf && printf '%s\000%s' "$KEYMAP" "$LANG"`)
		cmd.Dir, cmd.Env = root, []string{"PATH=/nonexistent"}
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("%q: sourcing: %v", v, err)
		}
		if got := string(out); got != v+"\x00"+v {
			t.Errorf("%q read back as %q", v, got)
		}
		if _, err := os.Stat(filepath.Join(root, "pwned")); err == nil {
			t.Errorf("%q ran a command", v)
		}
	}
}
