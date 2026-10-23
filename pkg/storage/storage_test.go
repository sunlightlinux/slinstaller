package storage

import (
	"fmt"
	"strings"
	"testing"

	"github.com/sunlightlinux/slinstaller/pkg/descriptor"
)

func TestPartitionName(t *testing.T) {
	cases := []struct {
		disk string
		n    int
		want string
	}{
		{"/dev/sda", 1, "/dev/sda1"},
		{"/dev/sda", 2, "/dev/sda2"},
		{"/dev/nvme0n1", 1, "/dev/nvme0n1p1"},
		{"/dev/mmcblk0", 3, "/dev/mmcblk0p3"},
		{"/dev/loop0", 1, "/dev/loop0p1"},
	}
	for _, c := range cases {
		if got := PartitionName(c.disk, c.n); got != c.want {
			t.Errorf("PartitionName(%q, %d) = %q, want %q", c.disk, c.n, got, c.want)
		}
	}
}

func TestParseSize(t *testing.T) {
	ok := map[string]uint64{
		"512M":  512 << 20,
		"8G":    8 << 30,
		"1024K": 1024 << 10,
		"1T":    1 << 40,
		"2048":  2048,
	}
	for in, want := range ok {
		got, err := ParseSize(in)
		if err != nil {
			t.Errorf("ParseSize(%q) error: %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("ParseSize(%q) = %d, want %d", in, got, want)
		}
	}

	for _, bad := range []string{"", "rest", "12X", "M"} {
		if _, err := ParseSize(bad); err == nil {
			t.Errorf("ParseSize(%q) = nil error, want failure", bad)
		}
	}
}

// uefiDescriptor is a minimal, valid UEFI layout: an ESP, swap, and a
// root that takes the rest. It drives most of the tests below.
func uefiDescriptor() *descriptor.Descriptor {
	return &descriptor.Descriptor{
		TargetDisk: "/dev/sda",
		Wipe:       true,
		Partitions: []descriptor.Partition{
			{Mountpoint: "/boot", Size: "512M", Filesystem: "vfat", Label: "EFI"},
			{Size: "2G", Filesystem: "swap"},
			{Mountpoint: "/", Size: descriptor.SizeRest, Filesystem: "ext4", Label: "root"},
		},
	}
}

func TestBuildPlan(t *testing.T) {
	pl, err := BuildPlan(uefiDescriptor())
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	if pl.Disk != "/dev/sda" || !pl.Wipe {
		t.Fatalf("plan disk/wipe = %q/%v", pl.Disk, pl.Wipe)
	}
	if len(pl.Parts) != 3 {
		t.Fatalf("got %d partitions, want 3", len(pl.Parts))
	}
	wantDev := []string{"/dev/sda1", "/dev/sda2", "/dev/sda3"}
	for i, p := range pl.Parts {
		if p.Number != i+1 {
			t.Errorf("part %d number = %d", i, p.Number)
		}
		if p.Device != wantDev[i] {
			t.Errorf("part %d device = %q, want %q", i, p.Device, wantDev[i])
		}
	}
}

func TestBuildPlanRestNotLast(t *testing.T) {
	d := &descriptor.Descriptor{
		TargetDisk: "/dev/sda",
		Wipe:       true,
		Partitions: []descriptor.Partition{
			{Mountpoint: "/", Size: descriptor.SizeRest, Filesystem: "ext4"},
			{Mountpoint: "/home", Size: "10G", Filesystem: "ext4"},
		},
	}
	if _, err := BuildPlan(d); err == nil {
		t.Fatal("BuildPlan with non-final rest partition = nil error, want failure")
	}
}

func TestBuildPlanBoundaries(t *testing.T) {
	if _, err := BuildPlan(&descriptor.Descriptor{Partitions: []descriptor.Partition{{}}}); err == nil {
		t.Error("BuildPlan with no disk = nil error, want failure")
	}
	if _, err := BuildPlan(&descriptor.Descriptor{TargetDisk: "/dev/sda"}); err == nil {
		t.Error("BuildPlan with no partitions = nil error, want failure")
	}
}

func TestCommandsDryRun(t *testing.T) {
	pl, err := BuildPlan(uefiDescriptor())
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	cmds, err := pl.Commands()
	if err != nil {
		t.Fatalf("Commands: %v", err)
	}
	got := FormatCommands(cmds)

	want := []string{
		"parted -s /dev/sda mklabel gpt",
		"parted -a optimal --script /dev/sda mkpart EFI fat32 0% 512MiB",
		"parted -a optimal --script /dev/sda mkpart linux-swap 512MiB 2560MiB",
		"parted -a optimal --script /dev/sda mkpart / 2560MiB 100%",
		"sgdisk /dev/sda --typecode=1:C12A7328-F81F-11D2-BA4B-00A0C93EC93B",
		"sgdisk /dev/sda --typecode=2:0657FD6D-A4AB-43C4-84E5-0933C84B4F4F",
		"sgdisk /dev/sda --typecode=3:4F68BCE3-E8CD-4DB1-96E7-FBCAF984B709",
		"partprobe /dev/sda",
		"mkfs.vfat -n EFI -F32 /dev/sda1",
		"mkswap -L root /dev/sda2", // swap has no label here; see below
		"mkfs.ext4 -L root -v -F -b 4096 /dev/sda3",
	}
	// The swap partition carries no label, so correct the expectation.
	want[9] = "mkswap /dev/sda2"

	for _, line := range want {
		if !strings.Contains(got, line) {
			t.Errorf("dry run missing line:\n  %s\nfull output:\n%s", line, got)
		}
	}
}

func TestCommandsNoWipeSkipsMklabel(t *testing.T) {
	d := uefiDescriptor()
	d.Wipe = false
	pl, err := BuildPlan(d)
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	cmds, err := pl.Commands()
	if err != nil {
		t.Fatalf("Commands: %v", err)
	}
	if strings.Contains(FormatCommands(cmds), "mklabel") {
		t.Error("no-wipe plan still emits mklabel")
	}
}

func TestMkfsLabelTruncation(t *testing.T) {
	// vfat labels cap at 11 and use -n, not -L.
	got, err := mkfsCommand(Partition{Device: "/dev/sda1", Filesystem: "vfat", Label: "ThisLabelIsWayTooLong"})
	if err != nil {
		t.Fatalf("mkfsCommand: %v", err)
	}
	line := strings.Join(got, " ")
	if !strings.Contains(line, "-n ThisLabelIs ") {
		t.Errorf("vfat label not truncated to 11 with -n: %q", line)
	}
}

func TestMkfsUnknownFilesystem(t *testing.T) {
	if _, err := mkfsCommand(Partition{Device: "/dev/sda1", Filesystem: "zfs"}); err == nil {
		t.Error("mkfsCommand for zfs = nil error, want failure")
	}
}

func TestRunCommands(t *testing.T) {
	var ran [][]string
	fake := func(args []string) error {
		ran = append(ran, args)
		return nil
	}
	cmds := [][]string{{"a", "b"}, {"c"}}
	if err := RunCommands(cmds, fake); err != nil {
		t.Fatalf("RunCommands: %v", err)
	}
	if len(ran) != 2 {
		t.Fatalf("ran %d commands, want 2", len(ran))
	}

	// A failing command stops the sequence.
	ran = nil
	stop := func(args []string) error {
		ran = append(ran, args)
		return fmt.Errorf("boom")
	}
	if err := RunCommands(cmds, stop); err == nil {
		t.Fatal("RunCommands with failing runner = nil error")
	}
	if len(ran) != 1 {
		t.Fatalf("ran %d commands after failure, want 1", len(ran))
	}
}
