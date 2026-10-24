package storage

import (
	"fmt"
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

	for _, bad := range []string{"", "rest", "12X", "M"} {
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
			{Number: 3, Device: "/dev/sda3", Mountpoint: "/", Filesystem: "ext4", Label: "root", Size: restSize},
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
	missing := MissingTools([]string{"sh", "slinstaller-definitely-not-a-real-tool"})
	if len(missing) != 1 || missing[0] != "slinstaller-definitely-not-a-real-tool" {
		t.Errorf("MissingTools = %v, want the one bogus name", missing)
	}
}
