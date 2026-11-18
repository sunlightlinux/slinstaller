package main

import (
	"fmt"
	"io"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/sunlightlinux/slinstaller/pkg/blockdev"
	"github.com/sunlightlinux/slinstaller/pkg/config"
	"github.com/sunlightlinux/slinstaller/pkg/storage"
)

// discoverConfig describes the layout already on a disk as a config: a
// reusePartitions install that keeps every partition. It is a starting
// point, not an install — nothing is mounted anywhere yet, so the operator
// says which partition becomes / (and stops preserving it) and which is the
// ESP. notes say what could not be carried over.
func discoverConfig(devs []blockdev.Device, disk string) (*config.Config, []string, error) {
	d := blockdev.FindByPath(devs, disk)
	switch {
	case d == nil:
		return nil, nil, fmt.Errorf("%s is not among the machine's block devices", disk)
	case d.Type != "disk" && d.Type != "loop":
		return nil, nil, fmt.Errorf("%s is a %s, not a whole disk", disk, d.Type)
	}
	m := &config.Media{Name: d.Name, Type: "disk"}
	if serial := strings.TrimSpace(d.Serial); serial != "" {
		// By serial, so the config still means this disk after a reboot.
		m.Match = &config.DiskMatch{Serial: serial}
	}
	var notes []string
	for _, p := range d.Children {
		if p.Type != "part" {
			notes = append(notes, fmt.Sprintf("%s is a %s, not a partition; left out", p.Path, p.Type))
			continue
		}
		ch := &config.Media{
			Name: p.Name, Type: "part", Label: p.Label, Preserve: true,
			Size: fmt.Sprintf("%dM", p.Size>>20),
		}
		switch {
		case storage.KnownFilesystem(p.FsType):
			ch.FsType = p.FsType
		case p.FsType == "":
			notes = append(notes, fmt.Sprintf("%s has no filesystem slinstaller recognises; give it an fstype to use it", p.Path))
		default:
			notes = append(notes, fmt.Sprintf("%s holds %s, which an install into existing partitions cannot use; leave it out or keep it untouched", p.Path, p.FsType))
		}
		if p.FsType == "vfat" {
			notes = append(notes, fmt.Sprintf("%s is vfat: if it is the EFI System Partition, give it mountpoint: /boot/efi", p.Path))
		}
		if len(p.Children) > 0 {
			notes = append(notes, fmt.Sprintf("%s carries other devices (LVM, RAID or LUKS), which are not described", p.Path))
		}
		m.Children = append(m.Children, ch)
	}
	if len(m.Children) == 0 {
		notes = append(notes, fmt.Sprintf("%s has no partitions; a config that partitions it is what --template writes", d.Path))
	}
	return &config.Config{TargetMedia: []*config.Media{m}, ReusePartitions: true, Version: 1}, notes, nil
}

// printDiscovered writes the discovered config to out and what the operator
// has to decide to notes.
func printDiscovered(out, notes io.Writer, disk string) error {
	devs, err := blockdev.List()
	if err != nil {
		return err
	}
	cfg, remarks, err := discoverConfig(devs, disk)
	if err != nil {
		return err
	}
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "# The layout on %s, as slinstaller found it. Every partition is kept\n", disk)
	fmt.Fprintf(out, "# (preserve: true). To install: give one partition mountpoint: / and remove\n")
	fmt.Fprintf(out, "# its preserve, give the ESP its mountpoint, and add users.\n%s", data)
	for _, n := range remarks {
		fmt.Fprintln(notes, "note:", n)
	}
	return nil
}
