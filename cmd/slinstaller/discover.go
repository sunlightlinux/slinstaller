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
		// What it holds is written down as it is: a kept partition is
		// checked against it at install time, and a resize needs it.
		ch.FsType = p.FsType
		switch {
		case storage.KnownFilesystem(p.FsType):
		case p.FsType == "":
			notes = append(notes, fmt.Sprintf("%s has no filesystem slinstaller recognises; give it an fstype to use it", p.Path))
		case storage.Resizable(p.FsType):
			notes = append(notes, fmt.Sprintf("%s holds %s: it can be kept, and shrunk with resize, but not installed to", p.Path, p.FsType))
		default:
			notes = append(notes, fmt.Sprintf("%s holds %s, which can only be kept as it is", p.Path, p.FsType))
		}
		// A partition named for its role in the partition table is given
		// that role: the layout was prepared for an install, with whatever
		// tool, and says so itself.
		role, said := roleFromLabel(p.PartLabel, ch)
		if said != "" {
			notes = append(notes, fmt.Sprintf("%s is named %s: %s", p.Path, p.PartLabel, said))
		}
		if p.FsType == "vfat" && !role {
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
	fmt.Fprintf(out, "# The layout on %s, as slinstaller found it. A partition is kept\n", disk)
	fmt.Fprintf(out, "# where it says preserve: true (see the notes for any its name has set to\n")
	fmt.Fprintf(out, "# be formatted). To install: give one partition mountpoint: / and remove\n")
	fmt.Fprintf(out, "# its preserve, give the ESP its mountpoint, and add users.\n%s", data)
	for _, n := range remarks {
		fmt.Fprintln(notes, "note:", n)
	}
	return nil
}

// roleFromLabel reads the role a partition was given by its name in the
// partition table, in clr-installer's convention for a layout prepared by
// hand: CLR_BOOT (the ESP, at /boot), CLR_ROOT (/), CLR_SWAP, and
// CLR_MNT_/path for anything else, with _F appended to have it formatted.
// Without _F the partition is kept as it is. It sets ch accordingly and
// returns whether the name gave a role, and what to tell the operator.
func roleFromLabel(label string, ch *config.Media) (bool, string) {
	if !strings.HasPrefix(label, "CLR_") {
		return false, ""
	}
	name := label
	format, encrypt := false, false
	for {
		switch {
		case strings.HasSuffix(name, "_F"):
			name, format = strings.TrimSuffix(name, "_F"), true
			continue
		case strings.HasSuffix(name, "_E"):
			name, encrypt = strings.TrimSuffix(name, "_E"), true
			continue
		}
		break
	}
	var said string
	// A role on a partition with no filesystem (one slinstaller knows) is
	// formatted, as clr-installer does: kept, it was refused at --apply
	// ("to be kept as swap, but holds -"), or failed validation outright.
	if ch.FsType == "" || !storage.KnownFilesystem(ch.FsType) {
		format = true
	}
	switch {
	case name == "CLR_BOOT":
		ch.Mountpoint, said = "/boot", "the EFI System Partition, mounted at /boot"
	case name == "CLR_ROOT":
		ch.Mountpoint, said = "/", "the root filesystem"
	case name == "CLR_SWAP" && !format && ch.FsType != "swap":
		// Labelled swap but holding a filesystem: what it holds is kept,
		// as clr-installer keeps it, not relabelled as a swap to keep.
		return false, "labelled swap, but holds " + ch.FsType + "; left as it is"
	case name == "CLR_SWAP":
		ch.FsType, said = "swap", "swap"
	case strings.HasPrefix(name, "CLR_MNT_/"):
		ch.Mountpoint = strings.TrimPrefix(name, "CLR_MNT_")
		said = "mounted at " + ch.Mountpoint
	default:
		return false, "not a role slinstaller knows; left as it is"
	}
	if format {
		ch.Preserve = false
		said += ", to be FORMATTED"
		if ch.FsType == "" || !storage.KnownFilesystem(ch.FsType) {
			ch.FsType = "ext4"
			if name == "CLR_BOOT" {
				ch.FsType = "vfat"
			}
			said += " as " + ch.FsType
		}
	} else {
		said += ", kept as it is"
	}
	if encrypt {
		said += "; it asks for encryption, which an install into existing partitions does not do"
	}
	return true, said
}
