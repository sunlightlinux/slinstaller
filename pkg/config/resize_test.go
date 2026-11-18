package config

import (
	"strings"
	"testing"
)

// The layout a discovered config becomes once edited: shrink the data
// partition, and put the new root in the room that leaves.
func resizeConfig() *Config {
	return &Config{ReusePartitions: true, TargetMedia: []*Media{{Name: "sda", Type: "disk", Children: []*Media{
		{Name: "sda1", Type: "part", FsType: "vfat", Mountpoint: "/boot/efi", Preserve: true},
		{Name: "sda2", Type: "part", FsType: "ext4", Preserve: true, Resize: "40G"},
		{Name: "sda3", Type: "part", FsType: "ext4", Mountpoint: "/", Create: true},
	}}}}
}

func TestResizeAndCreateInFreeSpace(t *testing.T) {
	c := resizeConfig()
	if err := c.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	plan, err := c.Plan()
	if err != nil {
		t.Fatal(err)
	}
	if plan.Wipe {
		t.Fatal("a resize install must not wipe the disk")
	}
	old, root := plan.Parts[1], plan.Parts[2]
	if old.ResizeMiB != 40960 || !old.Preserve || old.Create {
		t.Errorf("resized partition = %+v", old)
	}
	if !root.Create || !root.CreateInFree || root.EndMiB != 0 || root.Preserve {
		t.Errorf("new root = %+v", root)
	}
	// A size on the new partition bounds it; a start places it as before.
	c.TargetMedia[0].Children[2].Size = "20G"
	plan, _ = c.Plan()
	if plan.Parts[2].EndMiB != 20480 || !plan.Parts[2].CreateInFree {
		t.Errorf("bounded root = %+v", plan.Parts[2])
	}
	c.TargetMedia[0].Children[2].Start = "41473M"
	plan, _ = c.Plan()
	if p := plan.Parts[2]; p.CreateInFree || p.StartMiB != 41473 || p.EndMiB != 41473+20480 {
		t.Errorf("placed root = %+v", p)
	}
}

func TestResizeValidation(t *testing.T) {
	for name, tc := range map[string]struct {
		change func(c *Config)
		want   string
	}{
		"normal install": {func(c *Config) { c.ReusePartitions = false }, "resize is for a reusePartitions install"},
		"not preserved":  {func(c *Config) { c.TargetMedia[0].Children[1].Preserve = false }, "has to be an existing one with preserve: true"},
		"a new one":      {func(c *Config) { c.TargetMedia[0].Children[1].Create = true }, "has to be an existing one with preserve: true"},
		"not ext":        {func(c *Config) { c.TargetMedia[0].Children[1].FsType = "btrfs" }, `only ext2, ext3 and ext4 can be resized, not "btrfs"`},
		"not a size":     {func(c *Config) { c.TargetMedia[0].Children[1].Resize = "smaller" }, `resize "smaller" is not a size`},
		"zero":           {func(c *Config) { c.TargetMedia[0].Children[1].Resize = "0" }, `resize "0" is not a size`},
		"two in free": {func(c *Config) {
			c.TargetMedia[0].Children = append(c.TargetMedia[0].Children,
				&Media{Name: "sda4", Type: "part", FsType: "ext4", Mountpoint: "/home", Create: true})
		}, "only one partition per disk can be created without a start"},
	} {
		c := resizeConfig()
		tc.change(c)
		err := c.Validate()
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: error %v, want %q", name, err, tc.want)
		}
	}
	// Plan guards a size it cannot read on its own, too.
	c := resizeConfig()
	c.SkipValidationAll = true
	c.TargetMedia[0].Children[1].Resize = "smaller"
	if _, err := c.Plan(); err == nil || !strings.Contains(err.Error(), "is not a size") {
		t.Errorf("Plan with a bad resize: %v", err)
	}
	c = resizeConfig()
	c.TargetMedia[0].Children[2].Size = "lots"
	c.SkipValidationAll = true
	if _, err := c.Plan(); err == nil {
		t.Error("Plan with a bad size on a new partition")
	}
}
