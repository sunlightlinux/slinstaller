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

// VGCreate creates the volume group on its physical volumes.
func VGCreate(vg string, devices ...string) []string {
	return append([]string{"vgcreate", vg}, devices...)
}

// LVCreate creates a logical volume. A size of FillSize takes all the
// free space in the group; otherwise it is a human size like "30G".
func LVCreate(vg, name, size string) []string {
	if size == FillSize || size == "" {
		return []string{"lvcreate", "-y", "-l", "100%FREE", "-n", name, vg}
	}
	return []string{"lvcreate", "-y", "-L", size, "-n", name, vg}
}

// Commands returns the full LVM setup for a plan: pvcreate for each
// physical volume, vgcreate for each group, then lvcreate for every logical
// volume in its group, those of a fixed size before one that fills it.
func Commands(pl *storage.Plan) [][]string {
	var cmds [][]string
	// A group may span several physical volumes — partitions, on one disk
	// or several, or a RAID array — so they are all initialised first and
	// each group is created once, on all of its own.
	var groups []string
	pvs := map[string][]string{}
	for _, p := range pl.Parts {
		if p.IsPV {
			// EffectiveDevice is the mapper for an encrypted PV
			// (LVM-on-LUKS), the array for one on RAID, and the raw
			// partition otherwise.
			dev := p.EffectiveDevice()
			cmds = append(cmds, PVCreate(dev))
			if _, seen := pvs[p.VGName]; !seen {
				groups = append(groups, p.VGName)
			}
			pvs[p.VGName] = append(pvs[p.VGName], dev)
		}
	}
	for _, vg := range groups {
		cmds = append(cmds, VGCreate(vg, pvs[vg]...))
	}
	// The volumes of a fixed size first, then any that takes what is left:
	// created in the order declared, a "fill the group" volume listed
	// before a sized one took everything and the next lvcreate failed —
	// after the disk was wiped.
	for _, fill := range []bool{false, true} {
		for _, p := range pl.Parts {
			if p.LV && (p.Size == FillSize || p.Size == "") == fill {
				cmds = append(cmds, LVCreate(p.VGName, p.LVName, p.Size))
			}
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
