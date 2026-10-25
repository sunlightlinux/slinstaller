// Package tui is the interactive front-end: it asks a few questions and
// builds a config, which the controller then installs.
//
// Like every front-end, it only produces a config.Config (and, for an
// encrypted layout, the passphrase) — it performs no installation step
// itself. It is a plain prompt flow rather than a full-screen UI: that is
// enough to choose a disk, a layout and the basics, it has no
// dependencies, and it works over a serial console, where a full-screen
// TUI would not. The config it produces is the same one a file would.
package tui

import (
	"bufio"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"

	"github.com/sunlightlinux/slinstaller/pkg/blockdev"
	"github.com/sunlightlinux/slinstaller/pkg/config"
	"github.com/sunlightlinux/slinstaller/pkg/storage"
)

// rootFilesystems are the choices offered for the root partition.
var rootFilesystems = []string{"ext4", "btrfs", "xfs", "f2fs"}

// Run asks the operator for a target disk and a layout and returns the
// config to install, plus the LUKS passphrase when the root is encrypted
// (empty otherwise). Prompts go to w; answers are read from in.
func Run(in *bufio.Reader, w io.Writer) (*config.Config, string, error) {
	disks, err := listDisks(w)
	if err != nil {
		return nil, "", err
	}
	disk := disks[promptIndex(in, w, "Install to which disk", len(disks))-1]
	fmt.Fprintf(w, "\nSelected %s — it will be ERASED.\n\n", disk.Path)

	fs := promptChoice(in, w, "Root filesystem", rootFilesystems, "ext4")

	encrypt := promptYesNo(in, w, "Encrypt the root partition?", false)
	passphrase := ""
	if encrypt {
		passphrase = promptSecret(in, w, "LUKS passphrase")
		if passphrase == "" {
			return nil, "", fmt.Errorf("tui: an encrypted root needs a passphrase")
		}
	}

	swap := promptChoice(in, w, "Swap", []string{"partition", "file", "none"}, "partition")
	swapSize := ""
	if swap != "none" {
		swapSize = promptDefault(in, w, "Swap size", "2G")
	}

	hostname := promptDefault(in, w, "Hostname", "sunlight")
	login := promptDefault(in, w, "Username", "user")
	hash, err := cryptHash(promptSecret(in, w, "Password for "+login))
	if err != nil {
		return nil, "", err
	}

	cfg := assemble(assembly{
		Disk: disk.Name, RootFS: fs, Encrypt: encrypt,
		Swap: swap, SwapSize: swapSize,
		Hostname: hostname, Login: login, PassHash: hash,
	})

	summarise(w, disk, fs, encrypt, swap, swapSize, hostname, login)
	if !promptYesNo(in, w, "Proceed with this layout?", false) {
		return nil, "", fmt.Errorf("cancelled")
	}
	return cfg, passphrase, nil
}

func listDisks(w io.Writer) ([]blockdev.Device, error) {
	devs, err := blockdev.List()
	if err != nil {
		return nil, err
	}
	disks := blockdev.Disks(devs)
	if len(disks) == 0 {
		return nil, fmt.Errorf("tui: no disks found")
	}
	fmt.Fprintln(w, "Disks:")
	for i, d := range disks {
		fmt.Fprintf(w, "  %d) %-14s %s\n", i+1, d.Path, d.Model)
	}
	return disks, nil
}

// assembly is the answers Run collected; assemble turns it into a config.
type assembly struct {
	Disk, RootFS    string
	Encrypt         bool
	Swap, SwapSize  string
	Hostname, Login string
	PassHash        string
}

// assemble builds the config for the chosen layout: an ESP, an optional
// swap partition, and a root (encrypted or not) that fills the disk, plus
// the identity and a single admin account. Separate from the prompting so
// it can be tested.
func assemble(a assembly) *config.Config {
	n := 0
	part := func() string {
		n++
		return strings.TrimPrefix(storage.PartitionName("/dev/"+a.Disk, n), "/dev/")
	}

	children := []*config.Media{
		{Name: part(), Type: "part", FsType: "vfat", Mountpoint: "/boot", Size: "512M"},
	}
	if a.Swap == "partition" {
		children = append(children, &config.Media{Name: part(), Type: "part", FsType: "swap", Size: a.SwapSize})
	}
	rootType := "part"
	if a.Encrypt {
		rootType = "crypt"
	}
	children = append(children, &config.Media{Name: part(), Type: rootType, FsType: a.RootFS, Mountpoint: "/"})

	cfg := &config.Config{
		TargetMedia: []*config.Media{{Name: a.Disk, Type: "disk", Children: children}},
		Hostname:    a.Hostname,
		Keyboard:    "us",
		Language:    "en_US.UTF-8",
		Users:       []*config.User{{Login: a.Login, Password: a.PassHash, Admin: true}},
	}
	if a.Swap == "file" {
		cfg.SwapFileSize = a.SwapSize
	}
	return cfg
}

func summarise(w io.Writer, disk blockdev.Device, fs string, encrypt bool, swap, swapSize, hostname, login string) {
	enc := "no"
	if encrypt {
		enc = "yes (LUKS)"
	}
	sw := "none"
	switch swap {
	case "partition":
		sw = swapSize + " partition"
	case "file":
		sw = swapSize + " file"
	}
	fmt.Fprintf(w, "\n  Disk:        %s (ERASED)\n", disk.Path)
	fmt.Fprintf(w, "  Root:        %s, fills the disk\n", fs)
	fmt.Fprintf(w, "  Encryption:  %s\n", enc)
	fmt.Fprintf(w, "  Swap:        %s\n", sw)
	fmt.Fprintf(w, "  Hostname:    %s\n", hostname)
	fmt.Fprintf(w, "  Admin user:  %s\n\n", login)
}

// cryptHash turns a plaintext password into a crypt(3) SHA-512 hash with
// openssl, feeding it on stdin so it never appears in the process list.
func cryptHash(pass string) (string, error) {
	cmd := exec.Command("openssl", "passwd", "-6", "-stdin")
	cmd.Stdin = strings.NewReader(pass + "\n")
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("tui: hashing password with openssl: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

func prompt(in *bufio.Reader, w io.Writer, label string) string {
	fmt.Fprintf(w, "%s: ", label)
	line, _ := in.ReadString('\n')
	return strings.TrimSpace(line)
}

func promptDefault(in *bufio.Reader, w io.Writer, label, def string) string {
	fmt.Fprintf(w, "%s [%s]: ", label, def)
	line, _ := in.ReadString('\n')
	if s := strings.TrimSpace(line); s != "" {
		return s
	}
	return def
}

// promptSecret reads a value twice and requires the two to match, so a
// mistyped password or passphrase is caught here, not at the login prompt.
func promptSecret(in *bufio.Reader, w io.Writer, label string) string {
	for {
		a := prompt(in, w, label)
		b := prompt(in, w, label+" (again)")
		if a == b {
			return a
		}
		fmt.Fprintln(w, "  they do not match, try again")
	}
}

// promptChoice accepts one of opts, or the default on an empty line; an
// unrecognised answer falls back to the default with a note.
func promptChoice(in *bufio.Reader, w io.Writer, label string, opts []string, def string) string {
	fmt.Fprintf(w, "%s (%s) [%s]: ", label, strings.Join(opts, "/"), def)
	line, _ := in.ReadString('\n')
	s := strings.TrimSpace(line)
	if s == "" {
		return def
	}
	for _, o := range opts {
		if s == o {
			return o
		}
	}
	fmt.Fprintf(w, "  not one of %s — using %s\n", strings.Join(opts, "/"), def)
	return def
}

func promptYesNo(in *bufio.Reader, w io.Writer, label string, def bool) bool {
	hint := "y/N"
	if def {
		hint = "Y/n"
	}
	fmt.Fprintf(w, "%s [%s]: ", label, hint)
	line, _ := in.ReadString('\n')
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "":
		return def
	case "y", "yes":
		return true
	default:
		return false
	}
}

// promptIndex asks for a number in 1..max, repeating until it gets one.
func promptIndex(in *bufio.Reader, w io.Writer, label string, max int) int {
	for {
		fmt.Fprintf(w, "%s (1-%d): ", label, max)
		line, err := in.ReadString('\n')
		if n, perr := strconv.Atoi(strings.TrimSpace(line)); perr == nil && n >= 1 && n <= max {
			return n
		}
		if err == io.EOF {
			return 1 // non-interactive fallback: the first disk
		}
	}
}
