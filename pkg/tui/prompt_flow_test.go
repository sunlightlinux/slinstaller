package tui

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

// fakeLsblk puts an lsblk that prints the given devices first on PATH.
func fakeLsblk(t *testing.T, devices string) {
	t.Helper()
	dir := t.TempDir()
	script := "#!/bin/sh\ncat <<'JSON'\n{\"blockdevices\":[" + devices + "]}\nJSON\n"
	if devices == "FAIL" {
		script = "#!/bin/sh\nexit 1\n"
	}
	if err := os.WriteFile(filepath.Join(dir, "lsblk"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

const twoDisks = `{"name":"sda","path":"/dev/sda","size":64000000000,"type":"disk","model":"Live USB","children":[
	{"name":"sda1","path":"/dev/sda1","size":1000,"type":"part","mountpoint":"/run/live"}]},
	{"name":"sdb","path":"/dev/sdb","size":64000000000,"type":"disk","model":"First SSD"},
	{"name":"nvme0n1","path":"/dev/nvme0n1","size":64000000000,"type":"disk","model":"Second SSD"}`

// The prompt installer is the one used over a serial console. It offers only
// disks nothing is using, and builds the same kind of config the full-screen
// one does.
func TestPromptInstall(t *testing.T) {
	if _, err := cryptHash("probe-password"); err != nil {
		t.Skipf("no openssl to hash the password with: %v", err)
	}
	fakeLsblk(t, twoDisks)
	var out bytes.Buffer
	cfg, pass, err := Run(answers(
		"2",                                      // the second free disk
		"btrfs",                                  // root filesystem
		"y",                                      // encrypt
		"a long passphrase", "a long passphrase", // LUKS passphrase, twice
		"file", "0", "4G", // swap: none is not size 0
		"-bad", "box", // hostname, the first refused
		"Bad Login!",                     // refused
		"ion",                            // username
		"correct horse", "correct horse", // password, twice
		"y", // proceed
	), &out)
	if err != nil {
		t.Fatalf("Run: %v\n%s", err, out.String())
	}
	shown := out.String()
	// The disk in use (the live medium) is not offered.
	if strings.Contains(shown, "/dev/sda ") || !strings.Contains(shown, "1) /dev/sdb") || !strings.Contains(shown, "2) /dev/nvme0n1") {
		t.Errorf("disks offered:\n%s", shown)
	}
	for _, want := range []string{
		"Selected /dev/nvme0n1 (60GiB Second SSD) - it will be ERASED.", "try again",
		"Disk:        /dev/nvme0n1, 60GiB Second SSD (ERASED)", "2) /dev/nvme0n1   60GiB    Second SSD", "Root:        btrfs, fills the disk",
		"Encryption:  yes (LUKS)", "Swap:        4G file", "Hostname:    box", "Admin user:  ion",
		"Kernel:      Server profile", "a swap of size 0 is no swap", "hostname \"-bad\" must be",
	} {
		if !strings.Contains(shown, want) {
			t.Errorf("prompt output missing %q:\n%s", want, shown)
		}
	}
	for _, secret := range []string{"a long passphrase", "correct horse"} {
		if strings.Contains(shown, secret) {
			t.Errorf("%q was shown", secret)
		}
	}
	if pass != "a long passphrase" {
		t.Errorf("passphrase = %q", pass)
	}
	if cfg.TargetMedia[0].Name != "nvme0n1" || cfg.Hostname != "box" || cfg.SwapFileSize != "4G" {
		t.Errorf("config: disk %q hostname %q swap file %q", cfg.TargetMedia[0].Name, cfg.Hostname, cfg.SwapFileSize)
	}
	if len(cfg.Users) != 1 || cfg.Users[0].Login != "ion" || !strings.HasPrefix(cfg.Users[0].Password, "$6$") {
		t.Errorf("user = %+v", cfg.Users)
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("the prompt installer built an invalid config: %v", err)
	}

	// The defaults: Enter throughout, then decline. Nothing is returned to
	// install.
	out.Reset()
	cfg, _, err = Run(answers("1", "", "", "", "", "", "", "correct horse", "correct horse", "n"), &out)
	if err == nil || err.Error() != "cancelled" || cfg != nil {
		t.Errorf("declined install: cfg=%v err=%v", cfg, err)
	}
	for _, want := range []string{"Encryption:  no", "Swap:        2G partition", "Root:        ext4"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("defaults missing %q:\n%s", want, out.String())
		}
	}
	// At "Proceed?" Enter, or input that ends, is no: the disk is not wiped
	// on a keystroke that did not say yes.
	for name, last := range map[string][]string{"Enter": {""}, "ended": nil} {
		a := append([]string{"1", "", "", "", "", "", "", "correct horse", "correct horse"}, last...)
		if cfg, _, err = Run(answers(a...), &out); err == nil || err.Error() != "cancelled" || cfg != nil {
			t.Errorf("%s at Proceed?: cfg=%v err=%v", name, cfg, err)
		}
	}
	// No swap: its size is not asked for.
	out.Reset()
	_, _, _ = Run(answers("1", "", "", "none", "", "", "correct horse", "correct horse", "n"), &out)
	if strings.Contains(out.String(), "Swap size") || !strings.Contains(out.String(), "Swap:        none") {
		t.Errorf("swap none:\n%s", out.String())
	}

	// Input that ends at a secret ends the install rather than looping.
	if _, _, err = Run(answers("1", "", "y"), &out); !errors.Is(err, ErrNoInput) {
		t.Errorf("input ended at the passphrase: %v", err)
	}
	if _, _, err = Run(answers("1"), &out); !errors.Is(err, ErrNoInput) {
		t.Errorf("input ended at the password: %v", err)
	}
}

func TestPromptInstallWithoutADisk(t *testing.T) {
	var out bytes.Buffer
	// Every disk is in use.
	fakeLsblk(t, `{"name":"sda","path":"/dev/sda","size":1000,"type":"disk","fstype":"iso9660"}`)
	if _, _, err := Run(answers(), &out); err == nil || !strings.Contains(err.Error(), "no disk to install to") {
		t.Errorf("no free disk: %v", err)
	}
	fakeLsblk(t, "FAIL")
	if _, _, err := Run(answers(), &out); err == nil || !strings.Contains(err.Error(), "lsblk") {
		t.Errorf("lsblk failing: %v", err)
	}
}

func TestPasswordHashing(t *testing.T) {
	hash, err := CryptHash("correct horse")
	if err != nil {
		t.Skipf("no openssl: %v", err)
	}
	if !strings.HasPrefix(hash, "$6$") || strings.Contains(hash, "correct horse") {
		t.Errorf("hash = %q", hash)
	}
	// Without openssl the password cannot be hashed, and that is an error —
	// never a config carrying the password itself.
	t.Setenv("PATH", t.TempDir())
	if _, err := CryptHash("correct horse"); err == nil || !strings.Contains(err.Error(), "openssl") {
		t.Errorf("hashing without openssl: %v", err)
	}
	fakeLsblk(t, `{"name":"sdb","path":"/dev/sdb","size":1000,"type":"disk"}`)
	var out bytes.Buffer
	if cfg, _, err := Run(answers("1", "", "", "", "", "", "", "correct horse", "correct horse", "y"), &out); err == nil || cfg != nil {
		t.Errorf("install without a password hash: cfg=%v err=%v", cfg, err)
	}
}

// echoProbe is input that records, when it is read, whether the terminal is
// echoing.
type echoProbe struct {
	fd      int
	echoing *bool
	text    io.Reader
}

func (p echoProbe) Read(b []byte) (int, error) {
	if tio, err := unix.IoctlGetTermios(p.fd, unix.TCGETS); err == nil {
		*p.echoing = tio.Lflag&unix.ECHO != 0
	}
	return p.text.Read(b)
}

// A secret typed at a terminal is not echoed, and echo is back afterwards.
func TestSecretIsNotEchoedOnATerminal(t *testing.T) {
	pty, err := os.OpenFile("/dev/ptmx", os.O_RDWR, 0)
	if err != nil {
		t.Skipf("no pseudo-terminal: %v", err)
	}
	defer pty.Close()
	fd := int(pty.Fd())
	before, err := unix.IoctlGetTermios(fd, unix.TCGETS)
	if err != nil || before.Lflag&unix.ECHO == 0 {
		t.Skipf("the pseudo-terminal does not start echoing (%v)", err)
	}
	saved := os.Stdin
	os.Stdin = pty
	defer func() { os.Stdin = saved }()

	echoing := true
	var out bytes.Buffer
	got, err := readSecret(bufio.NewReader(echoProbe{fd, &echoing, strings.NewReader("hunter2\n")}), &out, "Password")
	if err != nil || got != "hunter2" {
		t.Fatalf("readSecret = %q, %v", got, err)
	}
	if echoing {
		t.Error("the terminal was echoing while the secret was read")
	}
	after, _ := unix.IoctlGetTermios(fd, unix.TCGETS)
	if after.Lflag&unix.ECHO == 0 {
		t.Error("echo was left off after the prompt")
	}
	// The Enter was not echoed either, so the prompt ends its own line.
	if out.String() != "Password: \n" {
		t.Errorf("prompt output = %q", out.String())
	}
}
