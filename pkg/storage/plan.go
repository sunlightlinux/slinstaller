package storage

import (
	"fmt"
	"strings"
)

// Partition is one partition to create on the target disk.
//
// Number and Device are the on-disk position and the device path the
// kernel gives it; the rest describes the filesystem to make there. The
// caller — pkg/config — fills these in from the install config.
type Partition struct {
	Number int
	Device string // e.g. /dev/sda1

	// Disk is the whole disk this physical partition sits on (e.g.
	// /dev/sda). It matters for a multi-disk layout (RAID), where each
	// disk is partitioned separately. Empty means the plan's primary Disk.
	Disk string

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

	// RAID. IsRaidMember marks a physical partition that is a member of an
	// mdadm array: parted cuts it and flags it raid, and no filesystem is
	// made on it — the array carries that. RaidName is the array it joins.
	// Raid marks the array itself (not a physical partition): mdadm
	// assembles it from RaidMembers at RaidLevel, and its filesystem lives
	// at /dev/md/<RaidName>.
	IsRaidMember bool
	Raid         bool
	RaidName     string
	RaidLevel    string
	RaidMembers  []string
}

// EffectiveDevice is where the filesystem lives: the array device for a
// RAID array, a logical volume's path for an LV, the LUKS mapper for an
// encrypted partition, the raw partition otherwise.
func (p Partition) EffectiveDevice() string {
	switch {
	case p.Raid:
		return RaidDevice(p.RaidName)
	case p.LV:
		return "/dev/" + p.VGName + "/" + p.LVName
	case p.Encrypted:
		return "/dev/mapper/" + p.MapName
	default:
		return p.Device
	}
}

// RaidDevice is the device path for an mdadm array named name: /dev/md0
// for a plain "mdN" name, /dev/md/<name> otherwise (mdadm creates that
// symlink).
func RaidDevice(name string) string {
	if isMdName(name) {
		return "/dev/" + name
	}
	return "/dev/md/" + name
}

func isMdName(s string) bool {
	if !strings.HasPrefix(s, "md") || len(s) == 2 {
		return false
	}
	for _, r := range s[2:] {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// Plan is one or more disks laid out: the primary target (which holds the
// ESP), every disk to partition, whether they are being wiped, and the
// partitions and arrays to create, in on-disk order.
type Plan struct {
	Disk  string   // primary disk (holds the ESP / bootloader)
	Disks []string // every disk to partition (defaults to [Disk])
	Wipe  bool
	Parts []Partition
}

// DiskList is every disk the plan partitions: Disks when set, else the
// single primary Disk.
func (pl *Plan) DiskList() []string {
	if len(pl.Disks) > 0 {
		return pl.Disks
	}
	return []string{pl.Disk}
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
