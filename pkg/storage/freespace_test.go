package storage

import (
	"strings"
	"testing"
)

const partedSample = `BYT;
/dev/sda:45000MiB:scsi:512:512:gpt:VBOX HARDDISK:;
1:1.00MiB:513MiB:512MiB:fat32::boot, esp;
2:513MiB:20000MiB:19487MiB:ext4::;
1:20000MiB:45000MiB:25000MiB:free;
`

func TestParsePartedFree(t *testing.T) {
	parts, free := parsePartedFree(partedSample)
	if len(parts) != 2 {
		t.Fatalf("got %d partitions, want 2", len(parts))
	}
	if parts[0].Number != 1 || !strings.Contains(parts[0].Flags, "esp") || !strings.HasPrefix(parts[0].FsType, "fat") {
		t.Errorf("part 1 wrong: %+v", parts[0])
	}
	if len(free) != 1 || free[0].StartMiB != 20000 || free[0].EndMiB != 45000 || free[0].SizeMiB != 25000 {
		t.Fatalf("free region wrong: %+v", free)
	}
}

func TestFreeSpaceHelpers(t *testing.T) {
	saved := partedPrintFree
	defer func() { partedPrintFree = saved }()
	partedPrintFree = func(string) (string, error) { return partedSample, nil }

	esp, ok, _ := ExistingESP("/dev/sda")
	if !ok || esp != "/dev/sda1" {
		t.Errorf("ExistingESP = %q ok=%v, want /dev/sda1", esp, ok)
	}
	r, ok, _ := LargestFree("/dev/sda", 10000)
	if !ok || r.SizeMiB != 25000 {
		t.Errorf("LargestFree = %+v ok=%v", r, ok)
	}
	if _, ok, _ := LargestFree("/dev/sda", 30000); ok {
		t.Errorf("LargestFree should reject a too-large request")
	}
	if n, _ := NextPartitionNumber("/dev/sda"); n != 3 {
		t.Errorf("NextPartitionNumber = %d, want 3", n)
	}
}

func TestAlongsideTableCommands(t *testing.T) {
	// A non-wiping plan with a Create partition cuts just that one.
	pl := &Plan{
		Disk:  "/dev/sda",
		Disks: []string{"/dev/sda"},
		Wipe:  false,
		Parts: []Partition{
			{Number: 1, Device: "/dev/sda1", Disk: "/dev/sda", Preserve: true, Filesystem: "vfat", Mountpoint: "/boot/efi"},
			{Number: 3, Device: "/dev/sda3", Disk: "/dev/sda", Create: true, StartMiB: 20000, EndMiB: 45000, Filesystem: "ext4", Mountpoint: "/"},
		},
	}
	tc := pl.TableCommands()
	flat := ""
	for _, c := range tc {
		flat += strings.Join(c, " ") + "\n"
	}
	if !strings.Contains(flat, "mkpart") || !strings.Contains(flat, "20000MiB 45000MiB") {
		t.Errorf("alongside did not cut the new partition:\n%s", flat)
	}
	if strings.Contains(flat, "mklabel") {
		t.Errorf("alongside must not mklabel:\n%s", flat)
	}
	// mkfs runs for the new partition, not the preserved ESP.
	mkfs, _ := pl.MkfsCommands()
	mf := ""
	for _, c := range mkfs {
		mf += strings.Join(c, " ") + "\n"
	}
	if !strings.Contains(mf, "/dev/sda3") || strings.Contains(mf, "/dev/sda1") {
		t.Errorf("mkfs wrong for alongside:\n%s", mf)
	}
}
