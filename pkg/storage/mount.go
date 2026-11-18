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
		if p.Mountpoint == "" {
			continue
		}
		if len(p.Subvolumes) == 0 {
			out = append(out, p)
			continue
		}
		// One mount per subvolume, all from the same filesystem.
		for _, sv := range p.Subvolumes {
			m := p
			m.Mountpoint, m.Subvol, m.Subvolumes = sv.Mountpoint, sv.Name, nil
			out = append(out, m)
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
		if err := syscall.Mount(p.EffectiveDevice(), target, p.Filesystem, syscall.MS_RELATIME, p.mountData()); err != nil {
			return fmt.Errorf("storage: mount %s on %s: %w", p.EffectiveDevice(), target, err)
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
//
// The bind is recursive (MS_REC), the way arch-chroot's `mount --rbind`
// is, because the submounts matter: efivarfs lives at
// /sys/firmware/efi/efivars as its own mount, and a plain bind of /sys
// would leave it behind — then grub-install's efibootmgr call fails with
// "EFI variables are not supported on this system". /dev/pts and
// /dev/shm come along for the same reason.
func (m *Mounter) MountMeta() error {
	for _, name := range metaMounts {
		src := "/" + name
		target := filepath.Join(m.root, name)
		if err := os.MkdirAll(target, 0o755); err != nil {
			return fmt.Errorf("storage: mkdir %s: %w", target, err)
		}
		// MS_BIND rebinds the live filesystem; MS_REC carries submounts.
		if err := syscall.Mount(src, target, "", syscall.MS_BIND|syscall.MS_REC, ""); err != nil {
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

// vfsOptions are fstab options the kernel's mount layer or mount(8) itself
// handles, rather than the filesystem. The install mounts with its own
// flags, so these are left for the installed system's fstab.
var vfsOptions = map[string]bool{
	"defaults": true, "rw": true, "ro": true, "auto": true, "noauto": true, "nofail": true,
	"user": true, "nouser": true, "users": true, "owner": true, "group": true,
	"exec": true, "noexec": true, "suid": true, "nosuid": true, "dev": true, "nodev": true,
	"atime": true, "noatime": true, "diratime": true, "nodiratime": true, "relatime": true,
	"norelatime": true, "strictatime": true, "lazytime": true, "nolazytime": true,
	"sync": true, "async": true, "dirsync": true, "silent": true, "loud": true, "_netdev": true,
}

// mountData is what the install passes to the filesystem when mounting the
// partition: its subvolume and the filesystem's own options (compress=zstd,
// discard), so they already apply to what the install writes.
func (p Partition) mountData() string {
	var data []string
	if p.Subvol != "" {
		data = append(data, "subvol="+p.Subvol)
	}
	for _, o := range strings.Split(p.MountOptions, ",") {
		if o == "" || vfsOptions[o] || strings.HasPrefix(o, "x-") || strings.HasPrefix(o, "subvol") {
			continue
		}
		data = append(data, o)
	}
	return strings.Join(data, ",")
}

// fstabOptions is the options column of the partition's fstab entry.
func (p Partition) fstabOptions() string {
	opts := p.MountOptions
	if opts == "" {
		opts = "defaults"
	}
	if p.Subvol != "" {
		opts += ",subvol=" + p.Subvol
	}
	return opts
}

// CreateSubvolumes makes the btrfs subvolumes of every freshly made
// filesystem that has them: the filesystem is mounted on its own for a
// moment, `btrfs subvolume create` runs for each, and it is unmounted.
func CreateSubvolumes(pl *Plan, run func([]string) error) error {
	for _, p := range pl.Parts {
		if len(p.Subvolumes) == 0 || p.Preserve {
			continue
		}
		if err := createSubvolumes(p, run); err != nil {
			return err
		}
	}
	return nil
}

func createSubvolumes(p Partition, run func([]string) error) (err error) {
	top, err := os.MkdirTemp("", "slinstaller-btrfs-")
	if err != nil {
		return fmt.Errorf("storage: %w", err)
	}
	defer os.Remove(top)
	if err := syscall.Mount(p.EffectiveDevice(), top, p.Filesystem, 0, ""); err != nil {
		return fmt.Errorf("storage: mount %s to make its subvolumes: %w", p.EffectiveDevice(), err)
	}
	defer func() {
		if uerr := syscall.Unmount(top, 0); uerr != nil && err == nil {
			err = fmt.Errorf("storage: unmount %s: %w", top, uerr)
		}
	}()
	for _, sv := range p.Subvolumes {
		if err := run([]string{"btrfs", "subvolume", "create", filepath.Join(top, sv.Name)}); err != nil {
			return err
		}
	}
	return nil
}
