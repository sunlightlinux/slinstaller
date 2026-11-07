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
	"sort"
	"strconv"
	"strings"
)

// Device is one block device: a disk or one of its partitions.
type Device struct {
	Name     string // sda
	Path     string // /dev/sda
	Type     string // disk, part, loop, rom, ...
	Model    string
	Size     uint64 // bytes
	Children []Device

	// What lsblk sees on it now: where it is mounted ("[SWAP]" for active
	// swap), its filesystem or signature type, and its label.
	Mountpoint string
	FsType     string
	Label      string
}

// List runs lsblk and returns the top-level block devices, each with its
// partitions as children.
func List() ([]Device, error) {
	out, err := exec.Command("lsblk", "-J", "-b", "-o", "NAME,PATH,SIZE,TYPE,MODEL,MOUNTPOINT,FSTYPE,LABEL").Output()
	if err != nil {
		return nil, fmt.Errorf("blockdev: lsblk: %w", err)
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
			Size:     uint64(j.Size),
			Children: convert(j.Children),

			Mountpoint: j.Mountpoint,
			FsType:     j.FsType,
			Label:      j.Label,
		})
	}
	return out
}

type jsonDev struct {
	Name     string    `json:"name"`
	Path     string    `json:"path"`
	Type     string    `json:"type"`
	Model    string    `json:"model"`
	Size     flexUint  `json:"size"`
	Children []jsonDev `json:"children"`

	Mountpoint string `json:"mountpoint"`
	FsType     string `json:"fstype"`
	Label      string `json:"label"`
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
		if d.InUse() == "" {
			out = append(out, d)
		}
	}
	return out
}

// isHolder reports whether a device is something the live system runs on
// top of a disk: a RAID array or a device-mapper volume (LVM, LUKS).
func isHolder(t string) bool {
	return t == "crypt" || t == "lvm" || t == "dm" || t == "linear" ||
		(strings.HasPrefix(t, "raid") && t != "raid")
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
	var cmds [][]string
	seen := map[string]bool{}
	var release func(dev Device)
	release = func(dev Device) {
		for _, c := range dev.Children {
			release(c)
		}
		if !isHolder(dev.Type) || seen[dev.Path] {
			return
		}
		seen[dev.Path] = true
		if strings.HasPrefix(dev.Type, "raid") || dev.Type == "linear" {
			cmds = append(cmds, []string{"mdadm", "--stop", dev.Path})
		} else {
			cmds = append(cmds, []string{"dmsetup", "remove", dev.Name})
		}
	}
	for _, c := range d.Children {
		release(c)
	}
	for _, c := range d.Children {
		if c.Type == "part" {
			cmds = append(cmds, []string{"wipefs", "-a", c.Path})
		}
	}
	return append(cmds, []string{"wipefs", "-a", d.Path})
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
		if isHolder(dev.Type) && !seen[dev.Path] {
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
