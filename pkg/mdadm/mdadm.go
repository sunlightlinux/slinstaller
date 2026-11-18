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
	"os/exec"
	"strings"

	"github.com/sunlightlinux/slinstaller/pkg/storage"
)

// UUID reads an assembled array's RAID UUID (the md metadata UUID, not its
// filesystem's) with mdadm, for the rd.md.uuid= kernel argument that tells
// the initramfs to assemble it at boot. The array must already exist, so
// this runs after Commands.
func UUID(device string) (string, error) {
	out, err := exec.Command("mdadm", "--detail", "--export", device).Output()
	if err != nil {
		return "", fmt.Errorf("mdadm: --detail --export %s: %w", device, err)
	}
	for _, line := range strings.Split(string(out), "\n") {
		if v, ok := strings.CutPrefix(line, "MD_UUID="); ok {
			return strings.TrimSpace(v), nil
		}
	}
	return "", fmt.Errorf("mdadm: no MD_UUID for %s", device)
}

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
//
// spares are hot spares: listed after the members, they hold no data until
// a member fails and the array rebuilds onto one.
func Create(device, level string, members, spares []string) []string {
	return CreateWith(device, level, "", members, spares)
}

// CreateWith is Create with the superblock format chosen ("" for 1.2).
func CreateWith(device, level, metadata string, members, spares []string) []string {
	if metadata == "" {
		metadata = "1.2"
	}
	cmd := []string{
		"mdadm", "--create", device, "--run", "--force",
		"--metadata=" + metadata,
		"--level=" + normalizeLevel(level),
		fmt.Sprintf("--raid-devices=%d", len(members)),
	}
	if len(spares) > 0 {
		cmd = append(cmd, fmt.Sprintf("--spare-devices=%d", len(spares)))
	}
	return append(append(cmd, members...), spares...)
}

// Commands returns the array-creation commands for a plan, in the order
// the plan lists the arrays.
func Commands(pl *storage.Plan) [][]string {
	var cmds [][]string
	for _, p := range pl.Parts {
		if p.Raid {
			cmds = append(cmds, CreateWith(storage.RaidDevice(p.RaidName), p.RaidLevel, p.RaidMetadata, p.RaidMembers, p.RaidSpares))
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
