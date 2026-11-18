package controller

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sunlightlinux/slinstaller/pkg/blockdev"
	"github.com/sunlightlinux/slinstaller/pkg/config"
)

// fakeBin puts stand-in commands first on PATH. Each records its arguments
// in <dir>/<name>.args and then runs its script.
func fakeBin(t *testing.T, tools map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, script := range tools {
		body := "#!/bin/sh\necho \"$@\" >> \"" + dir + "/" + name + ".args\"\n" + script + "\n"
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return dir
}

// failOn is a Runner that records like recorder and fails the first command
// containing substr.
func (r *recorder) failOn(substr string) func([]string) error {
	return func(args []string) error {
		r.cmds = append(r.cmds, args)
		if strings.Contains(strings.Join(args, " "), substr) {
			return errors.New("boom")
		}
		return nil
	}
}

func lvmConfig(espAt string, encrypted bool) *config.Config {
	return &config.Config{TargetMedia: []*config.Media{{Name: "sda", Type: "disk", Children: []*config.Media{
		{Name: "sda1", Type: "part", FsType: "vfat", Mountpoint: espAt, Size: "512M"},
		{Name: "sda2", Type: "lvm", VolumeGroup: "vg0", Encrypted: encrypted, Children: []*config.Media{
			{Name: "root", FsType: "ext4", Mountpoint: "/"},
		}},
	}}}}
}

func raidConfig() *config.Config {
	return &config.Config{
		TargetMedia: []*config.Media{
			{Name: "sda", Type: "disk", Children: []*config.Media{
				{Name: "sda1", Type: "part", FsType: "vfat", Mountpoint: "/boot", Size: "512M"},
				{Name: "sda2", Type: "raid", Raid: "md0"},
			}},
			{Name: "sdb", Type: "disk", Children: []*config.Media{
				{Name: "sdb1", Type: "raid", Raid: "md0"},
			}},
		},
		Raid: []*config.RaidArray{{Name: "md0", Level: "1", FsType: "ext4", Mountpoint: "/"}},
	}
}

func readArgs(t *testing.T, dir, tool string) string {
	t.Helper()
	data, _ := os.ReadFile(filepath.Join(dir, tool+".args"))
	return string(data)
}

func TestPartitionEncryptsBeforeMakingFilesystems(t *testing.T) {
	dir := fakeBin(t, map[string]string{"cryptsetup": "cat > /dev/null"})
	var r recorder
	var out bytes.Buffer
	err := Install(cryptConfig("/boot"), Options{
		Through: "partition", Runner: r.run, Devices: noDevices,
		Passphrase: testPassphrase, Output: &out,
	})
	if err != nil {
		t.Fatalf("Install through partition: %v", err)
	}
	args := readArgs(t, dir, "cryptsetup")
	// /boot is not encrypted, so the root keeps cryptsetup's stronger
	// default key derivation.
	if !strings.Contains(args, "luksFormat /dev/sda2 -") || strings.Contains(args, "pbkdf2") {
		t.Errorf("luksFormat:\n%s", args)
	}
	if !strings.Contains(args, "luksOpen /dev/sda2 cryptroot -") {
		t.Errorf("luksOpen:\n%s", args)
	}
	// The filesystem goes on the mapper, never on the raw partition.
	flat := r.flat()
	if !strings.Contains(flat, "/dev/mapper/cryptroot") || strings.Contains(flat, "mkfs.ext4 /dev/sda2") {
		t.Errorf("mkfs target:\n%s", flat)
	}
	if strings.Contains(out.String(), testPassphrase) {
		t.Error("passphrase in the install output")
	}
}

func TestPartitionFormatsAnEncryptedBootForGrub(t *testing.T) {
	// With the ESP at /boot/efi, /boot is on the encrypted root and GRUB has
	// to open it: that volume needs a key derivation GRUB can do.
	dir := fakeBin(t, map[string]string{"cryptsetup": "cat > /dev/null"})
	var r recorder
	var out bytes.Buffer
	if err := Install(cryptConfig("/boot/efi"), Options{
		Through: "partition", Runner: r.run, Devices: noDevices, Passphrase: testPassphrase, Output: &out,
	}); err != nil {
		t.Fatalf("Install: %v", err)
	}
	if args := readArgs(t, dir, "cryptsetup"); !strings.Contains(args, "--pbkdf pbkdf2 /dev/sda2") {
		t.Errorf("an encrypted /boot was not formatted for GRUB:\n%s", args)
	}
}

func TestPartitionRefusesEncryptionWithoutAPassphrase(t *testing.T) {
	dir := fakeBin(t, map[string]string{"cryptsetup": "cat > /dev/null"})
	var r recorder
	err := Install(cryptConfig("/boot"), Options{Through: "partition", Runner: r.run, Devices: noDevices})
	if err == nil || !strings.Contains(err.Error(), "/dev/sda2 is encrypted but no passphrase was given") {
		t.Fatalf("error = %v", err)
	}
	if args := readArgs(t, dir, "cryptsetup"); args != "" {
		t.Errorf("cryptsetup ran without a passphrase:\n%s", args)
	}
	// No filesystem was made after the failure.
	if strings.Contains(r.flat(), "mkfs") {
		t.Errorf("mkfs ran after the passphrase check failed:\n%s", r.flat())
	}
}

func TestPartitionStopsWhenCryptsetupFails(t *testing.T) {
	for _, step := range []string{"luksFormat", "luksOpen"} {
		fakeBin(t, map[string]string{"cryptsetup": `cat > /dev/null; for a; do [ "$a" = ` + step + ` ] && exit 1; done; exit 0`})
		var r recorder
		var out bytes.Buffer
		err := Install(cryptConfig("/boot"), Options{
			Through: "partition", Runner: r.run, Devices: noDevices, Passphrase: testPassphrase, Output: &out,
		})
		if err == nil || !strings.Contains(err.Error(), step) {
			t.Errorf("%s failing: %v", step, err)
		}
		if strings.Contains(r.flat(), "mkfs.ext4") {
			t.Errorf("%s failed but a filesystem was still made:\n%s", step, r.flat())
		}
	}
}

func TestPartitionBuildsLVMAndRAIDBeforeFilesystems(t *testing.T) {
	order := func(flat string, first, then string) bool {
		a, b := strings.Index(flat, first), strings.Index(flat, then)
		return a >= 0 && b >= 0 && a < b
	}

	var r recorder
	if err := Install(lvmConfig("/boot", false), Options{Through: "partition", Runner: r.run, Devices: noDevices}); err != nil {
		t.Fatalf("LVM install: %v", err)
	}
	flat := r.flat()
	if !order(flat, "pvcreate", "vgcreate") || !order(flat, "vgcreate", "lvcreate") || !order(flat, "lvcreate", "/dev/vg0/root") {
		t.Errorf("LVM order wrong:\n%s", flat)
	}

	r = recorder{}
	if err := Install(raidConfig(), Options{Through: "partition", Runner: r.run, Devices: noDevices}); err != nil {
		t.Fatalf("RAID install: %v", err)
	}
	flat = r.flat()
	// Both disks are partitioned, the array is created from the two members,
	// and only then does the filesystem go on the array.
	for _, want := range []string{"/dev/sda mklabel gpt", "/dev/sdb mklabel gpt", "/dev/sda2", "/dev/sdb1"} {
		if !strings.Contains(flat, want) {
			t.Errorf("RAID commands missing %q:\n%s", want, flat)
		}
	}
	if !order(flat, "mdadm", "mkfs.ext4") || !strings.Contains(flat, "/dev/md0") {
		t.Errorf("RAID order wrong:\n%s", flat)
	}
}

func TestPartitionStopsAtTheFirstFailingStep(t *testing.T) {
	for name, tc := range map[string]struct {
		cfg    *config.Config
		failOn string
		never  string // must not run after the failure
	}{
		"table":  {twoPartConfig(), "mklabel", "mkfs"},
		"raid":   {raidConfig(), "mdadm", "mkfs.ext4"},
		"lvm":    {lvmConfig("/boot", false), "vgcreate", "lvcreate"},
		"mkfs":   {twoPartConfig(), "mkfs.vfat", "mkfs.ext4"},
		"format": {&config.Config{TargetMedia: []*config.Media{{Name: "sda", Type: "disk", Children: []*config.Media{{Name: "sda1", FsType: "ntfs", Mountpoint: "/"}}}}}, "never-matches", "mkfs"},
	} {
		var r recorder
		err := Install(tc.cfg, Options{Through: "partition", Runner: r.failOn(tc.failOn), Devices: noDevices})
		if err == nil || !strings.Contains(err.Error(), "controller: partition") {
			t.Errorf("%s: error = %v", name, err)
		}
		flat := r.flat()
		if i := strings.Index(flat, tc.failOn); i >= 0 && strings.Contains(flat[i:], tc.never) {
			t.Errorf("%s: %q ran after %q failed:\n%s", name, tc.never, tc.failOn, flat)
		}
	}
}

func TestPartitionReleasesDisksFromTheRealListing(t *testing.T) {
	// With no listing given, the machine's own (lsblk) is used: a volume
	// group living on the target disk is taken down before it is wiped.
	fakeBin(t, map[string]string{"lsblk": `cat <<'JSON'
{"blockdevices":[{"name":"sda","path":"/dev/sda","size":1000,"type":"disk","children":[
  {"name":"sda1","path":"/dev/sda1","size":900,"type":"part","fstype":"LVM2_member","children":[
    {"name":"vg0-root","path":"/dev/mapper/vg0-root","size":800,"type":"lvm"}]}]}]}
JSON`})
	var r recorder
	if err := Install(twoPartConfig(), Options{Through: "partition", Runner: r.run}); err != nil {
		t.Fatalf("Install: %v", err)
	}
	flat := r.flat()
	if i, j := strings.Index(flat, "vg0"), strings.Index(flat, "mklabel"); i < 0 || i > j {
		t.Errorf("the volume group was not released before the wipe:\n%s", flat)
	}

	// The listing failing stops the install before anything is wiped.
	r = recorder{}
	listErr := errors.New("lsblk: cannot read")
	err := Install(twoPartConfig(), Options{Through: "partition", Runner: r.run,
		Devices: func() ([]blockdev.Device, error) { return nil, listErr }})
	if !errors.Is(err, listErr) || len(r.cmds) != 0 {
		t.Errorf("listing failure: %v, ran %d commands", err, len(r.cmds))
	}

	// Releasing the disk failing stops it too, naming the disk.
	r = recorder{}
	err = Install(twoPartConfig(), Options{Through: "partition", Runner: r.failOn("vg0")})
	if err == nil || !strings.Contains(err.Error(), "release /dev/sda") || strings.Contains(r.flat(), "mklabel") {
		t.Errorf("release failure: %v\n%s", err, r.flat())
	}
}

func TestSettleIsSkippedWithoutUdevadm(t *testing.T) {
	var r recorder
	in := stagedInstaller(t, twoPartConfig(), &r)
	t.Setenv("PATH", t.TempDir()) // no udevadm anywhere
	in.settle()
	if len(r.cmds) != 0 {
		t.Errorf("settle ran %v without udevadm", r.cmds)
	}
	// With it, settling is one udevadm call through the runner.
	fakeBin(t, map[string]string{"udevadm": "exit 0"})
	in.settle()
	if r.flat() != "udevadm settle\n" {
		t.Errorf("settle = %q", r.flat())
	}
}

func TestInstallOptions(t *testing.T) {
	// No runner given: the real one is used (nothing runs through a stage
	// that has no commands). A config with no layout is refused first.
	if err := Install(twoPartConfig(), Options{Through: "pre-install", Devices: noDevices}); err != nil {
		t.Errorf("Install with the default runner: %v", err)
	}
	if err := Install(&config.Config{}, Options{}); err == nil {
		t.Error("a config with no layout was installed")
	}

	// Progress and the log follow the stages; a staged run ends with
	// "complete" for the stages it ran.
	var r recorder
	var log bytes.Buffer
	var seen []string
	err := Install(twoPartConfig(), Options{
		Through: "partition", Runner: r.run, Devices: noDevices, Log: &log,
		Progress: func(done, total int, stage string) {
			seen = append(seen, stage)
			if total != 2 || done > total {
				t.Errorf("progress %d/%d at %s", done, total, stage)
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(seen, ",") != "pre-install,partition,complete" {
		t.Errorf("progress stages = %v", seen)
	}
	if got := log.String(); got != "==> pre-install\n==> partition\n" {
		t.Errorf("log = %q", got)
	}

	// Without an Output writer, sub-command output goes to stderr.
	in := stagedInstaller(t, twoPartConfig(), &r)
	if in.output() != os.Stderr {
		t.Error("output() should default to stderr")
	}
}

func TestPartitionWipesAndBuildsSpares(t *testing.T) {
	// A disk asked to be zeroed is, before its table is written.
	cfg := twoPartConfig()
	cfg.TargetMedia[0].Wipe = "zero"
	var r recorder
	if err := Install(cfg, Options{Through: "partition", Runner: r.run, Devices: noDevices}); err != nil {
		t.Fatal(err)
	}
	flat := r.flat()
	if i, j := strings.Index(flat, "blkdiscard -f -z /dev/sda"), strings.Index(flat, "mklabel"); i < 0 || i > j {
		t.Errorf("the disk was not zeroed before partitioning:\n%s", flat)
	}
	// The wipe failing stops the install with the old table still there.
	r = recorder{}
	err := Install(cfg, Options{Through: "partition", Runner: r.failOn("blkdiscard"), Devices: noDevices})
	if err == nil || strings.Contains(r.flat(), "mklabel") {
		t.Errorf("a failed wipe went on to partition: %v\n%s", err, r.flat())
	}

	// A hot spare is handed to mdadm as one, after the members.
	raid := raidConfig()
	raid.TargetMedia = append(raid.TargetMedia, &config.Media{Name: "sdc", Type: "disk", Children: []*config.Media{
		{Name: "sdc1", Type: "raid", Raid: "md0", Spare: true}}})
	r = recorder{}
	if err := Install(raid, Options{Through: "partition", Runner: r.run, Devices: noDevices}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(r.flat(), "--raid-devices=2 --spare-devices=1 /dev/sda2 /dev/sdb1 /dev/sdc1") {
		t.Errorf("mdadm:\n%s", r.flat())
	}
}

// A resize install: the old partition is shrunk — filesystem, then partition
// — before the new one is cut in the room it leaves, and only the new one
// gets a filesystem.
func TestPartitionResizesThenCreates(t *testing.T) {
	cfg := &config.Config{ReusePartitions: true, TargetMedia: []*config.Media{{Name: "sda", Type: "disk", Children: []*config.Media{
		{Name: "sda1", Type: "part", FsType: "vfat", Mountpoint: "/boot/efi", Preserve: true},
		{Name: "sda2", Type: "part", FsType: "ext4", Preserve: true, Resize: "40G"},
		{Name: "sda3", Type: "part", FsType: "ext4", Mountpoint: "/", Create: true},
	}}}}
	// parted, asked what is free once the shrink is done.
	fakeBin(t, map[string]string{"parted": `echo "BYT;"; echo "1:1MiB:513MiB:512MiB:fat32::boot, esp;"
echo "2:513MiB:41473MiB:40960MiB:ext4::;"; echo "1:41473MiB:102400MiB:60927MiB:free;"`})
	var r recorder
	if err := Install(cfg, Options{Through: "partition", Runner: r.run, Devices: noDevices}); err != nil {
		t.Fatal(err)
	}
	flat := r.flat()
	order := []string{"e2fsck -f -p /dev/sda2", "resize2fs /dev/sda2 40960M", "sfdisk --no-reread -N 2 /dev/sda", "41473MiB 102399MiB", "mkfs.ext4"}
	last := -1
	for _, step := range order {
		i := strings.Index(flat, step)
		if i < 0 || i < last {
			t.Fatalf("step %q missing or out of order:\n%s", step, flat)
		}
		last = i
	}
	if strings.Contains(flat, "mklabel") || strings.Contains(flat, "mkfs.ext4 -v -F -b 4096 /dev/sda2") || strings.Contains(flat, "mkfs.vfat") {
		t.Errorf("a kept partition was repartitioned or formatted:\n%s", flat)
	}

	// The filesystem refusing to shrink stops everything: the partition is
	// not cut down and nothing new is made.
	r = recorder{}
	err := Install(cfg, Options{Through: "partition", Runner: r.failOn("resize2fs"), Devices: noDevices})
	if err == nil || strings.Contains(r.flat(), "sfdisk") || strings.Contains(r.flat(), "mkpart") || strings.Contains(r.flat(), "mkfs") {
		t.Errorf("a failed shrink went on: %v\n%s", err, r.flat())
	}
	// No room after all: reported, with nothing created.
	fakeBin(t, map[string]string{"parted": `echo "BYT;"; echo "1:1MiB:513MiB:512MiB:fat32::boot, esp;"`})
	r = recorder{}
	err = Install(cfg, Options{Through: "partition", Runner: r.run, Devices: noDevices})
	if err == nil || !strings.Contains(err.Error(), "has no unallocated space") || strings.Contains(r.flat(), "mkfs") {
		t.Errorf("no room: %v\n%s", err, r.flat())
	}
}

// Resuming never partitions, formats or runs the pre-install hooks again: it
// starts at the mount.
func TestResumeSkipsPartitioning(t *testing.T) {
	cfg := twoPartConfig()
	cfg.PreInstall = []*config.Hook{{Cmd: "echo should-not-run"}}
	var r recorder
	var seen []string
	err := Install(cfg, Options{Resume: true, Through: "mount", Runner: r.run, Devices: noDevices, Root: t.TempDir(),
		Progress: func(done, total int, stage string) { seen = append(seen, stage) }})
	// The mount itself fails here (there is no such disk); what matters is
	// what did not run before it.
	if err == nil || !strings.Contains(err.Error(), "controller: mount") {
		t.Fatalf("resume: %v", err)
	}
	if len(r.cmds) != 0 {
		t.Errorf("a resumed install ran commands before mounting:\n%s", r.flat())
	}
	if strings.Join(seen, ",") != "mount" {
		t.Errorf("stages = %v", seen)
	}
	// The stages it would go on to are the rest of the install, in order.
	in := &installer{opts: Options{Resume: true}}
	var names []string
	for _, s := range in.stages() {
		names = append(names, s.name)
	}
	if got := strings.Join(names, ","); !strings.HasPrefix(got, "mount,meta-fs,base-install,fstab") || strings.Contains(got, "partition") {
		t.Errorf("resumed stages = %s", got)
	}
	// The stage names a front-end shows are still the whole install's.
	if all := StageNames(); all[0] != "pre-install" || all[1] != "partition" {
		t.Errorf("StageNames = %v", all)
	}
}
