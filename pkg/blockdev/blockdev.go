// Package blockdev discovers the machine's block devices with lsblk.
//
// clr-installer keeps a rich BlockDevice tree parsed from lsblk and
// drives the whole install from it. slinstaller takes the disk layout
// from the config instead, so this is deliberately lean: enough to
// confirm the target disk exists and is a whole disk before anything is
// wiped, and to list what is available to a person choosing one.
package blockdev

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/sunlightlinux/slinstaller/pkg/storage"
)

// Device is one block device: a disk or one of its partitions.
type Device struct {
	Name     string // sda
	Path     string // /dev/sda
	Type     string // disk, part, loop, rom, ...
	Model    string
	Serial   string
	WWN      string
	Size     uint64 // bytes
	Children []Device

	// What lsblk sees on it now: where it is mounted ("[SWAP]" for active
	// swap), its filesystem or signature type, and its label.
	Mountpoint string
	FsType     string
	Label      string
	// PartLabel is the partition's name in the partition table (GPT),
	// which is not the filesystem's label.
	PartLabel string
	// UUID is the filesystem's or the member signature's UUID: the members
	// of one RAID array, LVM group or multi-device btrfs share it.
	UUID string
}

// List runs lsblk and returns the top-level block devices, each with its
// partitions as children.
func List() ([]Device, error) {
	out, err := exec.Command("lsblk", "-J", "-b", "-o", "NAME,PATH,SIZE,TYPE,MODEL,SERIAL,WWN,MOUNTPOINT,FSTYPE,LABEL,PARTLABEL,UUID").Output()
	if err != nil {
		return nil, fmt.Errorf("blockdev: lsblk: %w", storage.WithStderr(err))
	}
	return parse(out)
}

// Disks returns only the whole disks among devs.
func Disks(devs []Device) []Device {
	var out []Device
	for _, d := range devs {
		if d.Type == "disk" {
			out = append(out, d)
		}
	}
	return out
}

// FindByPath returns the device at the given path (e.g. /dev/sda or
// /dev/sda1), searching disks and their partitions. nil if not found.
func FindByPath(devs []Device, path string) *Device {
	for i := range devs {
		if devs[i].Path == path || "/dev/"+devs[i].Name == path {
			return &devs[i]
		}
		if d := FindByPath(devs[i].Children, path); d != nil {
			return d
		}
	}
	return nil
}

// parse turns lsblk -J output into devices. It is separate from List so
// the parsing is testable without a real lsblk.
func parse(data []byte) ([]Device, error) {
	var doc struct {
		BlockDevices []jsonDev `json:"blockdevices"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("blockdev: parse lsblk: %w", err)
	}
	return convert(doc.BlockDevices), nil
}

func convert(in []jsonDev) []Device {
	var out []Device
	for _, j := range in {
		out = append(out, Device{
			Name:     j.Name,
			Path:     j.Path,
			Type:     j.Type,
			Model:    j.Model,
			Serial:   j.Serial,
			WWN:      j.WWN,
			Size:     uint64(j.Size),
			Children: convert(j.Children),

			Mountpoint: j.Mountpoint,
			FsType:     j.FsType,
			Label:      j.Label,
			PartLabel:  j.PartLabel,
			UUID:       j.UUID,
		})
	}
	return out
}

type jsonDev struct {
	Name     string    `json:"name"`
	Path     string    `json:"path"`
	Type     string    `json:"type"`
	Model    string    `json:"model"`
	Serial   string    `json:"serial"`
	WWN      string    `json:"wwn"`
	Size     flexUint  `json:"size"`
	Children []jsonDev `json:"children"`

	Mountpoint string `json:"mountpoint"`
	FsType     string `json:"fstype"`
	Label      string `json:"label"`
	PartLabel  string `json:"partlabel"`
	UUID       string `json:"uuid"`
}

// flexUint accepts a size that lsblk may emit as a JSON number (newer)
// or a quoted string (older).
type flexUint uint64

func (f *flexUint) UnmarshalJSON(b []byte) error {
	b = bytes.Trim(b, `"`)
	if len(b) == 0 || string(b) == "null" {
		*f = 0
		return nil
	}
	n, err := strconv.ParseUint(string(b), 10, 64)
	if err != nil {
		return err
	}
	*f = flexUint(n)
	return nil
}

// InUse says why d (with everything on it) must not be installed to, or
// "" if it may be: a filesystem on it is mounted, a swap area on it is
// active, or it is the live medium the installer runs from. clr-installer
// leaves such disks out of its choices for the same reason: wiping one
// pulls the ground from under the running system.
func (d Device) InUse() string {
	switch {
	case d.Mountpoint == "[SWAP]":
		return d.Path + " is in use as swap"
	case d.Mountpoint != "":
		return d.Path + " is mounted at " + d.Mountpoint
	case d.FsType == "iso9660" || d.FsType == "squashfs":
		return d.Path + " holds the live medium"
	}
	for _, c := range d.Children {
		if why := c.InUse(); why != "" {
			return why
		}
	}
	return ""
}

// Available returns the whole disks among devs that may be installed to:
// those InUse finds nothing on.
func Available(devs []Device) []Device {
	var out []Device
	for _, d := range Disks(devs) {
		if d.InUse() == "" && Installable(d) {
			out = append(out, d)
		}
	}
	return out
}

// Installable reports whether a disk lsblk lists can hold an install at
// all: not a compressed-RAM swap device (zram), not an eMMC's boot or RPMB
// area (mmcblk0boot0: a few MiB, read-only), not an empty card reader
// (size 0). Offered, they were picked and failed later.
func Installable(d Device) bool {
	return d.Size > 0 && !strings.HasPrefix(d.Name, "zram") &&
		!(strings.HasPrefix(d.Name, "mmcblk") && (strings.Contains(d.Name, "boot") || strings.Contains(d.Name, "rpmb")))
}

// isHolder reports whether a device is something the live system runs on
// top of a disk: a RAID array (an Intel RST/DDF container too) or a
// device-mapper volume (LVM, LUKS, multipath, dmraid). lsblk names an md
// device by its level and a dm device by its UUID's prefix.
func isHolder(t string) bool {
	return t == "crypt" || t == "lvm" || t == "dm" || t == "linear" || t == "container" ||
		t == "mpath" || t == "dmraid" || (strings.HasPrefix(t, "raid") && t != "raid")
}

// Teardown returns the commands that release disk d before its partition
// table is rewritten, deepest first: device-mapper volumes opened on it
// (an LVM logical volume, a LUKS mapping) are removed and RAID arrays
// assembled from it stopped, then the signatures on each partition and on
// the disk are erased. A live system assembles an old install's arrays and
// activates its volume groups by itself; left running, they hold the disk
// busy, and left on disk, an old RAID superblock or LVM label is found
// again on the next boot. clr-installer clears the same before
// partitioning (pvremove, vgremove, mdadm --stop and --zero-superblock).
// Nothing found means only the signatures are erased.
func Teardown(d Device) [][]string {
	return TeardownAll([]Device{d})
}

// TeardownAll is Teardown for several disks at once: every holder on any
// of them is released before any signature is erased, and each only once.
// lsblk lists an array or a volume spanning disks under every member, so
// tearing the disks down one by one stopped md127 for the first disk and
// failed on the second ("mdadm --stop /dev/md127": no such device) —
// every reinstall over a two-disk RAID 1 stopped there.
func TeardownAll(ds []Device) [][]string {
	var cmds [][]string
	seen := map[string]bool{}
	var containers [][]string
	var release func(dev Device, onDM bool)
	release = func(dev Device, onDM bool) {
		dm := dev.Type == "mpath" || dev.Type == "dmraid"
		for _, c := range dev.Children {
			release(c, dm)
		}
		// A partition of a multipath or dmraid device is itself a
		// device-mapper volume (kpartx's mpatha1), holding its parent.
		if onDM && dev.Type == "part" && !seen[dev.Path] {
			seen[dev.Path] = true
			cmds = append(cmds, []string{"dmsetup", "remove", dev.Name})
			return
		}
		if !isHolder(dev.Type) || seen[dev.Path] {
			return
		}
		seen[dev.Path] = true
		// An RST/DDF container is stopped after every array in it, which
		// lsblk lists beside it, not under it.
		if dev.Type == "container" {
			containers = append(containers, []string{"mdadm", "--stop", dev.Path})
			return
		}
		if strings.HasPrefix(dev.Type, "raid") || dev.Type == "linear" {
			cmds = append(cmds, []string{"mdadm", "--stop", dev.Path})
		} else {
			cmds = append(cmds, []string{"dmsetup", "remove", dev.Name})
		}
	}
	for _, d := range ds {
		for _, c := range d.Children {
			release(c, false)
		}
	}
	cmds = append(cmds, containers...)
	for _, d := range ds {
		for _, c := range d.Children {
			if c.Type == "part" {
				cmds = append(cmds, []string{"wipefs", "-a", c.Path})
			}
		}
		cmds = append(cmds, []string{"wipefs", "-a", d.Path})
	}
	return cmds
}

// disksHolding returns the paths of the disks among devs whose tree holds
// a device at path: a partition's disk, or every disk a RAID array or
// volume draws on (lsblk lists such a device under each of its members).
func disksHolding(devs []Device, path string) []string {
	var out []string
	var has func(d Device) bool
	has = func(d Device) bool {
		if d.Path == path {
			return true
		}
		for _, c := range d.Children {
			if has(c) {
				return true
			}
		}
		return false
	}
	for _, d := range Disks(devs) {
		if has(d) {
			out = append(out, d.Path)
		}
	}
	return out
}

// resolveDev follows a device link to the node (filepath.EvalSymlinks; a
// variable for tests).
var resolveDev = filepath.EvalSymlinks

// disksHoldingLink is disksHolding for a path lsblk does not print: pvs
// names a physical volume as lvm.conf's preferred_names make it (a
// /dev/disk/by-id link, /dev/dm-N), so the path and lsblk's are compared
// by the node each leads to.
func disksHoldingLink(devs []Device, path string) []string {
	node, err := resolveDev(path)
	if err != nil {
		return nil
	}
	var out []string
	var has func(d Device) bool
	has = func(d Device) bool {
		if r, err := resolveDev(d.Path); err == nil && r == node {
			return true
		}
		for _, c := range d.Children {
			if has(c) {
				return true
			}
		}
		return false
	}
	for _, d := range Disks(devs) {
		if has(d) {
			out = append(out, d.Path)
		}
	}
	return out
}

// Shared lists what on disk would be broken on another disk by wiping it:
// a RAID array or device-mapper volume that also draws on a disk outside
// wiped, or an LVM volume group (vgs maps a group to its physical volumes)
// with a physical volume both on disk and on such a disk. Wiping one
// member silently degrades or destroys the rest, so clr-installer refuses
// it unless forced (--force-destructive). wiped are the disks the install
// wipes; those may share freely.
func Shared(devs []Device, disk string, wiped []string, vgs map[string][]string) []string {
	inWiped := map[string]bool{}
	for _, w := range wiped {
		inWiped[w] = true
	}
	elsewhere := func(paths ...string) []string {
		set := map[string]bool{}
		for _, p := range paths {
			for _, dk := range disksHolding(devs, p) {
				if !inWiped[dk] {
					set[dk] = true
				}
			}
		}
		var out []string
		for dk := range set {
			out = append(out, dk)
		}
		sort.Strings(out)
		return out
	}

	var out []string
	d := FindByPath(devs, disk)
	if d == nil {
		return nil
	}
	seen := map[string]bool{}
	var walk func(dev Device)
	walk = func(dev Device) {
		// A multipath map's other members are other paths to this same
		// disk, not other disks: nothing on them is harmed that is not on
		// this one. (Counted, one LUN was refused as shared with itself.)
		if dev.Type == "mpath" {
			for _, dk := range disksHolding(devs, dev.Path) {
				inWiped[dk] = true
			}
		}
		if isHolder(dev.Type) && dev.Type != "mpath" && !seen[dev.Path] {
			seen[dev.Path] = true
			if other := elsewhere(dev.Path); len(other) > 0 {
				out = append(out, fmt.Sprintf("%s (%s) also uses %s", dev.Path, dev.Type, strings.Join(other, ", ")))
			}
		}
		for _, c := range dev.Children {
			walk(c)
		}
	}
	for _, c := range d.Children {
		walk(c)
	}

	names := make([]string, 0, len(vgs))
	for vg := range vgs {
		names = append(names, vg)
	}
	sort.Strings(names)
	for _, vg := range names {
		pvs := vgs[vg]
		onDisk := false
		for _, pv := range pvs {
			for _, dk := range disksHolding(devs, pv) {
				onDisk = onDisk || dk == disk
			}
		}
		if !onDisk {
			continue
		}
		if other := elsewhere(pvs...); len(other) > 0 {
			out = append(out, fmt.Sprintf("volume group %s also has physical volumes on %s", vg, strings.Join(other, ", ")))
		}
	}
	return out
}

// VolumeGroups maps each LVM volume group on the machine to its physical
// volumes, from pvs. Without LVM tools, or with no LVM, it is empty: there
// is then no group a wipe could break.
func VolumeGroups() map[string][]string {
	out, err := exec.Command("pvs", "--noheadings", "--separator", ":", "-o", "pv_name,vg_name").Output()
	if err != nil {
		return nil
	}
	return parseVolumeGroups(string(out))
}

func parseVolumeGroups(s string) map[string][]string {
	vgs := map[string][]string{}
	for _, line := range strings.Split(s, "\n") {
		pv, vg, ok := strings.Cut(strings.TrimSpace(line), ":")
		if ok && pv != "" && vg != "" {
			vgs[vg] = append(vgs[vg], pv)
		}
	}
	return vgs
}

// memberTypes are the signatures of a device that is one part of something
// spanning several: a RAID array, an LVM volume group, a ZFS pool, a
// multi-device btrfs. Their members share one UUID.
// (Not LVM2_member or bcache: lsblk's UUID for those is the device's own,
// not the group's, so it matches only a clone — Shared, through pvs, sees
// the groups.)
var memberTypes = map[string]bool{"linux_raid_member": true, "zfs_member": true, "btrfs": true}

// SharedMembers reports the members, among the devices targets destroy
// (each target and what is under it), of an array, group or pool that also
// has a member outside them: wiping or formatting it breaks that one too.
// Shared sees only what the live system has assembled or activated; this
// sees the signatures on disk — an array not assembled, a group whose
// tools are missing, a multi-device btrfs mounted through another member.
func SharedMembers(devs []Device, targets []string) []string {
	type entry struct {
		dev    Device
		inside bool
	}
	isTarget := map[string]bool{}
	for _, t := range targets {
		isTarget[t] = true
	}
	var all []entry
	var walk func(d Device, inside bool)
	walk = func(d Device, inside bool) {
		inside = inside || isTarget[d.Path]
		all = append(all, entry{d, inside})
		for _, c := range d.Children {
			walk(c, inside)
		}
	}
	for _, d := range devs {
		walk(d, false)
	}
	// A path listed both inside and outside is one device seen twice (a
	// multipath map's partitions under each path to the LUN), not two.
	insidePaths := map[string]bool{}
	for _, e := range all {
		if e.inside {
			insidePaths[e.dev.Path] = true
		}
	}
	var out []string
	said := map[string]bool{}
	for _, e := range all {
		if !e.inside || !memberTypes[e.dev.FsType] || e.dev.UUID == "" || said[e.dev.Path] {
			continue
		}
		var others []string
		for _, o := range all {
			if !o.inside && !insidePaths[o.dev.Path] && o.dev.UUID == e.dev.UUID && o.dev.FsType == e.dev.FsType && !slices.Contains(others, o.dev.Path) {
				others = append(others, o.dev.Path)
			}
		}
		if len(others) > 0 {
			said[e.dev.Path] = true
			out = append(out, fmt.Sprintf("%s (%s %s) has a fellow member on %s (or a dd clone of it is there)", e.dev.Path, e.dev.FsType, e.dev.UUID, strings.Join(others, ", ")))
		}
	}
	return out
}

// VGNameClashes lists the volume groups among names (the ones an install
// creates) that the machine already has a group of, with a physical volume
// that is not on a wiped disk — or not found on any disk: wiping does not
// remove that group, so vgcreate fails ("already exists"), after the wipe.
// vgs maps a group to its physical volumes (VolumeGroups).
func VGNameClashes(devs []Device, vgs map[string][]string, names, wiped []string) []string {
	// A multipath map under a wiped disk is under each path to its LUN:
	// those paths are the same disk, wiped with it (as Shared counts them).
	wiped = slices.Clone(wiped)
	var paths func(d Device)
	paths = func(d Device) {
		if d.Type == "mpath" {
			wiped = append(wiped, disksHolding(devs, d.Path)...)
		}
		for _, c := range d.Children {
			paths(c)
		}
	}
	for _, w := range slices.Clone(wiped) {
		if d := FindByPath(devs, w); d != nil {
			paths(*d)
		}
	}
	var out []string
	sorted := slices.Clone(names)
	sort.Strings(sorted)
	for _, vg := range slices.Compact(sorted) {
		var kept []string
		for _, pv := range vgs[vg] {
			on := disksHolding(devs, pv)
			if len(on) == 0 {
				on = disksHoldingLink(devs, pv)
			}
			gone := len(on) > 0
			for _, dk := range on {
				gone = gone && slices.Contains(wiped, dk)
			}
			if !gone {
				kept = append(kept, pv)
			}
		}
		if len(kept) > 0 {
			out = append(out, fmt.Sprintf("volume group %s already exists, on %s", vg, strings.Join(kept, ", ")))
		}
	}
	return out
}
