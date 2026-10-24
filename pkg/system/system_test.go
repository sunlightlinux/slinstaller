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
