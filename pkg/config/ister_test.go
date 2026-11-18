package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const descriptor = `{
  "DestinationType": "physical",
  "PartitionLayout": [
    {"disk": "sda", "partition": 2, "size": "2G",   "type": "swap"},
    {"disk": "sda", "partition": 1, "size": "512M", "type": "EFI"},
    {"disk": "sda", "partition": 3, "size": "rest", "type": "linux"}],
  "FilesystemTypes": [
    {"disk": "sda", "partition": 1, "type": "vfat"},
    {"disk": "sda", "partition": 2, "type": "swap"},
    {"disk": "sda", "partition": 3, "type": "ext4", "options": "-b 4096"}],
  "PartitionMountPoints": [
    {"disk": "sda", "partition": 1, "mount": "/boot"},
    {"disk": "sda", "partition": 3, "mount": "/"}],
  "Version": 31000,
  "Bundles": ["os-core", "kernel-lts", "openssh-server"],
  "Users": [{"username": "ion", "key": "ssh-ed25519 AAAA", "uid": 1001, "sudo": true, "password": "$6$a$b"}],
  "Hostname": "box",
  "Static_IP": {"iface": "eth0", "address": "10.0.0.2/24", "gateway": "10.0.0.1", "dns": "10.0.0.53"},
  "PostNonChroot": ["/srv/after.sh"],
  "PostNonChrootShell": ["echo done"],
  "HTTPSProxy": "http://proxy:3128",
  "MirrorURL": "https://mirror.example/current",
  "cmdline": "quiet  console=ttyS0",
  "VersionURL": "https://example.org/version"
}`

func writeDescriptor(t *testing.T, text string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "old.json")
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestFromJSONDescriptor(t *testing.T) {
	c, notes, err := FromJSONDescriptor(writeDescriptor(t, descriptor))
	if err != nil {
		t.Fatal(err)
	}
	// The disk is an alias, so the result is pointed at a machine by
	// changing one line; its partitions are in on-disk order whatever the
	// descriptor's.
	if len(c.BlockDevices) != 1 || c.BlockDevices[0].Name != "sda" || c.BlockDevices[0].File != "/dev/sda" {
		t.Errorf("alias = %+v", c.BlockDevices)
	}
	d := c.TargetMedia[0]
	if d.Name != "${sda}" || len(d.Children) != 3 {
		t.Fatalf("disk = %+v", d)
	}
	for i, want := range []Media{
		{Name: "${sda}1", Size: "512M", FsType: "vfat", Mountpoint: "/boot"},
		{Name: "${sda}2", Size: "2G", FsType: "swap"},
		{Name: "${sda}3", Size: "0", FsType: "ext4", Mountpoint: "/", Options: "-b 4096"},
	} {
		got := d.Children[i]
		if got.Name != want.Name || got.Size != want.Size || got.FsType != want.FsType || got.Mountpoint != want.Mountpoint || got.Options != want.Options {
			t.Errorf("partition %d = %+v, want %+v", i+1, got, want)
		}
	}
	if c.Kernel != "kernel-lts" || strings.Join(c.Bundles, ",") != "os-core,openssh-server" {
		t.Errorf("kernel %q, bundles %v", c.Kernel, c.Bundles)
	}
	u := c.Users[0]
	if u.Login != "ion" || !u.Admin || u.Password != "$6$a$b" || u.SSHKeys[0] != "ssh-ed25519 AAAA" {
		t.Errorf("user = %+v", u)
	}
	ni := c.NetworkInterfaces[0]
	if ni.Name != "eth0" || ni.Addrs[0].IP != "10.0.0.2" || ni.Addrs[0].Netmask != "24" || ni.Gateway != "10.0.0.1" || ni.DNS != "10.0.0.53" || !ni.Static() {
		t.Errorf("interface = %+v", ni)
	}
	if c.PostInstall[0].Cmd != "/srv/after.sh" || c.PostInstall[1].Cmd != "/bin/bash -c echo done" || c.PostInstall[0].Chroot {
		t.Errorf("hooks = %+v, %+v", c.PostInstall[0], c.PostInstall[1])
	}
	if strings.Join(c.KernelArguments.Add, "|") != "quiet|console=ttyS0" || c.Hostname != "box" || c.Version != 31000 ||
		c.SwupdMirror != "https://mirror.example/current" || c.HTTPSProxy != "http://proxy:3128" {
		t.Errorf("carried fields: %+v", c)
	}
	if len(notes) != 1 || !strings.Contains(notes[0], "VersionURL") {
		t.Errorf("notes = %v", notes)
	}

	// What it converts to is a config that loads, validates and plans.
	out := filepath.Join(t.TempDir(), "new.yaml")
	if err := Save(out, c); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(out)
	if err != nil {
		t.Fatal(err)
	}
	if err := loaded.Validate(); err != nil {
		t.Fatalf("the converted config does not validate: %v", err)
	}
	plan, err := loaded.Plan()
	if err != nil || plan.Disk != "/dev/sda" || plan.Parts[2].Device != "/dev/sda3" {
		t.Errorf("plan: %v, %+v", err, plan)
	}
}

func TestFromJSONDescriptorRefusals(t *testing.T) {
	for name, tc := range map[string]struct {
		text, want string
	}{
		"not JSON":         {`{"DestinationType": `, "parsing"},
		"destination":      {`{"DestinationType": "cloud", "PartitionLayout": [{"disk": "sda", "partition": 1}]}`, "is neither physical nor virtual"},
		"twice":            {`{"DestinationType": "physical", "PartitionLayout": [{"disk": "sda", "partition": 1}, {"disk": "sda", "partition": 1}]}`, "is defined twice"},
		"fs for nothing":   {`{"DestinationType": "physical", "PartitionLayout": [{"disk": "sda", "partition": 1}], "FilesystemTypes": [{"disk": "sda", "partition": 2, "type": "ext4"}]}`, "FilesystemTypes names partition 2 of sda"},
		"mount for no one": {`{"DestinationType": "physical", "PartitionLayout": [{"disk": "sda", "partition": 1}], "PartitionMountPoints": [{"disk": "sdb", "partition": 1, "mount": "/"}]}`, "PartitionMountPoints names partition 1 of sdb"},
		"address":          {`{"DestinationType": "physical", "Static_IP": {"iface": "eth0", "address": "10.0.0.2"}}`, "is not in address/prefix form"},
	} {
		_, _, err := FromJSONDescriptor(writeDescriptor(t, tc.text))
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: error %v, want %q", name, err, tc.want)
		}
	}
	if _, _, err := FromJSONDescriptor(filepath.Join(t.TempDir(), "absent.json")); err == nil {
		t.Error("a missing descriptor was converted")
	}
	// An image file and a BIOS layout are converted, with what to change said.
	c, notes, err := FromJSONDescriptor(writeDescriptor(t, `{"DestinationType": "virtual", "LegacyBios": true,
		"PartitionLayout": [{"disk": "cloud.img", "partition": 1, "size": "4G"}]}`))
	if err != nil || c.BlockDevices[0].Name != "cloud" || c.BlockDevices[0].File != "cloud.img" || c.TargetMedia[0].Name != "${cloud}" {
		t.Fatalf("virtual: %v, %+v", err, c)
	}
	all := strings.Join(notes, "\n")
	if !strings.Contains(all, "is an image file") || !strings.Contains(all, "UEFI only") {
		t.Errorf("notes = %v", notes)
	}
}
