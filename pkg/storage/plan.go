package storage

import (
	"fmt"

	"github.com/sunlightlinux/slinstaller/pkg/descriptor"
)

// Partition is one partition to create on the target disk.
//
// Number and Device are what the descriptor does not carry: the on-disk
// position, in declaration order, and the device path the kernel will
// give it. Everything else is copied from the descriptor's partition.
type Partition struct {
	Number int
	Device string // e.g. /dev/sda1

	Mountpoint string
	Filesystem string
	Label      string

	// Size is a human size such as "512M", or descriptor.SizeRest for
	// the partition that takes whatever space is left.
	Size string
}

// Plan is a disk laid out: the target, whether it is being wiped, and
// the partitions to create on it, in on-disk order.
type Plan struct {
	Disk  string
	Wipe  bool
	Parts []Partition
}

// BuildPlan derives the on-disk layout from a descriptor.
//
// It assumes the descriptor has already passed Validate — this is not a
// second validator — but it rechecks the few things that would make the
// layout meaningless rather than merely invalid: a target disk must be
// named, there must be partitions, and the "rest" partition, if any,
// must be last, because a partition after it would have nowhere to go.
func BuildPlan(d *descriptor.Descriptor) (*Plan, error) {
	if d.TargetDisk == "" {
		return nil, fmt.Errorf("storage: no target disk")
	}
	if len(d.Partitions) == 0 {
		return nil, fmt.Errorf("storage: no partitions declared")
	}

	pl := &Plan{Disk: d.TargetDisk, Wipe: d.Wipe}
	for i, p := range d.Partitions {
		if p.Size == descriptor.SizeRest && i != len(d.Partitions)-1 {
			return nil, fmt.Errorf("storage: partition %d has size %q but is not the last; %q must be the final partition",
				i, descriptor.SizeRest, descriptor.SizeRest)
		}
		pl.Parts = append(pl.Parts, Partition{
			Number:     i + 1,
			Device:     PartitionName(d.TargetDisk, i+1),
			Mountpoint: p.Mountpoint,
			Filesystem: p.Filesystem,
			Label:      p.Label,
			Size:       p.Size,
		})
	}
	return pl, nil
}

// PartitionName returns the device path for partition n on disk:
// /dev/sda -> /dev/sda1, /dev/nvme0n1 -> /dev/nvme0n1p1.
//
// The "p" separator is used when the disk name ends in a digit, which
// is how the kernel names partitions on nvme, mmcblk and loop devices;
// a trailing digit on a plain name like /dev/sda would otherwise run
// into the partition number.
func PartitionName(disk string, n int) string {
	sep := ""
	if last := disk[len(disk)-1]; last >= '0' && last <= '9' {
		sep = "p"
	}
	return fmt.Sprintf("%s%s%d", disk, sep, n)
}

// GPT partition type GUIDs, ported from clr-installer. Only the types
// slinstaller assigns are listed; any other partition is left at
// parted's default, a generic Linux filesystem type.
var partTypeGUID = map[string]string{
	"/":     "4F68BCE3-E8CD-4DB1-96E7-FBCAF984B709",
	"/home": "933AC7E1-2EB4-4F13-B844-0E14E2AEF915",
	"/srv":  "3B8F8425-20E0-4F3B-907F-1A25A76F98E8",
	"swap":  "0657FD6D-A4AB-43C4-84E5-0933C84B4F4F",
	"efi":   "C12A7328-F81F-11D2-BA4B-00A0C93EC93B",
}

// isESP reports whether this partition is the EFI System Partition.
//
// clr-installer keyed the ESP off vfat mounted at /boot; Sunlight is
// UEFI-only and may mount it at /boot or /boot/efi, so both count.
func (p Partition) isESP() bool {
	return p.Filesystem == "vfat" && (p.Mountpoint == "/boot" || p.Mountpoint == "/boot/efi")
}

// guid returns the GPT type code for this partition, or "" to leave it
// at parted's default.
func (p Partition) guid() string {
	switch {
	case p.isESP():
		return partTypeGUID["efi"]
	case p.Filesystem == "swap":
		return partTypeGUID["swap"]
	default:
		return partTypeGUID[p.Mountpoint]
	}
}
