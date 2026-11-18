package storage

import (
	"strings"
	"testing"
)

func btrfsPlan() *Plan {
	return &Plan{Parts: []Partition{
		{Device: "/dev/sda1", Filesystem: "vfat", Mountpoint: "/boot", MountOptions: "umask=0077"},
		{Device: "/dev/sda2", Filesystem: "swap", MountOptions: "discard,pri=10"},
		{Device: "/dev/sda3", Filesystem: "btrfs", Mountpoint: "/", Subvol: "@",
			MountOptions: "noatime,compress=zstd,x-gvfs-hide",
			Subvolumes:   []Subvolume{{"@home", "/home"}, {"@", "/"}, {"@log", "/var/log"}}},
		{Device: "/dev/sda4", Filesystem: "ext4", Mountpoint: "/srv"},
	}}
}

func TestMountOrderExpandsSubvolumes(t *testing.T) {
	var got []string
	for _, p := range btrfsPlan().MountOrder() {
		got = append(got, p.Mountpoint+"="+p.EffectiveDevice()+":"+p.Subvol)
	}
	// One mount per subvolume from the same device, root first, deeper last.
	want := "/=/dev/sda3:@ /boot=/dev/sda1: /home=/dev/sda3:@home /srv=/dev/sda4: /var/log=/dev/sda3:@log"
	if strings.Join(got, " ") != want {
		t.Errorf("mount order:\n got %s\nwant %s", strings.Join(got, " "), want)
	}
}

func TestFstabCarriesOptionsAndSubvolumes(t *testing.T) {
	fstab := btrfsPlan().Fstab(map[string]string{"/dev/sda3": "BTR", "/dev/sda1": "ESP", "/dev/sda2": "SWP", "/dev/sda4": "SRV"})
	for _, want := range []string{
		"UUID=BTR\t/\tbtrfs\tnoatime,compress=zstd,x-gvfs-hide,subvol=@\t0 1",
		"UUID=BTR\t/home\tbtrfs\tnoatime,compress=zstd,x-gvfs-hide,subvol=@home\t0 2",
		"UUID=BTR\t/var/log\tbtrfs\tnoatime,compress=zstd,x-gvfs-hide,subvol=@log\t0 2",
		"UUID=ESP\t/boot\tvfat\tumask=0077\t0 2",
		"UUID=SRV\t/srv\text4\tdefaults\t0 2", // nothing asked for: as before
		"UUID=SWP\tnone\tswap\tdiscard,pri=10\t0 0",
	} {
		if !strings.Contains(fstab, want+"\n") {
			t.Errorf("fstab missing %q:\n%s", want, fstab)
		}
	}
}

// The install mounts with the filesystem's own options, so compression
// applies to what it writes; options for the mount layer are left to fstab.
func TestMountDataKeepsOnlyFilesystemOptions(t *testing.T) {
	for _, tc := range []struct {
		p    Partition
		want string
	}{
		{Partition{}, ""},
		{Partition{MountOptions: "defaults"}, ""},
		{Partition{MountOptions: "noatime,nodev,nofail,x-systemd.automount"}, ""},
		{Partition{MountOptions: "noatime,compress=zstd,discard=async"}, "compress=zstd,discard=async"},
		{Partition{Subvol: "@", MountOptions: "compress=zstd"}, "subvol=@,compress=zstd"},
		// A subvol= written by hand does not fight the one the plan sets.
		{Partition{Subvol: "@home", MountOptions: "subvol=@,subvolid=5,ssd"}, "subvol=@home,ssd"},
	} {
		if got := tc.p.mountData(); got != tc.want {
			t.Errorf("mountData(%q, subvol %q) = %q, want %q", tc.p.MountOptions, tc.p.Subvol, got, tc.want)
		}
	}
}

func TestSubvolumesNeedTheBtrfsTool(t *testing.T) {
	if got := strings.Join(btrfsPlan().RequiredTools(), " "); !strings.Contains(got, " btrfs ") || !strings.Contains(got, "mkfs.btrfs") {
		t.Errorf("required tools = %s", got)
	}
	plain := &Plan{Parts: []Partition{{Filesystem: "btrfs", Mountpoint: "/"}}}
	if got := " " + strings.Join(plain.RequiredTools(), " ") + " "; strings.Contains(got, " btrfs ") {
		t.Errorf("btrfs without subvolumes should not need the btrfs tool: %s", got)
	}
}

func TestCreateSubvolumesSkipsWhatHasNone(t *testing.T) {
	ran := 0
	run := func([]string) error { ran++; return nil }
	pl := &Plan{Parts: []Partition{
		{Device: "/dev/sda1", Filesystem: "ext4", Mountpoint: "/"},
		// Kept as it is: its subvolumes are already there.
		{Device: "/dev/sda2", Filesystem: "btrfs", Mountpoint: "/home", Preserve: true, Subvolumes: []Subvolume{{"@home", "/home"}}},
	}}
	if err := CreateSubvolumes(pl, run); err != nil || ran != 0 {
		t.Errorf("CreateSubvolumes = %v, ran %d", err, ran)
	}
	// A filesystem that cannot be mounted is an error, with nothing run.
	pl = &Plan{Parts: []Partition{{Device: "/dev/slinstaller-no-such-device", Filesystem: "btrfs", Mountpoint: "/", Subvolumes: []Subvolume{{"@", "/"}}}}}
	if err := CreateSubvolumes(pl, run); err == nil || !strings.Contains(err.Error(), "to work inside it") || ran != 0 {
		t.Errorf("unmountable filesystem: %v, ran %d", err, ran)
	}
	t.Setenv("TMPDIR", "/no/such/dir")
	if err := CreateSubvolumes(pl, run); err == nil {
		t.Error("no temporary directory, but no error")
	}
}

func TestWipeCommands(t *testing.T) {
	pl := &Plan{Disks: []string{"/dev/sda", "/dev/sdb", "/dev/sdc", "/dev/sdd"},
		DiskWipe: map[string]string{"/dev/sda": "zero", "/dev/sdb": "random", "/dev/sdc": "discard"}}
	var got []string
	for _, c := range pl.WipeCommands() {
		got = append(got, strings.Join(c, " "))
	}
	// Each names its own disk; the disk that asked for nothing gets nothing.
	want := "blkdiscard -f -z /dev/sda|shred -n 1 /dev/sdb|blkdiscard -f /dev/sdc"
	if strings.Join(got, "|") != want {
		t.Errorf("wipe commands = %v", got)
	}
	if tools := " " + strings.Join(pl.RequiredTools(), " ") + " "; !strings.Contains(tools, " blkdiscard ") || !strings.Contains(tools, " shred ") || strings.Count(tools, "blkdiscard") != 1 {
		t.Errorf("required tools = %s", tools)
	}
	if cmds := (&Plan{Disk: "/dev/sda"}).WipeCommands(); len(cmds) != 0 {
		t.Errorf("a plan that asks for no wipe has %v", cmds)
	}
}

// A dry run shows everything the install will do to the disk, the whole-disk
// wipe and the subvolumes included.
func TestCommandsShowWipeAndSubvolumes(t *testing.T) {
	pl := btrfsPlan()
	pl.Disk, pl.Wipe = "/dev/sda", true
	pl.DiskWipe = map[string]string{"/dev/sda": "discard"}
	for i := range pl.Parts {
		pl.Parts[i].Number, pl.Parts[i].Size = i+1, "1G"
	}
	cmds, err := pl.Commands()
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	for _, c := range cmds {
		lines = append(lines, strings.Join(c, " "))
	}
	all := strings.Join(lines, "\n")
	if lines[0] != "blkdiscard -f /dev/sda" {
		t.Errorf("the wipe is not first:\n%s", all)
	}
	for _, want := range []string{"btrfs subvolume create [/dev/sda3]/@", "btrfs subvolume create [/dev/sda3]/@home"} {
		if !strings.Contains(all, want+"\n") && !strings.HasSuffix(all, want) {
			t.Errorf("missing %q:\n%s", want, all)
		}
	}
	if strings.Index(all, "btrfs subvolume create") < strings.Index(all, "mkfs.btrfs") {
		t.Errorf("subvolumes before their filesystem:\n%s", all)
	}
}
