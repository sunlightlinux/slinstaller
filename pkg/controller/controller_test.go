package controller

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sunlightlinux/slinstaller/pkg/blockdev"
	"github.com/sunlightlinux/slinstaller/pkg/config"
)

// twoPartConfig is a minimal valid UEFI config: an ESP and a root.
func twoPartConfig() *config.Config {
	return &config.Config{
		TargetMedia: []*config.Media{{
			Name: "sda",
			Type: "disk",
			Children: []*config.Media{
				{Name: "sda1", Type: "part", FsType: "vfat", Mountpoint: "/boot", Size: "512M"},
				{Name: "sda2", Type: "part", FsType: "ext4", Mountpoint: "/", Size: "20G"},
			},
		}},
	}
}

// recorder is a storage.Runner that records commands instead of running
// them, so tests can watch the controller without touching a disk.
type recorder struct{ cmds [][]string }

func (r *recorder) run(args []string) error {
	r.cmds = append(r.cmds, args)
	return nil
}

func (r *recorder) flat() string {
	var b strings.Builder
	for _, c := range r.cmds {
		b.WriteString(strings.Join(c, " ") + "\n")
	}
	return b.String()
}

func TestInstallUnknownStage(t *testing.T) {
	var r recorder
	err := Install(twoPartConfig(), Options{Through: "nope", Runner: r.run, Devices: noDevices})
	if err == nil || !strings.Contains(err.Error(), "unknown stage") {
		t.Fatalf("unknown stage error = %v, want 'unknown stage'", err)
	}
	if len(r.cmds) != 0 {
		t.Errorf("unknown-stage install ran %d commands", len(r.cmds))
	}
}

func TestInstallThroughPartition(t *testing.T) {
	var r recorder
	// Through "partition" runs pre-install (no hooks here) and partition,
	// then stops before mount, which would make real syscalls.
	if err := Install(twoPartConfig(), Options{Through: "partition", Runner: r.run, Devices: noDevices}); err != nil {
		t.Fatalf("Install through partition: %v", err)
	}
	flat := r.flat()
	for _, want := range []string{
		"parted -s /dev/sda mklabel gpt",
		"mkfs.vfat", "mkfs.ext4", "partprobe /dev/sda",
	} {
		if !strings.Contains(flat, want) {
			t.Errorf("partition commands missing %q\n%s", want, flat)
		}
	}
}

func TestInstallThroughPreInstallRunsHooks(t *testing.T) {
	cfg := twoPartConfig()
	cfg.PreInstall = []*config.Hook{{Cmd: "echo hello"}}

	var r recorder
	// Stopping at pre-install runs the hook and nothing destructive.
	if err := Install(cfg, Options{Through: "pre-install", Runner: r.run, Devices: noDevices}); err != nil {
		t.Fatalf("Install through pre-install: %v", err)
	}
	got := r.flat()
	if !strings.Contains(got, " -c echo hello") {
		t.Errorf("pre-install hook not run: %q", got)
	}
	// clr-style hook variables are passed as environment.
	if !strings.Contains(got, "chrootDir=") || !strings.Contains(got, "chrooted=0") {
		t.Errorf("hook env vars missing: %q", got)
	}
	if strings.Contains(r.flat(), "parted") {
		t.Error("pre-install stage reached partition")
	}
}

func TestInterfacesFromConfig(t *testing.T) {
	ifaces := interfacesFromConfig([]*config.Interface{
		nil,                          // skipped
		{Name: ""},                   // skipped (no name)
		{Name: "eth0", DHCP: "true"}, // DHCP, not static
		{ // addresses, no dhcp field -> static (clr: static unless dhcp:true)
			Name:    "eth1",
			Addrs:   []*config.Address{{IP: "10.0.0.5", Netmask: "255.255.255.0"}},
			Gateway: "10.0.0.1",
			DNS:     "10.0.0.1",
			Domain:  "lan",
		},
		{ // addresses but dhcp:true -> not static
			Name:  "eth2",
			DHCP:  "true",
			Addrs: []*config.Address{{IP: "10.0.0.6", Netmask: "24"}},
		},
	})

	if len(ifaces) != 3 {
		t.Fatalf("got %d interfaces, want 3 (eth0, eth1, eth2)", len(ifaces))
	}
	byName := map[string]bool{}
	for _, i := range ifaces {
		byName[i.Name] = i.Static
	}
	if byName["eth0"] {
		t.Error("eth0 (dhcp:true, no addrs) should not be static")
	}
	if !byName["eth1"] {
		t.Error("eth1 (addrs, no dhcp) should be static")
	}
	if byName["eth2"] {
		t.Error("eth2 (dhcp:true with addrs) should not be static")
	}

	// eth1 carries its address, gateway, dns and domain through.
	found := false
	for _, i := range ifaces {
		if i.Name != "eth1" {
			continue
		}
		found = true
		if len(i.Addrs) != 1 || i.Addrs[0].IP != "10.0.0.5" {
			t.Errorf("eth1 address not mapped: %+v", i.Addrs)
		}
		if i.Gateway != "10.0.0.1" || i.DNS != "10.0.0.1" || i.Domain != "lan" {
			t.Errorf("eth1 gateway/dns/domain not mapped: %+v", i)
		}
	}
	if !found {
		t.Error("eth1 missing from result")
	}
}

func TestBootsSlinit(t *testing.T) {
	withSlinit := t.TempDir()
	bin := filepath.Join(withSlinit, "usr", "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "slinit"), []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	// The binary alone, with no boot service: not a system slinit boots.
	binaryOnly := t.TempDir()
	os.MkdirAll(filepath.Join(binaryOnly, "usr", "bin"), 0o755)
	os.WriteFile(filepath.Join(binaryOnly, "usr", "bin", "slinit"), []byte("x"), 0o755)
	os.MkdirAll(filepath.Join(withSlinit, "usr", "lib", "slinit.d"), 0o755)
	os.WriteFile(filepath.Join(withSlinit, "usr", "lib", "slinit.d", "boot"), nil, 0o644)
	// The service set in /etc, as sunlight-slinit-services ships it.
	etcBoot := t.TempDir()
	os.MkdirAll(filepath.Join(etcBoot, "usr", "bin"), 0o755)
	os.WriteFile(filepath.Join(etcBoot, "usr", "bin", "slinit"), []byte("x"), 0o755)
	os.MkdirAll(filepath.Join(etcBoot, "etc", "slinit.d"), 0o755)
	os.WriteFile(filepath.Join(etcBoot, "etc", "slinit.d", "boot"), nil, 0o644)

	libBoot := t.TempDir()
	os.MkdirAll(filepath.Join(libBoot, "usr", "bin"), 0o755)
	os.WriteFile(filepath.Join(libBoot, "usr", "bin", "slinit"), []byte("x"), 0o755)
	os.MkdirAll(filepath.Join(libBoot, "lib", "slinit.d"), 0o755)
	os.WriteFile(filepath.Join(libBoot, "lib", "slinit.d", "boot"), nil, 0o644)

	cases := []struct {
		name   string
		root   string
		remove map[string]bool
		want   bool
	}{
		{"slinit installed", withSlinit, nil, true},
		{"slinit not installed (runit-only base)", t.TempDir(), nil, false},
		{"config removes init=", withSlinit, map[string]bool{slinitInitArg: true}, false},
		{"unrelated removal", withSlinit, map[string]bool{"quiet": true}, true},
		{"the binary without a boot service", binaryOnly, nil, false},
		{"the boot service in /etc/slinit.d", etcBoot, nil, true},
		{"the boot service in /lib/slinit.d (unmerged /usr)", libBoot, nil, true},
	}
	for _, c := range cases {
		if got := bootsSlinit(c.root, c.remove); got != c.want {
			t.Errorf("%s: bootsSlinit = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestRemoveIfPresent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "resolv.conf")
	if err := os.WriteFile(path, []byte("nameserver 192.168.0.1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := removeIfPresent(path); err != nil {
		t.Fatalf("removeIfPresent: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("%s still exists: the live medium's DNS would outlive the install", path)
	}
	// Already gone, or never copied: not an error.
	if err := removeIfPresent(path); err != nil {
		t.Errorf("second removal: %v", err)
	}
	if err := removeIfPresent(""); err != nil {
		t.Errorf("empty path: %v", err)
	}
}

func TestCopyLiveNetwork(t *testing.T) {
	live := t.TempDir()
	// A wpa_supplicant directory with a file, and a dhcpcd.conf.
	wpaDir := filepath.Join(live, "etc", "wpa_supplicant")
	if err := os.MkdirAll(wpaDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wpaDir, "wpa_supplicant-wlan0.conf"), []byte("network={}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(live, "etc", "dhcpcd.conf"), []byte("# custom\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	saved := liveNetworkPaths
	liveNetworkPaths = []string{
		filepath.Join(live, "etc", "wpa_supplicant"),
		filepath.Join(live, "etc", "dhcpcd.conf"),
		filepath.Join(live, "etc", "absent.conf"), // skipped, not an error
	}
	defer func() { liveNetworkPaths = saved }()

	target := t.TempDir()
	in := &installer{opts: Options{Root: target}}
	if err := in.copyLiveNetwork(); err != nil {
		t.Fatalf("copyLiveNetwork: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(target, live, "etc", "wpa_supplicant", "wpa_supplicant-wlan0.conf"))
	if err != nil || string(got) != "network={}\n" {
		t.Errorf("wpa_supplicant file not copied: %q, %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(target, live, "etc", "dhcpcd.conf")); err != nil {
		t.Errorf("dhcpcd.conf not copied: %v", err)
	}
}

func TestHooksReceiveConfigEnv(t *testing.T) {
	cfg := twoPartConfig()
	cfg.PreInstall = []*config.Hook{{Cmd: "echo hi"}}
	cfg.Environment = map[string]string{"FOO": "bar", "BAZ": "qux"}

	var r recorder
	if err := Install(cfg, Options{Through: "pre-install", Runner: r.run, Devices: noDevices, ConfigDir: "/cfg"}); err != nil {
		t.Fatalf("Install through pre-install: %v", err)
	}
	got := r.flat()
	// Config env is injected, sorted with the hook variables.
	if !strings.Contains(got, "env BAZ=qux FOO=bar chrootDir=/mnt chrooted=0 yamlDir=/cfg ") {
		t.Errorf("config env not injected in sorted order: %q", got)
	}
}

// The install log is copied into the target's /root, private to root.
func TestArchiveLog(t *testing.T) {
	root, live := t.TempDir(), filepath.Join(t.TempDir(), "slinstaller.log")
	if err := os.WriteFile(live, []byte("==> partition\n+ sgdisk --zap-all /dev/vda\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	in := &installer{opts: Options{Root: root, LogFile: live}}
	if err := in.archiveLog(); err != nil {
		t.Fatal(err)
	}
	got := filepath.Join(root, "root", "slinstaller.log")
	data, err := os.ReadFile(got)
	if err != nil || string(data) != "==> partition\n+ sgdisk --zap-all /dev/vda\n" {
		t.Fatalf("archived log = %q, %v", data, err)
	}
	if fi, _ := os.Stat(got); fi.Mode().Perm() != 0o600 {
		t.Errorf("archived log mode %v, want 0600", fi.Mode().Perm())
	}
	// No log file set: nothing to do.
	if err := (&installer{opts: Options{Root: root}}).archiveLog(); err != nil {
		t.Errorf("no LogFile: %v", err)
	}
}

// A wiping install releases the disk before it rewrites the table: an old
// install's RAID array is stopped and the signatures erased, all before
// parted touches it. A reuse install leaves the disk alone.
func TestPartitionReleasesTheDiskFirst(t *testing.T) {
	devs := func() ([]blockdev.Device, error) {
		return []blockdev.Device{{Name: "sda", Path: "/dev/sda", Type: "disk", Children: []blockdev.Device{
			{Name: "sda1", Path: "/dev/sda1", Type: "part", Children: []blockdev.Device{
				{Name: "md127", Path: "/dev/md127", Type: "raid1"},
			}},
		}}}, nil
	}
	var r recorder
	if err := Install(twoPartConfig(), Options{Through: "partition", Runner: r.run, Devices: devs}); err != nil {
		t.Fatal(err)
	}
	flat := r.flat()
	stop := strings.Index(flat, "mdadm --stop /dev/md127")
	wipe := strings.Index(flat, "wipefs -a /dev/sda\n")
	mklabel := strings.Index(flat, "parted -s /dev/sda mklabel gpt")
	if stop < 0 || wipe < 0 || !(stop < wipe && wipe < mklabel) {
		t.Errorf("want mdadm --stop, then wipefs, then mklabel:\n%s", flat)
	}

	cfg := twoPartConfig()
	cfg.ReusePartitions = true
	r = recorder{}
	if err := Install(cfg, Options{Through: "partition", Runner: r.run, Devices: devs}); err != nil {
		t.Fatal(err)
	}
	if f := r.flat(); strings.Contains(f, "wipefs") || strings.Contains(f, "mdadm --stop") {
		t.Errorf("a reuse install released the disk:\n%s", f)
	}
}

// noDevices is a machine with no block devices, so a test's install finds
// nothing to release and never consults the host's lsblk.
func noDevices() ([]blockdev.Device, error) { return nil, nil }

// Hook variables are substituted into the command as clr-installer does,
// both forms, a longer name before one it begins with.
func TestExpandHookVars(t *testing.T) {
	vars := map[string]string{"chrootDir": "/mnt", "chrooted": "0", "yamlDir": "/media/cfg", "chroot": "X"}
	got := expandHookVars(vars, `cp ${yamlDir}/motd $chrootDir/etc/motd; echo $chrooted ${chroot}`)
	if want := `cp /media/cfg/motd /mnt/etc/motd; echo 0 X`; got != want {
		t.Errorf("expandHookVars = %q, want %q", got, want)
	}
}

func TestApplyLogLevel(t *testing.T) {
	three := 3
	// Nil leaves args untouched.
	in := []string{"quiet", "loglevel=7"}
	if got := applyLogLevel(append([]string{}, in...), nil); len(got) != 2 || got[1] != "loglevel=7" {
		t.Errorf("nil changed args: %v", got)
	}
	// A configured level drops an existing loglevel= and appends the new one.
	got := applyLogLevel([]string{"quiet", "loglevel=7", "ro"}, &three)
	want := []string{"quiet", "ro", "loglevel=3"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

// postArchive: false, clr-installer's switch, keeps the log off the target.
func TestPostArchiveFalse(t *testing.T) {
	root, live := t.TempDir(), filepath.Join(t.TempDir(), "slinstaller.log")
	os.WriteFile(live, []byte("log\n"), 0o600)
	off := false
	in := &installer{cfg: &config.Config{PostArchive: &off}, opts: Options{Root: root, LogFile: live}}
	if err := in.archiveLog(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "root", "slinstaller.log")); err == nil {
		t.Error("the log was archived with postArchive: false")
	}
}

// The saved config and the archived log are private files in the target's
// /root whatever was there: a link is replaced, not written through, an
// existing loose mode does not stay, and a /root out of the target is refused.
func TestRootFilesNotThroughLinks(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	victim := filepath.Join(outside, "shadow")
	os.WriteFile(victim, []byte("keep\n"), 0o644)
	os.MkdirAll(filepath.Join(root, "root"), 0o700)
	os.Symlink(victim, filepath.Join(root, "root", "slinstaller.yaml"))
	os.WriteFile(filepath.Join(root, "root", "slinstaller.log"), []byte("old"), 0o644)
	live := filepath.Join(t.TempDir(), "live.log")
	os.WriteFile(live, []byte("LOG\n"), 0o600)
	in := &installer{cfg: &config.Config{Hostname: "box"}, opts: Options{Root: root, LogFile: live}}
	if err := in.saveConfig(); err != nil {
		t.Fatal(err)
	}
	if err := in.archiveLog(); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(victim); string(got) != "keep\n" {
		t.Errorf("written through the link: %q", got)
	}
	for _, n := range []string{"slinstaller.yaml", "slinstaller.log"} {
		fi, err := os.Lstat(filepath.Join(root, "root", n))
		if err != nil || !fi.Mode().IsRegular() || fi.Mode().Perm() != 0o600 {
			t.Errorf("%s: %v %v", n, fi.Mode(), err)
		}
	}

	root2 := t.TempDir()
	os.Symlink(outside, filepath.Join(root2, "root"))
	in.opts.Root = root2
	// An absolute link is the installed system's: followed inside the
	// target, never onto the live system's directory of that name.
	if err := in.saveConfig(); err != nil {
		t.Errorf("/root an absolute link: %v", err)
	}
	if ents, _ := os.ReadDir(outside); len(ents) != 1 {
		t.Errorf("written outside: %v", ents)
	}
}

// A write-file under a target directory that links out makes nothing outside.
func TestWriteFilesMakesNoDirsOutside(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	os.Symlink(outside, filepath.Join(root, "etc"))
	in := &installer{cfg: &config.Config{WriteFiles: []*config.WriteFile{{Path: "/etc/app/deep/x.conf", Content: "x"}}}, opts: Options{Root: root}}
	if err := in.writeFiles(); err == nil || !strings.Contains(err.Error(), "leads outside") {
		t.Errorf("err %v", err)
	}
	if ents, _ := os.ReadDir(outside); len(ents) != 0 {
		t.Errorf("made outside: %v", ents)
	}
}

// A copied network file takes the live one's mode over an existing file's,
// and a link at its place is replaced, not written through.
func TestCopyPathModeAndLinks(t *testing.T) {
	root, live, outside := t.TempDir(), t.TempDir(), t.TempDir()
	src := filepath.Join(live, "wpa.conf")
	os.WriteFile(src, []byte("psk=secret\n"), 0o600)
	fi, _ := os.Lstat(src)
	dst := filepath.Join(root, "etc", "wpa.conf")
	os.MkdirAll(filepath.Dir(dst), 0o755)
	os.WriteFile(dst, []byte("packaged\n"), 0o644)
	if err := copyPath(root, src, dst, fi); err != nil {
		t.Fatal(err)
	}
	if st, _ := os.Stat(dst); st.Mode().Perm() != 0o600 {
		t.Errorf("mode %v over a 0644 file", st.Mode().Perm())
	}
	victim := filepath.Join(outside, "f")
	os.WriteFile(victim, []byte("keep\n"), 0o644)
	os.Remove(dst)
	os.Symlink(victim, dst)
	if err := copyPath(root, src, dst, fi); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(victim); string(got) != "keep\n" {
		t.Error("written through a link")
	}
}

// A copied directory under a target directory that links out makes nothing
// outside.
func TestCopyPathMakesNoDirsOutside(t *testing.T) {
	root, live, outside := t.TempDir(), t.TempDir(), t.TempDir()
	os.MkdirAll(filepath.Join(live, "wpa", "sub"), 0o755)
	os.WriteFile(filepath.Join(live, "wpa", "sub", "f"), []byte("x"), 0o600)
	os.MkdirAll(filepath.Join(root, "etc"), 0o755)
	os.Symlink(outside, filepath.Join(root, "etc", "wpa"))
	fi, _ := os.Lstat(filepath.Join(live, "wpa"))
	if err := copyPath(root, filepath.Join(live, "wpa"), filepath.Join(root, "etc", "wpa"), fi); err == nil {
		t.Error("copied through a link out of the target")
	}
	if ents, _ := os.ReadDir(outside); len(ents) != 0 {
		t.Errorf("made outside: %v", ents)
	}
}

// writeSynced makes a new file of the mode asked, never through what is there.
func TestWriteSynced(t *testing.T) {
	p := filepath.Join(t.TempDir(), "key")
	if err := writeSynced(p, []byte("k"), 0o400); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(p); fi.Mode().Perm() != 0o400 {
		t.Errorf("mode %v", fi.Mode().Perm())
	}
	if err := writeSynced(p, []byte("k"), 0o400); err == nil {
		t.Error("written over an existing file")
	}
}

// An image's /root that is an absolute link is followed inside the target.
func TestRootFilesThroughAnImagesAbsoluteRootLink(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "var", "roothome"), 0o700)
	os.Symlink("/var/roothome", filepath.Join(root, "root"))
	in := &installer{cfg: &config.Config{Hostname: "box"}, opts: Options{Root: root}}
	if err := in.saveConfig(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "var", "roothome", "slinstaller.yaml")); err != nil {
		t.Errorf("not saved where the installed system's /root is: %v", err)
	}
}

// The target's own config files are not written through a link onto the
// live system: an image's xbps.d entry or dracut drop-in that is a link out
// is replaced by a file of the target's.
func TestTargetConfigNotThroughLinks(t *testing.T) {
	var r recorder
	in := stagedInstaller(t, twoPartConfig(), &r)
	outside := filepath.Join(t.TempDir(), "victim")
	os.WriteFile(outside, []byte("keep\n"), 0o644)
	dir := mkTarget(t, in, "etc", "xbps.d")
	os.Symlink(outside, filepath.Join(dir, "10-sunlight.conf"))
	in.opts.TargetRepos = []string{"https://mirror.example/sunlight"}
	if err := in.writeTargetRepos(); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(outside); string(got) != "keep\n" {
		t.Errorf("written through the link: %q", got)
	}
	if got := readTarget(t, in, "etc", "xbps.d", "10-sunlight.conf"); !strings.Contains(got, "mirror.example") {
		t.Errorf("10-sunlight.conf = %q", got)
	}
}

// first-boot's hostname and the bootloader's /etc/default/grub are not
// written through a link out of the target.
func TestFirstBootAndGrubNotThroughLinks(t *testing.T) {
	var r recorder
	cfg := twoPartConfig()
	cfg.Hostname = "box"
	in := stagedInstaller(t, cfg, &r)
	outside := t.TempDir()
	for _, n := range []string{"hostname", "grub"} {
		os.WriteFile(filepath.Join(outside, n), []byte("keep\n"), 0o644)
	}
	etc := mkTarget(t, in, "etc")
	os.Symlink(filepath.Join(outside, "hostname"), filepath.Join(etc, "hostname"))
	mkTarget(t, in, "etc", "default")
	os.Symlink(filepath.Join(outside, "grub"), filepath.Join(etc, "default", "grub"))
	in.firstBoot()
	in.bootloader()
	for _, n := range []string{"hostname", "grub"} {
		if got, _ := os.ReadFile(filepath.Join(outside, n)); string(got) != "keep\n" {
			t.Errorf("%s written through the link: %q", n, got)
		}
	}
}
