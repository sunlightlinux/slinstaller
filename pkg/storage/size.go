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
