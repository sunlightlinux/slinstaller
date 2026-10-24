// Package tui is the interactive front-end: it asks a few questions and
// builds a config, which the controller then installs.
//
// Like every front-end, it only produces a config.Config — it performs
// no installation step itself. It is a plain prompt flow rather than a
// full-screen UI: that is enough to choose a disk and the basics, it has
// no dependencies, and it works over a serial console, where a
// full-screen TUI would not. A richer UI can layer on later; the config
// it produces is the same.
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

// Run asks the operator for a target disk and the basics, and returns
// the config to install. Prompts go to w; answers are read from r.
func Run(r io.Reader, w io.Writer) (*config.Config, error) {
	in := bufio.NewReader(r)

	disks, err := listDisks(w)
	if err != nil {
		return nil, err
	}

	n := promptIndex(in, w, "Install to which disk", len(disks))
	disk := disks[n-1]
	fmt.Fprintf(w, "\nSelected %s — it will be ERASED.\n\n", disk.Path)

	hostname := promptDefault(in, w, "Hostname", "sunlight")
	login := promptDefault(in, w, "Username", "user")

	pass := prompt(in, w, "Password for "+login)
	hash, err := cryptHash(pass)
	if err != nil {
		return nil, err
	}

	return assemble(disk.Name, hostname, login, hash), nil
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

// assemble builds a standard UEFI layout for the chosen disk: an ESP,
// swap, and a root that fills the rest, plus the identity and a single
// admin account. It is separate from the prompting so it can be tested.
func assemble(diskName, hostname, login, passHash string) *config.Config {
	part := func(n int) string {
		return strings.TrimPrefix(storage.PartitionName("/dev/"+diskName, n), "/dev/")
	}
	return &config.Config{
		TargetMedia: []*config.Media{{
			Name: diskName,
			Type: "disk",
			Children: []*config.Media{
				{Name: part(1), Type: "part", FsType: "vfat", Mountpoint: "/boot", Size: "512M"},
				{Name: part(2), Type: "part", FsType: "swap", Size: "2G"},
				{Name: part(3), Type: "part", FsType: "ext4", Mountpoint: "/"}, // fills the disk
			},
		}},
		Hostname: hostname,
		Keyboard: "us",
		Language: "en_US.UTF-8",
		Users:    []*config.User{{Login: login, Password: passHash, Admin: true}},
	}
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
