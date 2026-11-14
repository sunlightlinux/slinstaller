package storage

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeTool puts a stand-in for a command first on PATH, running script (a
// shell body). It lets the wrappers around blkid be tested without a device.
func fakeTool(t *testing.T, name, script string) string {
	t.Helper()
	dir := t.TempDir()
	body := "#!/bin/sh\necho \"$@\" >> \"" + dir + "/args\"\n" + script + "\n"
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return dir
}

func TestProbeUUIDProbesTheDeviceNotTheCache(t *testing.T) {
	dir := fakeTool(t, "blkid", `echo "  1234-ABCD  "`)
	got, err := ProbeUUID("/dev/sda2")
	if err != nil || got != "1234-ABCD" {
		t.Fatalf("ProbeUUID = %q, %v", got, err)
	}
	// -p reads the device itself: on a reused disk blkid's cache can still
	// hold the previous filesystem's UUID, which in fstab leaves the system
	// unbootable.
	args, _ := os.ReadFile(filepath.Join(dir, "args"))
	if got := strings.TrimSpace(string(args)); got != "-p -s UUID -o value /dev/sda2" {
		t.Errorf("blkid called with %q", got)
	}
}

func TestProbeUUIDFailureNamesTheDevice(t *testing.T) {
	fakeTool(t, "blkid", "exit 2")
	_, err := ProbeUUID("/dev/sda9")
	if err == nil || !strings.Contains(err.Error(), "/dev/sda9") {
		t.Errorf("error = %v, want one naming the device", err)
	}
}

func TestProbeUUIDsReadsWhereTheFilesystemLives(t *testing.T) {
	// The fake answers with the device it was asked about, so the map shows
	// which device each partition was probed at.
	dir := fakeTool(t, "blkid", `for a; do last=$a; done; echo "uuid-of-$last"`)
	pl := &Plan{Parts: []Partition{
		{Device: "/dev/sda1", Filesystem: "vfat", Mountpoint: "/boot"},
		{Device: "/dev/sda2", Filesystem: "ext4", Mountpoint: "/", Encrypted: true, MapName: "cryptroot"},
		{Device: "/dev/sda3", IsPV: true, VGName: "vg0"},
		{Filesystem: "ext4", Mountpoint: "/home", LV: true, VGName: "vg0", LVName: "home"},
		{Device: "/dev/sdb1", IsRaidMember: true, RaidName: "md0"},
		{Filesystem: "ext4", Mountpoint: "/data", Raid: true, RaidName: "md0"},
	}}
	uuids, err := ProbeUUIDs(pl)
	if err != nil {
		t.Fatal(err)
	}
	// The filesystem is on the mapper, the logical volume and the array —
	// not on the raw partition under them.
	want := map[string]string{
		"/dev/sda1":             "uuid-of-/dev/sda1",
		"/dev/mapper/cryptroot": "uuid-of-/dev/mapper/cryptroot",
		"/dev/vg0/home":         "uuid-of-/dev/vg0/home",
		"/dev/md0":              "uuid-of-/dev/md0",
	}
	if len(uuids) != len(want) {
		t.Errorf("probed %d devices, want %d: %v", len(uuids), len(want), uuids)
	}
	for dev, id := range want {
		if uuids[dev] != id {
			t.Errorf("uuid[%s] = %q, want %q", dev, uuids[dev], id)
		}
	}
	// A physical volume and a RAID member hold no mountable filesystem and
	// are not probed at all.
	args, _ := os.ReadFile(filepath.Join(dir, "args"))
	for _, raw := range []string{"/dev/sda3", "/dev/sdb1", "/dev/sda2"} {
		if strings.Contains(string(args), raw+"\n") {
			t.Errorf("blkid was run on %s:\n%s", raw, args)
		}
	}
}

func TestProbeUUIDsStopsOnTheFirstFailure(t *testing.T) {
	fakeTool(t, "blkid", "exit 2")
	if _, err := ProbeUUIDs(&Plan{Parts: []Partition{{Device: "/dev/sda1", Filesystem: "vfat"}}}); err == nil {
		t.Error("a failed probe was swallowed")
	}
}

func TestWriteFstab(t *testing.T) {
	root := t.TempDir()
	if err := WriteFstab(root, "UUID=abc / ext4 defaults 0 1\n"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(root, "etc", "fstab"))
	if err != nil || string(data) != "UUID=abc / ext4 defaults 0 1\n" {
		t.Errorf("fstab = %q, %v", data, err)
	}
	// It replaces what the base system shipped rather than appending to it.
	if err := WriteFstab(root, "second\n"); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(filepath.Join(root, "etc", "fstab")); string(data) != "second\n" {
		t.Errorf("fstab after rewrite = %q", data)
	}
}

func TestEffectiveDevice(t *testing.T) {
	for _, c := range []struct {
		p    Partition
		want string
	}{
		{Partition{Device: "/dev/sda2"}, "/dev/sda2"},
		{Partition{Device: "/dev/sda2", Encrypted: true, MapName: "cryptroot"}, "/dev/mapper/cryptroot"},
		{Partition{LV: true, VGName: "vg0", LVName: "root"}, "/dev/vg0/root"},
		{Partition{Raid: true, RaidName: "md0"}, "/dev/md0"},
		{Partition{Raid: true, RaidName: "data"}, "/dev/md/data"},
		// An array is the array even if a member's fields are also set.
		{Partition{Raid: true, RaidName: "md1", Device: "/dev/sda1", Encrypted: true, MapName: "x"}, "/dev/md1"},
	} {
		if got := c.p.EffectiveDevice(); got != c.want {
			t.Errorf("EffectiveDevice(%+v) = %q, want %q", c.p, got, c.want)
		}
	}
}

func TestESPMountpoint(t *testing.T) {
	esp := func(parts ...Partition) string { return (&Plan{Parts: parts}).ESPMountpoint() }
	if got := esp(Partition{Filesystem: "ext4", Mountpoint: "/"}, Partition{Filesystem: "vfat", Mountpoint: "/boot"}); got != "/boot" {
		t.Errorf("ESP at /boot = %q", got)
	}
	if got := esp(Partition{Filesystem: "vfat", Mountpoint: "/boot/efi"}); got != "/boot/efi" {
		t.Errorf("ESP at /boot/efi = %q", got)
	}
	// vfat elsewhere, or /boot on another filesystem, is not an ESP.
	if got := esp(Partition{Filesystem: "vfat", Mountpoint: "/data"}, Partition{Filesystem: "ext4", Mountpoint: "/boot"}); got != "" {
		t.Errorf("no ESP in the plan, got %q", got)
	}
}

func TestKnownFilesystemAndLabelLimits(t *testing.T) {
	for _, fs := range []string{"ext4", "vfat", "btrfs", "xfs", "f2fs", "swap"} {
		if !KnownFilesystem(fs) {
			t.Errorf("%s should be creatable", fs)
		}
	}
	for _, fs := range []string{"", "ntfs", "zfs", "EXT4"} {
		if KnownFilesystem(fs) {
			t.Errorf("%q should be refused before any disk is touched", fs)
		}
	}
	for fs, want := range map[string]int{
		"ext2": 16, "ext3": 16, "ext4": 16, "swap": 15, "xfs": 12,
		"f2fs": 512, "btrfs": 255, "vfat": 11, "anything": 11,
	} {
		if got := MaxLabelLength(fs); got != want {
			t.Errorf("MaxLabelLength(%q) = %d, want %d", fs, got, want)
		}
	}
}

func TestDefaultRunnerReportsWhatTheCommandPrinted(t *testing.T) {
	if err := DefaultRunner([]string{"true"}); err != nil {
		t.Errorf("true: %v", err)
	}
	if err := DefaultRunner(nil); err == nil {
		t.Error("an empty command was run")
	}
	// The command's own output is the part worth reading in the error.
	err := DefaultRunner([]string{"sh", "-c", "echo disk is busy >&2; exit 3"})
	if err == nil || !strings.Contains(err.Error(), "disk is busy") || !strings.Contains(err.Error(), "exit status 3") {
		t.Errorf("error = %v", err)
	}
	// A silent failure still fails.
	if err := DefaultRunner([]string{"false"}); err == nil {
		t.Error("false succeeded")
	}
	if err := VerboseRunner(nil); err == nil {
		t.Error("VerboseRunner ran an empty command")
	}
}
