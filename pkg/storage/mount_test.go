package storage

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMountOrder(t *testing.T) {
	pl := &Plan{Parts: []Partition{
		{Device: "/dev/sda4", Mountpoint: "/home", Filesystem: "ext4"},
		{Device: "/dev/sda3", Mountpoint: "/", Filesystem: "ext4"},
		{Device: "/dev/sda2", Filesystem: "swap"}, // no mountpoint
		{Device: "/dev/sda1", Mountpoint: "/boot/efi", Filesystem: "vfat"},
		{Device: "/dev/sda5", Mountpoint: "/boot", Filesystem: "ext4"},
	}}

	got := pl.MountOrder()
	var mnts []string
	for _, p := range got {
		mnts = append(mnts, p.Mountpoint)
	}
	want := []string{"/", "/boot", "/home", "/boot/efi"}
	if strings.Join(mnts, ",") != strings.Join(want, ",") {
		t.Errorf("MountOrder = %v, want %v", mnts, want)
	}
	// Swap, having no mountpoint, is excluded.
	if len(got) != 4 {
		t.Errorf("MountOrder returned %d, want 4 (swap excluded)", len(got))
	}
	// A parent always precedes anything beneath it.
	seen := map[string]bool{}
	for _, p := range got {
		parent := p.Mountpoint[:strings.LastIndex(p.Mountpoint, "/")+1]
		if parent != "/" && parent != "" {
			parent = strings.TrimSuffix(parent, "/")
			if !seen[parent] {
				t.Errorf("%s mounted before its parent %s", p.Mountpoint, parent)
			}
		}
		seen[p.Mountpoint] = true
	}
}

func TestFstab(t *testing.T) {
	pl := &Plan{Parts: []Partition{
		{Device: "/dev/sda1", Mountpoint: "/boot", Filesystem: "vfat"},
		{Device: "/dev/sda2", Filesystem: "swap"},
		{Device: "/dev/sda3", Mountpoint: "/", Filesystem: "ext4"},
	}}
	uuid := map[string]string{
		"/dev/sda1": "AAAA-BBBB",
		"/dev/sda2": "11111111-2222-3333-4444-555555555555",
		// sda3 intentionally missing to exercise the device fallback.
	}

	got := pl.Fstab(uuid)

	for _, line := range []string{
		"UUID=AAAA-BBBB\t/boot\tvfat\tdefaults\t0 2",
		"/dev/sda3\t/\text4\tdefaults\t0 1", // root: fallback device + pass 1
		"UUID=11111111-2222-3333-4444-555555555555\tnone\tswap\tdefaults\t0 0",
	} {
		if !strings.Contains(got, line) {
			t.Errorf("fstab missing line:\n  %q\nfull:\n%s", line, got)
		}
	}

	// Root (/) comes before /boot in the file (mount order).
	if strings.Index(got, "\t/\text4") > strings.Index(got, "\t/boot\tvfat") {
		t.Errorf("fstab lists /boot before /:\n%s", got)
	}
}

// Neither is fatal where it cannot act: a regular file is no block device,
// a temp directory's filesystem may not freeze.
func TestBlockReadOnlyAndFreezeBestEffort(t *testing.T) {
	f := filepath.Join(t.TempDir(), "img")
	os.WriteFile(f, nil, 0o600)
	if setBlockReadOnly(f) != nil || setBlockReadOnly(filepath.Join(t.TempDir(), "none")) != nil {
		t.Error("a file taken for a block device")
	}
	FreezeThaw(filepath.Join(t.TempDir(), "none"))
	if os.Geteuid() != 0 { // as root it would freeze the host's /tmp
		FreezeThaw(t.TempDir())
	}
}

// A mountpoint that leads out of the target through a link is not mounted on.
func TestMountpointInside(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	os.Symlink(outside, filepath.Join(root, "boot"))
	m := NewMounter(root)
	if err := m.inside(filepath.Join(root, "boot")); err == nil || !strings.Contains(err.Error(), "leads outside the target") {
		t.Errorf("boot -> outside: %v", err)
	}
	os.MkdirAll(filepath.Join(root, "real", "home"), 0o755)
	os.Symlink("real/home", filepath.Join(root, "home"))
	if err := m.inside(filepath.Join(root, "home")); err != nil {
		t.Errorf("a relative link inside: %v", err)
	}
	if err := m.inside(root); err != nil {
		t.Errorf("the root itself: %v", err)
	}
	if err := (&Mounter{root: root}).MountAll(&Plan{Parts: []Partition{{Device: "tmpfs", Filesystem: "tmpfs", Mountpoint: "/boot"}}}); err == nil || !strings.Contains(err.Error(), "leads outside") {
		t.Errorf("MountAll through the link: %v", err)
	}
}

// A root that is itself a link is fine; a mountpoint below a link out of
// the target makes nothing outside.
func TestMountpointInsideLinkedRoot(t *testing.T) {
	real, outside := t.TempDir(), t.TempDir()
	root := filepath.Join(t.TempDir(), "mnt")
	os.Symlink(real, root)
	os.MkdirAll(filepath.Join(root, "boot"), 0o755)
	if err := NewMounter(root).inside(filepath.Join(root, "boot")); err != nil {
		t.Errorf("a linked root: %v", err)
	}
	os.Symlink(outside, filepath.Join(real, "var"))
	err := (&Mounter{root: real}).MountAll(&Plan{Parts: []Partition{{Device: "tmpfs", Filesystem: "tmpfs", Mountpoint: "/var/lib/x"}}})
	if err == nil || !strings.Contains(err.Error(), "leads outside") {
		t.Errorf("through /var: %v", err)
	}
	if ents, _ := os.ReadDir(outside); len(ents) != 0 {
		t.Errorf("made outside: %v", ents)
	}
}

// A sibling whose name starts with the root's (/mnt and /mntx) is outside
// it, and a mountpoint that is not there cannot be judged inside.
func TestMountpointInsideSiblingPrefix(t *testing.T) {
	top := t.TempDir()
	root, sibling := filepath.Join(top, "mnt"), filepath.Join(top, "mntx")
	os.MkdirAll(root, 0o755)
	os.MkdirAll(sibling, 0o755)
	os.Symlink(sibling, filepath.Join(root, "boot"))
	if err := NewMounter(root).inside(filepath.Join(root, "boot")); err == nil || !strings.Contains(err.Error(), "leads outside the target") {
		t.Errorf("boot -> /mntx: %v", err)
	}
	if err := NewMounter(root).inside(filepath.Join(root, "nothing")); err == nil {
		t.Error("a missing mountpoint judged inside")
	}
}

// A dangling link on the way to a mountpoint is not walked past: MkdirAll
// would make what it points to, outside the target.
func TestMountAllDanglingLinkOut(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	os.Symlink(filepath.Join(outside, "made"), filepath.Join(root, "boot"))
	err := NewMounter(root).MountAll(&Plan{Parts: []Partition{{Device: "tmpfs", Filesystem: "tmpfs", Mountpoint: "/boot/efi"}}})
	if err == nil {
		t.Error("mounted through a dangling link")
	}
	if _, err := os.Lstat(filepath.Join(outside, "made")); err == nil {
		t.Error("made a directory outside the target")
	}
}

// MountMeta checks /proc, /sys and /dev lead into the target before binding
// onto them, as MountAll does.
func TestMountMetaDanglingLinkOut(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("as root a mistaken bind would reach the host's /proc")
	}
	root, outside := t.TempDir(), t.TempDir()
	os.Symlink(outside, filepath.Join(root, "proc"))
	if err := NewMounter(root).MountMeta(); err == nil || !strings.Contains(err.Error(), "leads outside") {
		t.Errorf("MountMeta through a link out: %v", err)
	}
	// The deepest thing there on the way is the link itself, dangling or not.
	os.Symlink(filepath.Join(outside, "gone"), filepath.Join(root, "dangling"))
	if got := existingParent(filepath.Join(root, "dangling", "a", "b")); got != filepath.Join(root, "dangling") {
		t.Errorf("existingParent past a dangling link = %s", got)
	}
}

// A plan with no root filesystem is not looked into through another one.
func TestReadRootFileWantsTheRoot(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("as root the other filesystem would be mounted")
	}
	pl := &Plan{Parts: []Partition{{Device: "tmpfs", Filesystem: "tmpfs", Mountpoint: "/boot"}}}
	if _, err := NewMounter(t.TempDir()).ReadRootFile(pl, "x"); err == nil || !strings.Contains(err.Error(), "no root filesystem") {
		t.Errorf("ReadRootFile with only /boot: %v", err)
	}
}

// A look at a filesystem on a device set read-only skips its journal or
// log, keeping the options it is mounted with; on anything else (not a
// block device) the options are as they are, and a filesystem with no
// journal gets nothing added.
func TestLookOptions(t *testing.T) {
	for _, c := range []struct {
		fs, opts string
		ro       bool
		want     string
	}{
		{"ext4", "", true, "noload"},
		{"ext3", "", true, "noload"},
		{"xfs", "", true, "norecovery"},
		{"f2fs", "", true, "norecovery"},
		{"btrfs", "subvol=@", true, "subvol=@,rescue=nologreplay"},
		{"btrfs", "subvol=@", false, "subvol=@"},
		{"ext4", "", false, ""},
		{"ext2", "", true, ""},
		{"vfat", "umask=0077", true, "umask=0077"},
	} {
		if got := lookOptions(c.fs, c.opts, c.ro); got != c.want {
			t.Errorf("lookOptions(%q, %q, %v) = %q, want %q", c.fs, c.opts, c.ro, got, c.want)
		}
	}
}
