package config

import (
	"strings"
	"testing"
)

// sdaLayout is a one-disk config with the given partitions on sda.
func sdaLayout(children ...*Media) *Config {
	return &Config{TargetMedia: []*Media{{Name: "sda", Type: "disk", Children: children}}}
}

// validateWants checks that Validate refuses c with a problem containing
// want, or accepts it when want is "".
func validateWants(t *testing.T, name string, c *Config, want string) {
	t.Helper()
	err := c.Validate()
	switch {
	case want == "" && err != nil:
		t.Errorf("%s: rejected: %v", name, err)
	case want != "" && err == nil:
		t.Errorf("%s: validated, want %q", name, want)
	case want != "" && !strings.Contains(err.Error(), want):
		t.Errorf("%s: error does not say %q:\n%v", name, want, err)
	}
}

// Only a real filesystem counts as the root, and the firmware gets one ESP.
func TestValidateRootAndESPShape(t *testing.T) {
	esp := func(n, mnt string) *Media {
		return &Media{Name: n, Type: "part", FsType: "vfat", Mountpoint: mnt, Size: "512M"}
	}
	root := func(n string) *Media {
		return &Media{Name: n, Type: "part", FsType: "ext4", Mountpoint: "/", Size: "rest"}
	}
	for _, tc := range []struct {
		name string
		c    *Config
		want string
	}{
		{"swap at /", sdaLayout(esp("sda1", "/boot"),
			&Media{Name: "sda2", Type: "part", FsType: "swap", Mountpoint: "/", Size: "rest"}),
			"sda2: swap is not mounted"},
		{"swap at /swap beside a root", sdaLayout(esp("sda1", "/boot"),
			&Media{Name: "sda2", Type: "part", FsType: "swap", Mountpoint: "/swap", Size: "1G"}, root("sda3")),
			"sda2: swap is not mounted"},
		{"vfat root", sdaLayout(esp("sda1", "/boot/efi"),
			&Media{Name: "sda2", Type: "part", FsType: "vfat", Mountpoint: "/", Size: "rest"}),
			"sda2: vfat cannot hold the root"},
		{"two ESPs", sdaLayout(esp("sda1", "/boot/efi"), esp("sda2", "/boot"), root("sda3")),
			"2 EFI System Partitions"},
		{"one ESP", sdaLayout(esp("sda1", "/boot"), root("sda2")), ""},
		{"spare ESP for RAID", raidWithSpareESP(), ""},
	} {
		validateWants(t, tc.name, tc.c, tc.want)
	}
}

// raidWithSpareESP is raidConfig with a spare ESP on the second disk.
func raidWithSpareESP() *Config {
	c := raidConfig()
	vdb := c.TargetMedia[1]
	vdb.Children = []*Media{
		{Name: "vdb1", Type: "part", FsType: "vfat", ESP: true, Size: "512M"},
		{Name: "vdb2", Type: "raid", Raid: "root", Size: "rest"},
	}
	return c
}
