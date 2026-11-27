package controller

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sunlightlinux/slinstaller/pkg/config"
)

// Every stage after partitioning writes into a target whose disk has already
// been wiped. When a write or a command fails there, the stage has to stop
// and say what failed — an install that carries on produces a system that
// does not boot, or boots without what the operator asked for. These tests
// fail each step in turn.
//
// Three blocks that work for root too (see pkg/system's tests): a file where
// a directory must be made, a dangling symlink where a file must be written,
// a non-empty directory where a file or link must go.

func blockDir(t *testing.T, in *installer, rel ...string) {
	t.Helper()
	p := filepath.Join(append([]string{in.opts.Root}, rel...)...)
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, nil, 0o644); err != nil {
		t.Fatal(err)
	}
}

func blockFile(t *testing.T, in *installer, rel ...string) {
	t.Helper()
	p := filepath.Join(append([]string{in.opts.Root}, rel...)...)
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.Symlink(filepath.Join(in.opts.Root, "no-such-dir", "x"), p); err != nil {
		t.Fatal(err)
	}
}

func blockWithDir(t *testing.T, in *installer, rel ...string) {
	t.Helper()
	p := filepath.Join(append([]string{in.opts.Root}, rel...)...)
	if err := os.MkdirAll(filepath.Join(p, "occupied"), 0o755); err != nil {
		t.Fatal(err)
	}
}

// fullDisk makes every write to the file fail, as on a full disk.
func fullDisk(t *testing.T, in *installer, rel ...string) {
	t.Helper()
	if _, err := os.Stat("/dev/full"); err != nil {
		t.Skip("no /dev/full to fail a write with")
	}
	p := filepath.Join(append([]string{in.opts.Root}, rel...)...)
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.Symlink("/dev/full", p); err != nil {
		t.Fatal(err)
	}
}

func wantErr(t *testing.T, err error, part string) {
	t.Helper()
	if err == nil {
		t.Fatalf("the failure was not reported (want %q)", part)
	}
	if !strings.Contains(err.Error(), part) {
		t.Errorf("error %q does not say %q", err, part)
	}
}

func TestSaveConfigAndLogFailures(t *testing.T) {
	var r recorder
	in := stagedInstaller(t, twoPartConfig(), &r)
	blockDir(t, in, "root")
	wantErr(t, in.saveConfig(), "mkdir")

	in = stagedInstaller(t, twoPartConfig(), &r)
	blockFile(t, in, "root", "slinstaller.yaml")
	wantErr(t, in.saveConfig(), "save config")

	// The install log: a missing source and an unwritable target both fail;
	// no log asked for is fine.
	in = stagedInstaller(t, twoPartConfig(), &r)
	if err := in.archiveLog(); err != nil {
		t.Errorf("no log file: %v", err)
	}
	in.opts.LogFile = filepath.Join(t.TempDir(), "missing.log")
	if err := in.archiveLog(); err == nil {
		t.Error("a missing log was archived")
	}
	in.opts.LogFile = filepath.Join(t.TempDir(), "install.log")
	os.WriteFile(in.opts.LogFile, []byte("log\n"), 0o644)
	blockDir(t, in, "root")
	if err := in.archiveLog(); err == nil {
		t.Error("archiving under a file succeeded")
	}

	// A resolv.conf that cannot be removed is reported; a missing one is not.
	if err := removeIfPresent(""); err != nil {
		t.Errorf("empty path: %v", err)
	}
	stuck := filepath.Join(t.TempDir(), "resolv.conf")
	os.MkdirAll(filepath.Join(stuck, "occupied"), 0o755)
	wantErr(t, removeIfPresent(stuck), "remove")
}

func TestBaseInstallFailures(t *testing.T) {
	var r recorder
	// Nothing to install is an error before any command runs.
	in := stagedInstaller(t, twoPartConfig(), &r)
	in.opts.Repo = "https://repo.example/current"
	wantErr(t, in.baseInstall(), "no packages")
	if len(r.cmds) != 0 {
		t.Errorf("commands ran with nothing to install: %s", r.flat())
	}

	in.opts.Packages = []string{"sunlight-base"}
	in.opts.Runner = r.failOn("xbps-install")
	wantErr(t, in.baseInstall(), "boom")

	// The extra mirror is recorded for the installed system's updates …
	in = stagedInstaller(t, twoPartConfig(), &r)
	in.opts.Repo, in.opts.Packages = "https://repo.example/current", []string{"sunlight-base"}
	in.opts.ExtraRepos = []string{"https://mirror.example/sunlight"}
	if err := in.baseInstall(); err != nil {
		t.Fatalf("baseInstall: %v", err)
	}
	if got := readTarget(t, in, "etc", "xbps.d", "10-sunlight.conf"); !strings.Contains(got, "repository=https://mirror.example/sunlight\n") {
		t.Errorf("mirror config:\n%s", got)
	}
	// … and failing to record it fails the stage.
	in = stagedInstaller(t, twoPartConfig(), &r)
	in.opts.ExtraRepos = []string{"https://mirror.example/sunlight"}
	blockDir(t, in, "etc", "xbps.d")
	wantErr(t, in.writeTargetRepos(), "mkdir")
	in = stagedInstaller(t, twoPartConfig(), &r)
	in.opts.ExtraRepos = []string{"https://mirror.example/sunlight"}
	blockFile(t, in, "etc", "xbps.d", "10-sunlight.conf")
	wantErr(t, in.writeTargetRepos(), "write")
}

func TestFstabStage(t *testing.T) {
	fakeBin(t, map[string]string{"blkid": `for a; do last=$a; done; echo "uuid-$(basename "$last")"`})
	var r recorder
	in := stagedInstaller(t, twoPartConfig(), &r)
	if err := in.fstab(); err != nil {
		t.Fatalf("fstab: %v", err)
	}
	got := readTarget(t, in, "etc", "fstab")
	// Every mount by the UUID read back from its own device, root with the
	// first fsck pass.
	for _, want := range []string{"UUID=uuid-sda2\t/\text4\tdefaults\t0 1", "UUID=uuid-sda1\t/boot\tvfat\tdefaults\t0 2"} {
		if !strings.Contains(got, want) {
			t.Errorf("fstab missing %q:\n%s", want, got)
		}
	}

	// The filesystem cannot be written: reported.
	in = stagedInstaller(t, twoPartConfig(), &r)
	blockWithDir(t, in, "etc", "fstab")
	wantErr(t, in.fstab(), "write fstab")

	// blkid failing stops the stage rather than writing device paths that
	// may not be stable.
	fakeBin(t, map[string]string{"blkid": "exit 2"})
	in = stagedInstaller(t, twoPartConfig(), &r)
	wantErr(t, in.fstab(), "blkid")
	if _, err := os.Stat(filepath.Join(in.opts.Root, "etc", "fstab")); err == nil {
		t.Error("fstab written although the UUIDs could not be read")
	}
}

func TestKeyfileAndCrypttabFailures(t *testing.T) {
	home := &config.Media{Name: "sda3", Type: "crypt", FsType: "ext4", Mountpoint: "/home"}
	var r recorder

	fakeCryptsetup(t)
	in, _ := cryptInstaller(t, cryptConfig("/boot", home), &r)
	blockDir(t, in, "etc", "cryptkeys")
	wantErr(t, in.crypttab(), "mkdir")

	in, _ = cryptInstaller(t, cryptConfig("/boot", home), &r)
	blockWithDir(t, in, keyfilePath) // a stale file or link at the name is replaced
	wantErr(t, in.crypttab(), "write keyfile")

	in, _ = cryptInstaller(t, cryptConfig("/boot", home), &r)
	blockWithDir(t, in, "etc", "crypttab")
	wantErr(t, in.crypttab(), "write crypttab")

	// An encrypted /boot embeds the keyfile in the initramfs through a
	// dracut drop-in; failing to write it fails the stage.
	in, _ = cryptInstaller(t, cryptConfig("/boot/efi"), &r)
	blockDir(t, in, "etc", "dracut.conf.d")
	wantErr(t, in.crypttab(), "mkdir")
	in, _ = cryptInstaller(t, cryptConfig("/boot/efi"), &r)
	blockFile(t, in, "etc", "dracut.conf.d", "10-slinstaller-keyfile.conf")
	wantErr(t, in.crypttab(), "write dracut keyfile conf")

	// cryptsetup refusing the key, or failing to report a UUID.
	fakeBin(t, map[string]string{"cryptsetup": `cat > /dev/null; for a; do [ "$a" = luksAddKey ] && exit 1; done; exit 0`})
	in, _ = cryptInstaller(t, cryptConfig("/boot", home), &r)
	wantErr(t, in.crypttab(), "luksAddKey")

	fakeBin(t, map[string]string{"cryptsetup": `[ "$1" = luksUUID ] && exit 1; cat > /dev/null`})
	in, _ = cryptInstaller(t, cryptConfig("/boot", home), &r)
	wantErr(t, in.crypttab(), "luksUUID")
	in, _ = cryptInstaller(t, cryptConfig("/boot"), &r)
	wantErr(t, in.bootloader(), "luksUUID")
}

func TestSwapfileFailures(t *testing.T) {
	cfg := twoPartConfig()
	cfg.SwapFileSize = "2G"
	var r recorder
	in := stagedInstaller(t, cfg, &r)
	in.opts.Runner = r.failOn("mkswap")
	wantErr(t, in.swapfile(), "boom")
	if _, err := os.Stat(filepath.Join(in.opts.Root, "etc", "fstab")); err == nil {
		t.Error("fstab got a swap line although mkswap failed")
	}

	in = stagedInstaller(t, cfg, &r)
	blockWithDir(t, in, "etc", "fstab")
	wantErr(t, in.swapfile(), "open")

	in = stagedInstaller(t, cfg, &r)
	fullDisk(t, in, "etc", "fstab")
	if err := in.swapfile(); err == nil {
		t.Error("a failed fstab write was not reported")
	}
}

func TestBootloaderFailures(t *testing.T) {
	var r recorder
	// A layout with no ESP cannot boot under UEFI: refused before GRUB runs.
	noESP := &config.Config{TargetMedia: []*config.Media{{Name: "sda", Type: "disk", Children: []*config.Media{
		{Name: "sda1", FsType: "ext4", Mountpoint: "/"},
	}}}}
	in := stagedInstaller(t, noESP, &r)
	wantErr(t, in.bootloader(), "no EFI System Partition")
	if len(r.cmds) != 0 {
		t.Errorf("GRUB ran without an ESP: %s", r.flat())
	}

	in = stagedInstaller(t, twoPartConfig(), &r)
	blockWithDir(t, in, "etc", "default", "grub")
	wantErr(t, in.bootloader(), "open")

	in = stagedInstaller(t, twoPartConfig(), &r)
	fullDisk(t, in, "etc", "default", "grub")
	if err := in.bootloader(); err == nil {
		t.Error("a failed write to /etc/default/grub was not reported")
	}

	in = stagedInstaller(t, twoPartConfig(), &r)
	mkTarget(t, in, "etc", "default")
	in.opts.Runner = r.failOn("grub-install")
	wantErr(t, in.bootloader(), "boom")
	if strings.Contains(r.flat(), "grub-mkconfig") {
		t.Errorf("grub-mkconfig ran after grub-install failed:\n%s", r.flat())
	}

	// With no target root there is nowhere to install GRUB to: no command is
	// produced. (Run from a scratch directory, as the path is then relative.)
	t.Chdir(t.TempDir())
	os.MkdirAll(filepath.Join("etc", "default"), 0o755)
	in = stagedInstaller(t, twoPartConfig(), &r)
	in.opts.Root = ""
	r = recorder{}
	in.opts.Runner = r.run
	wantErr(t, in.bootloader(), "no target root")
	if len(r.cmds) != 0 {
		t.Errorf("GRUB ran with no target root: %s", r.flat())
	}
}

func TestBootloaderForLVMAndRAIDRoots(t *testing.T) {
	// A root on LVM: the initramfs has to activate the volume, and is
	// rebuilt so it carries the lvm module.
	var r recorder
	in := stagedInstaller(t, lvmConfig("/boot", false), &r)
	mkTarget(t, in, "etc", "default")
	if err := in.bootloader(); err != nil {
		t.Fatalf("bootloader (LVM): %v", err)
	}
	if args := cmdlineArgs(t, in); !contains(args, "rd.lvm.lv=vg0/root") {
		t.Errorf("LVM root: %v", args)
	}
	if !strings.Contains(r.flat(), "xbps-reconfigure -fa") {
		t.Errorf("initramfs not rebuilt for LVM:\n%s", r.flat())
	}

	// A root on RAID: assembled by UUID in the initramfs, with the arrays
	// recorded and dracut's mdraid module forced in.
	fakeBin(t, map[string]string{"mdadm": `case "$*" in
*--export*) echo "MD_UUID=aaaa:bbbb:cccc:dddd" ;;
*--brief*/dev/md0*) echo "ARRAY /dev/md0 UUID=aaaa:bbbb:cccc:dddd" ;;
esac`})
	r = recorder{}
	in = stagedInstaller(t, raidConfig(), &r)
	mkTarget(t, in, "etc", "default")
	if err := in.bootloader(); err != nil {
		t.Fatalf("bootloader (RAID): %v", err)
	}
	if args := cmdlineArgs(t, in); !contains(args, "rd.md.uuid=aaaa:bbbb:cccc:dddd") {
		t.Errorf("RAID root: %v", args)
	}
	if got := readTarget(t, in, "etc", "mdadm.conf"); !strings.Contains(got, "ARRAY /dev/md0 UUID=aaaa:bbbb:cccc:dddd") {
		t.Errorf("mdadm.conf:\n%s", got)
	}
	if got := readTarget(t, in, "etc", "dracut.conf.d", "10-slinstaller-mdraid.conf"); !strings.Contains(got, `add_dracutmodules+=" mdraid "`) {
		t.Errorf("dracut mdraid conf:\n%s", got)
	}
	if !strings.Contains(r.flat(), "xbps-reconfigure -fa") {
		t.Errorf("initramfs not rebuilt for RAID:\n%s", r.flat())
	}
}

func contains(ss []string, v string) bool {
	for _, s := range ss {
		if s == v {
			return true
		}
	}
	return false
}

func TestBootloaderRAIDFailures(t *testing.T) {
	var r recorder
	// The array's UUID cannot be read.
	fakeBin(t, map[string]string{"mdadm": "exit 1"})
	in := stagedInstaller(t, raidConfig(), &r)
	mkTarget(t, in, "etc", "default")
	wantErr(t, in.bootloader(), "mdadm")

	// The UUID is read but the arrays cannot be listed for mdadm.conf.
	fakeBin(t, map[string]string{"mdadm": `case "$*" in *--export*) echo "MD_UUID=aa:bb" ;; *) exit 1 ;; esac`})
	in = stagedInstaller(t, raidConfig(), &r)
	mkTarget(t, in, "etc", "default")
	wantErr(t, in.bootloader(), "mdadm --detail --brief /dev/md0")

	// mdadm.conf is written but the dracut drop-in cannot be.
	fakeBin(t, map[string]string{"mdadm": `case "$*" in *--export*) echo "MD_UUID=aa:bb" ;; *) echo "ARRAY /dev/md0" ;; esac`})
	in = stagedInstaller(t, raidConfig(), &r)
	mkTarget(t, in, "etc", "default")
	blockDir(t, in, "etc", "dracut.conf.d")
	wantErr(t, in.bootloader(), "mkdir")

	in = stagedInstaller(t, raidConfig(), &r)
	blockFile(t, in, "etc", "dracut.conf.d", "10-slinstaller-mdraid.conf")
	wantErr(t, in.forceDracutModule("mdraid"), "write")

	in = stagedInstaller(t, raidConfig(), &r)
	blockDir(t, in, "etc")
	wantErr(t, in.writeMdadmConf(), "mkdir")
	in = stagedInstaller(t, raidConfig(), &r)
	blockWithDir(t, in, "etc", "mdadm.conf")
	wantErr(t, in.writeMdadmConf(), "write mdadm.conf")
}

func TestWhichVolumeGrubMustUnlock(t *testing.T) {
	var r recorder
	// LVM-on-LUKS with /boot inside the group: GRUB has to open the group's
	// encrypted physical volume, which is also on the root's path.
	in := stagedInstaller(t, lvmConfig("/boot/efi", true), &r)
	if got := in.bootCryptDevice(); got != "/dev/sda2" {
		t.Errorf("bootCryptDevice = %q, want the encrypted physical volume", got)
	}
	if in.rootVGName() != "vg0" {
		t.Errorf("rootVGName = %q", in.rootVGName())
	}
	var pv, lv = in.plan.Parts[1], in.plan.Parts[2]
	if !in.isRootCrypt(pv) {
		t.Error("the root group's encrypted physical volume is on the root's path")
	}
	if in.isRootCrypt(lv) {
		t.Error("an unencrypted volume is not a crypt volume")
	}
	// The same layout with /boot on its own unencrypted ESP: nothing for
	// GRUB to unlock.
	in = stagedInstaller(t, lvmConfig("/boot", true), &r)
	if got := in.bootCryptDevice(); got != "" {
		t.Errorf("bootCryptDevice with an unencrypted /boot = %q", got)
	}
	// A layout with neither / nor /boot has no boot volume at all.
	in = stagedInstaller(t, &config.Config{TargetMedia: []*config.Media{{Name: "sda", Type: "disk", Children: []*config.Media{
		{Name: "sda1", FsType: "ext4", Mountpoint: "/home"},
	}}}}, &r)
	if got := in.bootCryptDevice(); got != "" || in.rootVGName() != "" {
		t.Errorf("no root: bootCryptDevice = %q, rootVGName = %q", got, in.rootVGName())
	}
}

func TestFirstBootFailures(t *testing.T) {
	base := func() *config.Config {
		c := twoPartConfig()
		c.Language = "en_US.UTF-8"
		c.Users = []*config.User{{Login: "ion", Password: "$6$x", Admin: true, SSHKeys: []string{"ssh-ed25519 AAAA"}}}
		return c
	}
	glibc := func(t *testing.T, in *installer) {
		def := mkTarget(t, in, "etc", "default")
		os.WriteFile(filepath.Join(def, "libc-locales"), []byte("#en_US.UTF-8 UTF-8\n"), 0o644)
	}
	lv := 3
	for name, tc := range map[string]struct {
		cfg   func() *config.Config
		setup func(t *testing.T, in *installer, r *recorder)
		want  string
	}{
		"hostname": {func() *config.Config { c := base(); c.Hostname = "box"; return c },
			func(t *testing.T, in *installer, r *recorder) { blockFile(t, in, "etc", "hostname") }, "write hostname"},
		"machine-id": {base,
			func(t *testing.T, in *installer, r *recorder) { blockFile(t, in, "etc", "machine-id") }, "write machine-id"},
		"timezone": {func() *config.Config { c := base(); c.Timezone = "UTC"; return c },
			func(t *testing.T, in *installer, r *recorder) { blockWithDir(t, in, "etc", "localtime") }, "link localtime"},
		"keymap": {func() *config.Config { c := base(); c.Keyboard = "us"; return c },
			func(t *testing.T, in *installer, r *recorder) { blockWithDir(t, in, "etc", "rc.conf") }, "open rc.conf"},
		"printk": {func() *config.Config { c := base(); c.ConsoleLogLevel = &lv; return c },
			func(t *testing.T, in *installer, r *recorder) { blockDir(t, in, "etc", "sysctl.d") }, "sysctl.d"},
		"locale": {base,
			func(t *testing.T, in *installer, r *recorder) { blockFile(t, in, "etc", "locale.conf") }, "write locale.conf"},
		"locale generation": {base,
			func(t *testing.T, in *installer, r *recorder) {
				glibc(t, in)
				in.opts.Runner = r.failOn("glibc-locales")
			}, "boom"},
		"useradd": {base,
			func(t *testing.T, in *installer, r *recorder) { in.opts.Runner = r.failOn("useradd") }, "boom"},
		"ssh keys": {base,
			func(t *testing.T, in *installer, r *recorder) { blockDir(t, in, "home") }, "system:"},
		"ssh key ownership": {base,
			func(t *testing.T, in *installer, r *recorder) { in.opts.Runner = r.failOn("chown") }, "boom"},
		"sudoers": {base,
			func(t *testing.T, in *installer, r *recorder) { blockDir(t, in, "etc", "sudoers.d") }, "system:"},
		"root lock": {base,
			func(t *testing.T, in *installer, r *recorder) { in.opts.Runner = r.failOn("usermod") }, "boom"},
	} {
		t.Run(name, func(t *testing.T) {
			var r recorder
			in := stagedInstaller(t, tc.cfg(), &r)
			tc.setup(t, in, &r)
			wantErr(t, in.firstBoot(), tc.want)
		})
	}
}

func TestNetworkFailures(t *testing.T) {
	static := func(mask string) *config.Config {
		c := twoPartConfig()
		c.NetworkInterfaces = []*config.Interface{{Name: "eth0", Addrs: []*config.Address{nil, {IP: "10.0.0.2", Netmask: mask}}}}
		return c
	}
	var r recorder
	in := stagedInstaller(t, static("255.0.255.0"), &r)
	wantErr(t, in.network(), "eth0")

	in = stagedInstaller(t, static("255.255.255.0"), &r)
	blockWithDir(t, in, "etc", "dhcpcd.conf")
	wantErr(t, in.network(), "write")

	in = stagedInstaller(t, static("255.255.255.0"), &r)
	fullDisk(t, in, "etc", "dhcpcd.conf")
	if err := in.network(); err == nil {
		t.Error("a failed dhcpcd.conf write was not reported")
	}

	// A slinit target without an sshd service to enable: reported, not
	// left with a dangling link.
	in = stagedInstaller(t, twoPartConfig(), &r)
	dir := mkTarget(t, in, "usr", "lib", "slinit.d")
	os.WriteFile(filepath.Join(dir, "boot"), nil, 0o644)
	wantErr(t, in.network(), "enable slinit service sshd")
}

func TestCopyNetworkFromTheLiveSystem(t *testing.T) {
	saved := liveNetworkPaths
	defer func() { liveNetworkPaths = saved }()
	live := t.TempDir()
	// A directory with a file, a nested directory and a symlink (skipped: it
	// would not mean the same thing in the target).
	wpa := filepath.Join(live, "wpa_supplicant")
	os.MkdirAll(filepath.Join(wpa, "nested"), 0o750)
	os.WriteFile(filepath.Join(wpa, "wlan0.conf"), []byte("psk\n"), 0o600)
	os.WriteFile(filepath.Join(wpa, "nested", "x"), []byte("x\n"), 0o644)
	os.Symlink("/dev/null", filepath.Join(wpa, "link"))
	liveNetworkPaths = []string{wpa, filepath.Join(live, "absent")}

	cfg := twoPartConfig()
	cfg.CopyNetwork = true
	var r recorder
	in := stagedInstaller(t, cfg, &r)
	if err := in.network(); err != nil {
		t.Fatalf("network with copyNetwork: %v", err)
	}
	// The secret keeps its private mode in the target.
	st, err := os.Stat(filepath.Join(in.opts.Root, wpa, "wlan0.conf"))
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Errorf("copied credentials: %v, mode %v", err, st.Mode().Perm())
	}
	if _, err := os.Stat(filepath.Join(in.opts.Root, wpa, "nested", "x")); err != nil {
		t.Errorf("nested file not copied: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(in.opts.Root, wpa, "link")); err == nil {
		t.Error("a symlink was copied into the target")
	}

	// A copy that cannot be written fails the stage.
	in = stagedInstaller(t, cfg, &r)
	blockDir(t, in, strings.TrimPrefix(live, "/"))
	wantErr(t, in.network(), "copyNetwork")

	// A nested entry that cannot be written, likewise.
	in = stagedInstaller(t, cfg, &r)
	blockDir(t, in, strings.TrimPrefix(filepath.Join(wpa, "nested"), "/"))
	wantErr(t, in.network(), "copyNetwork")

	// A single file whose target directory cannot be made.
	single := filepath.Join(live, "dhcpcd.conf")
	os.WriteFile(single, []byte("hostname\n"), 0o644)
	liveNetworkPaths = []string{single}
	in = stagedInstaller(t, cfg, &r)
	blockDir(t, in, strings.TrimPrefix(live, "/"))
	wantErr(t, in.network(), "copyNetwork")

	// A path that cannot be examined (its parent is a file) is an error,
	// unlike one that is simply absent.
	liveNetworkPaths = []string{filepath.Join(single, "under-a-file")}
	in = stagedInstaller(t, cfg, &r)
	wantErr(t, in.network(), "copyNetwork: stat")
}

func TestCopyNetworkUnreadableSource(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads through file modes")
	}
	saved := liveNetworkPaths
	defer func() { liveNetworkPaths = saved }()
	live := t.TempDir()
	var r recorder
	cfg := twoPartConfig()
	cfg.CopyNetwork = true

	secret := filepath.Join(live, "secret.conf")
	os.WriteFile(secret, []byte("x"), 0o000)
	liveNetworkPaths = []string{secret}
	wantErr(t, stagedInstaller(t, cfg, &r).network(), "copyNetwork")

	closed := filepath.Join(live, "closed")
	os.MkdirAll(closed, 0o000)
	defer os.Chmod(closed, 0o755)
	liveNetworkPaths = []string{closed}
	wantErr(t, stagedInstaller(t, cfg, &r).network(), "copyNetwork")
}

func TestHooks(t *testing.T) {
	cfg := twoPartConfig()
	cfg.PostInstall = []*config.Hook{
		nil,
		{Cmd: ""}, // nothing to run
		{Cmd: "echo in $chrootDir", Chroot: true},
		{Cmd: "echo out"},
	}
	var r recorder
	in := stagedInstaller(t, cfg, &r)
	if err := in.postHooks(); err != nil {
		t.Fatalf("postHooks: %v", err)
	}
	if len(r.cmds) != 2 {
		t.Fatalf("ran %d hooks, want 2 (the empty ones skipped):\n%s", len(r.cmds), r.flat())
	}
	chrooted := strings.Join(r.cmds[0], " ")
	// A chroot hook runs inside the target, where the target is "/", under
	// sh when the target has no bash.
	if !strings.HasPrefix(chrooted, "chroot "+in.opts.Root+" env chrootDir=/ chrooted=1") || !strings.Contains(chrooted, " sh -c echo in /") {
		t.Errorf("chroot hook = %q", chrooted)
	}
	if outside := strings.Join(r.cmds[1], " "); strings.HasPrefix(outside, "chroot") || !strings.Contains(outside, "chrooted=0") {
		t.Errorf("live hook = %q", outside)
	}

	// With bash in the target, a chroot hook runs under it as a login shell,
	// as a hook written for clr-installer expects.
	bin := mkTarget(t, in, "usr", "bin")
	os.WriteFile(filepath.Join(bin, "bash"), nil, 0o755)
	if got := strings.Join(in.hookShell(true), " "); got != "bash -l -c" {
		t.Errorf("hook shell with bash in the target = %q", got)
	}
	// No bash on the live system either: sh.
	t.Setenv("PATH", t.TempDir())
	if got := strings.Join(in.hookShell(false), " "); got != "sh -c" {
		t.Errorf("hook shell without bash = %q", got)
	}

	// A failing hook stops the install.
	in.opts.Runner = r.failOn("echo in")
	wantErr(t, in.postHooks(), "boom")
}

func TestOutputWriterIsUsedWhenGiven(t *testing.T) {
	var r recorder
	var out bytes.Buffer
	in := stagedInstaller(t, twoPartConfig(), &r)
	in.opts.Output = &out
	if in.output() != &out {
		t.Error("the configured output writer was not used")
	}
}

func TestBootloaderOptions(t *testing.T) {
	off := false
	cfg := twoPartConfig()
	cfg.KernelArguments = &config.KernelArguments{Add: []string{"console=tty0", "console=ttyS1,57600n8"}}
	cfg.Bootloader = &config.Bootloader{Terminal: "serial", Removable: true, UpdateNVRAM: &off, ProbeOtherOS: true}
	var r recorder
	in := stagedInstaller(t, cfg, &r)
	mkTarget(t, in, "etc", "default")
	if err := in.bootloader(); err != nil {
		t.Fatal(err)
	}
	grubDefault := readTarget(t, in, "etc", "default", "grub")
	for _, want := range []string{
		`GRUB_TERMINAL="console serial"`,
		`GRUB_SERIAL_COMMAND="serial --unit=1 --speed=57600 --word=8 --parity=no --stop=1"`,
		"GRUB_DISABLE_OS_PROBER=false",
	} {
		if !strings.Contains(grubDefault, want) {
			t.Errorf("/etc/default/grub missing %q:\n%s", want, grubDefault)
		}
	}
	flat := r.flat()
	if !strings.Contains(flat, "--no-nvram") || !strings.Contains(flat, "--removable") {
		t.Errorf("grub-install:\n%s", flat)
	}
	// The file is written before grub-mkconfig reads it.
	if strings.Index(flat, "grub-mkconfig") < strings.Index(flat, "--removable") {
		t.Errorf("order:\n%s", flat)
	}

	// Without the section nothing of the kind is written.
	r = recorder{}
	in = stagedInstaller(t, twoPartConfig(), &r)
	mkTarget(t, in, "etc", "default")
	if err := in.bootloader(); err != nil {
		t.Fatal(err)
	}
	if got := readTarget(t, in, "etc", "default", "grub"); strings.Contains(got, "GRUB_TERMINAL") || strings.Contains(got, "OS_PROBER") {
		t.Errorf("default /etc/default/grub:\n%s", got)
	}
	if strings.Contains(r.flat(), "--removable") || strings.Contains(r.flat(), "--no-nvram") {
		t.Errorf("default grub-install:\n%s", r.flat())
	}
}

func TestWriteFiles(t *testing.T) {
	cfg := twoPartConfig()
	cfg.WriteFiles = []*config.WriteFile{
		nil,
		{Path: "/etc/motd", Content: "Welcome.\n"},
		{Path: "/etc/app/secret.conf", Content: "token\n", Permissions: "0600"},
		{Path: "/usr/local/bin/tool", Content: "#!/bin/sh\n", Permissions: "4755"},
		{Path: "/etc/resolv.conf", Content: "nameserver 10.0.0.1\n"},
	}
	var r recorder
	in := stagedInstaller(t, cfg, &r)
	// In the installed system resolv.conf is a link to a place that, from
	// the installer, is on the live system. It is replaced, not followed.
	outside := filepath.Join(t.TempDir(), "live-resolv.conf")
	os.WriteFile(outside, []byte("live\n"), 0o644)
	etc := mkTarget(t, in, "etc")
	os.Symlink(outside, filepath.Join(etc, "resolv.conf"))
	// An existing file keeps nothing of its old mode.
	os.WriteFile(filepath.Join(etc, "motd"), []byte("old\n"), 0o600)

	if err := in.writeFiles(); err != nil {
		t.Fatal(err)
	}
	if got := readTarget(t, in, "etc", "motd"); got != "Welcome.\n" {
		t.Errorf("motd = %q", got)
	}
	for rel, want := range map[string]os.FileMode{
		"etc/motd": 0o644, "etc/app/secret.conf": 0o600, "usr/local/bin/tool": 0o755 | os.ModeSetuid,
	} {
		st, err := os.Stat(filepath.Join(in.opts.Root, rel))
		if err != nil || st.Mode()&(os.ModePerm|os.ModeSetuid) != want {
			t.Errorf("%s: %v, mode %v, want %v", rel, err, st.Mode(), want)
		}
	}
	if live, _ := os.ReadFile(outside); string(live) != "live\n" {
		t.Errorf("a file outside the target was written through a link: %q", live)
	}
	if fi, _ := os.Lstat(filepath.Join(etc, "resolv.conf")); fi.Mode()&os.ModeSymlink != 0 {
		t.Error("the link was left in place")
	}

	// A directory that is a link out of the target is refused outright.
	in = stagedInstaller(t, cfg, &r)
	away := t.TempDir()
	os.Symlink(away, filepath.Join(in.opts.Root, "etc"))
	err := in.writeFiles()
	wantErr(t, err, "leads outside the installed system")
	if left, _ := os.ReadDir(away); len(left) != 0 {
		t.Errorf("files were written outside the target: %d", len(left))
	}

	// What cannot be written is reported, with the stage failing.
	one := func(path, perm string) *config.Config {
		c := twoPartConfig()
		c.WriteFiles = []*config.WriteFile{{Path: path, Content: "x", Permissions: perm}}
		return c
	}
	in = stagedInstaller(t, one("/etc/motd", ""), &r)
	blockDir(t, in, "etc")
	wantErr(t, in.writeFiles(), "write-files")
	// They are written as the last step of configuring the system.
	in = stagedInstaller(t, one("/etc/motd", ""), &r)
	if err := in.firstBoot(); err != nil || readTarget(t, in, "etc", "motd") != "x" {
		t.Errorf("first-boot did not write the files: %v", err)
	}
	in = stagedInstaller(t, one("/etc/motd", ""), &r)
	blockWithDir(t, in, "etc", "motd")
	wantErr(t, in.writeFiles(), "write-files")
	in = stagedInstaller(t, one("/etc/motd", "rwx"), &r)
	wantErr(t, in.writeFiles(), "not an octal mode")
}

func TestNetworkLinksAreMadeAtBoot(t *testing.T) {
	cfg := twoPartConfig()
	cfg.NetworkInterfaces = []*config.Interface{
		{Name: "bond0", Bond: &config.Bond{Members: []string{"eth0", "eth1"}, Mode: "802.3ad"}},
		{Name: "bond0.100", VLAN: &config.VLAN{ID: 100, Link: "bond0"},
			Addrs:  []*config.Address{{IP: "10.0.100.2", Netmask: "24"}},
			Routes: []*config.Route{nil, {To: "10.1.0.0/16", Via: "10.0.100.254"}}},
		{Name: "br0", Bridge: &config.Bridge{Members: []string{"eth2"}}},
	}
	// On a slinit system: a service of its own, enabled, running the script.
	var r recorder
	in := stagedInstaller(t, cfg, &r)
	dir := mkTarget(t, in, "usr", "lib", "slinit.d")
	os.WriteFile(filepath.Join(dir, "boot"), nil, 0o644)
	os.WriteFile(filepath.Join(dir, "sshd"), nil, 0o644)
	if err := in.network(); err != nil {
		t.Fatal(err)
	}
	script := readTarget(t, in, "etc", "slinit.d", "sunlight-netlinks.sh")
	if !strings.Contains(script, "type bond mode 802.3ad") || !strings.Contains(script, "type vlan id 100") || !strings.Contains(script, "type bridge") {
		t.Errorf("link script:\n%s", script)
	}
	if st, _ := os.Stat(filepath.Join(in.opts.Root, "etc", "slinit.d", "sunlight-netlinks.sh")); st.Mode().Perm() != 0o755 {
		t.Errorf("link script mode %v, want it executable", st.Mode().Perm())
	}
	if svc := readTarget(t, in, "etc", "slinit.d", "sunlight-netlinks"); !strings.Contains(svc, "command = /etc/slinit.d/sunlight-netlinks.sh") {
		t.Errorf("service:\n%s", svc)
	}
	if target, err := os.Readlink(filepath.Join(in.opts.Root, "etc", "slinit.d", "boot.d", "sunlight-netlinks")); err != nil || target != "/etc/slinit.d/sunlight-netlinks" {
		t.Errorf("the service is not enabled: %q, %v", target, err)
	}
	conf := readTarget(t, in, "etc", "dhcpcd.conf")
	if !strings.Contains(conf, "denyinterfaces eth0 eth1 eth2") || !strings.Contains(conf, "static routes=10.1.0.0/16 10.0.100.254") {
		t.Errorf("dhcpcd.conf:\n%s", conf)
	}

	// On a runit system the script is run from rc.local.
	in = stagedInstaller(t, cfg, &r)
	if err := in.network(); err != nil {
		t.Fatal(err)
	}
	if rc := readTarget(t, in, "etc", "rc.local"); !strings.Contains(rc, "/etc/slinit.d/sunlight-netlinks.sh") {
		t.Errorf("rc.local:\n%s", rc)
	}
	// With no such interfaces nothing of the kind is written.
	in = stagedInstaller(t, twoPartConfig(), &r)
	_ = in.network()
	if _, err := os.Stat(filepath.Join(in.opts.Root, "etc", "slinit.d", "sunlight-netlinks.sh")); err == nil {
		t.Error("a link script was written with no links configured")
	}

	// What cannot be written fails the stage.
	in = stagedInstaller(t, cfg, &r)
	blockDir(t, in, "etc", "slinit.d")
	wantErr(t, in.network(), "system:")
	in = stagedInstaller(t, cfg, &r)
	blockFile(t, in, "etc", "slinit.d", "sunlight-netlinks.sh")
	wantErr(t, in.network(), "sunlight-netlinks.sh")
	in = stagedInstaller(t, cfg, &r)
	blockWithDir(t, in, "etc", "rc.local")
	wantErr(t, in.network(), "system:")
	in = stagedInstaller(t, cfg, &r)
	sd := mkTarget(t, in, "usr", "lib", "slinit.d")
	os.WriteFile(filepath.Join(sd, "boot"), nil, 0o644)
	blockFile(t, in, "etc", "slinit.d", "sunlight-netlinks")
	wantErr(t, in.network(), "write sunlight-netlinks")
}
