package storage

import (
	"fmt"
	"strconv"
)

// ParseSize converts a Plan partition size such as "512M" or "8G" to
// bytes.
//
// It accepts an integer followed by an optional K, M, G or T, which are
// powers of 1024, or a bare byte count. The value "rest" is not a size
// and is rejected here; the caller handles it as "whatever remains on
// the disk".
func ParseSize(s string) (uint64, error) {
	if s == "" {
		return 0, fmt.Errorf("empty size")
	}

	mult := uint64(1)
	digits := s
	switch s[len(s)-1] {
	case 'K', 'k':
		mult = 1 << 10
	case 'M', 'm':
		mult = 1 << 20
	case 'G', 'g':
		mult = 1 << 30
	case 'T', 't':
		mult = 1 << 40
	}
	if mult != 1 {
		digits = s[:len(s)-1]
	}

	n, err := strconv.ParseUint(digits, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("size %q: not a number optionally followed by K, M, G or T", s)
	}
	return n * mult, nil
}

// Minimum partition sizes, ported from clr-installer, used by config
// validation to catch a layout that cannot work before any disk is
// touched. They are floors, not recommendations, and the caller can skip
// them (clr's --skip-validation-size).
const (
	// MinPartitionSize is the smallest a partition may be (clr's
	// MinimumPartitionSize): below this parted/mkfs is meaningless.
	MinPartitionSize = uint64(1) << 20 // 1 MiB
	// MinESPSize is the smallest EFI System Partition that reliably holds a
	// kernel and initramfs (clr's minBootSize: 100 MB for a few kernels).
	MinESPSize = uint64(100) * 1000 * 1000
	// MinRootSize is a conservative floor for the root filesystem. Sunlight's
	// base is far smaller than Clear's, so this is below clr's 4 GB server
	// minimum — enough for the base system, a kernel and headroom.
	MinRootSize = uint64(2) * 1000 * 1000 * 1000
)

// GPTReserveBytes is the space a GPT layout needs beyond the partitions
// themselves: the primary and backup partition tables plus the 1 MiB
// alignment gap parted leaves at the start. Used by the disk-fit check so a
// layout that is a hair under the raw disk size is not wrongly accepted.
const GPTReserveBytes = uint64(2) << 20 // 2 MiB

// MaxLabelLength is the longest filesystem label each type accepts,
// ported from clr-installer. A longer label is truncated rather than
// passed to mkfs, where it would fail.
func MaxLabelLength(fs string) int {
	switch fs {
	case "ext2", "ext3", "ext4":
		return 16
	case "swap":
		return 15
	case "xfs":
		return 12
	case "f2fs":
		return 512
	case "btrfs":
		return 255
	default: // vfat and anything else
		return 11
	}
}
