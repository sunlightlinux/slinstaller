package system

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
