package lvm

import (
	"strings"
	"testing"

	"github.com/sunlightlinux/slinstaller/pkg/storage"
)

func TestBuilders(t *testing.T) {
	if got := strings.Join(PVCreate("/dev/sda2"), " "); got != "pvcreate -ff -y /dev/sda2" {
		t.Errorf("PVCreate = %q", got)
	}
	if got := strings.Join(VGCreate("vg0", "/dev/sda2"), " "); got != "vgcreate vg0 /dev/sda2" {
		t.Errorf("VGCreate = %q", got)
	}
	if got := strings.Join(LVCreate("vg0", "root", "30G"), " "); got != "lvcreate -y -L 30G -n root vg0" {
		t.Errorf("LVCreate sized = %q", got)
	}
	if got := strings.Join(LVCreate("vg0", "home", storage.SizeRest), " "); got != "lvcreate -y -l 100%FREE -n home vg0" {
		t.Errorf("LVCreate fill = %q", got)
	}
}

func TestCommandsAndUsed(t *testing.T) {
	pl := &storage.Plan{Parts: []storage.Partition{
		{Device: "/dev/sda1", Mountpoint: "/boot", Filesystem: "vfat"},
		{Device: "/dev/sda2", IsPV: true, VGName: "vg0"},
		{LV: true, VGName: "vg0", LVName: "root", Mountpoint: "/", Filesystem: "ext4", Size: "30G"},
		{LV: true, VGName: "vg0", LVName: "home", Mountpoint: "/home", Filesystem: "ext4", Size: storage.SizeRest},
	}}
	if !Used(pl) {
		t.Fatal("Used = false")
	}
	cmds := Commands(pl)
	flat := ""
	for _, c := range cmds {
		flat += strings.Join(c, " ") + "\n"
	}
	for _, want := range []string{
		"pvcreate -ff -y /dev/sda2",
		"vgcreate vg0 /dev/sda2",
		"lvcreate -y -L 30G -n root vg0",
		"lvcreate -y -l 100%FREE -n home vg0",
	} {
		if !strings.Contains(flat, want) {
			t.Errorf("missing %q:\n%s", want, flat)
		}
	}

	plain := &storage.Plan{Parts: []storage.Partition{{Device: "/dev/sda1", Mountpoint: "/", Filesystem: "ext4"}}}
	if Used(plain) {
		t.Error("Used = true for a plain plan")
	}
}

func TestCommandsEncryptedPV(t *testing.T) {
	// LVM-on-LUKS: the PV is a LUKS container, so pvcreate/vgcreate must
	// target the mapper, not the raw partition.
	pl := &storage.Plan{Parts: []storage.Partition{
		{Device: "/dev/sda2", IsPV: true, Encrypted: true, MapName: "cryptvg0", VGName: "vg0"},
		{LV: true, VGName: "vg0", LVName: "root", Mountpoint: "/", Filesystem: "ext4", Size: "20G"},
	}}
	flat := ""
	for _, c := range Commands(pl) {
		flat += strings.Join(c, " ") + "\n"
	}
	for _, want := range []string{
		"pvcreate -ff -y /dev/mapper/cryptvg0",
		"vgcreate vg0 /dev/mapper/cryptvg0",
	} {
		if !strings.Contains(flat, want) {
			t.Errorf("missing %q:\n%s", want, flat)
		}
	}
	if strings.Contains(flat, "/dev/sda2") {
		t.Errorf("pvcreate/vgcreate should use the mapper, not the raw partition:\n%s", flat)
	}
}
