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
