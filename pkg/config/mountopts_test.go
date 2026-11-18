package config

import (
	"strings"
	"testing"
)

func btrfsConfig() *Config {
	return &Config{TargetMedia: []*Media{{Name: "sda", Type: "disk", Children: []*Media{
		{Name: "sda1", Type: "part", FsType: "vfat", Mountpoint: "/boot", Size: "512M"},
		{Name: "sda2", Type: "part", FsType: "swap", Size: "2G", MountOptions: "discard"},
		{Name: "sda3", Type: "part", FsType: "btrfs", Mountpoint: "/", Size: "0",
			MountOptions: "noatime,compress=zstd",
			Subvolumes:   []*Subvolume{{Name: "@", Mountpoint: "/"}, nil, {Name: "@home", Mountpoint: "/home"}}},
	}}}}
}

func TestBtrfsSubvolumesAndMountOptionsReachThePlan(t *testing.T) {
	c := btrfsConfig()
	if err := c.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	plan, err := c.Plan()
	if err != nil {
		t.Fatal(err)
	}
	root := plan.Parts[2]
	if root.Subvol != "@" || len(root.Subvolumes) != 2 || root.MountOptions != "noatime,compress=zstd" || root.Mountpoint != "/" {
		t.Errorf("root = %+v", root)
	}
	if plan.Parts[1].MountOptions != "discard" {
		t.Errorf("swap options = %q", plan.Parts[1].MountOptions)
	}
	fstab := plan.Fstab(nil)
	if !strings.Contains(fstab, "/home\tbtrfs\tnoatime,compress=zstd,subvol=@home") {
		t.Errorf("fstab:\n%s", fstab)
	}
	// The file round-trips.
	saved := t.TempDir() + "/c.yaml"
	if err := Save(saved, c); err != nil {
		t.Fatal(err)
	}
	back, err := Load(saved)
	if err != nil || back.TargetMedia[0].Children[2].Subvolumes[2].Name != "@home" || back.TargetMedia[0].Children[2].MountOptions != "noatime,compress=zstd" {
		t.Errorf("reloaded: %v", err)
	}
}

func TestSubvolumeAndMountOptionValidation(t *testing.T) {
	for name, tc := range map[string]struct {
		change func(root, swap *Media)
		want   string
	}{
		"not btrfs":        {func(r, _ *Media) { r.FsType = "ext4" }, `subvolumes need a btrfs filesystem, not "ext4"`},
		"none at the root": {func(r, _ *Media) { r.Subvolumes[0].Mountpoint = "/data" }, `one subvolume must be mounted at the filesystem's own mountpoint ("/")`},
		"nested name":      {func(r, _ *Media) { r.Subvolumes[2].Name = "@/home" }, "must be a single path component"},
		"no name":          {func(r, _ *Media) { r.Subvolumes[2].Name = "" }, "must be a single path component"},
		"twice":            {func(r, _ *Media) { r.Subvolumes[2].Name = "@" }, `subvolume "@" is listed twice`},
		"relative mount":   {func(r, _ *Media) { r.Subvolumes[2].Mountpoint = "home" }, `mountpoint "home" is not absolute`},
		"taken mountpoint": {func(r, _ *Media) { r.Subvolumes[2].Mountpoint = "/boot" }, `mountpoint "/boot" is used more than once`},
		"spaces":           {func(r, _ *Media) { r.MountOptions = "noatime, compress=zstd" }, "must be comma-separated, without spaces"},
		"not mounted":      {func(r, _ *Media) { r.Mountpoint, r.Subvolumes = "", nil }, "mount-options on a filesystem that is not mounted"},
	} {
		c := btrfsConfig()
		tc.change(c.TargetMedia[0].Children[2], c.TargetMedia[0].Children[1])
		err := c.Validate()
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: error %v, want %q", name, err, tc.want)
		}
	}

	// Two roots — one a partition, one a subvolume — are still two roots.
	c := btrfsConfig()
	c.TargetMedia[0].Children = append(c.TargetMedia[0].Children,
		&Media{Name: "sda4", Type: "part", FsType: "btrfs", Mountpoint: "/data", Size: "10G",
			Subvolumes: []*Subvolume{{Name: "@data", Mountpoint: "/data"}, {Name: "@r", Mountpoint: "/"}}})
	if err := c.Validate(); err == nil {
		t.Error("a second root, as a subvolume, was accepted")
	}

	// In a reuse install a kept filesystem cannot be given subvolumes.
	c = btrfsConfig()
	c.ReusePartitions = true
	c.TargetMedia[0].Children[2].Preserve = true
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "cannot be made on a preserved filesystem") {
		t.Errorf("preserved with subvolumes: %v", err)
	}

	// On a logical volume and on a RAID array, likewise.
	lv := &Config{TargetMedia: []*Media{{Name: "sda", Type: "disk", Children: []*Media{
		{Name: "sda1", Type: "part", FsType: "vfat", Mountpoint: "/boot", Size: "512M"},
		{Name: "sda2", Type: "lvm", VolumeGroup: "vg0", Children: []*Media{
			{Name: "root", FsType: "btrfs", Mountpoint: "/", MountOptions: "ssd",
				Subvolumes: []*Subvolume{{Name: "@", Mountpoint: "/"}}}}},
	}}}}
	if err := lv.Validate(); err != nil {
		t.Fatalf("LV with subvolumes: %v", err)
	}
	plan, _ := lv.Plan()
	if got := plan.Parts[len(plan.Parts)-1]; got.Subvol != "@" || got.MountOptions != "ssd" {
		t.Errorf("LV in the plan: %+v", got)
	}
	raid := &Config{
		TargetMedia: []*Media{
			{Name: "sda", Type: "disk", Children: []*Media{
				{Name: "sda1", Type: "part", FsType: "vfat", Mountpoint: "/boot", Size: "512M"},
				{Name: "sda2", Type: "raid", Raid: "md0"}}},
			{Name: "sdb", Type: "disk", Children: []*Media{{Name: "sdb1", Type: "raid", Raid: "md0"}}},
		},
		Raid: []*RaidArray{{Name: "md0", Level: "1", FsType: "btrfs", Mountpoint: "/", MountOptions: "noatime",
			Subvolumes: []*Subvolume{{Name: "@", Mountpoint: "/"}}}},
	}
	if err := raid.Validate(); err != nil {
		t.Fatalf("RAID with subvolumes: %v", err)
	}
	plan, _ = raid.Plan()
	if got := plan.Parts[len(plan.Parts)-1]; !got.Raid || got.Subvol != "@" || got.MountOptions != "noatime" {
		t.Errorf("array in the plan: %+v", got)
	}
}
