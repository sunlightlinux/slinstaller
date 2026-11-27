package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func machineDisks() []DiskInfo {
	return []DiskInfo{
		{Name: "sda", Path: "/dev/sda", Serial: "S1AAA", WWN: "0x5000c500a1b2c3d4", Model: "Fast SSD 1T"},
		{Name: "sdb", Path: "/dev/sdb", Serial: "S1BBB", Model: "Fast SSD 1T"},
		{Name: "nvme0n1", Path: "/dev/nvme0n1", Serial: "NV42", Model: "Tiny NVMe"},
	}
}

func matchConfig(name string, m *DiskMatch, children ...string) *Config {
	d := &Media{Name: name, Type: "disk", Match: m}
	for i, ch := range children {
		c := &Media{Name: ch, Type: "part", FsType: "ext4", Mountpoint: "/", Size: "0"}
		if i == 0 {
			c.FsType, c.Mountpoint, c.Size = "vfat", "/boot", "512M"
		}
		d.Children = append(d.Children, c)
	}
	return &Config{TargetMedia: []*Media{d}}
}

// A config naming its disk by serial installs to that disk whatever the
// kernel called it this boot, and its partitions follow.
func TestResolveDisksBySerial(t *testing.T) {
	// Written on a machine where the disk was sda; here it is nvme0n1.
	c := matchConfig("sda", &DiskMatch{Serial: "NV42"}, "sda1", "sda2")
	if !c.NeedsDisks() {
		t.Fatal("a config with a match needs the disks listed")
	}
	if err := c.ResolveDisks(machineDisks()); err != nil {
		t.Fatal(err)
	}
	d := c.TargetMedia[0]
	if d.Name != "nvme0n1" || d.Children[0].Name != "nvme0n1p1" || d.Children[1].Name != "nvme0n1p2" {
		t.Errorf("resolved to %s with %s, %s", d.Name, d.Children[0].Name, d.Children[1].Name)
	}
	if err := c.Validate(); err != nil {
		t.Errorf("the resolved config does not validate: %v", err)
	}
	plan, err := c.Plan()
	if err != nil || plan.Disk != "/dev/nvme0n1" || plan.Parts[1].Device != "/dev/nvme0n1p2" {
		t.Errorf("plan: %v, %+v", err, plan)
	}

	// Partitions keep their own numbers; unnamed ones go by position.
	c = matchConfig("", &DiskMatch{WWN: "0x5000C500A1B2C3D4"}, "", "")
	if err := c.ResolveDisks(machineDisks()); err != nil {
		t.Fatal(err)
	}
	if d = c.TargetMedia[0]; d.Name != "sda" || d.Children[0].Name != "sda1" || d.Children[1].Name != "sda2" {
		t.Errorf("by WWN: %s %s %s", d.Name, d.Children[0].Name, d.Children[1].Name)
	}
	// An empty entry is no partition, here as in Plan: the next is 2.
	c = matchConfig("", &DiskMatch{WWN: "0x5000C500A1B2C3D4"}, "", "")
	c.TargetMedia[0].Children = []*Media{c.TargetMedia[0].Children[0], nil, c.TargetMedia[0].Children[1]}
	if err := c.ResolveDisks(machineDisks()); err != nil {
		t.Fatal(err)
	}
	if got := c.TargetMedia[0].Children[2].Name; got != "sda2" {
		t.Errorf("after an empty entry: %s", got)
	}
	if _, err := c.Plan(); err != nil {
		t.Errorf("plan after an empty entry: %v", err)
	}
	c = matchConfig("sdx", &DiskMatch{Serial: "S1BBB"}, "sdx1", "sdx5")
	_ = c.ResolveDisks(machineDisks())
	if got := c.TargetMedia[0].Children[1].Name; got != "sdb5" {
		t.Errorf("a partition's number was not kept: %s", got)
	}
	if (&Config{TargetMedia: []*Media{nil, {Name: "sda"}}}).NeedsDisks() {
		t.Error("a config without a match does not need the disks listed")
	}
}

// The install must never guess which disk to erase.
func TestResolveDisksRefusesToGuess(t *testing.T) {
	for name, tc := range map[string]struct {
		match *DiskMatch
		want  string
	}{
		"no such disk":      {&DiskMatch{Serial: "GONE"}, "no disk on this machine matches serial GONE"},
		"two of that model": {&DiskMatch{Model: "Fast SSD 1T"}, "matches several disks (/dev/sda, /dev/sdb)"},
		"fields disagree":   {&DiskMatch{Serial: "S1AAA", Model: "Tiny NVMe"}, "no disk on this machine matches serial S1AAA, model Tiny NVMe"},
		"empty":             {&DiskMatch{}, "empty match"},
		"missing path":      {&DiskMatch{Path: "/dev/disk/by-id/no-such-disk"}, "disk match path"},
	} {
		c := matchConfig("sda", tc.match, "sda1", "sda2")
		err := c.ResolveDisks(machineDisks())
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: error %v, want %q", name, err, tc.want)
		}
		if c.TargetMedia[0].Name != "sda" {
			t.Errorf("%s: the disk was renamed despite the error", name)
		}
	}
	// A model that is unique is enough; with the serial it names one of two.
	c := matchConfig("", &DiskMatch{Model: "Fast SSD 1T", Serial: "S1BBB"}, "", "")
	if err := c.ResolveDisks(machineDisks()); err != nil || c.TargetMedia[0].Name != "sdb" {
		t.Errorf("model and serial: %v, %s", err, c.TargetMedia[0].Name)
	}
	// Two entries must not land on the same disk.
	two := &Config{TargetMedia: []*Media{
		{Type: "disk", Match: &DiskMatch{Serial: "NV42"}},
		{Type: "disk", Match: &DiskMatch{Model: "Tiny NVMe"}},
	}}
	if err := two.ResolveDisks(machineDisks()); err == nil || !strings.Contains(err.Error(), "both match /dev/nvme0n1") {
		t.Errorf("two matches for one disk: %v", err)
	}
}

func TestResolveDisksByStablePath(t *testing.T) {
	// A by-id link as udev makes them: a symlink to the device node. The
	// "device" here is a plain file standing in for it.
	dir := t.TempDir()
	node := filepath.Join(dir, "sdq")
	os.WriteFile(node, nil, 0o644)
	link := filepath.Join(dir, "ata-Fast_SSD_S1QQQ")
	if err := os.Symlink(node, link); err != nil {
		t.Fatal(err)
	}
	real, _ := filepath.EvalSymlinks(node)
	c := matchConfig("", &DiskMatch{Path: link}, "", "")
	if err := c.ResolveDisks([]DiskInfo{{Name: "sdq", Path: real}, {Name: "sdr", Path: "/dev/sdr"}}); err != nil {
		t.Fatal(err)
	}
	if c.TargetMedia[0].Name != "sdq" {
		t.Errorf("by path: %s", c.TargetMedia[0].Name)
	}

	// The same link works as a block-devices alias.
	al := &Config{
		BlockDevices: []*StorageAlias{{Name: "disk", File: link}},
		TargetMedia:  []*Media{{Name: "${disk}", Type: "disk", Children: []*Media{{Name: "${disk}1"}}}},
	}
	al.expandAliases()
	if al.TargetMedia[0].Name != "sdq" || al.TargetMedia[0].Children[0].Name != "sdq1" {
		t.Errorf("alias through a link: %s, %s", al.TargetMedia[0].Name, al.TargetMedia[0].Children[0].Name)
	}
}

func TestValidateRefusesPathsAsNames(t *testing.T) {
	c := matchConfig("/dev/sda", nil, "/dev/sda1", "/dev/sda2")
	err := c.Validate()
	if err == nil || !strings.Contains(err.Error(), "give the device's name (e.g. sda), not a path") {
		t.Errorf("a path as a name: %v", err)
	}
	// A match belongs on a disk.
	c = matchConfig("sda", nil, "sda1", "sda2")
	c.TargetMedia[0].Children[1].Match = &DiskMatch{Serial: "X"}
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "match is for a disk") {
		t.Errorf("a match on a partition: %v", err)
	}
}

// A matched disk's named partition must be one of its own: renamed by
// position, a typo (sdb5 under sda) became an existing partition nobody
// named, and was formatted.
func TestMatchRefusesAForeignPartitionName(t *testing.T) {
	c := matchConfig("sda", &DiskMatch{Serial: "NV42"}, "sda1", "sdb5")
	c.ReusePartitions = true
	if err := c.ResolveDisks(machineDisks()); err == nil || !strings.Contains(err.Error(), "partition sdb5 is not one of disk \"sda\"") {
		t.Errorf("ResolveDisks = %v", err)
	}
}

// A reuse layout with an empty entry numbers its partitions as Plan does,
// with or without a match: the same config formats the same partition.
func TestReuseNumbersLikePlan(t *testing.T) {
	c := &Config{ReusePartitions: true, TargetMedia: []*Media{{Name: "sda", Type: "disk", Children: []*Media{
		nil,
		{FsType: "vfat", Mountpoint: "/boot/efi", Preserve: true},
		{FsType: "ext4", Mountpoint: "/"},
	}}}}
	pl, err := c.Plan()
	if err != nil {
		t.Fatal(err)
	}
	if pl.Parts[0].Device != "/dev/sda1" || pl.Parts[1].Device != "/dev/sda2" {
		t.Errorf("reuse devices: %s, %s", pl.Parts[0].Device, pl.Parts[1].Device)
	}
}
