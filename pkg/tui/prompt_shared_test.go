package tui

import (
	"bytes"
	"strings"
	"testing"

	"github.com/sunlightlinux/slinstaller/pkg/blockdev"
)

// The serial text flow offered a disk that shares a volume group with
// another one; the installer then refused it - unless --force-destructive -
// after every question, the password included, had been answered. It is
// refused when it is picked now, and another one can be.
func TestPromptRefusesASharedDisk(t *testing.T) {
	if _, err := cryptHash("probe-password"); err != nil {
		t.Skipf("no openssl to hash the password with: %v", err)
	}
	fakeLsblk(t, twoDisks)
	old := machineVolumes
	t.Cleanup(func() { machineVolumes = old })
	machineVolumes = func() ([]blockdev.Device, map[string][]string) {
		devs := []blockdev.Device{
			{Name: "sda", Path: "/dev/sda", Type: "disk", Size: 64e9, Children: []blockdev.Device{
				{Name: "sda2", Path: "/dev/sda2", Type: "part", FsType: "LVM2_member"}}},
			{Name: "sdb", Path: "/dev/sdb", Type: "disk", Size: 64e9, Children: []blockdev.Device{
				{Name: "sdb1", Path: "/dev/sdb1", Type: "part", FsType: "LVM2_member"}}},
			{Name: "nvme0n1", Path: "/dev/nvme0n1", Type: "disk", Size: 64e9},
		}
		return devs, map[string][]string{"data": {"/dev/sda2", "/dev/sdb1"}}
	}
	rest := []string{"", "n", "none", "", "", "correct horse", "correct horse", "y"}

	var out bytes.Buffer
	cfg, _, err := RunText(answers(append([]string{"1", "2"}, rest...)...), &out, false)
	if err != nil {
		t.Fatalf("Run: %v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "would break other disks") || cfg.TargetMedia[0].Name != "nvme0n1" {
		t.Errorf("the shared disk was taken (%s):\n%s", cfg.TargetMedia[0].Name, out.String())
	}

	// With --force-destructive it is the operator's to take.
	out.Reset()
	cfg, _, err = RunText(answers(append([]string{"1"}, rest...)...), &out, true)
	if err != nil || cfg.TargetMedia[0].Name != "sdb" {
		t.Errorf("with --force-destructive: %v\n%s", err, out.String())
	}
}
