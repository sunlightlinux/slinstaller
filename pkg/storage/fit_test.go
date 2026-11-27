package storage

import (
	"errors"
	"math"
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

// A FAT partition flagged legacy_boot is not an ESP.
func TestExistingESPWantsTheFlag(t *testing.T) {
	saved := partedPrintFree
	defer func() { partedPrintFree = saved }()
	partedPrintFree = func(string) (string, error) {
		return "BYT;\n/dev/sda:100000MiB:scsi:512:512:gpt:DISK:;\n1:1MiB:513MiB:512MiB:fat32::legacy_boot;\n2:513MiB:1025MiB:512MiB:fat32::boot, esp;\n", nil
	}
	if dev, ok, err := ExistingESP("/dev/sda"); err != nil || !ok || dev != "/dev/sda2" {
		t.Errorf("ExistingESP = %q %v %v, want /dev/sda2", dev, ok, err)
	}
}

func TestCheckVGFit(t *testing.T) {
	size := func(n uint64) func(string) (uint64, bool) { return func(string) (uint64, bool) { return n, true } }
	pl := &Plan{Disk: "/dev/sda", Parts: []Partition{
		{Device: "/dev/sda1", Disk: "/dev/sda", Filesystem: "vfat", Mountpoint: "/boot", Size: "512M"},
		{Device: "/dev/sda2", Disk: "/dev/sda", IsPV: true, VGName: "vg0", Size: SizeRest, Encrypted: true},
		{LV: true, VGName: "vg0", Mountpoint: "/", Filesystem: "ext4", Size: "500G"},
		{LV: true, VGName: "vg0", Mountpoint: "/home", Filesystem: "ext4", Size: SizeRest},
	}}
	if err := pl.CheckFit(size(64<<30), MinRootSize, "2 GB"); err == nil || !strings.Contains(err.Error(), "logical volumes of vg0 need 500.0G") {
		t.Errorf("500G on 64G: %v", err)
	}
	pl.Parts[2].Size = "40G"
	if err := pl.CheckFit(size(64<<30), MinRootSize, "2 GB"); err != nil {
		t.Errorf("40G on 64G: %v", err)
	}
	// A disk it does not know, or a PV on an array: not checked.
	pl.Parts[2].Size = "500G"
	if err := pl.CheckFit(func(string) (uint64, bool) { return 0, false }, MinRootSize, "2 GB"); err != nil {
		t.Errorf("unknown disk: %v", err)
	}
	pl.Parts[1].Raid = true
	if err := pl.CheckFit(size(64<<30), MinRootSize, "2 GB"); err != nil {
		t.Errorf("PV on an array: %v", err)
	}
	pl.Parts[1].Raid, pl.Parts[1].Size = false, "20G"
	if err := pl.CheckFit(size(64<<30), MinRootSize, "2 GB"); err == nil {
		t.Error("500G on a 20G PV")
	}
	pl.Parts[1].Size = "bogus"
	if err := pl.CheckFit(size(64<<30), MinRootSize, "2 GB"); err != nil {
		t.Errorf("unparsed PV size: %v", err)
	}
}

// A decimal LV size is read, and a fill LV needs room left for it.
func TestCheckVGFitDecimalAndFill(t *testing.T) {
	size := func(string) (uint64, bool) { return 64 << 30, true }
	pl := &Plan{Disk: "/dev/sda", Parts: []Partition{
		{Device: "/dev/sda1", Disk: "/dev/sda", Filesystem: "vfat", Mountpoint: "/boot", Size: "512M"},
		{Device: "/dev/sda2", Disk: "/dev/sda", IsPV: true, VGName: "vg0", Size: SizeRest},
		{LV: true, VGName: "vg0", Mountpoint: "/home", Filesystem: "ext4", Size: "500.5G"},
		{LV: true, VGName: "vg0", Mountpoint: "/", Filesystem: "ext4", Size: SizeRest},
	}}
	if err := pl.CheckFit(size, MinRootSize, "2 GB"); err == nil {
		t.Error("500.5G on 64G passed")
	}
	pl.Parts[2].Size = "63G" // the fixed LV fits, but leaves the root less than its minimum
	if err := pl.CheckFit(size, MinRootSize, "2 GB"); err == nil {
		t.Error("a fill root with no room passed")
	}
	pl.Parts[2].Size = "50.5G"
	if err := pl.CheckFit(size, MinRootSize, "2 GB"); err != nil {
		t.Errorf("50.5G and a root: %v", err)
	}
	pl.Parts[2].Size = "lots"
	if err := pl.CheckFit(size, MinRootSize, "2 GB"); err != nil {
		t.Errorf("an unread size is not judged: %v", err)
	}
	if _, err := parseLVSize("1.5"); err == nil {
		t.Error("a unitless decimal taken")
	}
}

// The edges of the volume group check: exactly full fits, the LUKS header
// and the GPT's own space count against the group, a sum too big to count
// saturates rather than wraps, and a group with something not judged (a PV
// on an array, a size not read, no PV the plan knows) is not judged at all.
func TestCheckVGFitEdges(t *testing.T) {
	size := func(n uint64) func(string) (uint64, bool) { return func(string) (uint64, bool) { return n, true } }
	pv := func(sz string, enc bool) Partition {
		return Partition{Device: "/dev/sda2", Disk: "/dev/sda", IsPV: true, VGName: "vg0", Size: sz, Encrypted: enc}
	}
	lv := func(sz string) Partition {
		return Partition{LV: true, VGName: "vg0", Mountpoint: "/srv", Filesystem: "ext4", Size: sz}
	}
	fits := func(parts ...Partition) error {
		return (&Plan{Disk: "/dev/sda", Parts: parts}).CheckFit(size(1<<40), MinRootSize, "2 GB")
	}
	// 1G less LVM's 4M is 1020M: that much fits, a MiB more does not.
	if err := fits(pv("1G", false), lv("1020M")); err != nil {
		t.Errorf("exactly full: %v", err)
	}
	if err := fits(pv("1G", false), lv("1021M")); err == nil {
		t.Error("a MiB over passed")
	}
	// Encrypted, the LUKS2 header takes 16M more.
	if err := fits(pv("1G", true), lv("1010M")); err == nil {
		t.Error("an encrypted PV counted without its LUKS header")
	}
	if err := fits(pv("1G", true), lv("1004M")); err != nil {
		t.Errorf("encrypted, exactly full: %v", err)
	}
	// Two halves of 2^64 saturate, never wrap to 0 and fit.
	if err := fits(pv("1G", false), lv("8388608T"), lv("8388608T")); err == nil {
		t.Error("an overflowing sum of volumes fit")
	}
	// A PV too small for LVM's own space adds nothing, and takes nothing.
	if err := fits(pv("1M", false), pv("1G", false), lv("1020M")); err != nil {
		t.Errorf("a 1M PV took from the group: %v", err)
	}
	// Not judged: one PV on an array, an LV size not read, no PV at all.
	arr := pv("1G", false)
	arr.Raid = true
	if err := fits(arr, pv("1G", false), lv("5G")); err != nil {
		t.Errorf("a group with a PV on an array: %v", err)
	}
	if err := fits(pv("1G", false), lv("lots"), lv("5G")); err != nil {
		t.Errorf("a group with an unread LV size: %v", err)
	}
	if err := fits(lv("5G")); err != nil {
		t.Errorf("a group with no PV in the plan: %v", err)
	}
	// A PV that fills its disk gets what the disk leaves less the GPT's 2M:
	// on a disk of 1G+2M, 1020M of volumes.
	fill := func(disk uint64, lvSize string) error {
		pl := &Plan{Disk: "/dev/sda", Parts: []Partition{pv(SizeRest, false), lv(lvSize)}}
		return pl.CheckFit(size(disk), MinRootSize, "2 GB")
	}
	if err := fill(1<<30+GPTReserveBytes, "1020M"); err != nil {
		t.Errorf("a fill PV, exactly full: %v", err)
	}
	if err := fill(1<<30+GPTReserveBytes, "1021M"); err == nil {
		t.Error("a fill PV counted the GPT's space as its own")
	}
}

// parseLVSize: a negative size is refused, a decimal one is in its own
// unit, and one too big to count saturates.
func TestParseLVSizeEdges(t *testing.T) {
	if _, err := parseLVSize("-1.5G"); err == nil {
		t.Error("a negative size taken")
	}
	if n, err := parseLVSize("1.5M"); err != nil || n != 3<<19 {
		t.Errorf("1.5M = %d, %v", n, err)
	}
	if n, err := parseLVSize("16777216.0T"); err != nil || n != math.MaxUint64 {
		t.Errorf("2^64 bytes = %d, %v; want it saturated", n, err)
	}
}
