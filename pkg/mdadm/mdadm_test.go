package mdadm

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sunlightlinux/slinstaller/pkg/storage"
)

func TestLevels(t *testing.T) {
	cases := []struct {
		level string
		min   int
	}{
		{"0", 2}, {"raid0", 2}, {"1", 2}, {"RAID1", 2},
		{"5", 3}, {"6", 4}, {"10", 4},
		{"3", 0}, {"nonsense", 0}, {"", 0},
	}
	for _, c := range cases {
		if got := MinMembers(c.level); got != c.min {
			t.Errorf("MinMembers(%q) = %d, want %d", c.level, got, c.min)
		}
		if KnownLevel(c.level) != (c.min > 0) {
			t.Errorf("KnownLevel(%q) = %v, want %v", c.level, KnownLevel(c.level), c.min > 0)
		}
	}
}

func TestCreate(t *testing.T) {
	got := strings.Join(Create("/dev/md/root", "raid1", []string{"/dev/vda2", "/dev/vdb1"}), " ")
	want := "mdadm --create /dev/md/root --run --force --metadata=1.2 --level=1 --raid-devices=2 /dev/vda2 /dev/vdb1"
	if got != want {
		t.Errorf("Create =\n  %s\nwant\n  %s", got, want)
	}
}

func TestCommandsAndUsed(t *testing.T) {
	plain := &storage.Plan{Parts: []storage.Partition{{Device: "/dev/sda1", Mountpoint: "/", Filesystem: "ext4"}}}
	if Used(plain) {
		t.Error("Used = true for a plain plan")
	}
	if len(Commands(plain)) != 0 {
		t.Error("Commands should be empty for a plain plan")
	}

	pl := &storage.Plan{Parts: []storage.Partition{
		{Device: "/dev/vda1", Mountpoint: "/boot/efi", Filesystem: "vfat"},
		{Device: "/dev/vda2", IsRaidMember: true, RaidName: "root"},
		{Device: "/dev/vdb1", IsRaidMember: true, RaidName: "root"},
		{Raid: true, RaidName: "root", RaidLevel: "1", RaidMembers: []string{"/dev/vda2", "/dev/vdb1"},
			Mountpoint: "/", Filesystem: "ext4"},
	}}
	if !Used(pl) {
		t.Fatal("Used = false for a RAID plan")
	}
	cmds := Commands(pl)
	if len(cmds) != 1 {
		t.Fatalf("got %d commands, want 1", len(cmds))
	}
	flat := strings.Join(cmds[0], " ")
	if !strings.Contains(flat, "--create /dev/md/root") || !strings.Contains(flat, "--level=1") ||
		!strings.Contains(flat, "/dev/vda2 /dev/vdb1") {
		t.Errorf("create command wrong: %s", flat)
	}
}

func TestUUIDReadsTheArrayMetadata(t *testing.T) {
	dir := t.TempDir()
	script := "#!/bin/sh\n" +
		"[ \"$FAKE_MDADM\" = fail ] && exit 1\n" +
		"echo MD_LEVEL=raid1\n" +
		"[ \"$FAKE_MDADM\" = nouuid ] || echo 'MD_UUID=aaaa:bbbb:cccc:dddd  '\n" +
		"echo MD_NAME=host:0\n"
	if err := os.WriteFile(filepath.Join(dir, "mdadm"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	if got, err := UUID("/dev/md0"); err != nil || got != "aaaa:bbbb:cccc:dddd" {
		t.Errorf("UUID = %q, %v", got, err)
	}
	// No UUID in the output, or mdadm failing, is an error: an empty
	// rd.md.uuid= would leave the initramfs unable to assemble root.
	t.Setenv("FAKE_MDADM", "nouuid")
	if got, err := UUID("/dev/md0"); err == nil {
		t.Errorf("missing MD_UUID returned %q with no error", got)
	}
	t.Setenv("FAKE_MDADM", "fail")
	if _, err := UUID("/dev/md0"); err == nil || !strings.Contains(err.Error(), "/dev/md0") {
		t.Errorf("mdadm failure: %v", err)
	}
}
