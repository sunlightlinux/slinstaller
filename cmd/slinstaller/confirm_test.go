package main

import (
	"bufio"
	"reflect"
	"strings"
	"testing"

	"github.com/sunlightlinux/slinstaller/pkg/storage"
)

// confirm is the last guard before a disk is erased: the operator has to
// retype the disk path. These tests pin what does and does not get through.
func TestConfirmRequiresTheDiskPathRetyped(t *testing.T) {
	plan := &storage.Plan{Disk: "/dev/sda", Wipe: true}
	answer := func(s string) bool {
		return confirm(bufio.NewReader(strings.NewReader(s)), plan)
	}

	if !answer("/dev/sda\n") {
		t.Error("the retyped disk path was refused")
	}
	if !answer("  /dev/sda  \n") {
		t.Error("surrounding whitespace should not matter")
	}
	for _, no := range []string{
		"y\n", "yes\n", "\n", // a yes/no reflex is not a confirmation
		"/dev/sdb\n",  // another disk
		"/dev/sda1\n", // a partition of it
		"sda\n",       // not the path
		"/DEV/SDA\n",  // not the same path
		"",            // EOF: nothing was typed
		"/dev/sda",    // a path cut off before Enter (e.g. a closed pipe)
	} {
		if answer(no) {
			t.Errorf("%q was accepted as confirmation to erase /dev/sda", no)
		}
	}
}

func TestConfirmWithSeveralDisksAndOnReuse(t *testing.T) {
	// Several disks (a RAID layout): the first is the one to retype.
	raid := &storage.Plan{Disk: "/dev/sda", Disks: []string{"/dev/sda", "/dev/sdb"}, Wipe: true}
	if !confirm(bufio.NewReader(strings.NewReader("/dev/sda\n")), raid) {
		t.Error("first disk of a multi-disk plan refused")
	}
	if confirm(bufio.NewReader(strings.NewReader("/dev/sdb\n")), raid) {
		t.Error("the second disk confirmed a multi-disk plan")
	}

	// A reuse install does not repartition, but still formats: same guard.
	reuse := &storage.Plan{Disk: "/dev/sda", Wipe: false, Parts: []storage.Partition{
		{Device: "/dev/sda1", Filesystem: "vfat", Mountpoint: "/boot/efi", Preserve: true},
		{Device: "/dev/sda2", Filesystem: "ext4", Mountpoint: "/"},
	}}
	if !confirm(bufio.NewReader(strings.NewReader("/dev/sda\n")), reuse) {
		t.Error("reuse install: the retyped disk path was refused")
	}
	if confirm(bufio.NewReader(strings.NewReader("yes\n")), reuse) {
		t.Error("reuse install confirmed by 'yes'")
	}
}

func TestLayoutPackages(t *testing.T) {
	// A plain ext4/vfat layout needs nothing beyond the base.
	plain := &storage.Plan{Parts: []storage.Partition{
		{Filesystem: "vfat"}, {Filesystem: "ext4"},
	}}
	if got := layoutPackages(plain); len(got) != 0 {
		t.Errorf("plain layout packages = %v, want none", got)
	}

	// Each feature brings the tool the installed system needs at boot, and a
	// filesystem used twice is listed once.
	full := &storage.Plan{Parts: []storage.Partition{
		{Filesystem: "btrfs"}, {Filesystem: "btrfs"},
		{Filesystem: "xfs"}, {Filesystem: "f2fs"},
	}}
	if got, want := layoutPackages(full), []string{"btrfs-progs", "xfsprogs", "f2fs-tools"}; !reflect.DeepEqual(got, want) {
		t.Errorf("filesystem packages = %v, want %v", got, want)
	}
	has := func(parts []storage.Partition, pkg string) bool {
		for _, p := range layoutPackages(&storage.Plan{Parts: parts}) {
			if p == pkg {
				return true
			}
		}
		return false
	}
	if !has([]storage.Partition{{Filesystem: "ext4", Encrypted: true}}, "cryptsetup") {
		t.Error("an encrypted partition needs cryptsetup")
	}
	if !has([]storage.Partition{{IsPV: true}}, "lvm2") || !has([]storage.Partition{{LV: true, Filesystem: "ext4"}}, "lvm2") {
		t.Error("an LVM layout needs lvm2")
	}
	if !has([]storage.Partition{{IsRaidMember: true}}, "mdadm") || !has([]storage.Partition{{Raid: true, Filesystem: "ext4"}}, "mdadm") {
		t.Error("a RAID layout needs mdadm")
	}
}

func TestHumanSize(t *testing.T) {
	for in, want := range map[uint64]string{
		0:                  "0B",
		1023:               "1023B",
		1024:               "1.0K",
		512 << 20:          "512.0M",
		20 << 30:           "20.0G",
		1536 << 30:         "1.5T",
		uint64(3) << 50:    "3.0P",
		uint64(4096) << 50: "4096.0P", // past the last unit: stays in it
	} {
		if got := humanSize(in); got != want {
			t.Errorf("humanSize(%d) = %q, want %q", in, got, want)
		}
	}
	if orDash("") != "-" || orDash("/home") != "/home" {
		t.Error("orDash")
	}
}
