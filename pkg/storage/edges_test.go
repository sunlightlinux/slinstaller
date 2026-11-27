package storage

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func flatCmds(cmds [][]string) string {
	var b strings.Builder
	for _, c := range cmds {
		b.WriteString(strings.Join(c, " ") + "\n")
	}
	return b.String()
}

func TestPartedPrintFreeRunsParted(t *testing.T) {
	dir := fakeTool(t, "parted", `echo "BYT;"; echo "1:1.00MiB:513MiB:512MiB:fat32::boot, esp;"`)
	out, err := partedPrintFree("/dev/sdz")
	if err != nil || !strings.Contains(out, "fat32") {
		t.Fatalf("partedPrintFree = %q, %v", out, err)
	}
	// Machine-readable, non-interactive, in MiB, with the free regions.
	args, _ := os.ReadFile(filepath.Join(dir, "args"))
	if got := strings.TrimSpace(string(args)); got != "-m -s /dev/sdz unit MiB print free" {
		t.Errorf("parted called with %q", got)
	}

	fakeTool(t, "parted", "exit 1")
	if _, err := partedPrintFree("/dev/sdz"); err == nil || !strings.Contains(err.Error(), "/dev/sdz") {
		t.Errorf("parted failure: %v", err)
	}
}

func TestFreeSpaceHelpersPassOnAPartedFailure(t *testing.T) {
	saved := partedPrintFree
	defer func() { partedPrintFree = saved }()
	boom := errors.New("parted: device busy")
	partedPrintFree = func(string) (string, error) { return "", boom }

	// A Safe install must not guess a layout when the disk cannot be read.
	if _, err := FreeRegions("/dev/sda"); !errors.Is(err, boom) {
		t.Errorf("FreeRegions: %v", err)
	}
	if _, ok, err := LargestFree("/dev/sda", 1); ok || !errors.Is(err, boom) {
		t.Errorf("LargestFree: %v, %v", ok, err)
	}
	if dev, ok, err := ExistingESP("/dev/sda"); dev != "" || ok || !errors.Is(err, boom) {
		t.Errorf("ExistingESP: %q, %v, %v", dev, ok, err)
	}
	if n, err := NextPartitionNumber("/dev/sda"); n != 0 || !errors.Is(err, boom) {
		t.Errorf("NextPartitionNumber: %d, %v", n, err)
	}
}

func TestFreeRegionsAreLargestFirstAndESPNeedsBothFatAndFlag(t *testing.T) {
	saved := partedPrintFree
	defer func() { partedPrintFree = saved }()
	partedPrintFree = func(string) (string, error) {
		return `BYT;
/dev/sda:100000MiB:scsi:512:512:gpt:DISK:;
1:1.00MiB:300MiB:299MiB:free;
1:300MiB:800MiB:500MiB:fat32::msftdata;
2:800MiB:1300MiB:500MiB:ext4::boot;
1:1300MiB:50000MiB:48700MiB:free;
1:50000MiB:60000MiB:10000MiB:free;
4:60000MiB:100000MiB:40000MiB:ntfs::;
`, nil
	}
	regions, err := FreeRegions("/dev/sda")
	if err != nil || len(regions) != 3 {
		t.Fatalf("regions = %v, %v", regions, err)
	}
	if regions[0].SizeMiB != 48700 || regions[1].SizeMiB != 10000 || regions[2].SizeMiB != 299 {
		t.Errorf("not largest first: %+v", regions)
	}
	// No region is large enough: said so, not the nearest handed back.
	if _, ok, err := LargestFree("/dev/sda", 60000); ok || err != nil {
		t.Errorf("LargestFree(60000) = %v, %v", ok, err)
	}
	// A FAT partition without the flag, and a flagged one that is not FAT:
	// neither is an ESP to reuse.
	if dev, ok, _ := ExistingESP("/dev/sda"); ok {
		t.Errorf("found an ESP that is not one: %s", dev)
	}
	// 1, 2 and 4 are taken: the next free number is 3, not 5.
	if n, _ := NextPartitionNumber("/dev/sda"); n != 3 {
		t.Errorf("next partition number = %d, want 3", n)
	}
	// parted prints fractions; negative or junk reads as 0.
	for in, want := range map[string]uint64{"1.00MiB": 1, "512.6MiB": 513, " 20MiB ": 20, "-5MiB": 0, "x": 0, "": 0} {
		if got := parseMiB(in); got != want {
			t.Errorf("parseMiB(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestWriteFstabReportsFailure(t *testing.T) {
	// /etc cannot be made.
	file := filepath.Join(t.TempDir(), "file")
	os.WriteFile(file, nil, 0o644)
	if err := WriteFstab(file, "x\n"); err == nil || !strings.Contains(err.Error(), "mkdir") {
		t.Errorf("WriteFstab under a file: %v", err)
	}
	// fstab cannot be written.
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "etc", "fstab", "occupied"), 0o755)
	if err := WriteFstab(root, "x\n"); err == nil || !strings.Contains(err.Error(), "write fstab") {
		t.Errorf("WriteFstab onto a directory: %v", err)
	}
}

func TestMkfsCommandEdges(t *testing.T) {
	// Each filesystem takes its label with its own flag, truncated to what
	// the filesystem accepts.
	cmds, err := (&Plan{Parts: []Partition{
		{Device: "/dev/sda1", Filesystem: "f2fs", Label: "data"},
		{Device: "/dev/sda2", Filesystem: "vfat", Label: "ABCDEFGHIJKLMNOP"},
		{Device: "/dev/sda3", Filesystem: "ext4", Label: "root"},
	}}).MkfsCommands()
	if err != nil {
		t.Fatal(err)
	}
	flat := flatCmds(cmds)
	for _, want := range []string{"mkfs.f2fs -l data", "mkfs.vfat -n ABCDEFGHIJK ", "mkfs.ext4 -L root"} {
		if !strings.Contains(flat, want) {
			t.Errorf("missing %q in:\n%s", want, flat)
		}
	}
	// A filesystem slinstaller cannot make stops the plan before any command
	// is produced — including the partition table that would wipe the disk.
	bad := &Plan{Disk: "/dev/sda", Wipe: true, Parts: []Partition{{Device: "/dev/sda1", Number: 1, Filesystem: "ntfs", Mountpoint: "/"}}}
	if _, err := bad.MkfsCommands(); err == nil {
		t.Error("ntfs accepted")
	}
	if cmds, err := bad.Commands(); err == nil || cmds != nil {
		t.Errorf("Commands with an unknown filesystem = %v, %v", cmds, err)
	}
}

func TestTableCommandEdges(t *testing.T) {
	// A partition smaller than 1 MiB still gets a size parted accepts, and
	// one without a mountpoint a name.
	wipe := &Plan{Disk: "/dev/sda", Wipe: true, Parts: []Partition{
		{Device: "/dev/sda1", Number: 1, Filesystem: "ext4", Size: "4K"},
		{Device: "/dev/sda2", Number: 2, Filesystem: "ext4", Mountpoint: "/"},
	}}
	flat := flatCmds(wipe.TableCommands())
	if !strings.Contains(flat, "mkpart primary 0% 1MiB") {
		t.Errorf("tiny unnamed partition:\n%s", flat)
	}
	if !strings.Contains(flat, "mkpart / 1MiB 100%") {
		t.Errorf("the next partition should start where the tiny one ends:\n%s", flat)
	}

	// An alongside install that also has to create the ESP flags it, and
	// touches nothing else: no mklabel.
	along := &Plan{Disk: "/dev/sda", Wipe: false, Parts: []Partition{
		{Device: "/dev/sda1", Number: 1, Filesystem: "ntfs", Preserve: true},
		{Device: "/dev/sda2", Number: 2, Filesystem: "vfat", Mountpoint: "/boot/efi", Create: true, StartMiB: 1000, EndMiB: 1512},
	}}
	flat = flatCmds(along.TableCommands())
	if !strings.Contains(flat, "mkpart EFI fat32 1000MiB 1512MiB") || !strings.Contains(flat, "set 2 esp on") {
		t.Errorf("created ESP not cut or flagged:\n%s", flat)
	}
	if strings.Contains(flat, "mklabel") {
		t.Errorf("an alongside install relabelled the disk:\n%s", flat)
	}
}

func TestPlanSizeAccounting(t *testing.T) {
	pl := &Plan{Disk: "/dev/sda", Parts: []Partition{
		{Size: "512M"},                 // on the primary disk by default
		{Size: "1G", Disk: "/dev/sdb"}, //
		{Size: "not a size"},           // ignored rather than counted as 0 or fatal
		{Size: SizeRest},               // takes what is left
		{Size: "100G", LV: true},       // not cut by parted
		{Size: "100G", Raid: true},     //
	}}
	got := pl.FixedBytesByDisk()
	if got["/dev/sda"] != 512<<20 || got["/dev/sdb"] != 1<<30 || len(got) != 2 {
		t.Errorf("FixedBytesByDisk = %v", got)
	}
	// mdN is a kernel array name; anything else is a named array.
	for name, want := range map[string]string{"md0": "/dev/md0", "md127": "/dev/md127", "md": "/dev/md/md", "mdx1": "/dev/md/mdx1", "data": "/dev/md/data"} {
		if got := RaidDevice(name); got != want {
			t.Errorf("RaidDevice(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestRunnersRunAndRefuseEmptyCommands(t *testing.T) {
	if err := VerboseRunner([]string{"true"}); err != nil {
		t.Errorf("VerboseRunner(true) = %v", err)
	}
	if err := VerboseRunner([]string{"false"}); err == nil {
		t.Error("VerboseRunner(false) succeeded")
	}
	var sink strings.Builder
	if err := StreamRunner(&sink)(nil); err == nil {
		t.Error("StreamRunner ran an empty command")
	}
}

// Alongside another system, only the partitions this install cuts have
// their GPT entry set: an existing one's type, GUID and name are another
// system's, and stay.
func TestEntryCommandsOnlyCreatedAlongside(t *testing.T) {
	pl := &Plan{Disk: "/dev/sda", Parts: []Partition{
		{Number: 1, Device: "/dev/sda1", Preserve: true, PartName: "Windows", PartUUID: "11111111-1111-1111-1111-111111111111"},
		{Number: 3, Device: "/dev/sda3", Create: true, StartMiB: 100, EndMiB: 200, Filesystem: "ext4", Mountpoint: "/",
			PartName: "root", PartUUID: "33333333-3333-3333-3333-333333333333"},
	}}
	got := flatCmds(pl.TableCommands())
	for _, want := range []string{
		"sfdisk --part-uuid /dev/sda 3 33333333-3333-3333-3333-333333333333\n",
		"sfdisk --part-label /dev/sda 3 root\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "Windows") || strings.Contains(got, "11111111") || strings.Contains(got, " 1 ") {
		t.Errorf("the existing partition 1 touched:\n%s", got)
	}
}

// Each mkfs is given the configured UUID in its own spelling: mkfs.vfat a
// volume id without the dash, mkfs.xfs through -m, the rest -U; none when
// nothing was asked.
func TestMkfsUUIDSpelling(t *testing.T) {
	for _, c := range []struct{ fs, uuid, want string }{
		{"vfat", "1A2B-3C4D", "-i 1A2B3C4D"},
		{"xfs", "0b1a6f5e-1111-2222-3333-444455556666", "-m uuid=0b1a6f5e-1111-2222-3333-444455556666"},
		{"ext4", "0b1a6f5e-1111-2222-3333-444455556666", "-U 0b1a6f5e-1111-2222-3333-444455556666"},
		{"swap", "0b1a6f5e-1111-2222-3333-444455556666", "-U 0b1a6f5e-1111-2222-3333-444455556666"},
	} {
		cmd, err := mkfsCommand(Partition{Device: "/dev/sda1", Filesystem: c.fs, FsUUID: c.uuid})
		if err != nil {
			t.Fatal(err)
		}
		if got := strings.Join(cmd, " "); !strings.Contains(got, " "+c.want+" ") {
			t.Errorf("%s: %s, want %s in it", c.fs, got, c.want)
		}
	}
	if cmd, _ := mkfsCommand(Partition{Device: "/dev/sda1", Filesystem: "ext4"}); strings.Contains(strings.Join(cmd, " "), "-U") {
		t.Errorf("a UUID given unasked: %q", cmd)
	}
}

// SpareESPs lists the spare EFI System Partitions, and only them.
func TestSpareESPs(t *testing.T) {
	pl := &Plan{Parts: []Partition{
		{Device: "/dev/sda1", Filesystem: "vfat", Mountpoint: "/boot/efi"},
		{Device: "/dev/sdb1", Filesystem: "vfat", SpareESP: true},
		{Device: "/dev/sda2", Filesystem: "ext4", Mountpoint: "/"},
		{Device: "/dev/sdc1", Filesystem: "vfat", SpareESP: true},
	}}
	got := pl.SpareESPs()
	if len(got) != 2 || got[0].Device != "/dev/sdb1" || got[1].Device != "/dev/sdc1" {
		t.Errorf("SpareESPs = %+v", got)
	}
}
