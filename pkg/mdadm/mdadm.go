// Package mdadm assembles Linux software RAID arrays with mdadm.
//
// clr-installer builds RAID from its BlockDevice tree with mdadm;
// slinstaller builds it from the config's top-level raid list: each array
// names its level and the member partitions (marked `type: raid` on their
// disks) carry its name. The commands here run between partitioning (and
// any LUKS) and mkfs — the filesystem is then made and mounted on the
// array device (storage.Partition.EffectiveDevice), not the members.
package mdadm

import (
	"fmt"
	"strings"

	"github.com/sunlightlinux/slinstaller/pkg/storage"
)

// normalizeLevel turns "raid1"/"RAID1"/"1" into mdadm's "1" form.
func normalizeLevel(level string) string {
	return strings.TrimPrefix(strings.ToLower(strings.TrimSpace(level)), "raid")
}

// MinMembers is the smallest number of member devices a RAID level needs,
// or 0 for a level we do not support.
func MinMembers(level string) int {
	switch normalizeLevel(level) {
	case "0", "1":
		return 2
	case "5":
		return 3
	case "6", "10":
		return 4
	default:
		return 0
	}
}

// KnownLevel reports whether level is a RAID level mdadm (and slinstaller)
// supports: 0, 1, 5, 6 or 10 (with or without a "raid" prefix).
func KnownLevel(level string) bool { return MinMembers(level) > 0 }

// Create returns the mdadm command to build the array device at the given
// level from members. --run --force let it assemble non-interactively on a
// fresh set of partitions during an install; metadata 1.2 is the default
// for a data array (the ESP is kept off RAID, so 1.0 is not needed).
func Create(device, level string, members []string) []string {
	cmd := []string{
		"mdadm", "--create", device, "--run", "--force",
		"--metadata=1.2",
		"--level=" + normalizeLevel(level),
		fmt.Sprintf("--raid-devices=%d", len(members)),
	}
	return append(cmd, members...)
}

// Commands returns the array-creation commands for a plan, in the order
// the plan lists the arrays.
func Commands(pl *storage.Plan) [][]string {
	var cmds [][]string
	for _, p := range pl.Parts {
		if p.Raid {
			cmds = append(cmds, Create(p.EffectiveDevice(), p.RaidLevel, p.RaidMembers))
		}
	}
	return cmds
}

// Used reports whether the plan uses RAID at all.
func Used(pl *storage.Plan) bool {
	for _, p := range pl.Parts {
		if p.Raid {
			return true
		}
	}
	return false
}
