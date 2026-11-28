package tui

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

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
	cfg, _, err := RunText(answers(append([]string{"1", "2"}, rest...)...), &out, TextOptions{})
	if err != nil {
		t.Fatalf("Run: %v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "would break other disks") || cfg.TargetMedia[0].Name != "nvme0n1" {
		t.Errorf("the shared disk was taken (%s):\n%s", cfg.TargetMedia[0].Name, out.String())
	}

	// With --force-destructive it is the operator's to take.
	out.Reset()
	cfg, _, err = RunText(answers(append([]string{"1"}, rest...)...), &out, TextOptions{Force: true})
	if err != nil || cfg.TargetMedia[0].Name != "sdb" {
		t.Errorf("with --force-destructive: %v\n%s", err, out.String())
	}
}

// A swap that leaves the root too little of the disk was refused by the
// installer's fit check after the text flow had asked for the password;
// it is asked again at the swap size now. --skip-validation-size (or
// --skip-validation-all) turns that off, as it turns the fit check off.
func TestPromptSwapMustFitTheDisk(t *testing.T) {
	if _, err := cryptHash("probe-password"); err != nil {
		t.Skipf("no openssl to hash the password with: %v", err)
	}
	fakeLsblk(t, twoDisks) // 64 GB disks
	old := machineVolumes
	t.Cleanup(func() { machineVolumes = old })
	machineVolumes = func() ([]blockdev.Device, map[string][]string) { return nil, nil }
	tail := []string{"", "", "correct horse", "correct horse", "y"}
	for _, swap := range []string{"partition", "file"} {
		var out bytes.Buffer
		cfg, _, err := RunText(answers(append([]string{"1", "", "n", swap, "100G", "2G"}, tail...)...), &out, TextOptions{})
		if err != nil {
			t.Fatalf("%s: %v\n%s", swap, err, out.String())
		}
		got := cfg.SwapFileSize
		if swap == "partition" {
			got = cfg.TargetMedia[0].Children[1].Size
		}
		if got != "2G" || !strings.Contains(out.String(), "try again") {
			t.Errorf("%s: swap %q, a 100G swap on a 64 GB disk was taken:\n%s", swap, got, out.String())
		}

		out.Reset()
		cfg, _, err = RunText(answers(append([]string{"1", "", "n", swap, "100G"}, tail...)...), &out, TextOptions{SkipSize: true})
		if err != nil || strings.Contains(out.String(), "try again") {
			t.Errorf("%s with SkipSize: %v\n%s", swap, err, out.String())
		}
	}
}

// On a disk too small for even the default swap, an input that ends is the
// end of the flow, not the default asked again for ever.
func TestPromptSwapFitEndsWithTheInput(t *testing.T) {
	fakeLsblk(t, `{"name":"vda","path":"/dev/vda","size":3500000000,"type":"disk"}`)
	old := machineVolumes
	t.Cleanup(func() { machineVolumes = old })
	machineVolumes = func() ([]blockdev.Device, map[string][]string) { return nil, nil }
	done := make(chan error, 1)
	go func() {
		_, _, err := RunText(answers("1", "", "n", "partition"), io.Discard, TextOptions{})
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, ErrNoInput) {
			t.Errorf("err %v, want ErrNoInput", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the swap size was asked again for ever after the input ended")
	}
}
