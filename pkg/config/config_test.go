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
		"sgdisk /dev/sda --typecode=1:C12A7328-F81F-11D2-BA4B-00A0C93EC93B",
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
