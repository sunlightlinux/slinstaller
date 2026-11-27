package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sunlightlinux/slinstaller/pkg/storage"
)

// A config is often written by hand. Validate's job is to report everything
// wrong with it at once, before any disk is touched, in words that point at
// the entry. This config gets most things wrong.
func TestValidateReportsEveryProblemOfABrokenConfig(t *testing.T) {
	c := &Config{
		TargetMedia: []*Media{{Name: "sda", Type: "disk", Children: []*Media{
			nil, // tolerated
			{Type: "part", Mountpoint: "boot"},
			{Name: "sda3", Type: "lvm", Size: "junk", Children: []*Media{
				nil,
				{Mountpoint: "/"},
				{Name: "data", FsType: "ext4", Mountpoint: "/data", Size: "rest"},
			}},
			{Name: "sda4", Type: "raid"},
			{Name: "sda5", Type: "raid", Raid: "ghost"},
		}}},
		Raid: []*RaidArray{
			nil,
			{Level: "1", FsType: "ext4", Mountpoint: "/srv"},
			{Name: "md0", Level: "1", FsType: "vfat", Mountpoint: "/boot"},
		},
		Users: []*User{nil, {Login: ""}},
		NetworkInterfaces: []*Interface{
			nil,
			{Addrs: []*Address{nil, {IP: "not-an-ip"}}},
		},
	}
	err := c.Validate()
	if err == nil {
		t.Fatal("a broken config validated")
	}
	msg := err.Error()
	for _, want := range []string{
		"problems:",
		"partition 2: no filesystem",
		`partition 2: mountpoint "boot" is not absolute`,
		"sda3: lvm partition has no volumeGroup",
		"sda3/lv2: no filesystem",
		"sda3: more than one logical volume fills the group",
		"sda4: raid member names no array",
		`raid member references undeclared array "ghost"`,
		"raid[1]: array has no name",
		"level 1 needs at least 2 members, has 0",
		"raid md0: the EFI System Partition cannot be on RAID",
		"no EFI System Partition",
		"users[1]: no login",
		"networkInterfaces[1]: no name",
		"networkInterfaces[1]: address 2:",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("not reported: %q\n%s", want, msg)
		}
	}
}

func TestConfigWithoutADiskLayout(t *testing.T) {
	// Media that are not disks, or disks with no children, are not a layout.
	c := &Config{TargetMedia: []*Media{nil, {Name: "sda1", Type: "part"}, {Name: "sdb", Type: "disk"}}}
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "no disk with a partition layout") {
		t.Errorf("Validate: %v", err)
	}
	if _, err := c.Plan(); err == nil || !strings.Contains(err.Error(), "no disk with a partition layout") {
		t.Errorf("Plan: %v", err)
	}
}

func TestPlanRefusesWhatItCannotLayOut(t *testing.T) {
	disk := func(children ...*Media) *Config {
		return &Config{TargetMedia: []*Media{{Name: "sda", Type: "disk", Children: children}}}
	}
	for name, tc := range map[string]struct {
		cfg  *Config
		want string
	}{
		"unknown type": {disk(&Media{Name: "sda1", Type: "zfs", FsType: "ext4", Mountpoint: "/"}),
			`child "sda1" has type "zfs"`},
		"bad size": {disk(&Media{Name: "sda1", FsType: "ext4", Mountpoint: "/", Size: "big"}),
			"sda/sda1:"},
		"lvm without a group": {disk(&Media{Name: "sda1", Type: "lvm"}),
			`lvm partition "sda1" has no volumeGroup`},
		"lvm bad size": {disk(&Media{Name: "sda1", Type: "lvm", VolumeGroup: "vg0", Size: "big"}),
			"sda1:"},
		"only empty entries": {disk(nil, nil),
			"declares no partitions"},
		// A new table is numbered by parted, 1..n: a name that says
		// otherwise would have mkfs aim at the wrong device.
		"a partition of another disk": {disk(&Media{Name: "sda1", FsType: "vfat", Mountpoint: "/boot", Size: "512M"},
			&Media{Name: "nvme0n1p2", FsType: "ext4", Mountpoint: "/"}),
			"/dev/nvme0n1p2 is not a partition of /dev/sda"},
		"a gap in the numbers": {disk(&Media{Name: "sda1", FsType: "vfat", Mountpoint: "/boot", Size: "512M"},
			&Media{Name: "sda5", FsType: "ext4", Mountpoint: "/"}),
			"/dev/sda5 would be /dev/sda2"},
		"a partition declared twice": {disk(&Media{Name: "sda1", FsType: "vfat", Mountpoint: "/boot", Size: "512M"},
			&Media{Name: "sda1", FsType: "ext4", Mountpoint: "/"}),
			"/dev/sda1 is declared twice"},
		// Listed last but numbered second: on disk it is in the middle.
		"fill not last on disk": {disk(&Media{Name: "sda1", FsType: "vfat", Mountpoint: "/boot", Size: "512M"},
			&Media{Name: "sda3", FsType: "ext4", Mountpoint: "/home", Size: "1G"},
			&Media{Name: "sda2", FsType: "ext4", Mountpoint: "/"}),
			"/dev/sda2 fills its disk but is not the last partition"},
	} {
		if _, err := tc.cfg.Plan(); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v, want %q", name, err, tc.want)
		}
	}

	reuse := func(children ...*Media) *Config {
		c := disk(children...)
		c.ReusePartitions = true
		return c
	}
	for name, tc := range map[string]struct {
		cfg  *Config
		want string
	}{
		"reuse cannot create a crypt": {reuse(&Media{Name: "sda1", Type: "crypt", FsType: "ext4", Mountpoint: "/"}),
			"reusePartitions supports only plain partitions"},
		"create with a bad start": {reuse(&Media{Name: "sda3", FsType: "ext4", Mountpoint: "/", Create: true, Start: "x", Size: "10G"}),
			"sda/sda3 start:"},
		"create with a bad size": {reuse(&Media{Name: "sda3", FsType: "ext4", Mountpoint: "/", Create: true, Start: "1000M", Size: "x"}),
			"sda/sda3 size:"},
		"only empty entries": {reuse(nil),
			"declares no partitions"},
		"reuse a partition of another disk": {reuse(&Media{Name: "sda1", FsType: "vfat", Mountpoint: "/boot", Preserve: true},
			&Media{Name: "sdb2", FsType: "ext4", Mountpoint: "/"}),
			"/dev/sdb2 is not a partition of /dev/sda"},
		"reuse a partition twice": {reuse(&Media{Name: "sda1", FsType: "vfat", Mountpoint: "/boot", Preserve: true},
			&Media{Name: "sda1", FsType: "ext4", Mountpoint: "/"}),
			"/dev/sda1 is declared twice"},
	} {
		if _, err := tc.cfg.Plan(); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v, want %q", name, err, tc.want)
		}
	}
}

func TestPlanNamesAndNumbersUnnamedPartitions(t *testing.T) {
	// A hand-written config may leave partition names out, list an empty
	// entry, or an empty RAID/LV entry: positions give the names.
	c := &Config{
		TargetMedia: []*Media{{Name: "nvme0n1", Type: "disk", Children: []*Media{
			{FsType: "vfat", Mountpoint: "/boot", Size: "512M"},
			nil,
			{Type: "lvm", VolumeGroup: "vg0", Encrypted: true, Children: []*Media{
				nil, {Name: "root", FsType: "ext4", Mountpoint: "/"},
			}},
		}}},
		Raid: []*RaidArray{nil},
	}
	pl, err := c.Plan()
	if err != nil {
		t.Fatal(err)
	}
	if pl.Parts[0].Device != "/dev/nvme0n1p1" || pl.Parts[0].Number != 1 {
		t.Errorf("first partition = %s #%d", pl.Parts[0].Device, pl.Parts[0].Number)
	}
	// The third entry is partition 2 — the empty entry is not cut, and
	// parted numbers what it cuts — an encrypted physical volume named
	// after its group.
	pv := pl.Parts[1]
	if pv.Device != "/dev/nvme0n1p2" || !pv.IsPV || !pv.Encrypted || pv.MapName != "cryptvg0" {
		t.Errorf("physical volume = %+v", pv)
	}
	if len(pl.Parts) != 3 || !pl.Parts[2].LV || pl.Parts[2].Size != storage.SizeRest {
		t.Errorf("plan = %+v", pl.Parts)
	}
}

func TestSmallHelpers(t *testing.T) {
	// The mapper name says what the volume is.
	for want, ch := range map[string]*Media{
		"cryptroot":    {Mountpoint: "/"},
		"cryptswap":    {FsType: "swap"},
		"cryptvar-log": {Mountpoint: "/var/log"},
		"cryptsda7":    {Name: "sda7"},
		"cryptvg0":     {Type: "lvm", VolumeGroup: "vg0", Mountpoint: "/"},
	} {
		if got := cryptMapName(ch); got != want {
			t.Errorf("cryptMapName(%+v) = %q, want %q", ch, got, want)
		}
	}
	if n, err := parseSize("2T"); err != nil || n != 2<<40 {
		t.Errorf("parseSize(2T) = %d, %v", n, err)
	}
	if _, err := offsetMiB("x"); err == nil {
		t.Error("offsetMiB accepted junk")
	}
	// A name with no number, or one too large to be one, uses the position.
	if got := partNumber("root", 4); got != 4 {
		t.Errorf("partNumber(root) = %d", got)
	}
	if got := partNumber("sda99999999999999999999999", 2); got != 2 {
		t.Errorf("partNumber(overflow) = %d", got)
	}
	if planDiskOf(storage.Partition{}, "/dev/sda") != "/dev/sda" || planDiskOf(storage.Partition{Disk: "/dev/sdb"}, "/dev/sda") != "/dev/sdb" {
		t.Error("planDiskOf")
	}
}

func TestLoadWithAliases(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "c.yaml")
	os.WriteFile(path, []byte(`block-devices:
- {name: disk, file: /dev/sda}
- {name: unused}
targetMedia:
- name: ${disk}
  type: disk
  children:
  - {name: "${disk}1", fstype: vfat, mountpoint: /boot, size: 512M}
  - {name: "$disk2", fstype: ext4, mountpoint: /}
`), 0o644)

	// As written, the alias points at sda.
	c, err := Load(path)
	if err != nil || c.TargetMedia[0].Name != "sda" || c.TargetMedia[0].Children[1].Name != "sda2" {
		t.Fatalf("Load: %+v, %v", c, err)
	}
	// --block-device repoints the same config at another disk, and adds an
	// alias the file does not have; the partition names follow the device.
	c, err = LoadWith(path, []*StorageAlias{{Name: "disk", File: "/dev/nvme0n1"}, {Name: "extra", File: "/dev/sdz"}})
	if err != nil {
		t.Fatal(err)
	}
	if c.TargetMedia[0].Name != "nvme0n1" || c.TargetMedia[0].Children[0].Name != "nvme0n1p1" || c.TargetMedia[0].Children[1].Name != "nvme0n1p2" {
		t.Errorf("repointed layout = %s / %s / %s", c.TargetMedia[0].Name, c.TargetMedia[0].Children[0].Name, c.TargetMedia[0].Children[1].Name)
	}
	if len(c.BlockDevices) != 3 {
		t.Errorf("block devices = %d, want the file's two plus the added one", len(c.BlockDevices))
	}

	if _, err := Load(filepath.Join(dir, "missing.yaml")); err == nil {
		t.Error("a missing file loaded")
	}
	os.WriteFile(path, []byte("targetMedia: [unterminated\n"), 0o644)
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "parsing") {
		t.Errorf("bad YAML: %v", err)
	}
}

func TestAliasExpansionEdges(t *testing.T) {
	// Aliases with no file leave the names as written; empty entries are
	// skipped.
	c := &Config{
		BlockDevices: []*StorageAlias{nil, {Name: "a"}, {File: "/dev/sda"}},
		TargetMedia:  []*Media{{Name: "$a", Type: "disk"}},
	}
	c.expandAliases()
	if c.TargetMedia[0].Name != "$a" {
		t.Errorf("an alias with no file was expanded to %q", c.TargetMedia[0].Name)
	}
	c = &Config{
		BlockDevices: []*StorageAlias{{Name: "a", File: "/dev/sda"}},
		TargetMedia:  []*Media{nil, {Name: "$a", Type: "disk", Children: []*Media{nil, {Name: "${a}1"}}}},
	}
	c.expandAliases()
	if c.TargetMedia[1].Name != "sda" || c.TargetMedia[1].Children[1].Name != "sda1" {
		t.Errorf("expansion = %s / %s", c.TargetMedia[1].Name, c.TargetMedia[1].Children[1].Name)
	}
}

func TestAliasThatPrefixesAnotherIsNotMisread(t *testing.T) {
	// "$disk2" is the alias disk2, never the alias disk followed by a 2 —
	// whatever order the aliases are visited in.
	for i := 0; i < 50; i++ {
		c := &Config{
			BlockDevices: []*StorageAlias{{Name: "disk", File: "/dev/sda"}, {Name: "disk2", File: "/dev/sdb"}},
			TargetMedia:  []*Media{{Name: "$disk2", Type: "disk"}, {Name: "$disk", Type: "disk"}},
		}
		c.expandAliases()
		if c.TargetMedia[0].Name != "sdb" || c.TargetMedia[1].Name != "sda" {
			t.Fatalf("round %d: $disk2 -> %q, $disk -> %q", i, c.TargetMedia[0].Name, c.TargetMedia[1].Name)
		}
	}
}

func TestTwoAliasesOfTheSameLength(t *testing.T) {
	c := &Config{
		BlockDevices: []*StorageAlias{{Name: "os", File: "/dev/sda"}, {Name: "db", File: "/dev/sdb"}},
		TargetMedia:  []*Media{{Name: "${os}", Type: "disk"}, {Name: "${db}", Type: "disk"}},
	}
	c.expandAliases()
	if c.TargetMedia[0].Name != "sda" || c.TargetMedia[1].Name != "sdb" {
		t.Errorf("expansion = %s, %s", c.TargetMedia[0].Name, c.TargetMedia[1].Name)
	}
}

func TestSaveReportsFailure(t *testing.T) {
	if err := Save(filepath.Join(t.TempDir(), "no-such-dir", "c.yaml"), &Config{}); err == nil || !strings.Contains(err.Error(), "config: write") {
		t.Errorf("Save into a missing directory: %v", err)
	}
}

// Administrators with no password between them would leave a system nobody
// can become root on: root is locked, sudo wants a password.
func TestAnAdministratorNeedsAPassword(t *testing.T) {
	c := btrfsConfig()
	c.Users = []*User{{Login: "ops", Admin: true, SSHKeys: []string{"ssh-ed25519 AAAA"}}, {Login: "guest", Password: "$6$s$h"}}
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "no administrator has a password (ops)") {
		t.Errorf("Validate = %v", err)
	}
	c.Users = append(c.Users, &User{Login: "root2", Admin: true, Password: "$6$s$h"})
	if err := c.Validate(); err != nil {
		t.Errorf("with an administrator who has one: %v", err)
	}
	c.Users = []*User{{Login: "guest"}}
	if err := c.Validate(); err != nil {
		t.Errorf("no administrator at all (root left as it is): %v", err)
	}
	// A config that writes its own sudoers rule (NOPASSWD for a key-only
	// admin, the usual headless setup) is trusted to.
	c.Users = []*User{{Login: "ops", Admin: true, SSHKeys: []string{"ssh-ed25519 AAAA"}}}
	c.WriteFiles = []*WriteFile{{Path: "/etc/sudoers.d/ops", Content: "ops ALL=(ALL) NOPASSWD: ALL\n"}}
	if err := c.Validate(); err != nil {
		t.Errorf("a key-only admin with a sudoers file of the config's own: %v", err)
	}
}

// `encrypted: true` where nothing reads it installed the volume in the
// clear without a word; it is refused, saying what does encrypt it.
func TestEncryptedWhereNotReadIsRefused(t *testing.T) {
	for name, tc := range map[string]struct {
		mutate func(c *Config)
		want   string
	}{
		"plain partition": {func(c *Config) { c.TargetMedia[0].Children[2].Encrypted = true }, "use type: crypt"},
		"logical volume": {func(c *Config) {
			c.TargetMedia[0].Children[2] = &Media{Name: "sda3", Type: "lvm", VolumeGroup: "vg0",
				Children: []*Media{{Name: "root", FsType: "ext4", Mountpoint: "/", Encrypted: true}}}
		}, "set encrypted on the lvm partition"},
	} {
		c := btrfsConfig()
		tc.mutate(c)
		if err := c.Validate(); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v, want %q", name, err, tc.want)
		}
	}
	// Where it is read, it is fine.
	c := btrfsConfig()
	c.TargetMedia[0].Children[2] = &Media{Name: "sda3", Type: "lvm", VolumeGroup: "vg0", Encrypted: true,
		Children: []*Media{{Name: "root", FsType: "ext4", Mountpoint: "/"}}}
	if err := c.Validate(); err != nil {
		t.Errorf("LVM on LUKS: %v", err)
	}
	raid := raidWithSpare("1", 2)
	raid.TargetMedia[1].Children[0].Encrypted = true
	if err := raid.Validate(); err == nil || !strings.Contains(err.Error(), `set encrypted on raid "md0"`) {
		t.Errorf("RAID member: %v", err)
	}
}

func TestValidSwapSize(t *testing.T) {
	for _, ok := range []string{"auto", "2G", "512M"} {
		if err := ValidSwapSize(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	for _, bad := range []string{"0", "2GB", "", "-1G"} {
		if ValidSwapSize(bad) == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}
