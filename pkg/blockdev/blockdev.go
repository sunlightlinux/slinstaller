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
	"strconv"
)

// Device is one block device: a disk or one of its partitions.
type Device struct {
	Name     string // sda
	Path     string // /dev/sda
	Type     string // disk, part, loop, rom, ...
	Model    string
	Size     uint64 // bytes
	Children []Device
}

// List runs lsblk and returns the top-level block devices, each with its
// partitions as children.
func List() ([]Device, error) {
	out, err := exec.Command("lsblk", "-J", "-b", "-o", "NAME,PATH,SIZE,TYPE,MODEL").Output()
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
