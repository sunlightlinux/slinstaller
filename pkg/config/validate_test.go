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

// A mountpoint is compared and written to fstab as given: it has to be
// one clean path, and not one of the system's own mounts. Every entry
// that carries one is checked.
func TestValidateMountpointShape(t *testing.T) {
	part := func(mnt string) *Config {
		return sdaLayout(
			&Media{Name: "sda1", Type: "part", FsType: "vfat", Mountpoint: "/boot", Size: "512M"},
			&Media{Name: "sda2", Type: "part", FsType: "ext4", Mountpoint: "/home", Size: "1G"},
			&Media{Name: "sda3", Type: "part", FsType: "ext4", Mountpoint: mnt, Size: "1G"},
			&Media{Name: "sda4", Type: "part", FsType: "ext4", Mountpoint: "/", Size: "rest"},
		)
	}
	for mnt, want := range map[string]string{
		"/srv":         "",
		"/home/":       `"/home/" is not a clean path (write "/home")`,
		"/boot/../tmp": "not a clean path",
		"//srv":        "not a clean path",
		"/a b":         "space or control character",
		"/a\tb":        "space or control character",
		"/a\x01":       "space or control character",
		"/proc":        "under /proc",
		"/sys/fs":      "under /sys",
		"/dev":         "under /dev",
		"/run/media":   "under /run",
		"/devices":     "",
		"/running":     "",
	} {
		validateWants(t, "partition at "+mnt, part(mnt), want)
	}

	// Logical volumes, RAID arrays, RAID partitions and btrfs subvolumes
	// carry mountpoints too.
	lv := part("/srv")
	lv.TargetMedia[0].Children[2] = &Media{Name: "sda3", Type: "lvm", VolumeGroup: "vg", Size: "2G",
		Children: []*Media{{Name: "data", FsType: "ext4", Mountpoint: "/data/", Size: "1G"}}}
	validateWants(t, "logical volume", lv, "sda3/data: mountpoint \"/data/\" is not a clean path")

	arr := raidConfig()
	arr.Raid = append(arr.Raid, &RaidArray{Name: "srv", Level: "1", FsType: "ext4", Mountpoint: "/run/srv"})
	arr.TargetMedia[0].Children[1].Size = "1G"
	arr.TargetMedia[0].Children = append(arr.TargetMedia[0].Children, &Media{Name: "vda3", Type: "raid", Raid: "srv", Size: "rest"})
	arr.TargetMedia[1].Children[0].Size = "1G"
	arr.TargetMedia[1].Children = append(arr.TargetMedia[1].Children, &Media{Name: "vdb2", Type: "raid", Raid: "srv", Size: "rest"})
	validateWants(t, "raid array", arr, "raid srv: mountpoint \"/run/srv\" is under /run")

	rp := raidConfig()
	rp.Raid[0] = &RaidArray{Name: "root", Level: "1", Partitions: []*Media{
		{FsType: "ext4", Mountpoint: "/", Size: "10G"},
		{FsType: "ext4", Mountpoint: "/var/./log", Size: "rest"},
	}}
	validateWants(t, "raid partition", rp, "raid root partition 2: mountpoint \"/var/./log\" is not a clean path")

	sv := part("/srv")
	sv.TargetMedia[0].Children[3] = &Media{Name: "sda4", Type: "part", FsType: "btrfs", Mountpoint: "/", Size: "rest",
		Subvolumes: []*Subvolume{{Name: "@", Mountpoint: "/"}, {Name: "@log", Mountpoint: "/var/log "}}}
	validateWants(t, "subvolume", sv, "subvolume \"@log\": mountpoint \"/var/log \" has a space")
}

// Each LUKS container needs a mapper name of its own; two that come out
// the same are refused, naming both entries.
func TestValidateMapperNamesUnique(t *testing.T) {
	esp := &Media{Name: "sda1", Type: "part", FsType: "vfat", Mountpoint: "/boot", Size: "512M"}
	root := &Media{Name: "sda9", Type: "crypt", FsType: "ext4", Mountpoint: "/", Size: "rest"}
	crypt := func(n, fs, mnt string) *Media {
		return &Media{Name: n, Type: "crypt", FsType: fs, Mountpoint: mnt, Size: "1G"}
	}
	validateWants(t, "two encrypted swaps",
		sdaLayout(esp, crypt("sda2", "swap", ""), crypt("sda3", "swap", ""), root),
		"sda2 and sda3 would both be opened as /dev/mapper/cryptswap")
	validateWants(t, "/var/lib and /var-lib",
		sdaLayout(esp, crypt("sda2", "ext4", "/var/lib"), crypt("sda3", "ext4", "/var-lib"), root),
		"sda2 and sda3 would both be opened as /dev/mapper/cryptvar-lib")
	validateWants(t, "distinct names",
		sdaLayout(esp, crypt("sda2", "swap", ""), crypt("sda3", "ext4", "/var/lib"), root), "")

	// An encrypted array named root beside an encrypted root partition.
	arr := raidConfig()
	arr.Raid[0].Mountpoint = "/srv"
	arr.Raid[0].Encrypted = true
	arr.TargetMedia[0].Children[1].Size = "1G"
	arr.TargetMedia[0].Children = append(arr.TargetMedia[0].Children,
		&Media{Name: "vda3", Type: "crypt", FsType: "ext4", Mountpoint: "/", Size: "rest"})
	validateWants(t, "array named root", arr, "vda3 and raid root would both be opened as /dev/mapper/cryptroot")
}
