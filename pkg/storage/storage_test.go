package storage

import (
	"errors"
	"fmt"
	"math"
	"os/exec"
	"strings"
	"testing"
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

	for _, bad := range []string{"", "rest", "12X", "M", "-1G", "inf", "nan", "1e30", "16777216T", "18446744073709551616"} {
		if _, err := ParseSize(bad); err == nil {
			t.Errorf("ParseSize(%q) = nil error, want failure", bad)
		}
	}
}

// uefiPlan is a minimal UEFI layout: an ESP, swap, and a root that takes
// the rest. pkg/config builds such a plan from a config; the tests build
// it directly to exercise the storage layer on its own.
func uefiPlan() *Plan {
	return &Plan{
		Disk: "/dev/sda",
		Wipe: true,
		Parts: []Partition{
			{Number: 1, Device: "/dev/sda1", Mountpoint: "/boot", Filesystem: "vfat", Label: "EFI", Size: "512M"},
			{Number: 2, Device: "/dev/sda2", Filesystem: "swap", Size: "2G"},
			{Number: 3, Device: "/dev/sda3", Mountpoint: "/", Filesystem: "ext4", Label: "root", Size: SizeRest},
		},
	}
}

func TestCommandsDryRun(t *testing.T) {
	cmds, err := uefiPlan().Commands()
	if err != nil {
		t.Fatalf("Commands: %v", err)
	}
	got := FormatCommands(cmds)

	want := []string{
		"parted -s /dev/sda mklabel gpt",
		"parted -a optimal --script /dev/sda mkpart EFI fat32 0% 512MiB",
		"parted -a optimal --script /dev/sda mkpart linux-swap 512MiB 2560MiB",
		"parted -a optimal --script /dev/sda mkpart / 2560MiB 100%", // the "rest" partition
		"parted -s /dev/sda set 1 esp on",                           // ESP is partition 1
		"partprobe /dev/sda",
		"mkfs.vfat -n EFI -F32 /dev/sda1",
		"mkswap /dev/sda2", // swap carries no label here
		"mkfs.ext4 -L root -v -F -b 4096 /dev/sda3",
	}
	for _, line := range want {
		if !strings.Contains(got, line) {
			t.Errorf("dry run missing line:\n  %s\nfull output:\n%s", line, got)
		}
	}
}

func TestCommandsNoWipeSkipsMklabel(t *testing.T) {
	pl := uefiPlan()
	pl.Wipe = false
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

func TestRequiredTools(t *testing.T) {
	got := uefiPlan().RequiredTools()
	// Always-present tools plus one mkfs per distinct non-swap fs.
	want := map[string]bool{
		"parted": true, "partprobe": true, "mkswap": true, "blkid": true,
		"mkfs.vfat": true, "mkfs.ext4": true,
	}
	if len(got) != len(want) {
		t.Fatalf("RequiredTools = %v", got)
	}
	for _, g := range got {
		if !want[g] {
			t.Errorf("unexpected tool %q in %v", g, got)
		}
	}
}

func TestMissingTools(t *testing.T) {
	// "sh" exists on any system that runs these tests; the other does not.
	missing := MissingTools([]string{"sh", "slinstaller-definitely-not-a-real-tool", "slinstaller-definitely-not-a-real-tool"})
	if len(missing) != 1 || missing[0] != "slinstaller-definitely-not-a-real-tool" {
		t.Errorf("MissingTools = %v, want the one bogus name, once", missing)
	}
}

func TestSwapFileCommands(t *testing.T) {
	cmds := SwapFileCommands("/mnt", "2G", "ext4")
	want := []string{
		"fallocate -l 2G /mnt/swapfile",
		"chmod 600 /mnt/swapfile",
		"mkswap /mnt/swapfile",
	}
	if len(cmds) != len(want) {
		t.Fatalf("got %d commands, want %d", len(cmds), len(want))
	}
	for i, w := range want {
		if got := strings.Join(cmds[i], " "); got != w {
			t.Errorf("command %d = %q, want %q", i, got, w)
		}
	}
	// On btrfs the file is emptied and marked No_COW before it is
	// allocated, or swapon refuses it at boot.
	var got []string
	for _, c := range SwapFileCommands("/mnt", "2G", "btrfs") {
		got = append(got, strings.Join(c, " "))
	}
	if strings.Join(got[:3], "; ") != "truncate -s 0 /mnt/swapfile; chattr +C /mnt/swapfile; fallocate -l 2G /mnt/swapfile" {
		t.Errorf("btrfs swap file: %q", got)
	}
	if SwapFileFstab != "/swapfile none swap defaults 0 0\n" {
		t.Errorf("fstab line = %q", SwapFileFstab)
	}
}

func TestRaidDevice(t *testing.T) {
	cases := map[string]string{
		"root": "/dev/md/root",
		"data": "/dev/md/data",
		"md0":  "/dev/md0",
		"md12": "/dev/md12",
		"md":   "/dev/md/md", // not an mdN name
	}
	for name, want := range cases {
		if got := RaidDevice(name); got != want {
			t.Errorf("RaidDevice(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestEffectiveDeviceRaid(t *testing.T) {
	member := Partition{Device: "/dev/vda2", IsRaidMember: true, RaidName: "root"}
	if member.EffectiveDevice() != "/dev/vda2" {
		t.Errorf("a raid member's effective device is the raw partition, got %q", member.EffectiveDevice())
	}
	array := Partition{Raid: true, RaidName: "root"}
	if array.EffectiveDevice() != "/dev/md/root" {
		t.Errorf("the array's effective device = %q, want /dev/md/root", array.EffectiveDevice())
	}
}

// raidPlan mirrors demo/vm/config-raid1.yaml: an ESP on vda, a raid member
// on each of vda and vdb, and the root array over them.
func raidPlan() *Plan {
	return &Plan{
		Disk:  "/dev/vda",
		Disks: []string{"/dev/vda", "/dev/vdb"},
		Wipe:  true,
		Parts: []Partition{
			{Number: 1, Device: "/dev/vda1", Disk: "/dev/vda", Mountpoint: "/boot/efi", Filesystem: "vfat", Size: "512M"},
			{Number: 2, Device: "/dev/vda2", Disk: "/dev/vda", IsRaidMember: true, RaidName: "root", Size: SizeRest},
			{Number: 1, Device: "/dev/vdb1", Disk: "/dev/vdb", IsRaidMember: true, RaidName: "root", Size: SizeRest},
			{Raid: true, RaidName: "root", RaidLevel: "1", RaidMembers: []string{"/dev/vda2", "/dev/vdb1"},
				Mountpoint: "/", Filesystem: "ext4"},
		},
	}
}

func TestTableCommandsMultiDisk(t *testing.T) {
	got := FormatCommands(raidPlan().TableCommands())
	for _, line := range []string{
		"parted -s /dev/vda mklabel gpt",
		"parted -a optimal --script /dev/vda mkpart EFI fat32 0% 512MiB",
		"parted -a optimal --script /dev/vda mkpart raid 512MiB 100%",
		"parted -s /dev/vda set 1 esp on",
		"parted -s /dev/vda set 2 raid on",
		"partprobe /dev/vda",
		"parted -s /dev/vdb mklabel gpt",
		"parted -a optimal --script /dev/vdb mkpart raid 0% 100%",
		"parted -s /dev/vdb set 1 raid on",
		"partprobe /dev/vdb",
	} {
		if !strings.Contains(got, line) {
			t.Errorf("table commands missing:\n  %s\nfull:\n%s", line, got)
		}
	}
}

func TestMkfsSkipsRaidMembers(t *testing.T) {
	cmds, err := raidPlan().MkfsCommands()
	if err != nil {
		t.Fatalf("MkfsCommands: %v", err)
	}
	flat := FormatCommands(cmds)
	// The ESP and the array get a filesystem; the members do not.
	if !strings.Contains(flat, "mkfs.vfat") || !strings.Contains(flat, "/dev/md/root") {
		t.Errorf("expected mkfs for the ESP and the array:\n%s", flat)
	}
	if strings.Contains(flat, "/dev/vda2") || strings.Contains(flat, "/dev/vdb1") {
		t.Errorf("raid members should not be formatted:\n%s", flat)
	}
}

// The partition that fills a disk is held to its own minimum: the root's,
// the ESP's, or a partition's.
func TestFillMinByDisk(t *testing.T) {
	pl := &Plan{
		Disk:  "/dev/sda",
		Disks: []string{"/dev/sda", "/dev/sdb", "/dev/sdc", "/dev/sdd"},
		Parts: []Partition{
			{Device: "/dev/sda1", Disk: "/dev/sda", Size: "536870912", Filesystem: "vfat", Mountpoint: "/boot"},
			{Device: "/dev/sda2", Size: SizeRest, Filesystem: "ext4", Mountpoint: "/"}, // disk from pl.Disk
			{Device: "/dev/sdb1", Disk: "/dev/sdb", Size: SizeRest, Filesystem: "vfat", Mountpoint: "/boot/efi"},
			{Device: "/dev/sdc1", Disk: "/dev/sdc", Size: SizeRest, IsRaidMember: true},
			{Device: "/dev/sdd1", Disk: "/dev/sdd", Size: "1073741824", Filesystem: "ext4", Mountpoint: "/srv"},
			{LV: true, Size: SizeRest, Mountpoint: "/"}, // an LV fills its group, not a disk
			{Raid: true, Size: SizeRest},
		},
	}
	got := pl.FillMinByDisk(4e9)
	want := map[string]uint64{"/dev/sda": 4e9, "/dev/sdb": MinESPSize, "/dev/sdc": MinPartitionSize}
	if len(got) != len(want) {
		t.Errorf("FillMinByDisk = %v, want %v", got, want)
	}
	for d, w := range want {
		if got[d] != w {
			t.Errorf("%s fill minimum = %d, want %d", d, got[d], w)
		}
	}
}

func TestFixedBytesByDisk(t *testing.T) {
	pl := &Plan{
		Disk:  "/dev/sda",
		Disks: []string{"/dev/sda", "/dev/sdb"},
		Parts: []Partition{
			{Device: "/dev/sda1", Disk: "/dev/sda", Size: "536870912"},  // 512 MiB
			{Device: "/dev/sda2", Disk: "/dev/sda", Size: SizeRest},     // fill: ignored
			{Device: "/dev/sdb1", Disk: "/dev/sdb", Size: "1073741824"}, // 1 GiB
			{LV: true, Size: "999"},                                     // LV: ignored
			{Raid: true, Size: "999"},                                   // array: ignored
		},
	}
	got := pl.FixedBytesByDisk()
	if got["/dev/sda"] != 536870912 {
		t.Errorf("sda fixed = %d, want 536870912", got["/dev/sda"])
	}
	if got["/dev/sdb"] != 1073741824 {
		t.Errorf("sdb fixed = %d, want 1073741824", got["/dev/sdb"])
	}

	// An absurd size must not wrap to something small that fits a disk.
	pl = &Plan{Disk: "/dev/sda", Parts: []Partition{
		{Device: "/dev/sda1", Size: "16777216T"},                   // 2^64: overflows alone
		{Device: "/dev/sdb1", Disk: "/dev/sdb", Size: "16777215T"}, // fits alone...
		{Device: "/dev/sdb2", Disk: "/dev/sdb", Size: "16777215T"}, // ...not summed
	}}
	got = pl.FixedBytesByDisk()
	if got["/dev/sda"] != math.MaxUint64 || got["/dev/sdb"] != math.MaxUint64 {
		t.Errorf("overflowing sizes should saturate, got %v", got)
	}
}

func TestWithStderr(t *testing.T) {
	_, err := exec.Command("sh", "-c", "echo '  no such disk  ' >&2; exit 3").Output()
	if got := WithStderr(err); got == nil || !strings.HasSuffix(got.Error(), "exit status 3: no such disk") {
		t.Errorf("WithStderr = %v", got)
	}
	var ee *exec.ExitError
	if !errors.As(WithStderr(err), &ee) {
		t.Error("WithStderr should keep the *exec.ExitError reachable")
	}
	// Nothing on stderr, or not an exit error: unchanged.
	_, err = exec.Command("sh", "-c", "exit 3").Output()
	if got := WithStderr(err); got != err {
		t.Errorf("WithStderr with empty stderr = %v", got)
	}
	plain := errors.New("plain")
	if WithStderr(plain) != plain || WithStderr(nil) != nil {
		t.Error("WithStderr should pass other errors through")
	}
}

// After blkdiscard took the whole disk, mkfs does not discard it again.
func TestMkfsDoesNotDiscardADiscardedDisk(t *testing.T) {
	pl := &Plan{Wipe: true, Disk: "/dev/sda", DiskWipe: map[string]string{"/dev/sda": "discard"}, Parts: []Partition{
		{Number: 1, Device: "/dev/sda1", Filesystem: "vfat", Mountpoint: "/boot", Size: "512M"},
		{Number: 2, Device: "/dev/sda2", Filesystem: "ext4", Mountpoint: "/", Size: SizeRest},
		{Number: 3, Device: "/dev/sda3", Filesystem: "btrfs", Mountpoint: "/srv", Size: "1G"},
		{Number: 4, Device: "/dev/sda4", Filesystem: "ext4", Mountpoint: "/data", Options: []string{"-E", "lazy_itable_init=0"}},
	}}
	cmds, err := pl.MkfsCommands()
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, c := range cmds {
		got = append(got, strings.Join(c, " "))
	}
	want := []string{
		"mkfs.vfat -F32 /dev/sda1",
		"mkfs.ext4 -v -F -b 4096 -E nodiscard /dev/sda2",
		"mkfs.btrfs -f -K /dev/sda3",
		"mkfs.ext4 -v -F -b 4096 -E lazy_itable_init=0 /dev/sda4",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("mkfs:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	// Zeroing is not a trim: mkfs keeps its discard.
	pl.DiskWipe = map[string]string{"/dev/sda": "zero"}
	if cmds, _ = pl.MkfsCommands(); strings.Contains(strings.Join(cmds[1], " "), "nodiscard") {
		t.Errorf("nodiscard after wipe: zero: %v", cmds[1])
	}
	// Without the wipe, mkfs keeps its default.
	pl.DiskWipe = nil
	cmds, _ = pl.MkfsCommands()
	if strings.Contains(strings.Join(cmds[1], " "), "nodiscard") {
		t.Errorf("nodiscard without a discarded disk: %v", cmds[1])
	}
}
