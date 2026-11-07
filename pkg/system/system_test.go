package system

import (
	"fmt"
	"os"
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
	if strings.Join(chown, " ") != "chroot "+root+" chown -R ion:ion /home/ion/.ssh" {
		t.Errorf("chown command = %v", chown)
	}

	// No keys: nothing written, nil command.
	if cmd, err := (User{Login: "x"}).WriteSSHKeys(t.TempDir()); err != nil || cmd != nil {
		t.Errorf("no keys should be a no-op, got %v / %v", cmd, err)
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
	dir := t.TempDir()
	zoneinfoDir = dir
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
	tz := ListTimezones()
	if !contains(tz, "UTC") || !contains(tz, "Europe/Bucharest") {
		t.Errorf("timezones missing expected zones: %v", tz)
	}
	if contains(tz, "zone.tab") || contains(tz, "posix/Europe/Paris") {
		t.Errorf("timezones included skipped entries: %v", tz)
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

	// Locales: parse SUPPORTED, keep UTF-8.
	lf := filepath.Join(t.TempDir(), "SUPPORTED")
	os.WriteFile(lf, []byte("en_US.UTF-8 UTF-8\nen_US.ISO-8859-1 ISO-8859-1\n# comment\nro_RO.UTF-8 UTF-8\n"), 0o644)
	localesFile = lf
	lc := ListLocales()
	if !contains(lc, "en_US.UTF-8") || !contains(lc, "ro_RO.UTF-8") || contains(lc, "en_US.ISO-8859-1") {
		t.Errorf("locales wrong: %v", lc)
	}

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
