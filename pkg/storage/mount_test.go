package storage

import (
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
