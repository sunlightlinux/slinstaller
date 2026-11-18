package storage

import (
	"errors"
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
		"resize2fs /dev/sda2 40960M",
		"sh -c echo ',40960MiB' | sfdisk --no-reread -N 2 /dev/sda",
		"partprobe /dev/sda",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("resize commands:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
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
