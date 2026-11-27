package storage

import (
	"errors"
	"strings"
	"testing"
)

func TestCheckFit(t *testing.T) {
	pl := &Plan{Wipe: true, Disk: "/dev/sda", Parts: []Partition{
		{Number: 1, Device: "/dev/sda1", Filesystem: "vfat", Mountpoint: "/boot", Size: "512M"},
		{Number: 2, Device: "/dev/sda2", Filesystem: "swap", Size: "2G"},
		{Number: 3, Device: "/dev/sda3", Filesystem: "ext4", Mountpoint: "/", Size: SizeRest},
	}}
	size := func(n uint64) func(string) (uint64, bool) {
		return func(string) (uint64, bool) { return n, true }
	}
	if err := pl.CheckFit(size(64<<30), MinRootSize, "2 GB"); err != nil {
		t.Errorf("64G: %v", err)
	}
	if err := pl.CheckFit(size(2<<30), MinRootSize, "2 GB"); err == nil || !strings.Contains(err.Error(), "layout needs 2.5G") {
		t.Errorf("2G: %v", err)
	}
	if err := pl.CheckFit(size(3<<30), MinRootSize, "2 GB"); err == nil || !strings.Contains(err.Error(), "below its 2 GB minimum") {
		t.Errorf("3G: %v", err)
	}
	if err := pl.CheckFit(func(string) (uint64, bool) { return 0, false }, MinRootSize, "2 GB"); err != nil {
		t.Errorf("an unknown disk is not checked: %v", err)
	}
	// A size too big to count saturates and fails, not wraps and fits.
	pl.Parts[1].Size = "16000000000000000T"
	if err := pl.CheckFit(size(64<<30), MinRootSize, "2 GB"); err == nil {
		t.Error("an absurd size fit")
	}
	for in, want := range map[uint64]string{512: "512B", 1536: "1.5K", 3 << 30: "3.0G"} {
		if got := humanBytes(in); got != want {
			t.Errorf("humanBytes(%d) = %q, want %q", in, got, want)
		}
	}
}

// A Safe install's new partitions are checked against the numbers parted
// will give them: the lowest free, in the order they are cut.
func TestCheckCreateNumbers(t *testing.T) {
	saved := partedPrintFree
	defer func() { partedPrintFree = saved }()
	table := "BYT;\n/dev/sda:100000MiB:scsi:512:512:gpt:DISK:;\n" +
		"1:1MiB:513MiB:512MiB:fat32::boot, esp;\n" +
		"3:513MiB:20000MiB:19487MiB:ntfs::;\n" +
		"1:20000MiB:100000MiB:80000MiB:free;\n"
	partedPrintFree = func(string) (string, error) { return table, nil }
	plan := func(nums ...int) *Plan {
		pl := &Plan{Disk: "/dev/sda", Parts: []Partition{{Number: 1, Device: "/dev/sda1", Preserve: true}}}
		for _, n := range nums {
			pl.Parts = append(pl.Parts, Partition{Number: n, Device: PartitionName("/dev/sda", n), Create: true})
		}
		return pl
	}
	if err := plan(2, 4).CheckCreateNumbers(); err != nil {
		t.Errorf("2 then 4 (3 is taken): %v", err)
	}
	if err := plan(3).CheckCreateNumbers(); err == nil || !strings.Contains(err.Error(), "parted will number it 2 (/dev/sda2), and partition 3 already exists") {
		t.Errorf("3: %v", err)
	}
	if err := plan(4).CheckCreateNumbers(); err == nil || strings.Contains(err.Error(), "already exists") {
		t.Errorf("4 (2 is free): %v", err)
	}
	if err := (&Plan{Disk: "/dev/sda"}).CheckCreateNumbers(); err != nil {
		t.Errorf("nothing to create: %v", err)
	}
	boom := errors.New("boom")
	partedPrintFree = func(string) (string, error) { return "", boom }
	if err := plan(2).CheckCreateNumbers(); !errors.Is(err, boom) {
		t.Errorf("parted failing: %v", err)
	}
	// PhysOn is what parted cuts on a disk, in number order.
	pl := &Plan{Disk: "/dev/sda", Parts: []Partition{{Number: 2, Device: "/dev/sda2"}, {Number: 1, Device: "/dev/sda1"}, {LV: true}}}
	if got := pl.PhysOn("/dev/sda"); len(got) != 2 || got[0].Number != 1 {
		t.Errorf("PhysOn = %+v", got)
	}
}
