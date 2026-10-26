package config

import (
	"math"
	"strings"
	"testing"
)

// These fixtures are real clr-installer config files, copied unchanged
// from its test suite. That they load and plan here is the compatibility
// claim, tested rather than asserted.

func TestParseSize(t *testing.T) {
	cases := map[string]uint64{
		"157286400":  157286400,                                         // byte count
		"2147483648": 2147483648,                                        // byte count
		"150M":       150 << 20,                                         // integer + unit
		"2G":         2 << 30,                                           // integer + unit
		"1.364G":     uint64(math.Round(1.364 * float64(int64(1)<<30))), // decimal + unit
		"512B":       512,
	}
	for in, want := range cases {
		got, err := parseSize(in)
		if err != nil {
			t.Errorf("parseSize(%q): %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("parseSize(%q) = %d, want %d", in, got, want)
		}
	}
	for _, bad := range []string{"", "abc", "12Q5"} {
		if _, err := parseSize(bad); err == nil {
			t.Errorf("parseSize(%q) = nil error, want failure", bad)
		}
	}
}

func TestPartNumber(t *testing.T) {
	cases := []struct {
		name string
		want int
	}{
		{"sda1", 1}, {"sda3", 3}, {"nvme0n1p2", 2}, {"mmcblk0p10", 10}, {"sda", 99},
	}
	for _, c := range cases {
		if got := partNumber(c.name, 99); got != c.want {
			t.Errorf("partNumber(%q) = %d, want %d", c.name, got, c.want)
		}
	}
}

func TestLoadMinimalClrConfig(t *testing.T) {
	c, err := Load("testdata/valid-minimal.yaml")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}

	// Fields beyond the disk still parse.
	if c.Keyboard != "us" || c.Language != "en_US.UTF-8" || c.Kernel != "kernel-native" {
		t.Errorf("identity fields = %q/%q/%q", c.Keyboard, c.Language, c.Kernel)
	}
	if len(c.Bundles) != 2 {
		t.Errorf("bundles = %v, want 2", c.Bundles)
	}

	plan, err := c.Plan()
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if plan.Disk != "/dev/sda" || !plan.Wipe {
		t.Fatalf("plan disk/wipe = %q/%v", plan.Disk, plan.Wipe)
	}
	if len(plan.Parts) != 3 {
		t.Fatalf("got %d partitions, want 3", len(plan.Parts))
	}
	want := []struct {
		dev, fs, mnt string
	}{
		{"/dev/sda1", "vfat", "/boot"},
		{"/dev/sda2", "swap", ""},
		{"/dev/sda3", "ext4", "/"},
	}
	for i, w := range want {
		p := plan.Parts[i]
		if p.Device != w.dev || p.Filesystem != w.fs || p.Mountpoint != w.mnt {
			t.Errorf("part %d = %q/%q/%q, want %q/%q/%q",
				i, p.Device, p.Filesystem, p.Mountpoint, w.dev, w.fs, w.mnt)
		}
	}

	// The plan drives the storage layer we already have.
	cmds, err := plan.Commands()
	if err != nil {
		t.Fatalf("Commands: %v", err)
	}
	out := strings.Join([]string{}, "")
	for _, cmd := range cmds {
		out += strings.Join(cmd, " ") + "\n"
	}
	for _, line := range []string{
		"parted -s /dev/sda mklabel gpt",
		"mkfs.vfat", "mkswap /dev/sda2", "mkfs.ext4",
		"parted -s /dev/sda set 1 esp on",
	} {
		if !strings.Contains(out, line) {
			t.Errorf("command list missing %q\n%s", line, out)
		}
	}
}

func TestProbeFieldsIgnored(t *testing.T) {
	// real-example.yaml carries lsblk probe fields (model, majMin, ro,
	// rm, state) on every device. They must not break parsing.
	c, err := Load("testdata/real-example.yaml")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	plan, err := c.Plan()
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if plan.Disk != "/dev/sdc" {
		t.Errorf("plan disk = %q, want /dev/sdc", plan.Disk)
	}
}

func TestLoadUsersAndNetwork(t *testing.T) {
	c, err := Load("testdata/user-sshkeys.yaml")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(c.Users) != 1 || c.Users[0].Login != "foobar" || len(c.Users[0].SSHKeys) != 2 {
		t.Errorf("users parsed wrong: %+v", c.Users)
	}

	n, err := Load("testdata/valid-network.yaml")
	if err != nil {
		t.Fatalf("Load network: %v", err)
	}
	if len(n.NetworkInterfaces) != 1 || n.NetworkInterfaces[0].Name != "enp57s0u1u2" {
		t.Fatalf("network parsed wrong: %+v", n.NetworkInterfaces)
	}
	if len(n.NetworkInterfaces[0].Addrs) != 2 {
		t.Errorf("addrs = %d, want 2", len(n.NetworkInterfaces[0].Addrs))
	}
}

func TestLoadHooks(t *testing.T) {
	c, err := Load("testdata/valid-with-pre-post-hooks.yaml")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(c.PreInstall) != 1 || c.PreInstall[0].Cmd == "" {
		t.Errorf("pre-install parsed wrong: %+v", c.PreInstall)
	}
	if len(c.PostInstall) != 2 || !c.PostInstall[1].Chroot {
		t.Errorf("post-install parsed wrong: %+v", c.PostInstall)
	}
}

func TestBasicFourPartitions(t *testing.T) {
	c, err := Load("testdata/basic-valid-descriptor.yaml")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	plan, err := c.Plan()
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(plan.Parts) != 4 {
		t.Fatalf("got %d partitions, want 4", len(plan.Parts))
	}
	// 1.364G swap, resolved via the fractional-size path.
	if plan.Parts[1].Filesystem != "swap" {
		t.Errorf("part 2 fs = %q, want swap", plan.Parts[1].Filesystem)
	}
}

func TestPlanFillLastPartition(t *testing.T) {
	// Root with no size fills the disk → SizeRest → parted end 100%.
	c := &Config{TargetMedia: []*Media{{
		Name: "sda", Type: "disk",
		Children: []*Media{
			{Name: "sda1", Type: "part", FsType: "vfat", Mountpoint: "/boot", Size: "512M"},
			{Name: "sda2", Type: "part", FsType: "ext4", Mountpoint: "/"}, // no size = fill
		},
	}}}
	plan, err := c.Plan()
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	cmds, _ := plan.Commands()
	out := ""
	for _, cmd := range cmds {
		out += strings.Join(cmd, " ") + "\n"
	}
	if !strings.Contains(out, "mkpart / 512MiB 100%") {
		t.Errorf("fill partition did not run to 100%%:\n%s", out)
	}
}

func TestPlanFillMustBeLast(t *testing.T) {
	c := &Config{TargetMedia: []*Media{{
		Name: "sda", Type: "disk",
		Children: []*Media{
			{Name: "sda1", Type: "part", FsType: "ext4", Mountpoint: "/", Size: "0"}, // fill, not last
			{Name: "sda2", Type: "part", FsType: "ext4", Mountpoint: "/home", Size: "5G"},
		},
	}}}
	if _, err := c.Plan(); err == nil {
		t.Fatal("fill partition not last should error")
	}
}

func TestPlanEncrypted(t *testing.T) {
	c := &Config{TargetMedia: []*Media{{
		Name: "sda", Type: "disk",
		Children: []*Media{
			{Name: "sda1", Type: "part", FsType: "vfat", Mountpoint: "/boot", Size: "512M"},
			{Name: "sda2", Type: "crypt", FsType: "ext4", Mountpoint: "/"}, // encrypted root
		},
	}}}
	plan, err := c.Plan()
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	root := plan.Parts[1]
	if !root.Encrypted || root.MapName != "cryptroot" {
		t.Fatalf("root = %+v, want encrypted cryptroot", root)
	}
	if root.EffectiveDevice() != "/dev/mapper/cryptroot" {
		t.Errorf("EffectiveDevice = %q", root.EffectiveDevice())
	}
	// mkfs targets the mapper, not the raw partition.
	mkfs, _ := plan.MkfsCommands()
	flat := ""
	for _, c := range mkfs {
		for _, a := range c {
			flat += a + " "
		}
		flat += "\n"
	}
	if !strings.Contains(flat, "mkfs.ext4") || !strings.Contains(flat, "/dev/mapper/cryptroot") {
		t.Errorf("mkfs does not target the mapper:\n%s", flat)
	}
	if strings.Contains(flat, "/dev/sda2 ") {
		t.Errorf("mkfs targets the raw partition:\n%s", flat)
	}
}

func TestValidateCollectsProblems(t *testing.T) {
	c := &Config{
		Hostname:     "bad_host!",
		SwapFileSize: "notasize",
		TargetMedia: []*Media{{
			Name: "sda", Type: "disk",
			Children: []*Media{
				{Name: "sda1", Type: "crypt", FsType: "vfat", Mountpoint: "/boot"}, // encrypted ESP
				{Name: "sda2", Type: "part", FsType: "zfs", Mountpoint: "/"},       // unsupported fs
				{Name: "sda3", Type: "part", FsType: "ext4", Mountpoint: "/"},      // second root
				{Name: "sda4", Type: "part", FsType: "ext4", Mountpoint: "rel"},    // not absolute
			},
		}},
	}
	err := c.Validate()
	if err == nil {
		t.Fatal("Validate = nil, want problems")
	}
	msg := err.Error()
	for _, want := range []string{
		"EFI System Partition cannot be encrypted",
		`"zfs" is not supported`,
		"2 partitions mount /",
		"not absolute",
		"hostname",
		"swapFileSize",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("error missing %q:\n%s", want, msg)
		}
	}
}

func TestValidateAcceptsMinimalClrConfig(t *testing.T) {
	// A clr config with no users / no hostname must still validate — we
	// stay compatible and do not require fields clr allows to be absent.
	c, err := Load("testdata/valid-minimal.yaml")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if err := c.Validate(); err != nil {
		t.Errorf("minimal clr config rejected: %v", err)
	}
}

func TestValidateUsers(t *testing.T) {
	c := &Config{
		Hostname: "ok",
		TargetMedia: []*Media{{
			Name: "sda", Type: "disk",
			Children: []*Media{
				{Name: "sda1", Type: "part", FsType: "vfat", Mountpoint: "/boot"},
				{Name: "sda2", Type: "part", FsType: "ext4", Mountpoint: "/"},
			},
		}},
		Users: []*User{
			{Login: "Bad Name", Password: "$6$a$b"},    // invalid login
			{Login: "joe", Password: "plaintext"},      // plaintext, not a hash
			{Login: "joe"},                             // duplicate login
			{Login: "ok", SSHKeys: []string{"ssh..."}}, // fine: ssh-key, no password
		},
	}
	err := c.Validate()
	if err == nil {
		t.Fatal("Validate = nil, want user problems")
	}
	msg := err.Error()
	for _, want := range []string{"valid Unix name", "crypt(3) hash", "used more than once"} {
		if !strings.Contains(msg, want) {
			t.Errorf("missing %q:\n%s", want, msg)
		}
	}
}

func TestValidateNetwork(t *testing.T) {
	base := func(ifaces []*Interface) *Config {
		return &Config{
			TargetMedia: []*Media{{
				Name: "sda", Type: "disk",
				Children: []*Media{
					{Name: "sda1", Type: "part", FsType: "vfat", Mountpoint: "/boot"},
					{Name: "sda2", Type: "part", FsType: "ext4", Mountpoint: "/"},
				},
			}},
			NetworkInterfaces: ifaces,
		}
	}

	// A static interface with a bad IP, a bad netmask and a bad gateway.
	err := base([]*Interface{{
		Name:    "eth0",
		Addrs:   []*Address{{IP: "nope", Netmask: "255.255.255.0"}, {IP: "10.0.0.5", Netmask: "junk"}},
		Gateway: "not-an-ip",
	}}).Validate()
	if err == nil {
		t.Fatal("Validate = nil, want network problems")
	}
	for _, want := range []string{"invalid IP", "invalid netmask", "gateway"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("missing %q:\n%s", want, err.Error())
		}
	}

	// A DHCP interface (dhcp:true) with no addresses is fine, and so is a
	// clean static one.
	if err := base([]*Interface{
		{Name: "eth0", DHCP: "true"},
		{Name: "eth1", Addrs: []*Address{{IP: "192.168.0.2", Netmask: "255.255.255.0"}}, Gateway: "192.168.0.1"},
	}).Validate(); err != nil {
		t.Errorf("valid interfaces rejected: %v", err)
	}
}

func TestPlanLVM(t *testing.T) {
	c := &Config{
		Hostname: "box",
		Users:    []*User{{Login: "me", Password: "$6$a$b", Admin: true}},
		TargetMedia: []*Media{{
			Name: "sda", Type: "disk",
			Children: []*Media{
				{Name: "sda1", Type: "part", FsType: "vfat", Mountpoint: "/boot", Size: "512M"},
				{Name: "sda2", Type: "lvm", Size: "rest", VolumeGroup: "vg0", Children: []*Media{
					{Name: "root", FsType: "ext4", Mountpoint: "/", Size: "30G"},
					{Name: "home", FsType: "ext4", Mountpoint: "/home"}, // fill
				}},
			},
		}},
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	plan, err := c.Plan()
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	// ESP + PV + 2 LVs.
	if len(plan.Parts) != 4 {
		t.Fatalf("got %d parts, want 4", len(plan.Parts))
	}
	pv := plan.Parts[1]
	if !pv.IsPV || pv.VGName != "vg0" || pv.Device != "/dev/sda2" {
		t.Fatalf("PV = %+v", pv)
	}
	root := plan.Parts[2]
	if !root.LV || root.LVName != "root" || root.EffectiveDevice() != "/dev/vg0/root" {
		t.Fatalf("root LV = %+v", root)
	}
	if plan.Parts[3].Size != "rest" { // fill LV keeps SizeRest
		t.Errorf("home LV size = %q, want rest", plan.Parts[3].Size)
	}
}
