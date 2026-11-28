package storage

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"testing"
)

func resizePlan() *Plan {
	return &Plan{Disk: "/dev/sda", Parts: []Partition{
		{Number: 1, Device: "/dev/sda1", Disk: "/dev/sda", Filesystem: "vfat", Mountpoint: "/boot/efi", Preserve: true},
		{Number: 2, Device: "/dev/sda2", Disk: "/dev/sda", Filesystem: "ext4", Preserve: true, ResizeMiB: 40960},
		{Number: 3, Device: "/dev/sda3", Disk: "/dev/sda", Filesystem: "ext4", Mountpoint: "/", Create: true, CreateInFree: true},
	}}
}

// The filesystem is shrunk before the partition around it, never after: a
// partition smaller than its filesystem is lost data.
func TestResizeShrinksTheFilesystemFirst(t *testing.T) {
	var got []string
	for _, c := range resizePlan().ResizeCommands() {
		got = append(got, strings.Join(c, " "))
	}
	want := []string{
		"e2fsck -f -p /dev/sda2",
		"setsid -w resize2fs /dev/sda2 40960M",
		"setsid -w sh -c echo ',40960MiB' | sfdisk --no-reread -N 2 /dev/sda",
		"partprobe /dev/sda",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("resize commands:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	// ntfs: a trial that changes nothing, then the real run, then the
	// partition — the same order, with its own tool.
	nt := resizePlan()
	nt.Parts[1].Filesystem = "ntfs"
	var ntfs []string
	for _, c := range nt.ResizeCommands() {
		ntfs = append(ntfs, strings.Join(c, " "))
	}
	wantNTFS := []string{
		"ntfsresize --no-action --no-progress-bar -s 42949672960 /dev/sda2",
		"setsid -w sh -c echo y | ntfsresize --no-progress-bar -f -s 42949672960 /dev/sda2",
		"setsid -w sh -c echo ',40960MiB' | sfdisk --no-reread -N 2 /dev/sda",
		"partprobe /dev/sda",
	}
	if strings.Join(ntfs, "\n") != strings.Join(wantNTFS, "\n") {
		t.Errorf("ntfs resize commands:\n%s", strings.Join(ntfs, "\n"))
	}
	for fs, want := range map[string]bool{"ext2": true, "ext3": true, "ext4": true, "ntfs": true, "btrfs": true, "xfs": false, "f2fs": false, "": false} {
		if Resizable(fs) != want {
			t.Errorf("Resizable(%q) = %v", fs, !want)
		}
	}
	// Only the partition that asks is touched, and a plan with no resize
	// has no such commands.
	for _, c := range got {
		if strings.Contains(c, "sda1") || strings.Contains(c, "sda3") {
			t.Errorf("another partition is in the resize: %s", c)
		}
	}
	if cmds := (&Plan{Disk: "/dev/sda", Parts: []Partition{{Device: "/dev/sda1"}}}).ResizeCommands(); len(cmds) != 0 {
		t.Errorf("resize commands with nothing to resize: %v", cmds)
	}
	// The partition's own disk is used when it names none.
	pl := resizePlan()
	pl.Parts[1].Disk = ""
	if cmds := pl.ResizeCommands(); !strings.HasSuffix(strings.Join(cmds[2], " "), "-N 2 /dev/sda") {
		t.Errorf("sfdisk target: %v", cmds[2])
	}
	// The install needs the tools for it, and the dry run shows it ahead of
	// the new partition, whose place is not known until then.
	tools := " " + strings.Join(resizePlan().RequiredTools(), " ") + " "
	for _, tool := range []string{"e2fsck", "resize2fs", "sh"} {
		if !strings.Contains(tools, " "+tool+" ") {
			t.Errorf("required tools lack %s: %s", tool, tools)
		}
	}
	cmds, err := resizePlan().Commands()
	if err != nil {
		t.Fatal(err)
	}
	var all []string
	for _, c := range cmds {
		all = append(all, strings.Join(c, " "))
	}
	flat := strings.Join(all, "\n")
	if strings.Index(flat, "resize2fs") > strings.Index(flat, "mkpart") || !strings.Contains(flat, "<start of the largest free region> <its end>") {
		t.Errorf("dry run:\n%s", flat)
	}
}

func TestPlaceInFreeUsesTheLargestRegion(t *testing.T) {
	saved := partedPrintFree
	defer func() { partedPrintFree = saved }()
	table := "BYT;\n1:1MiB:513MiB:512MiB:fat32::boot, esp;\n2:513MiB:41473MiB:40960MiB:ext4::;\n" +
		"1:41473MiB:102400MiB:60927MiB:free;\n1:0.02MiB:1.00MiB:0.98MiB:free;"
	var asked string
	partedPrintFree = func(disk string) (string, error) { asked = disk; return table, nil }

	// With no size it takes the region, less a mebibyte at the end.
	pl := resizePlan()
	if err := pl.PlaceInFree(); err != nil {
		t.Fatal(err)
	}
	p := pl.Parts[2]
	if p.StartMiB != 41473 || p.EndMiB != 41473+60926 || p.CreateInFree || asked != "/dev/sda" {
		t.Errorf("placed at %d..%d (in free %v), asked %s", p.StartMiB, p.EndMiB, p.CreateInFree, asked)
	}
	cmds := pl.TableCommands()
	if got := strings.Join(cmds[0], " "); !strings.Contains(got, "41473MiB 102399MiB") {
		t.Errorf("mkpart = %s", got)
	}
	// With a size, that much of it.
	pl = resizePlan()
	pl.Parts[2].EndMiB, pl.Parts[2].Disk = 20480, ""
	if err := pl.PlaceInFree(); err != nil || pl.Parts[2].StartMiB != 41473 || pl.Parts[2].EndMiB != 41473+20480 {
		t.Errorf("sized: %v, %d..%d", err, pl.Parts[2].StartMiB, pl.Parts[2].EndMiB)
	}
	// More than there is, no room at all, and a disk that cannot be read.
	pl = resizePlan()
	pl.Parts[2].EndMiB = 70000
	if err := pl.PlaceInFree(); err == nil || !strings.Contains(err.Error(), "needs 70000 MiB but the largest free region on /dev/sda is 60926 MiB") {
		t.Errorf("too large: %v", err)
	}
	partedPrintFree = func(string) (string, error) { return "BYT;\n1:1MiB:513MiB:512MiB:fat32::boot, esp;", nil }
	if err := resizePlan().PlaceInFree(); err == nil || !strings.Contains(err.Error(), "has no unallocated space for /dev/sda3") {
		t.Errorf("no room: %v", err)
	}
	boom := errors.New("parted: busy")
	partedPrintFree = func(string) (string, error) { return "", boom }
	if err := resizePlan().PlaceInFree(); !errors.Is(err, boom) {
		t.Errorf("unreadable disk: %v", err)
	}
	// Nothing to place: nothing is read.
	partedPrintFree = func(string) (string, error) { t.Error("the disk was read for nothing"); return "", nil }
	if err := (&Plan{Parts: []Partition{{Device: "/dev/sda1"}}}).PlaceInFree(); err != nil {
		t.Error(err)
	}
}

// A kept partition needs no mkfs: asking for one would refuse an install
// beside a system whose filesystem the live medium cannot make.
func TestRequiredToolsSkipKeptPartitions(t *testing.T) {
	pl := resizePlan()
	pl.Parts[1].Filesystem = "ntfs"
	tools := " " + strings.Join(pl.RequiredTools(), " ") + " "
	if strings.Contains(tools, "mkfs.ntfs") || strings.Contains(tools, "mkfs.vfat") {
		t.Errorf("mkfs asked for a kept partition: %s", tools)
	}
	if !strings.Contains(tools, " mkfs.ext4 ") || !strings.Contains(tools, " ntfsresize ") {
		t.Errorf("tools for what is made and shrunk are missing: %s", tools)
	}
}

// btrfs is shrunk while mounted: the plan shows it by its device, and
// carrying it out mounts the filesystem for the purpose.
func TestResizeBtrfs(t *testing.T) {
	pl := resizePlan()
	pl.Parts[1].Filesystem = "btrfs"
	var shown []string
	for _, c := range pl.ResizeCommands() {
		shown = append(shown, strings.Join(c, " "))
	}
	want := "setsid -w btrfs filesystem resize 40960M [/dev/sda2]|setsid -w sh -c echo ',40960MiB' | sfdisk --no-reread -N 2 /dev/sda|partprobe /dev/sda"
	if strings.Join(shown, "|") != want {
		t.Errorf("btrfs resize, as shown:\n%s", strings.Join(shown, "\n"))
	}
	// A filesystem that cannot be mounted is not shrunk, and so its
	// partition is not cut down either.
	var ran []string
	err := pl.Resize(func(args []string) error { ran = append(ran, command(args)[0]); return nil })
	if err == nil || len(ran) != 0 {
		t.Errorf("Resize on an unmountable btrfs: %v, ran %v", err, ran)
	}
	// The others are run as shown, stopping at the first failure.
	pl.Parts[1].Filesystem = "ext4"
	ran = nil
	if err := pl.Resize(func(args []string) error { ran = append(ran, command(args)[0]); return nil }); err != nil || strings.Join(ran, ",") != "e2fsck,resize2fs,sh,partprobe" {
		t.Errorf("Resize ext4: %v, ran %v", err, ran)
	}
	ran = nil
	boom := errors.New("resize2fs: no space")
	err = pl.Resize(func(args []string) error {
		ran = append(ran, command(args)[0])
		if command(args)[0] == "resize2fs" {
			return boom
		}
		return nil
	})
	if !errors.Is(err, boom) || strings.Join(ran, ",") != "e2fsck,resize2fs" {
		t.Errorf("failed Resize: %v, ran %v", err, ran)
	}
}

// e2fsck exits 1 or 2 after correcting errors; that is not a failure of the
// check before a shrink. 4 and above are, and stop before resize2fs.
func TestResizeE2fsckCorrected(t *testing.T) {
	pl := &Plan{Disk: "/dev/sda", Parts: []Partition{
		{Device: "/dev/sda2", Number: 2, Filesystem: "ext4", ResizeMiB: 1024},
	}}
	for code, wantOK := range map[int]bool{1: true, 2: true, 4: false, 8: false} {
		var ran []string
		err := pl.Resize(func(args []string) error {
			ran = append(ran, command(args)[0])
			if args[0] == "e2fsck" {
				return exec.Command("sh", "-c", fmt.Sprintf("exit %d", code)).Run()
			}
			return nil
		})
		if wantOK && (err != nil || strings.Join(ran, ",") != "e2fsck,resize2fs,sh,partprobe") {
			t.Errorf("e2fsck exit %d: %v, ran %v", code, err, ran)
		}
		if !wantOK && (err == nil || strings.Join(ran, ",") != "e2fsck") {
			t.Errorf("e2fsck exit %d should stop the resize: %v, ran %v", code, err, ran)
		}
	}
}

// A free region that does not begin on a mebibyte — after a partition that
// starts at sector 63, as a disk partitioned by an old Windows has — is
// read in bytes and its start rounded up: parted's MiB print showed it as
// "100MiB" (it is 100.03), the new partition was asked for at 100MiB, and
// parted refused it as overlapping — after the shrink was already done.
func TestPlaceInFreeStartsInsideAnUnalignedRegion(t *testing.T) {
	saved := partedPrintFree
	defer func() { partedPrintFree = saved }()
	// parted -m unit B print free of an msdos image, partition 1 at 63s.
	partedPrintFree = func(string) (string, error) {
		return "BYT;\n/dev/sda:1073741824B:scsi:512:512:gpt:DISK:;\n" +
			"1:32256B:104889855B:104857600B:ntfs::;\n" +
			"1:104889856B:1073741823B:968851968B:free;\n", nil
	}
	pl := resizePlan()
	if err := pl.PlaceInFree(); err != nil {
		t.Fatal(err)
	}
	p := pl.Parts[2]
	// The region is 100.03MiB..1024MiB: whole mebibytes inside it are
	// 101..1024, less the slack.
	if p.StartMiB != 101 || p.EndMiB > 1023 || p.EndMiB <= p.StartMiB {
		t.Errorf("placed at %d..%d MiB; the free region starts at 100.03MiB", p.StartMiB, p.EndMiB)
	}
	// And a region ending short of a mebibyte (the next partition is
	// unaligned) ends at the whole mebibyte below.
	_, free := parsePartedFree("BYT;\n1:104889856B:524603391B:419713536B:free;\n")
	if len(free) != 1 || free[0].StartMiB != 101 || free[0].EndMiB != 500 || free[0].SizeMiB != 399 {
		t.Errorf("free = %+v, want 101..500 (399 MiB)", free)
	}
}

// The partition is cut down to the shrunk filesystem by sfdisk, run under
// sh: the tool check saw only "sh", and a live system without sfdisk had
// its filesystem shrunk before the install stopped at the partition.
func TestRequiredToolsIncludeTheResizeSfdisk(t *testing.T) {
	for _, fs := range []string{"ext4", "ntfs", "btrfs"} {
		pl := resizePlan()
		pl.Parts[1].Filesystem = fs
		tools := " " + strings.Join(pl.RequiredTools(), " ") + " "
		if !strings.Contains(tools, " sfdisk ") {
			t.Errorf("%s: sfdisk missing from %s", fs, tools)
		}
	}
	if tools := " " + strings.Join((&Plan{Disk: "/dev/sda", Parts: []Partition{{Number: 1, Device: "/dev/sda1", Filesystem: "ext4", Preserve: true}}}).RequiredTools(), " ") + " "; strings.Contains(tools, " sfdisk ") {
		t.Errorf("sfdisk asked for with nothing to resize: %s", tools)
	}
}
