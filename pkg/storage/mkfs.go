package storage

import "fmt"

// mkfsArgs are the filesystem-specific flags each mkfs gets, ported
// from clr-installer's table. A filesystem absent here is one
// slinstaller does not know how to create.
var mkfsArgs = map[string][]string{
	"ext2":  {"-v", "-F"},
	"ext3":  {"-v", "-F"},
	"ext4":  {"-v", "-F", "-b", "4096"},
	"btrfs": {"-f"},
	"xfs":   {"-f"},
	"f2fs":  {"-f"},
	"vfat":  {"-F32"},
}

// KnownFilesystem reports whether slinstaller can create this
// filesystem, so a config can be rejected before any disk is touched
// rather than at mkfs time.
func KnownFilesystem(fs string) bool {
	if fs == "swap" {
		return true
	}
	_, ok := mkfsArgs[fs]
	return ok
}

// mkfsCommand builds the command that creates this partition's
// filesystem. Swap is special — it is mkswap, not mkfs.swap — so it is
// handled apart; everything else is mkfs.<type> with its label and
// flags, the device last.
func mkfsCommand(p Partition) ([]string, error) {
	if p.Filesystem == "swap" {
		cmd := []string{"mkswap"}
		cmd = append(cmd, labelArgs(p)...)
		return append(cmd, p.EffectiveDevice()), nil
	}

	args, ok := mkfsArgs[p.Filesystem]
	if !ok {
		return nil, fmt.Errorf("storage: no mkfs for filesystem %q", p.Filesystem)
	}

	cmd := []string{"mkfs." + p.Filesystem}
	cmd = append(cmd, labelArgs(p)...)
	cmd = append(cmd, args...)
	return append(cmd, p.EffectiveDevice()), nil
}

// labelArgs returns the label flag and value for this filesystem, or
// nothing when there is no label. The flag differs by type, and a label
// too long for the type is truncated rather than rejected.
func labelArgs(p Partition) []string {
	if p.Label == "" {
		return nil
	}

	flag := "-L"
	switch p.Filesystem {
	case "vfat":
		flag = "-n"
	case "f2fs":
		flag = "-l"
	}

	label := p.Label
	if max := MaxLabelLength(p.Filesystem); len(label) > max {
		label = label[:max]
	}
	return []string{flag, label}
}
