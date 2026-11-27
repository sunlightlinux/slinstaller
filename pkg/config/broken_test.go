package config

import (
	"fmt"
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
			"/dev/sda5 is partition 5 of /dev/sda, but a new table numbers its partitions 1, 2, 3… in order: rename it sda2"},
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
	for _, bad := range []string{"0", "2Gi", "", "-1G"} {
		if ValidSwapSize(bad) == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

// useradd refuses a full name with a colon in it; that is caught before
// the disk is wiped, not at first boot.
func TestFullNameUseraddAccepts(t *testing.T) {
	c := btrfsConfig()
	c.Users = []*User{{Login: "ion", UserName: "Doe: J", Admin: true, Password: "$6$s$h"}}
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "cannot hold a colon") {
		t.Errorf("Validate = %v", err)
	}
	c.Users[0].UserName = "Ion Doe"
	if err := c.Validate(); err != nil {
		t.Errorf("a plain full name: %v", err)
	}
}

// Volume group and logical volume names LVM refuses — and a space breaks
// the /dev path in fstab — are refused before the disk is wiped.
func TestLVMNames(t *testing.T) {
	lvm := func(vg, lv string) *Config {
		return &Config{TargetMedia: []*Media{{Name: "sda", Type: "disk", Children: []*Media{
			{Name: "sda1", Type: "part", FsType: "vfat", Mountpoint: "/boot", Size: "512M"},
			{Name: "sda2", Type: "lvm", VolumeGroup: vg, Children: []*Media{{Name: lv, FsType: "ext4", Mountpoint: "/"}}},
		}}}}
	}
	for vg, lv := range map[string]string{"my vg": "root", "vg0": "root vol", "-vg": "root", "vg1": "-x",
		".": "root", "..": "root", "vg2": "snapshot", "vg3": "pvmove1", "vg4": "a_mlog"} {
		if err := lvm(vg, lv).Validate(); err == nil || !strings.Contains(err.Error(), "not one LVM takes") {
			t.Errorf("vg %q lv %q: %v", vg, lv, err)
		}
	}
	if err := lvm("vg_0.sys", "root-1").Validate(); err != nil {
		t.Errorf("good names: %v", err)
	}
}

// A logical volume's size is checked like a partition's, and a bare number
// — bytes in a config — reaches lvcreate as bytes, not as its default MiB.
func TestLVSizes(t *testing.T) {
	lv := func(size string) *Config {
		return &Config{TargetMedia: []*Media{{Name: "sda", Type: "disk", Children: []*Media{
			{Name: "sda1", Type: "part", FsType: "vfat", Mountpoint: "/boot", Size: "512M"},
			{Name: "sda2", Type: "lvm", VolumeGroup: "vg0", Children: []*Media{
				{Name: "data", FsType: "ext4", Mountpoint: "/data", Size: size},
				{Name: "root", FsType: "ext4", Mountpoint: "/"}}},
		}}}}
	}
	if err := lv("10GX").Validate(); err == nil || !strings.Contains(err.Error(), `size "10GX" is not a byte count`) {
		t.Errorf("10GX: %v", err)
	}
	// Every size without a unit lvcreate reads as the config does: in MiB.
	for in, want := range map[string]string{"1500000000": "1431m", "2e9": "1908m", "1048576.5": "2m", "10G": "10G", "10.5G": "10.5G", "512m": "512m",
		"10GiB": "10240m", "18446744073709549568": "17592186044416m"} { // near 2^64: no wrap to 0m
		pl, err := lv(in).Plan()
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range pl.Parts {
			if p.LV && p.LVName == "data" && p.Size != want {
				t.Errorf("LV size %q reaches lvcreate as %q, want %q", in, p.Size, want)
			}
		}
	}
	pl, err := lv("1073741824").Plan()
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range pl.Parts {
		if p.LV && p.LVName == "data" && p.Size != "1024m" {
			t.Errorf("bare-number LV size = %q", p.Size)
		}
	}
}

func TestSizeHelpers(t *testing.T) {
	if n, err := SizeBytes("59.5G"); err != nil || n != uint64(59.5*(1<<30)) {
		t.Errorf("SizeBytes = %d, %v", n, err)
	}
	if min, label := (&Config{}).RootMinimum(); min != storage.MinRootSize || label == "" {
		t.Errorf("RootMinimum = %d %q", min, label)
	}
	if got := CurlFetch("https://x/i.raw", false); !strings.Contains(got, "--proto-redir =https") {
		t.Errorf("https: %q", got)
	}
	if got := CurlFetch("https://x/i.raw", true); strings.Contains(got, "proto-redir") {
		t.Errorf("https with plain http allowed: %q", got)
	}
}

// A volume group over an encrypted and a plain physical volume would put
// some of what is on it — possibly the initramfs keyfile — in the clear.
func TestVolumeGroupIsEncryptedWhole(t *testing.T) {
	c := &Config{TargetMedia: []*Media{
		{Name: "vda", Type: "disk", Children: []*Media{
			{Name: "vda1", Type: "part", FsType: "vfat", Mountpoint: "/boot/efi", Size: "512M"},
			{Name: "vda2", Type: "lvm", VolumeGroup: "vg0", Encrypted: true, Children: []*Media{{Name: "root", FsType: "ext4", Mountpoint: "/"}}}}},
		{Name: "vdb", Type: "disk", Children: []*Media{
			{Name: "vdb1", Type: "lvm", VolumeGroup: "vg0"}}},
	}}
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "volume group vg0 has an encrypted physical volume (vda2) and a plain one (vdb1)") {
		t.Errorf("Validate = %v", err)
	}
	c.TargetMedia[1].Children[0].Encrypted = true
	if err := c.Validate(); err != nil {
		t.Errorf("all encrypted: %v", err)
	}
}

// An offline install fetches nothing: an image named by URL is refused.
func TestOfflineRefusesRemoteImages(t *testing.T) {
	c := btrfsConfig()
	c.Offline, c.Image = true, "https://img.example/base.tar.zst"
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "image: https://img.example/base.tar.zst is a URL, but the install is offline") {
		t.Errorf("Validate = %v", err)
	}
	c.Image = "/run/media/base.tar.zst"
	if err := c.Validate(); err != nil {
		t.Errorf("a local image offline: %v", err)
	}
}

// The mirror is an https URL, plain http with the allowance, or a local
// directory; anything else was a relative path to xbps.
func TestMirrorForms(t *testing.T) {
	for mirror, want := range map[string]string{
		"https://repo.example/current": "",
		"/run/media/repo":              "",
		"http://repo.example/current":  "is plain http",
		"repo.example/current":         "is not an https:// URL",
		"HTTPS://repo.example":         "is not an https:// URL",
	} {
		c := btrfsConfig()
		c.SwupdMirror = mirror
		err := c.Validate()
		if want == "" && err != nil || want != "" && (err == nil || !strings.Contains(err.Error(), want)) {
			t.Errorf("%q: %v, want %q", mirror, err, want)
		}
	}
	c := btrfsConfig()
	c.SwupdMirror, c.AllowInsecureHTTP = "http://repo.example/current", true
	if err := c.Validate(); err != nil {
		t.Errorf("http with the allowance: %v", err)
	}
}

// A saved config is private whatever was at its path before, and a link
// there is replaced, not written through.
func TestSaveIsPrivate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "c.yaml")
	os.WriteFile(path, []byte("old\n"), 0o644)
	c := &Config{Users: []*User{{Login: "ion", Password: "$6$s$h"}}}
	if err := Save(path, c); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
		t.Errorf("mode %v over an existing 0644 file", fi.Mode().Perm())
	}
	elsewhere := filepath.Join(t.TempDir(), "target")
	os.WriteFile(elsewhere, []byte("keep\n"), 0o644)
	link := filepath.Join(dir, "l.yaml")
	os.Symlink(elsewhere, link)
	if err := Save(link, c); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(elsewhere); string(got) != "keep\n" {
		t.Error("written through a link")
	}
}

func TestSecretName(t *testing.T) {
	for n, want := range map[string]bool{"API_TOKEN": true, "db_password": true, "WIFI_PSK": true, "ssh_key": true, "API_URL": false, "HOSTNAME": false,
		"KEYMAP": false, "KEYBOARD": false, "AUTHOR": false, "BYPASS_CACHE": false, "SSH_KEY_PATH": false, "MY_SECRET": true, "authToken": true, "dbPassword": true,
		"API_TOKENS": true, "CLIENT_SECRETS": true, "SSH_KEYS": true, "DB_PASSWORDS": true, "sshPrivateKey": true, "privateKey": true,
		"deployKey": true, "SECRETKEY": true, "dbPass": true, "DBPASS": true, "passwordHash": true, "tokenValue": true, "GPG_PRIVKEY": true,
		"KEYRING": false, "KEYMAPS": false, "LANGUAGE": false, "DEBUG": false, "ADDRESS": false,
		"token.value": true, "AWS_CRED": true, "DB_PWD": true, "SECRET_DIR": false, "WIFIPSK": true} {
		if SecretName(n) != want {
			t.Errorf("%s: %v", n, !want)
		}
	}
}

// Kernel arguments GRUB's script parser would break are refused.
func TestKernelArgsGrubSafe(t *testing.T) {
	for arg, bad := range map[string]bool{"quiet": false, `dyndbg="file x +p"`: false, "q=it's": true, "a=$y": true, "a;b": true, `a\b`: true, "a`b`": true} {
		c := btrfsConfig()
		c.KernelArguments = &KernelArguments{Add: []string{arg}}
		err := c.Validate()
		if bad != (err != nil && strings.Contains(err.Error(), "GRUB's config parsing")) {
			t.Errorf("%q: %v", arg, err)
		}
	}
}

// The swap file lives on the root, so the root's minimum includes it: a
// swap file the disk cannot hold is refused before anything is touched.
func TestSwapFileCountsAgainstRoot(t *testing.T) {
	c := btrfsConfig()
	c.SwapFileSize = "999T"
	min, label := c.RootMinimum()
	if min < 999<<40 || !strings.Contains(label, "plus the 999T swap file") {
		t.Errorf("minimum %d %q", min, label)
	}
	pl, err := c.Plan()
	if err != nil {
		t.Fatal(err)
	}
	sixty := func(string) (uint64, bool) { return 60 << 30, true }
	if err := pl.CheckFit(sixty, min, label); err == nil || !strings.Contains(err.Error(), "swap file") {
		t.Errorf("999T swap file on 60 GiB: %v", err)
	}
	c.SwapFileSize = "4G"
	min, label = c.RootMinimum()
	if err := pl.CheckFit(sixty, min, label); err != nil {
		t.Errorf("4G swap file on 60 GiB: %v", err)
	}
}

// A volume group's fixed-size volumes have to fit its physical volumes.
func TestLVsFitTheirVG(t *testing.T) {
	mk := func(pvSize, lvSize string, encrypted bool) *Config {
		return &Config{TargetMedia: []*Media{{Name: "sda", Type: "disk", Children: []*Media{
			{Name: "sda1", Type: "part", FsType: "vfat", Mountpoint: "/boot", Size: "512M"},
			{Name: "sda2", Type: "lvm", VolumeGroup: "vg0", Size: pvSize, Encrypted: encrypted, Children: []*Media{
				{Name: "root", FsType: "ext4", Mountpoint: "/", Size: lvSize},
				{Name: "home", FsType: "ext4", Mountpoint: "/home"},
			}},
		}}}}
	}
	disk := func(string) (uint64, bool) { return 64 << 30, true }
	for _, c := range []struct {
		pv, lv string
		enc    bool
		ok     bool
	}{
		{"", "500G", false, false}, // the PV fills a 64G disk
		{"20G", "30G", false, false},
		{"", "40G", true, true},
		{"20G", "20G", true, false}, // the LUKS header and LVM metadata take their share
		{"20G", "19G", true, true},
	} {
		pl, err := mk(c.pv, c.lv, c.enc).Plan()
		if err != nil {
			t.Fatalf("%+v: %v", c, err)
		}
		err = pl.CheckFit(disk, 2<<30, "2 GB")
		if c.ok != (err == nil) || err != nil && !strings.Contains(err.Error(), "logical volumes of vg0") {
			t.Errorf("%+v: %v", c, err)
		}
	}
}

// An array's name is one component under /dev/md: "../sdz" is refused.
func TestRaidNamePath(t *testing.T) {
	for name, ok := range map[string]bool{"root": true, "md0": true, "data.1": true, "../sdz": false, "a/b": false, "..": false, ".x": false,
		strings.Repeat("a", 32): true, strings.Repeat("a", 33): false} {
		c := raidConfig()
		c.Raid[0].Name = name
		for _, d := range c.TargetMedia {
			for _, ch := range d.Children {
				if ch.Type == "raid" {
					ch.Raid = name
				}
			}
		}
		err := c.Validate()
		if bad := err != nil && strings.Contains(err.Error(), "an array name is"); bad == ok {
			t.Errorf("%q: %v", name, err)
		}
		// skipValidationAll turns Validate off, not this: the plan
		// itself refuses a name that leaves /dev/md.
		c.SkipValidationAll = true
		pl, err := c.Plan()
		if bad := err != nil && strings.Contains(err.Error(), "an array name is"); bad == ok {
			t.Errorf("%q, skipValidationAll: %v", name, err)
		}
		if pl != nil {
			cmds, _ := pl.MkfsCommands()
			if s := fmt.Sprint(cmds, pl.TableCommands()); strings.Contains(s, "sdz") {
				t.Errorf("%q, skipValidationAll: planned %s", name, s)
			}
		}
	}
}

func TestDiskImageTarget(t *testing.T) {
	one := func() *Config {
		return &Config{DiskImage: "https://img.example/disk.raw.zst", TargetMedia: []*Media{{Name: "sda", Type: "disk"}}}
	}
	if d, err := one().DiskImageTarget(); err != nil || d.Name != "sda" {
		t.Errorf("one disk: %v", err)
	}
	for want, mod := range map[string]func(*Config){
		"plain http":       func(c *Config) { c.DiskImage = "http://img/x" },
		"not an https URL": func(c *Config) { c.DiskImage = "ftp://img/x" },
		"names 2 entries":  func(c *Config) { c.TargetMedia = append(c.TargetMedia, &Media{Name: "sdb"}) },
		"a layout cannot":  func(c *Config) { c.TargetMedia[0].Children = []*Media{{Name: "sda1"}} },
		"needs a name":     func(c *Config) { c.TargetMedia[0].Name = "" },
		"not both":         func(c *Config) { c.Image = "/x.tar" },
	} {
		c := one()
		mod(c)
		if _, err := c.DiskImageTarget(); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v", want, err)
		}
	}
	c := one()
	c.DiskImage, c.AllowInsecureHTTP = "http://img/x", true
	if _, err := c.DiskImageTarget(); err != nil {
		t.Errorf("http allowed: %v", err)
	}
	c.DiskImage = "/local/disk.raw"
	if _, err := c.DiskImageTarget(); err != nil {
		t.Errorf("a file: %v", err)
	}
}

// A disk is /dev/<name>: with Validate switched off, the plan still
// refuses a name that is a path or nothing, which would partition /dev/sdz
// ("../sdz", partitions "../sdz1") or /dev/ itself.
func TestPlanDiskNameSkipValidationAll(t *testing.T) {
	for _, name := range []string{"../sdz", "", "..", "mapper/x"} {
		c := &Config{SkipValidationAll: true, TargetMedia: []*Media{{Name: name, Type: "disk", Children: []*Media{
			{Name: name + "1", Type: "part", FsType: "vfat", Mountpoint: "/boot/efi", Size: "512M"},
			{Name: name + "2", Type: "part", FsType: "ext4", Mountpoint: "/"},
		}}}}
		if _, err := c.Plan(); err == nil || !strings.Contains(err.Error(), "not a path") {
			t.Errorf("disk %q: %v", name, err)
		}
	}
}

// skipValidationAll does not let a value reach a file the installed
// system runs as code: an interface name or bond mode goes unquoted into
// /etc/rc.local, a line break in a network field starts a new dhcpcd.conf
// directive, one in the hostname a new /etc/hosts entry.
func TestSkipValidationAllKeepsWrittenChecks(t *testing.T) {
	for what, mod := range map[string]func(c *Config){
		"interface name": func(c *Config) {
			c.NetworkInterfaces = []*Interface{{Name: "br0;reboot", Bridge: &Bridge{Members: []string{"eth0"}}}}
		},
		"bond member": func(c *Config) {
			c.NetworkInterfaces = []*Interface{{Name: "bond0", Bond: &Bond{Members: []string{"eth0$(id)"}}}}
		},
		"bond mode": func(c *Config) {
			c.NetworkInterfaces = []*Interface{{Name: "bond0", Bond: &Bond{Members: []string{"eth0"}, Mode: "802.3ad;id"}}}
		},
		"VLAN link":      func(c *Config) { c.NetworkInterfaces = []*Interface{{Name: "v5", VLAN: &VLAN{ID: 5, Link: "`id`"}}} },
		"dns line break": func(c *Config) { c.NetworkInterfaces = []*Interface{{Name: "eth0", DNS: "1.1.1.1\nscript /tmp/x"}} },
		// The break first: nothing before it to stop at.
		"dns starting with a break": func(c *Config) { c.NetworkInterfaces = []*Interface{{Name: "eth0", DNS: "\nscript /tmp/x"}} },
		"route": func(c *Config) {
			c.NetworkInterfaces = []*Interface{{Name: "eth0", Routes: []*Route{{To: "10.0.0.0/8\nx", Via: "1.1.1.1"}}}}
		},
		"hostname break": func(c *Config) { c.Hostname = "box\n1.2.3.4 mirror.example" },
		"address netmask": func(c *Config) {
			c.NetworkInterfaces = []*Interface{{Name: "eth0", Addrs: []*Address{{IP: "10.0.0.2", Netmask: "8\r"}}}}
		},
	} {
		c := linkedConfig()
		c.SkipValidationAll = true
		if err := c.Validate(); err != nil {
			t.Fatalf("%s: the plain config refused: %v", what, err)
		}
		mod(c)
		if err := c.Validate(); err == nil {
			t.Errorf("%s: passed with skipValidationAll", what)
		}
	}
	// What it is for still goes through: a layout Validate would refuse.
	// A space is no control character: two search domains pass.
	c := linkedConfig()
	c.SkipValidationAll = true
	c.NetworkInterfaces[2].Domain = "example.com corp.example"
	c.TargetMedia[0].Children = c.TargetMedia[0].Children[1:] // no ESP
	if err := c.Validate(); err != nil {
		t.Errorf("an unusual layout refused with skipValidationAll: %v", err)
	}
}

// What goes into fstab, crypttab or the kernel command line as one field
// is checked by Plan, which skipValidationAll does not skip: crypt-options
// "a, discard" was two kernel arguments and a crypttab line with a field
// too many; a space in mount-options or a mountpoint the same in fstab.
func TestPlanRefusesFieldsWithSpacesUnderSkipValidationAll(t *testing.T) {
	for name, set := range map[string]func(*Media){
		"crypt-options": func(m *Media) { m.Type, m.CryptOptions = "crypt", "discard, no-read-workqueue" },
		"mount-options": func(m *Media) { m.MountOptions = "noatime, nodev" },
		"mountpoint":    func(m *Media) { m.Mountpoint = "/srv/my data" },
		"line break":    func(m *Media) { m.MountOptions = "noatime\n/dev/sdz / ext4" },
	} {
		c := &Config{SkipValidationAll: true, TargetMedia: []*Media{{Name: "sda", Type: "disk", Children: []*Media{
			{Name: "sda1", Type: "part", FsType: "vfat", Mountpoint: "/boot/efi", Size: "512M"},
			{Name: "sda2", Type: "part", FsType: "ext4", Mountpoint: "/srv", Size: "1G"},
			{Name: "sda3", Type: "part", FsType: "ext4", Mountpoint: "/"},
		}}}}
		set(c.TargetMedia[0].Children[1])
		if c.Validate() != nil {
			t.Fatalf("%s: refused by Validate under skipValidationAll, not what is tested", name)
		}
		if _, err := c.Plan(); err == nil || !strings.Contains(err.Error(), "space or a control character") {
			t.Errorf("%s: Plan: %v", name, err)
		}
	}
}
