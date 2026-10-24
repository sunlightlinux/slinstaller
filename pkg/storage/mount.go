package storage

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
)

// MountOrder returns the partitions that have a mountpoint, ordered so a
// parent is always mounted before anything beneath it: "/" before
// "/boot" before "/boot/efi". Swap and other unmounted partitions are
// left out.
//
// clr-installer orders by mountpoint prefix; sorting by path depth and
// then lexically is the same intent expressed so the result is total and
// stable. The order a config lists partitions in is the on-disk order,
// not a mountable one, so it cannot be relied on here.
func (pl *Plan) MountOrder() []Partition {
	out := make([]Partition, 0, len(pl.Parts))
	for _, p := range pl.Parts {
		if p.Mountpoint != "" {
			out = append(out, p)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		di := strings.Count(path.Clean(out[i].Mountpoint), "/")
		dj := strings.Count(path.Clean(out[j].Mountpoint), "/")
		if di != dj {
			return di < dj
		}
		return out[i].Mountpoint < out[j].Mountpoint
	})
	return out
}

// Mounter mounts a plan's partitions under a root and remembers what it
// mounted so it can unmount in reverse. Like the partitioning it follows,
// this touches the real system: it runs as root, against real devices,
// and is exercised in a VM, never on hardware during development.
type Mounter struct {
	root    string
	mounted []string
}

// NewMounter returns a Mounter that mounts under root (the target tree,
// e.g. /mnt).
func NewMounter(root string) *Mounter {
	return &Mounter{root: root}
}

// MountAll mounts every partition with a mountpoint, parents first,
// creating each target directory. It must run after the filesystems
// exist (see Plan.Commands).
func (m *Mounter) MountAll(pl *Plan) error {
	for _, p := range pl.MountOrder() {
		target := filepath.Join(m.root, p.Mountpoint)
		if err := os.MkdirAll(target, 0o755); err != nil {
			return fmt.Errorf("storage: mkdir %s: %w", target, err)
		}
		if err := syscall.Mount(p.Device, target, p.Filesystem, syscall.MS_RELATIME, ""); err != nil {
			return fmt.Errorf("storage: mount %s on %s: %w", p.Device, target, err)
		}
		m.mounted = append(m.mounted, target)
	}
	return nil
}

// metaMounts are the kernel filesystems bind-mounted into the target so
// commands can run chrooted during install.
var metaMounts = []string{"proc", "sys", "dev"}

// MountMeta bind-mounts /proc, /sys and /dev into the target tree. The
// target root must already be mounted.
func (m *Mounter) MountMeta() error {
	for _, name := range metaMounts {
		src := "/" + name
		target := filepath.Join(m.root, name)
		if err := os.MkdirAll(target, 0o755); err != nil {
			return fmt.Errorf("storage: mkdir %s: %w", target, err)
		}
		// MS_BIND rebinds the live filesystem; the fstype is ignored.
		if err := syscall.Mount(src, target, "", syscall.MS_BIND, ""); err != nil {
			return fmt.Errorf("storage: bind %s to %s: %w", src, target, err)
		}
		m.mounted = append(m.mounted, target)
	}
	return nil
}

// UnmountAll unmounts everything this Mounter mounted, in reverse order
// so children come off before their parents. It reports the mounts it
// could not release rather than stopping at the first.
func (m *Mounter) UnmountAll() error {
	var failed []string
	for i := len(m.mounted) - 1; i >= 0; i-- {
		if err := syscall.Unmount(m.mounted[i], syscall.MNT_DETACH); err != nil {
			failed = append(failed, m.mounted[i])
		}
	}
	m.mounted = nil
	if len(failed) > 0 {
		return fmt.Errorf("storage: failed to unmount: %s", strings.Join(failed, ", "))
	}
	return nil
}
