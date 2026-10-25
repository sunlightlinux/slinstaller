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

	// Size is a byte count, a human size such as "512M", or SizeRest for
	// the partition that takes whatever space is left.
	Size string

	// Encrypted marks a LUKS partition. Its filesystem is made and
	// mounted on the mapper device, not the raw partition; MapName is the
	// mapper name (/dev/mapper/<MapName>).
	Encrypted bool
	MapName   string

	// LVM. IsPV marks a physical partition used as an LVM physical
	// volume: it is cut by parted and carries no filesystem — its volume
	// group (VGName) and logical volumes live on top. LV marks a logical
	// volume (not a physical partition): it is created by lvcreate in
	// VGName and named LVName, and its filesystem lives at
	// /dev/<VGName>/<LVName>.
	IsPV   bool
	VGName string
	LV     bool
	LVName string
}

// EffectiveDevice is where the filesystem lives: a logical volume's path
// for an LV, the LUKS mapper for an encrypted partition, the raw
// partition otherwise.
func (p Partition) EffectiveDevice() string {
	switch {
	case p.LV:
		return "/dev/" + p.VGName + "/" + p.LVName
	case p.Encrypted:
		return "/dev/mapper/" + p.MapName
	default:
		return p.Device
	}
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

// isESP reports whether this partition is the EFI System Partition.
//
// clr-installer keyed the ESP off vfat mounted at /boot; Sunlight is
// UEFI-only and may mount it at /boot or /boot/efi, so both count.
func (p Partition) isESP() bool {
	return p.Filesystem == "vfat" && (p.Mountpoint == "/boot" || p.Mountpoint == "/boot/efi")
}

// ESPMountpoint returns where the EFI System Partition is mounted in the
// target (e.g. /boot), or "" if the plan has no ESP. The bootloader
// needs it to know which directory is the ESP.
func (pl *Plan) ESPMountpoint() string {
	for _, p := range pl.Parts {
		if p.isESP() {
			return p.Mountpoint
		}
	}
	return ""
}
