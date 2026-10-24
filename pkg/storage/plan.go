package storage

import "fmt"

// Partition is one partition to create on the target disk.
//
// Number and Device are the on-disk position and the device path the
// kernel gives it; the rest describes the filesystem to make there. The
// caller — pkg/config — fills these in from the install config.
type Partition struct {
	Number int
	Device string // e.g. /dev/sda1

	Mountpoint string
	Filesystem string
	Label      string

	// Size is a byte count, a human size such as "512M", or restSize for
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
