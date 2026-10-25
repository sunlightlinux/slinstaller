// Package lvm sets up LVM — physical volumes, a volume group, and
// logical volumes — with the standard tools.
//
// clr-installer builds LVM from its BlockDevice tree; slinstaller builds
// it from the nested config: a partition typed `lvm` is the physical
// volume, carries the volume-group name, and its children are the logical
// volumes. The commands here are run between partitioning and mkfs — the
// filesystems are then made and mounted on the logical volumes
// (storage.Partition.EffectiveDevice), not the raw partition.
package lvm

import "github.com/sunlightlinux/slinstaller/pkg/storage"

// FillSize is the logical-volume size meaning "all the remaining space in
// the group". At most one LV per group may use it.
const FillSize = storage.SizeRest

// PVCreate initialises a partition as an LVM physical volume. -ff -y
// overwrites any stale signature without prompting, since the disk is
// being wiped anyway.
func PVCreate(device string) []string {
	return []string{"pvcreate", "-ff", "-y", device}
}

// VGCreate creates the volume group on the physical volume.
func VGCreate(vg, device string) []string {
	return []string{"vgcreate", vg, device}
}

// LVCreate creates a logical volume. A size of FillSize takes all the
// free space in the group; otherwise it is a human size like "30G".
func LVCreate(vg, name, size string) []string {
	if size == FillSize || size == "" {
		return []string{"lvcreate", "-y", "-l", "100%FREE", "-n", name, vg}
	}
	return []string{"lvcreate", "-y", "-L", size, "-n", name, vg}
}

// Commands returns the full LVM setup for a plan: pvcreate and vgcreate
// for each physical volume, then lvcreate for every logical volume in
// its group, in the order the plan lists them.
func Commands(pl *storage.Plan) [][]string {
	var cmds [][]string
	for _, p := range pl.Parts {
		if p.IsPV {
			cmds = append(cmds, PVCreate(p.Device), VGCreate(p.VGName, p.Device))
		}
	}
	for _, p := range pl.Parts {
		if p.LV {
			cmds = append(cmds, LVCreate(p.VGName, p.LVName, p.Size))
		}
	}
	return cmds
}

// Used reports whether the plan uses LVM at all.
func Used(pl *storage.Plan) bool {
	for _, p := range pl.Parts {
		if p.IsPV || p.LV {
			return true
		}
	}
	return false
}
