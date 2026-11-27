package controller

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sunlightlinux/slinstaller/pkg/config"
	"github.com/sunlightlinux/slinstaller/pkg/storage"
)

func serverConfig(array *config.RaidArray) *config.Config {
	return &config.Config{
		TargetMedia: []*config.Media{
			{Name: "sda", Type: "disk", Children: []*config.Media{
				{Name: "sda1", Type: "part", FsType: "vfat", Mountpoint: "/boot/efi", Size: "512M"},
				{Name: "sda2", Type: "raid", Raid: "md0"}}},
			{Name: "sdb", Type: "disk", Children: []*config.Media{
				{Name: "sdb1", Type: "part", FsType: "vfat", Size: "512M", ESP: true},
				{Name: "sdb2", Type: "raid", Raid: "md0"}}},
		},
		Raid: []*config.RaidArray{array},
	}
}

const fakeMdadm = `case "$*" in
*--export*) echo "MD_UUID=aaaa:bbbb:cccc:dddd" ;;
*--brief*/dev/md0*) echo "ARRAY /dev/md0 UUID=aaaa:bbbb:cccc:dddd" ;;
esac`

// Each layer is built on the one below it: the array from its members, LUKS
// on the array, the volume group on the mapper, filesystems on the volumes.
func TestPartitionBuildsTheStackInOrder(t *testing.T) {
	dir := fakeBin(t, map[string]string{"cryptsetup": "cat > /dev/null"})
	cfg := serverConfig(&config.RaidArray{Name: "md0", Level: "1", Encrypted: true, VolumeGroup: "vg0",
		Children: []*config.Media{{Name: "root", FsType: "ext4", Mountpoint: "/"}}})
	var out bytes.Buffer
	// Record what the runner ran and what cryptsetup ran in one sequence.
	var seq []string
	runner := func(args []string) error {
		// cryptsetup calls made so far, by now on the array, come first.
		for _, l := range strings.Split(strings.TrimSpace(readArgs(t, dir, "cryptsetup")), "\n") {
			if l != "" && !containsLine(seq, "cryptsetup "+l) {
				seq = append(seq, "cryptsetup "+l)
			}
		}
		seq = append(seq, strings.Join(args, " "))
		return nil
	}
	err := Install(cfg, Options{Through: "partition", Runner: runner, Devices: noDevices, Passphrase: testPassphrase, Output: &out})
	if err != nil {
		t.Fatal(err)
	}
	flat := strings.Join(seq, "\n")
	steps := []string{"mdadm --create /dev/md0", "luksFormat --pbkdf pbkdf2 /dev/md0", "luksOpen /dev/md0 cryptmd0",
		"pvcreate -ff -y /dev/mapper/cryptmd0", "vgcreate vg0 /dev/mapper/cryptmd0", "lvcreate", "/dev/vg0/root"}
	last := -1
	for _, step := range steps {
		i := strings.Index(flat, step)
		if i < 0 || i < last {
			t.Fatalf("step %q missing or out of order:\n%s", step, flat)
		}
		last = i
	}
	// (/boot is inside the encrypted array here, so GRUB has to open it:
	// hence the key derivation GRUB can do.)
	// The members themselves are not encrypted, and the spare ESP is made.
	if strings.Contains(flat, "luksFormat /dev/sda2") || !strings.Contains(flat, "mkfs.vfat -F32 /dev/sdb1") {
		t.Errorf("unexpected:\n%s", flat)
	}
}

func containsLine(ss []string, v string) bool {
	for _, s := range ss {
		if s == v {
			return true
		}
	}
	return false
}

// The initramfs is told about every layer between the disks and the root.
func TestBootArgumentsForStackedRoots(t *testing.T) {
	fakeBin(t, map[string]string{"mdadm": fakeMdadm, "cryptsetup": `[ "$1" = luksUUID ] && echo "luks-$(basename "$2")"; exit 0`})
	for name, tc := range map[string]struct {
		array *config.RaidArray
		want  []string
		never []string
	}{
		"LVM on RAID": {&config.RaidArray{Name: "md0", Level: "1", VolumeGroup: "vg0",
			Children: []*config.Media{{Name: "root", FsType: "ext4", Mountpoint: "/"}}},
			[]string{"rd.md.uuid=aaaa:bbbb:cccc:dddd", "rd.lvm.lv=vg0/root"}, []string{"rd.luks.uuid"}},
		"LUKS on RAID": {&config.RaidArray{Name: "md0", Level: "1", FsType: "ext4", Mountpoint: "/", Encrypted: true},
			[]string{"rd.md.uuid=aaaa:bbbb:cccc:dddd", "rd.luks.uuid=luks-md0"}, []string{"rd.lvm.lv"}},
		"LVM on LUKS on RAID": {&config.RaidArray{Name: "md0", Level: "1", Encrypted: true, VolumeGroup: "vg0",
			Children: []*config.Media{{Name: "root", FsType: "ext4", Mountpoint: "/"}}},
			[]string{"rd.md.uuid=aaaa:bbbb:cccc:dddd", "rd.luks.uuid=luks-md0", "rd.lvm.lv=vg0/root"}, nil},
		// An array that only holds data is not the initramfs's business.
		"data on RAID": {&config.RaidArray{Name: "md0", Level: "1", FsType: "ext4", Mountpoint: "/data"},
			nil, []string{"rd.md.uuid"}},
	} {
		cfg := serverConfig(tc.array)
		if name == "data on RAID" {
			cfg.TargetMedia[0].Children[1].Size = "10G"
			cfg.TargetMedia[0].Children = append(cfg.TargetMedia[0].Children,
				&config.Media{Name: "sda3", Type: "part", FsType: "ext4", Mountpoint: "/", Size: "20G"})
		}
		cfg.TargetMedia[1].Children[0].ESP = false // no spare here: it needs a real mount
		cfg.TargetMedia[1].Children = cfg.TargetMedia[1].Children[1:]
		cfg.TargetMedia[1].Children[0].Name = "sdb1" // now the disk's only partition
		var r recorder
		in := stagedInstaller(t, cfg, &r)
		in.opts.Passphrase = testPassphrase
		mkTarget(t, in, "etc", "default")
		if err := in.bootloader(); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		args := " " + strings.Join(cmdlineArgs(t, in), " ") + " "
		for _, w := range tc.want {
			if !strings.Contains(args, " "+w+" ") {
				t.Errorf("%s: missing %s in:%s", name, w, args)
			}
		}
		for _, n := range tc.never {
			if strings.Contains(args, n) {
				t.Errorf("%s: unexpected %s in:%s", name, n, args)
			}
		}
	}
}

// A spare ESP that cannot be mounted fails the stage: a machine believed to
// boot from either disk, and that does not, is worse than an error now.
func TestSpareESPFailureIsReported(t *testing.T) {
	fakeBin(t, map[string]string{"mdadm": fakeMdadm})
	var r recorder
	in := stagedInstaller(t, serverConfig(&config.RaidArray{Name: "md0", Level: "1", FsType: "ext4", Mountpoint: "/"}), &r)
	mkTarget(t, in, "etc", "default")
	err := in.bootloader()
	wantErr(t, err, "spare ESP /dev/sdb1")
	// GRUB was installed first; the spare is what failed.
	if !strings.Contains(r.flat(), "grub-install") {
		t.Errorf("grub-install did not run:\n%s", r.flat())
	}
}

// fillSpareESPsOnTmpfs is the spare-ESP copy for real, run from the
// namespace child (mount_ns_test.go) with tmpfs standing in for vfat.
func fillSpareESPsOnTmpfs(t *testing.T, base string) {
	root := filepath.Join(base, "spare-root")
	efi := filepath.Join(root, "boot", "efi", "EFI", "Sunlight")
	if err := os.MkdirAll(efi, 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(efi, "grubx64.efi"), []byte("GRUB"), 0o644)

	// An entry an earlier install left for this spare is replaced, not
	// added to; the others are left alone.
	saved := efiBootList
	defer func() { efiBootList = saved }()
	efiBootList = func() string {
		return "BootCurrent: 0001\nBootOrder: 0004,0001,0003\n" +
			"Boot0001* Sunlight\tHD(1,GPT,...)/File(\\EFI\\Sunlight\\grubx64.efi)\n" +
			"Boot0003* Windows Boot Manager\tHD(1,GPT,...)\n" +
			"Boot0004* Sunlight (sdb)\tHD(1,GPT,...)/File(\\EFI\\Sunlight\\grubx64.efi)\n"
	}
	in := nsInstaller(root)
	in.plan.Parts = append(in.plan.Parts[:0], in.plan.Parts[0])
	in.plan.Parts = append(in.plan.Parts,
		in.plan.Parts[0])
	in.plan.Parts[1].Mountpoint, in.plan.Parts[1].SpareESP = "", true
	in.plan.Parts[1].Disk, in.plan.Parts[1].Number = "/dev/sdb", 1
	// What ends up on the spare is read back while it is still mounted, by
	// the last command run against it.
	var copied string
	var cmds []string
	in.opts.Runner = func(args []string) error {
		cmds = append(cmds, strings.Join(args, " "))
		if args[0] == "chroot" {
			return nil // efibootmgr: recorded, not run
		}
		if err := realRun(args); err != nil {
			return err
		}
		if args[0] == "cp" && strings.HasSuffix(args[len(args)-1], "BOOTX64.EFI") {
			dir := filepath.Dir(filepath.Dir(filepath.Dir(args[len(args)-1])))
			a, _ := os.ReadFile(filepath.Join(dir, "EFI", "Sunlight", "grubx64.efi"))
			b, _ := os.ReadFile(filepath.Join(dir, "EFI", "BOOT", "BOOTX64.EFI"))
			copied = string(a) + "|" + string(b)
		}
		return nil
	}
	if err := in.fillSpareESPs("/boot/efi", false); err != nil {
		t.Fatalf("fillSpareESPs: %v", err)
	}
	if copied != "GRUB|GRUB" {
		t.Errorf("on the spare ESP: %q, want the loader at both paths", copied)
	}
	if del := cmds[len(cmds)-2]; !strings.HasSuffix(del, "efibootmgr --bootnum 0004 --delete-bootnum") {
		t.Errorf("the old entry for the spare was not removed first: %s", del)
	}
	for _, c := range cmds {
		if strings.Contains(c, "--bootnum 0001") || strings.Contains(c, "--bootnum 0003") {
			t.Errorf("an entry of something else was touched: %s", c)
		}
	}
	last := cmds[len(cmds)-1]
	if !strings.Contains(last, "efibootmgr --create --disk /dev/sdb --part 1 --label Sunlight (sdb)") || !strings.Contains(last, `\EFI\Sunlight\grubx64.efi`) {
		t.Errorf("boot entry = %s", last)
	}
	// With the firmware's entries left alone, no entry is made.
	cmds = nil
	if err := in.fillSpareESPs("/boot/efi", true); err != nil {
		t.Fatal(err)
	}
	for _, c := range cmds {
		if strings.Contains(c, "efibootmgr") {
			t.Errorf("an entry was made despite update-nvram: false: %s", c)
		}
	}
}

// Options for an encrypted volume go where it is opened from: crypttab for
// one opened after boot, the kernel command line for the root, which the
// initramfs opens.
func TestCryptOptions(t *testing.T) {
	fakeCryptsetup(t)
	home := &config.Media{Name: "sda3", Type: "crypt", FsType: "ext4", Mountpoint: "/home", CryptOptions: "discard"}
	cfg := cryptConfig("/boot", home)
	cfg.TargetMedia[0].Children[1].CryptOptions = "discard"
	var r recorder
	in, _ := cryptInstaller(t, cfg, &r)
	if err := in.crypttab(); err != nil {
		t.Fatal(err)
	}
	tab := readTarget(t, in, "etc", "crypttab")
	if !strings.Contains(tab, " luks,discard\n") || strings.Count(tab, "\n") != 1 {
		t.Errorf("crypttab:\n%s", tab)
	}
	mkTarget(t, in, "etc", "default")
	if err := in.bootloader(); err != nil {
		t.Fatal(err)
	}
	// dracut's crypt module takes discard as rd.luks.allow-discards=<uuid>.
	if args := cmdlineArgs(t, in); !contains(args, "rd.luks.options=discard") || !strings.Contains(strings.Join(args, " "), "rd.luks.allow-discards=") {
		t.Errorf("kernel arguments: %v", args)
	}
	// Other options are passed on, and only discard (under either name,
	// anywhere in the list) asks for discards, once.
	for opts, discards := range map[string]int{"no-read-workqueue": 0, "no-read-workqueue,allow-discards,discard": 1} {
		c := cryptConfig("/boot")
		c.TargetMedia[0].Children[1].CryptOptions = opts
		in, _ = cryptInstaller(t, c, &r)
		mkTarget(t, in, "etc", "default")
		if err := in.bootloader(); err != nil {
			t.Fatal(err)
		}
		args := strings.Join(cmdlineArgs(t, in), " ")
		if !strings.Contains(args, "rd.luks.options="+opts) || strings.Count(args, "rd.luks.allow-discards=") != discards {
			t.Errorf("crypt options %q: kernel arguments %s", opts, args)
		}
	}
	// Without the option nothing of the kind is written.
	in, _ = cryptInstaller(t, cryptConfig("/boot"), &r)
	mkTarget(t, in, "etc", "default")
	if err := in.bootloader(); err != nil {
		t.Fatal(err)
	}
	for _, a := range cmdlineArgs(t, in) {
		if strings.HasPrefix(a, "rd.luks.options") {
			t.Errorf("unexpected %s", a)
		}
	}
}

// A partitioned array is cut after it is assembled, and a root on one of
// its partitions has the array assembled by the initramfs.
func TestPartitionedArrayInTheInstall(t *testing.T) {
	fakeBin(t, map[string]string{"mdadm": fakeMdadm})
	cfg := serverConfig(&config.RaidArray{Name: "md0", Level: "1", Partitions: []*config.Media{
		{FsType: "ext4", Mountpoint: "/", Size: "20G"}, {FsType: "ext4", Mountpoint: "/srv"}}})
	cfg.TargetMedia[1].Children = cfg.TargetMedia[1].Children[1:] // no spare ESP: it needs a real mount
	cfg.TargetMedia[1].Children[0].Name = "sdb1"                  // now the disk's only partition
	var r recorder
	if err := Install(cfg, Options{Through: "partition", Runner: r.run, Devices: noDevices}); err != nil {
		t.Fatal(err)
	}
	flat := r.flat()
	last := -1
	for _, step := range []string{"mdadm --create /dev/md0", "parted -s /dev/md0 mklabel gpt", "mkpart /srv", "partprobe /dev/md0", "/dev/md0p1", "/dev/md0p2"} {
		i := strings.Index(flat, step)
		if i < 0 || i < last {
			t.Fatalf("step %q missing or out of order:\n%s", step, flat)
		}
		last = i
	}
	r = recorder{}
	if err := Install(cfg, Options{Through: "partition", Runner: r.failOn("/dev/md0 mklabel"), Devices: noDevices}); err == nil || strings.Contains(r.flat(), "mkfs") {
		t.Errorf("a failed array table went on to mkfs: %v", err)
	}

	in := stagedInstaller(t, cfg, &r)
	mkTarget(t, in, "etc", "default")
	if err := in.bootloader(); err != nil {
		t.Fatal(err)
	}
	if args := cmdlineArgs(t, in); !contains(args, "rd.md.uuid=aaaa:bbbb:cccc:dddd") {
		t.Errorf("kernel arguments: %v", args)
	}
	if in.rootDisk() != "/dev/md0" {
		t.Errorf("rootDisk = %q", in.rootDisk())
	}
}

func TestKernelRemove(t *testing.T) {
	cfg := twoPartConfig()
	cfg.KernelRemove = []string{"linux6.18", "linux-mainline"}
	var r recorder
	in := stagedInstaller(t, cfg, &r)
	in.opts.Repo, in.opts.Packages = "https://repo.example/current", []string{"sunlight-base"}
	if err := in.baseInstall(); err != nil {
		t.Fatal(err)
	}
	flat := r.flat()
	// After the base system is in, inside it.
	if i, j := strings.Index(flat, "xbps-install"), strings.Index(flat, "chroot "+in.opts.Root+" xbps-remove -Ry linux6.18 linux-mainline"); i < 0 || j < i {
		t.Errorf("commands:\n%s", flat)
	}
	in.opts.Runner = r.failOn("xbps-remove")
	wantErr(t, in.baseInstall(), "boom")
	// From an image too: it is the image's kernel that is usually in the way.
	cfg.Image = "/img/base.tar"
	r = recorder{}
	in = stagedInstaller(t, cfg, &r)
	bin := mkTarget(t, in, "usr", "bin")
	os.WriteFile(filepath.Join(bin, "grub-install"), nil, 0o755)
	if err := in.baseInstall(); err != nil || !strings.Contains(r.flat(), "xbps-remove -Ry linux6.18") {
		t.Errorf("image install: %v\n%s", err, r.flat())
	}
}

// rootDisk names the disk the root partition is cut on, and only when the
// root is a plain partition: a root on a logical volume or straight on an
// array has no disk of its own to name, and neither has a plan without a
// root. Partitions mounted elsewhere are passed over.
func TestRootDisk(t *testing.T) {
	for _, c := range []struct {
		name  string
		parts []storage.Partition
		want  string
	}{
		{"plain", []storage.Partition{{Disk: "/dev/sda", Mountpoint: "/boot"}, {Disk: "/dev/sdb", Mountpoint: "/"}}, "/dev/sdb"},
		{"on a volume", []storage.Partition{{Disk: "/dev/sda", Mountpoint: "/", LV: true}}, ""},
		{"on an array", []storage.Partition{{Disk: "/dev/sda", Mountpoint: "/", Raid: true}}, ""},
		{"no root", []storage.Partition{{Disk: "/dev/sda", Mountpoint: "/home"}}, ""},
		{"none", nil, ""},
	} {
		in := &installer{plan: &storage.Plan{Parts: c.parts}}
		if got := in.rootDisk(); got != c.want {
			t.Errorf("%s: rootDisk = %q, want %q", c.name, got, c.want)
		}
	}
}

// Only a BootXXXX line (four hex digits) whose label is exactly the one
// asked for is an entry of it: not BootCurrent or BootOrder, not a label
// that only starts the same, and a line too short to hold a number is
// passed over rather than read past its end.
func TestEFIEntriesLabelled(t *testing.T) {
	saved := efiBootList
	defer func() { efiBootList = saved }()
	efiBootList = func() string {
		return "BootCurrent: 0001\nBootOrder: 0005,0007\nBoot\nBoot000\n" +
			"Boot0005 Sunlight\n" + // inactive: no star
			"Boot0006* Sunlight (sdb)\tHD(1,GPT,...)\n" +
			"Bootxyz0* Sunlight\n" +
			"Boot000a* Sunlight\tHD(1,GPT,...)/File(\\EFI\\Sunlight\\grubx64.efi)\n"
	}
	if got := strings.Join(efiEntriesLabelled("Sunlight"), ","); got != "0005,000a" {
		t.Errorf("entries labelled Sunlight: %s", got)
	}
	if got := strings.Join(efiEntriesLabelled("Sunlight (sdb)"), ","); got != "0006" {
		t.Errorf("entries labelled Sunlight (sdb): %s", got)
	}
}
