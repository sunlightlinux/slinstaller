// Package system configures the installed target: its hostname,
// timezone, console keymap, locale and user accounts — the first-boot
// settings that make a freshly installed disk usable.
//
// hostname and timezone are the same on any Linux; keymap uses
// /etc/rc.conf, which both Void's core services and slinit's OpenRC
// compatibility read; locale follows the glibc convention. EnableService
// turns on a runit service in the target. Network interface
// configuration itself lives in pkg/network.
package system

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Hostname writes /etc/hostname in the target. An empty name is a no-op.
func Hostname(root, name string) error {
	if name == "" {
		return nil
	}
	path := filepath.Join(root, "etc", "hostname")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("system: %w", err)
	}
	if err := os.WriteFile(path, []byte(name+"\n"), 0o644); err != nil {
		return fmt.Errorf("system: write hostname: %w", err)
	}
	return nil
}

// MachineID writes /etc/machine-id in the target: a random 128-bit ID as
// 32 lowercase hex digits, the format systemd defines and slinit reads.
// slinit's journal and slinit-logind key on it, and slinit reads it as
// PID 1 before any boot service runs, so it must exist when the system
// first boots — on Void nothing else creates it. An existing non-empty
// file is kept: the ID is meant to stay with the machine.
func MachineID(root string) error {
	path := filepath.Join(root, "etc", "machine-id")
	if st, err := os.Stat(path); err == nil && st.Size() > 0 {
		return nil
	}
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return fmt.Errorf("system: machine-id: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("system: %w", err)
	}
	if err := os.WriteFile(path, []byte(hex.EncodeToString(id[:])+"\n"), 0o444); err != nil {
		return fmt.Errorf("system: write machine-id: %w", err)
	}
	return nil
}

// Timezone points /etc/localtime at the zoneinfo file for tz, the way
// every glibc system expects. An empty tz is a no-op. The link target is
// an absolute path inside the installed system, so it resolves once the
// target is booted, not from where the installer runs.
func Timezone(root, tz string) error {
	if tz == "" {
		return nil
	}
	link := filepath.Join(root, "etc", "localtime")
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		return fmt.Errorf("system: %w", err)
	}
	_ = os.Remove(link)
	if err := os.Symlink("/usr/share/zoneinfo/"+tz, link); err != nil {
		return fmt.Errorf("system: link localtime: %w", err)
	}
	return nil
}

// Keymap sets the console keymap by appending KEYMAP to /etc/rc.conf,
// which is where both Void's core services and slinit's OpenRC
// compatibility read it. An empty code is a no-op.
func Keymap(root, code string) error {
	if code == "" {
		return nil
	}
	path := filepath.Join(root, "etc", "rc.conf")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("system: %w", err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("system: open rc.conf: %w", err)
	}
	_, werr := fmt.Fprintf(f, "\n# console keymap set by slinstaller\nKEYMAP=%q\n", code)
	cerr := f.Close()
	if werr != nil {
		return fmt.Errorf("system: write rc.conf: %w", werr)
	}
	return cerr
}

// Locale sets the system locale. It writes /etc/locale.conf and, on a
// glibc system, uncomments the matching line in /etc/default/libc-locales
// so the locale can be generated. It reports whether a glibc locale was
// enabled, which tells the caller to regenerate locales; musl has neither
// the file nor the need, so there it is a no-op beyond locale.conf. An
// empty lang is a no-op.
func Locale(root, lang string) (bool, error) {
	if lang == "" {
		return false, nil
	}

	conf := filepath.Join(root, "etc", "locale.conf")
	if err := os.MkdirAll(filepath.Dir(conf), 0o755); err != nil {
		return false, fmt.Errorf("system: %w", err)
	}
	if err := os.WriteFile(conf, []byte("LANG="+lang+"\n"), 0o644); err != nil {
		return false, fmt.Errorf("system: write locale.conf: %w", err)
	}

	locales := filepath.Join(root, "etc", "default", "libc-locales")
	data, err := os.ReadFile(locales)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil // musl: nothing to generate
		}
		return false, fmt.Errorf("system: read libc-locales: %w", err)
	}

	lines := strings.Split(string(data), "\n")
	changed := false
	for i, line := range lines {
		if !strings.HasPrefix(line, "#") {
			continue
		}
		if fields := strings.Fields(strings.TrimLeft(line, "# ")); len(fields) > 0 && fields[0] == lang {
			lines[i] = strings.TrimPrefix(line, "#")
			changed = true
		}
	}
	if changed {
		if err := os.WriteFile(locales, []byte(strings.Join(lines, "\n")), 0o644); err != nil {
			return false, fmt.Errorf("system: write libc-locales: %w", err)
		}
	}
	return changed, nil
}

// WheelSudo gives the wheel group sudo by writing a sudoers drop-in in
// the target. Admin accounts are put in wheel, so this is what turns
// their sudo on — with a password; passwordless is a separate choice.
// The 0440 mode is what sudo requires of files in sudoers.d.
func WheelSudo(root string) error {
	dir := filepath.Join(root, "etc", "sudoers.d")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("system: %w", err)
	}
	path := filepath.Join(dir, "wheel")
	if err := os.WriteFile(path, []byte("%wheel ALL=(ALL:ALL) ALL\n"), 0o440); err != nil {
		return fmt.Errorf("system: write sudoers wheel: %w", err)
	}
	// WriteFile's mode is subject to umask; force it, since sudo rejects
	// a group- or world-writable drop-in.
	return os.Chmod(path, 0o440)
}

// EnableService enables a runit service in the target by symlinking it
// into the default runsvdir, which is what runit scans at boot. It
// points straight at /etc/runit/runsvdir/default rather than /var/service
// (a symlink that may not resolve from outside the installed system), the
// chroot-free way to enable a Void service at install time.
func EnableService(root, name string) error {
	dir := filepath.Join(root, "etc", "runit", "runsvdir", "default")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("system: %w", err)
	}
	link := filepath.Join(dir, name)
	_ = os.Remove(link)
	if err := os.Symlink("/etc/sv/"+name, link); err != nil {
		return fmt.Errorf("system: enable service %s: %w", name, err)
	}
	return nil
}

// User is an account to create in the target.
type User struct {
	Login        string
	FullName     string // GECOS / display name; "" leaves it empty
	PasswordHash string // a crypt(3) hash, or "" for no password
	Shell        string // "" leaves useradd's default
	Admin        bool   // added to the wheel group
	SSHKeys      []string
}

// AddCommand returns the chrooted useradd command that creates the user.
// The password is passed as its crypt hash via -p, never in the clear.
func (u User) AddCommand(root string) []string {
	args := []string{"chroot", root, "useradd", "-m"}
	if u.Admin {
		args = append(args, "-G", "wheel")
	}
	if u.Shell != "" {
		args = append(args, "-s", u.Shell)
	}
	if u.FullName != "" {
		args = append(args, "-c", u.FullName)
	}
	if u.PasswordHash != "" {
		args = append(args, "-p", u.PasswordHash)
	}
	return append(args, u.Login)
}

// WriteSSHKeys writes the user's authorized_keys into the target and
// returns the chrooted command that hands the .ssh directory to them.
// With no keys it writes nothing and returns nil.
func (u User) WriteSSHKeys(root string) ([]string, error) {
	if len(u.SSHKeys) == 0 {
		return nil, nil
	}
	dir := filepath.Join(root, "home", u.Login, ".ssh")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("system: %w", err)
	}
	content := strings.Join(u.SSHKeys, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(dir, "authorized_keys"), []byte(content), 0o600); err != nil {
		return nil, fmt.Errorf("system: write authorized_keys: %w", err)
	}
	return []string{"chroot", root, "chown", "-R", u.Login + ":" + u.Login, "/home/" + u.Login + "/.ssh"}, nil
}
