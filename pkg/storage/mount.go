package storage

import (
	"fmt"
	"golang.org/x/sys/unix"
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
	// meta are the bind mounts of the live system's /proc, /sys and /dev:
	// they hold no data of the install and carry submounts, so they are
	// detached rather than unmounted.
	meta map[string]bool
	// Lazy lists the filesystems the last UnmountAll could only detach
	// because something still held them open. They come off once the
	// last user lets go; the data was synced before.
	Lazy []string
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
		// A slave of the live mounts (arch-chroot's --make-rslave): where
		// the live system's mounts are shared (systemd makes / shared),
		// taking the bind off again propagated back and unmounted the
		// live /dev/pts, /dev/shm, efivars, cgroups.
		if err := syscall.Mount("", target, "", syscall.MS_SLAVE|syscall.MS_REC, ""); err != nil {
			return fmt.Errorf("storage: make %s a slave: %w", target, err)
		}
		if m.meta == nil {
			m.meta = map[string]bool{}
		}
		m.meta[target] = true
	}
	return nil
}

// HasMeta reports whether MountMeta's binds are in place.
func (m *Mounter) HasMeta() bool { return len(m.meta) > 0 }

// UnmountMeta takes the /proc, /sys and /dev binds MountMeta made back off
// the target, leaving its filesystems mounted.
func (m *Mounter) UnmountMeta() error {
	var kept, failed []string
	for i := len(m.mounted) - 1; i >= 0; i-- {
		target := m.mounted[i]
		if !m.meta[target] {
			kept = append([]string{target}, kept...)
			continue
		}
		if err := syscall.Unmount(target, syscall.MNT_DETACH); err != nil {
			failed = append(failed, fmt.Sprintf("%s (%v)", target, err))
			kept = append([]string{target}, kept...)
			continue
		}
		delete(m.meta, target)
	}
	m.mounted = kept
	if len(failed) > 0 {
		return fmt.Errorf("storage: failed to unmount: %s", strings.Join(failed, ", "))
	}
	return nil
}

// UnmountAll unmounts everything this Mounter mounted, in reverse order
// so children come off before their parents. It reports the mounts it
// could not release rather than stopping at the first.
//
// Everything is synced first, and each filesystem of the install is
// unmounted properly, so its data is on the disk when this returns. A
// lazy detach alone returned at once and left the writeback for later:
// "Install complete." printed while the ESP was still dirty, and a
// machine switched off then booted into a damaged one. Only a filesystem
// something still holds (EBUSY) falls back to a detach, and is listed in
// Lazy so the caller can say so.
func (m *Mounter) UnmountAll() error {
	var failed []string
	m.Lazy = nil
	if len(m.mounted) > 0 {
		syscall.Sync()
	}
	for i := len(m.mounted) - 1; i >= 0; i-- {
		target := m.mounted[i]
		if m.meta[target] {
			if err := syscall.Unmount(target, syscall.MNT_DETACH); err != nil {
				failed = append(failed, fmt.Sprintf("%s (%v)", target, err))
			}
			continue
		}
		err := syscall.Unmount(target, 0)
		if err == syscall.EBUSY {
			if err = syscall.Unmount(target, syscall.MNT_DETACH); err == nil {
				m.Lazy = append(m.Lazy, target)
			}
		}
		if err != nil {
			failed = append(failed, fmt.Sprintf("%s (%v)", target, err))
		}
	}
	m.mounted, m.meta = nil, nil
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

func createSubvolumes(p Partition, run func([]string) error) error {
	return WithMounted(p.EffectiveDevice(), p.Filesystem, func(top string) error {
		for _, sv := range p.Subvolumes {
			if err := run([]string{"btrfs", "subvolume", "create", filepath.Join(top, sv.Name)}); err != nil {
				return err
			}
		}
		return nil
	})
}

// WithMounted mounts a filesystem on a directory of its own for as long as
// f runs, and unmounts it after — for work that has to be done inside a
// filesystem the install does not otherwise mount.
func WithMounted(device, fstype string, f func(dir string) error) (err error) {
	top, err := os.MkdirTemp("", "slinstaller-mnt-")
	if err != nil {
		return fmt.Errorf("storage: %w", err)
	}
	defer os.Remove(top)
	if err := syscall.Mount(device, top, fstype, 0, ""); err != nil {
		return fmt.Errorf("storage: mount %s to work inside it: %w", device, err)
	}
	defer func() {
		if uerr := syscall.Unmount(top, 0); uerr != nil && err == nil {
			err = fmt.Errorf("storage: unmount %s: %w", top, uerr)
		}
	}()
	return f(top)
}

// ReadRootFile mounts the plan's root filesystem read-only at the mount
// root, reads rel from it, and unmounts it again: a look at a target before
// anything of it is mounted writable (a resume's check that the disk holds
// the install it resumes — mounting another system's filesystems
// read-write replays their journals).
func (m *Mounter) ReadRootFile(pl *Plan, rel string) ([]byte, error) {
	for _, p := range pl.MountOrder() {
		if p.Mountpoint != "/" {
			continue
		}
		if err := os.MkdirAll(m.root, 0o755); err != nil {
			return nil, err
		}
		// A read-only mount still writes: ext4 and xfs replay a dirty
		// journal, btrfs its log tree, f2fs rolls forward, and ext4 cleans
		// up orphans — on what may be the wrong disk, and with noload alone,
		// unjournaled. With the block device itself set read-only, none of
		// them writes; then the journal is skipped (noload, norecovery,
		// nologreplay), which a read-only device needs to mount a dirty one
		// at all. Where the device cannot be set read-only (not a block
		// device), it is a plain read-only mount, replay and all.
		flags := uintptr(syscall.MS_RDONLY | syscall.MS_NOEXEC | syscall.MS_NOSUID | syscall.MS_NODEV)
		dev, opts := p.EffectiveDevice(), p.mountData()
		mdata := opts
		if restore := setBlockReadOnly(dev); restore != nil {
			defer restore()
			nr := map[string]string{"ext3": "noload", "ext4": "noload", "xfs": "norecovery", "btrfs": "rescue=nologreplay", "f2fs": "norecovery"}[p.Filesystem]
			if nr != "" && opts != "" {
				mdata = opts + "," + nr
			} else if nr != "" {
				mdata = nr
			}
		}
		if err := syscall.Mount(dev, m.root, p.Filesystem, flags, mdata); err != nil {
			// %v, not %w: a device node not there is ENOENT, which
			// would read as the file not being there — "nothing to
			// resume" for a target that cannot be looked at.
			return nil, fmt.Errorf("storage: mount %s read-only on %s: %v", dev, m.root, err)
		}
		data, err := os.ReadFile(filepath.Join(m.root, rel))
		if uerr := syscall.Unmount(m.root, 0); uerr != nil && err == nil {
			err = fmt.Errorf("storage: unmount %s: %w", m.root, uerr)
		}
		return data, err
	}
	return nil, fmt.Errorf("storage: the plan has no root filesystem")
}

// setBlockReadOnly sets a block device read-only (BLKROSET) and returns
// what puts it back, or nil when dev is not a block device it can set.
func setBlockReadOnly(dev string) func() {
	f, err := os.Open(dev)
	if err != nil {
		return nil
	}
	fd := int(f.Fd())
	if _, err := unix.IoctlGetInt(fd, unix.BLKROGET); err != nil || unix.IoctlSetPointerInt(fd, unix.BLKROSET, 1) != nil {
		f.Close()
		return nil
	}
	// Back to read-write, not to what BLKROGET said before: a run killed
	// between the two left the flag set, the next look read it as the
	// device's own, "restored" it, and every mount after failed with
	// EACCES until a reboot. (A disk truly read-only cannot be installed
	// to anyway.)
	return func() {
		unix.IoctlSetPointerInt(fd, unix.BLKROSET, 0)
		f.Close()
	}
}

// FreezeThaw freezes the filesystem mounted at dir and thaws it at once:
// what is in its journal or log goes to its home on disk (a checkpoint), so
// a look that skips the journal finds it. Best effort.
func FreezeThaw(dir string) {
	const fifreeze, fithaw = 0xC0045877, 0xC0045878 // _IOWR('X', 119/120, int)
	f, err := os.Open(dir)
	if err != nil {
		return
	}
	defer f.Close()
	if unix.IoctlSetInt(int(f.Fd()), fifreeze, 0) == nil {
		unix.IoctlSetInt(int(f.Fd()), fithaw, 0)
	}
}
