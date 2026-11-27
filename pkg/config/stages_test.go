package config

import (
	"strings"
	"testing"
)

func TestInstallTypeRaisesTheRootMinimum(t *testing.T) {
	root := func(c *Config) *Media { return c.TargetMedia[0].Children[2] }
	for _, tc := range []struct {
		kind, size, want string
	}{
		{"", "3G", ""},
		{"server", "3G", `root size "3G" is below the 4 GB minimum`},
		{"server", "5G", ""},
		{"desktop", "10G", `root size "10G" is below the 20 GB minimum`},
		{"desktop", "30G", ""},
		{"desktop", "0", ""}, // filling the disk is checked against the disk, not here
		{"kiosk", "30G", `install-type: "kiosk" is not server or desktop`},
	} {
		c := btrfsConfig()
		c.InstallType = tc.kind
		root(c).Size = tc.size
		err := c.Validate()
		switch {
		case tc.want == "" && err != nil:
			t.Errorf("%s/%s: %v", tc.kind, tc.size, err)
		case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
			t.Errorf("%s/%s: error %v, want %q", tc.kind, tc.size, err, tc.want)
		}
	}
}

func TestStageHooksAndPowerDelayValidation(t *testing.T) {
	c := btrfsConfig()
	c.StageHooks = map[string][]*Hook{"partition": {{Cmd: "echo a"}}, "base-install": {{Cmd: "echo b", Chroot: true}}}
	c.PowerState, c.PowerStateDelay = "poweroff", 30
	if err := c.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	// pre-install and post-install have their own lists; a typo is a typo.
	for _, bad := range []string{"pre-install", "post-install", "bootloder"} {
		c := btrfsConfig()
		c.StageHooks = map[string][]*Hook{bad: {{Cmd: "true"}}}
		if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "is not a stage hooks can follow") {
			t.Errorf("stage %q: %v", bad, err)
		}
	}
	c = btrfsConfig()
	c.PowerStateDelay = -1
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "power-state-delay") {
		t.Errorf("negative delay: %v", err)
	}
}

// mkfs.xfs (xfsprogs 5.19 and later) refuses a filesystem under 300 MiB:
// accepted here, the install failed at mkfs, after the disk was wiped.
func TestXFSBelowItsMinimumIsRefused(t *testing.T) {
	for _, tc := range []struct {
		size string
		ok   bool
	}{{"200M", false}, {"299M", false}, {"300M", true}, {"1G", true}, {"0", true}} {
		c := btrfsConfig()
		c.TargetMedia[0].Children = append(c.TargetMedia[0].Children,
			&Media{Name: "sda4", Type: "part", FsType: "xfs", Mountpoint: "/data", Size: tc.size})
		c.TargetMedia[0].Children[2].Size = "20G"
		err := c.Validate()
		if tc.ok != (err == nil) {
			t.Errorf("xfs partition of %s: %v", tc.size, err)
		}
		// A logical volume likewise.
		c = btrfsConfig()
		c.TargetMedia[0].Children[2].Size = "20G"
		c.TargetMedia[0].Children = append(c.TargetMedia[0].Children,
			&Media{Name: "sda4", Type: "lvm", VolumeGroup: "vg0", Children: []*Media{
				{Name: "data", FsType: "xfs", Mountpoint: "/data", Size: tc.size}}})
		if err := c.Validate(); tc.ok != (err == nil) {
			t.Errorf("xfs volume of %s: %v", tc.size, err)
		}
	}
}
