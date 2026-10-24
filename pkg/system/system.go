// Package system configures the installed target: its hostname,
// timezone and user accounts — the first-boot settings that make a
// freshly installed disk usable.
//
// This is the universal part of first-boot, the same on any Linux. The
// parts that depend on how Sunlight and slinit are wired — console
// keymap, locale generation, network interfaces — are not here yet; they
// follow once those conventions are settled.
package system

import (
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

// User is an account to create in the target.
type User struct {
	Login        string
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
