package storage

import (
	"fmt"
	"math"
	"os/exec"
	"strconv"
	"strings"
)

// FreeRegion is a contiguous span of unallocated space on a disk, in MiB.
type FreeRegion struct {
	StartMiB, EndMiB, SizeMiB uint64
}

// existingPart is one partition already on the disk, as parted reports it.
type existingPart struct {
	Number           int
	StartMiB, EndMiB uint64
	FsType, Flags    string
}

// parsePartedFree parses the machine-readable `parted -m ... unit MiB print
// free` output into the existing partitions and the free regions. It is
// separate from the exec so the parsing is testable.
func parsePartedFree(out string) (parts []existingPart, free []FreeRegion) {
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(line), ";"))
		if line == "" {
			continue
		}
		f := strings.Split(line, ":")
		if len(f) < 5 {
			continue
		}
		// A free-space row carries "free" in the filesystem field.
		if f[4] == "free" {
			free = append(free, FreeRegion{
				StartMiB: parseMiB(f[1]),
				EndMiB:   parseMiB(f[2]),
				SizeMiB:  parseMiB(f[3]),
			})
			continue
		}
		// A partition row begins with its number; the disk and BYT header
		// rows do not, so they are skipped.
		n, err := strconv.Atoi(f[0])
		if err != nil {
			continue
		}
		p := existingPart{Number: n, StartMiB: parseMiB(f[1]), EndMiB: parseMiB(f[2]), FsType: f[4]}
		if len(f) >= 7 {
			p.Flags = f[6]
		}
		parts = append(parts, p)
	}
	return parts, free
}

// parseMiB parses parted's "512MiB" / "1.00MiB" into rounded MiB.
func parseMiB(s string) uint64 {
	s = strings.TrimSuffix(strings.TrimSpace(s), "MiB")
	v, err := strconv.ParseFloat(s, 64)
	if err != nil || v < 0 {
		return 0
	}
	return uint64(math.Round(v))
}

// partedPrintFree runs parted and returns its machine-readable table with
// free space. A variable so tests can stub it.
var partedPrintFree = func(disk string) (string, error) {
	out, err := exec.Command("parted", "-m", "-s", disk, "unit", "MiB", "print", "free").Output()
	if err != nil {
		return "", fmt.Errorf("parted print free %s: %w", disk, err)
	}
	return string(out), nil
}

// FreeRegions returns the disk's unallocated regions, largest first.
func FreeRegions(disk string) ([]FreeRegion, error) {
	out, err := partedPrintFree(disk)
	if err != nil {
		return nil, err
	}
	_, free := parsePartedFree(out)
	sortFreeBySizeDesc(free)
	return free, nil
}

// LargestFree returns the biggest free region of at least minMiB, or ok=false
// when the disk has none that large.
func LargestFree(disk string, minMiB uint64) (FreeRegion, bool, error) {
	regions, err := FreeRegions(disk)
	if err != nil {
		return FreeRegion{}, false, err
	}
	for _, r := range regions { // already largest-first
		if r.SizeMiB >= minMiB {
			return r, true, nil
		}
	}
	return FreeRegion{}, false, nil
}

// ExistingESP returns the device path of an EFI System Partition already on
// the disk (a FAT partition flagged esp or boot), for a Safe install to
// reuse. ok is false when there is none.
func ExistingESP(disk string) (device string, ok bool, err error) {
	out, perr := partedPrintFree(disk)
	if perr != nil {
		return "", false, perr
	}
	parts, _ := parsePartedFree(out)
	for _, p := range parts {
		fat := strings.HasPrefix(p.FsType, "fat")
		flagged := strings.Contains(p.Flags, "esp") || strings.Contains(p.Flags, "boot")
		if fat && flagged {
			return PartitionName(disk, p.Number), true, nil
		}
	}
	return "", false, nil
}

// NextPartitionNumber returns the first unused partition number on the disk,
// so a Safe install's new partition does not collide with an existing one.
func NextPartitionNumber(disk string) (int, error) {
	out, err := partedPrintFree(disk)
	if err != nil {
		return 0, err
	}
	parts, _ := parsePartedFree(out)
	used := map[int]bool{}
	for _, p := range parts {
		used[p.Number] = true
	}
	for n := 1; ; n++ {
		if !used[n] {
			return n, nil
		}
	}
}

func sortFreeBySizeDesc(fr []FreeRegion) {
	for i := 1; i < len(fr); i++ {
		for j := i; j > 0 && fr[j].SizeMiB > fr[j-1].SizeMiB; j-- {
			fr[j], fr[j-1] = fr[j-1], fr[j]
		}
	}
}
