package controller

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sunlightlinux/slinstaller/pkg/config"
)

// A stage that failed part of the way through is run again from its start
// when the install is resumed at it. What it wrote the first time must not
// be written twice, and what it did must not be done twice.
func TestStagesCanBeRunAgain(t *testing.T) {
	cfg := twoPartConfig()
	cfg.SwapFileSize, cfg.Keyboard, cfg.Hostname = "1G", "us", "box"
	cfg.KernelArguments = &config.KernelArguments{Add: []string{"quiet"}}
	cfg.Users = []*config.User{{Login: "ion", Password: "$6$x", Admin: true}}
	cfg.NetworkInterfaces = []*config.Interface{{Name: "eth0", Addrs: []*config.Address{{IP: "10.0.0.2", Netmask: "24"}}}}
	var r recorder
	in := stagedInstaller(t, cfg, &r)
	in.opts.ResumeFrom = "swapfile"
	mkTarget(t, in, "etc", "default")

	once := func(rel ...string) string {
		t.Helper()
		return readTarget(t, in, rel...)
	}
	for attempt := 1; attempt <= 2; attempt++ {
		for name, stage := range map[string]func() error{"swapfile": in.swapfile, "bootloader": in.bootloader, "first-boot": in.firstBoot, "network": in.network} {
			if err := stage(); err != nil {
				t.Fatalf("%s, attempt %d: %v", name, attempt, err)
			}
		}
		if attempt == 1 {
			// As the first attempt's useradd would have left it.
			os.WriteFile(filepath.Join(in.opts.Root, "etc", "passwd"), []byte("root:x:0:0::/root:/bin/sh\nion:x:1000:1000::/home/ion:/bin/sh\n"), 0o644)
		}
	}
	for rel, text := range map[string]string{
		"etc/fstab":        "/swapfile",
		"etc/default/grub": "GRUB_CMDLINE_LINUX_DEFAULT=",
		"etc/dhcpcd.conf":  "interface eth0",
		"etc/rc.conf":      "KEYMAP=",
	} {
		if n := strings.Count(once(strings.Split(rel, "/")...), text); n != 1 {
			t.Errorf("%s has %q %d times after two attempts", rel, text, n)
		}
	}
	// The account was made once: the second attempt found it there.
	if n := strings.Count(r.flat(), "useradd"); n != 1 {
		t.Errorf("useradd ran %d times:\n%s", n, r.flat())
	}
	// An account whose name merely starts the same is another account.
	if hasAccount(in.opts.Root, "io") || !hasAccount(in.opts.Root, "ion") || hasAccount(t.TempDir(), "ion") {
		t.Error("hasAccount")
	}
}

// Resuming past the stage that made it, the keyfile from the first attempt
// is the one used: no second key is added to the volumes.
func TestResumeReusesTheKeyfile(t *testing.T) {
	args := fakeCryptsetup(t)
	home := &config.Media{Name: "sda3", Type: "crypt", FsType: "ext4", Mountpoint: "/home"}
	var r recorder
	in, _ := cryptInstaller(t, cryptConfig("/boot", home), &r)
	if err := in.crypttab(); err != nil {
		t.Fatal(err)
	}
	first, _ := os.ReadFile(filepath.Join(in.opts.Root, keyfilePath))
	added := func() int {
		data, _ := os.ReadFile(filepath.Join(args, "args"))
		return strings.Count(string(data), "luksAddKey")
	}
	before := added()

	in.opts.ResumeFrom, in.keyfile = "fstab", ""
	if err := in.crypttab(); err != nil {
		t.Fatal(err)
	}
	again, _ := os.ReadFile(filepath.Join(in.opts.Root, keyfilePath))
	if string(first) != string(again) || added() != before || in.keyfile != keyfilePath {
		t.Errorf("resumed crypttab: key changed %v, luksAddKey %d -> %d, keyfile %q", string(first) != string(again), before, added(), in.keyfile)
	}
	// A fresh install always makes its own.
	in.opts.ResumeFrom = ""
	if err := in.crypttab(); err != nil {
		t.Fatal(err)
	}
	if added() == before {
		t.Error("a fresh install did not add its key")
	}
}

// A first attempt that failed between two volumes left one without the
// keyfile in a slot: the resume adds it there, and only there.
func TestResumeAddsAMissingKeySlot(t *testing.T) {
	args := fakeCryptsetup(t)
	home := &config.Media{Name: "sda3", Type: "crypt", FsType: "ext4", Mountpoint: "/home"}
	var r recorder
	in, _ := cryptInstaller(t, cryptConfig("/boot", home), &r)
	if err := in.crypttab(); err != nil {
		t.Fatal(err)
	}
	// The keyfile opens sda2, not sda3.
	script := "#!/bin/sh\necho \"$@\" >> \"" + args + "/args\"\n" +
		"case \"$*\" in *--test-passphrase*sda3) exit 2 ;; esac\n" +
		"[ \"$1\" = luksUUID ] && { echo \"uuid-$(basename \"$2\")\"; exit 0; }\ncat > /dev/null\n"
	os.WriteFile(filepath.Join(args, "cryptsetup"), []byte(script), 0o755)
	os.Remove(filepath.Join(args, "args"))

	in.opts.ResumeFrom, in.keyfile = "fstab", ""
	if err := in.crypttab(); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(args, "args"))
	var adds []string
	for _, l := range strings.Split(string(data), "\n") {
		if strings.Contains(l, "luksAddKey") {
			adds = append(adds, l)
		}
	}
	if len(adds) != 1 || !strings.Contains(adds[0], "/dev/sda3") {
		t.Errorf("luksAddKey on resume: %q\nall:\n%s", adds, data)
	}
}

func TestResumeFromValidation(t *testing.T) {
	var r recorder
	err := Install(twoPartConfig(), Options{ResumeFrom: "partition", Runner: r.run, Devices: noDevices})
	if err == nil || !strings.Contains(err.Error(), `cannot be resumed at "partition"`) || len(r.cmds) != 0 {
		t.Errorf("resume at partition: %v", err)
	}
	// From a later stage: mount and meta-fs again, then that stage on.
	in := &installer{opts: Options{ResumeFrom: "bootloader"}}
	var names []string
	for _, s := range in.stages() {
		names = append(names, s.name)
	}
	if got := strings.Join(names, ","); got != "mount,meta-fs,bootloader,first-boot,network,save-config,post-install" {
		t.Errorf("stages from bootloader = %s", got)
	}
	if got := strings.Join(ResumableStages(), ","); !strings.HasPrefix(got, "base-install,fstab") || strings.Contains(got, "mount") {
		t.Errorf("ResumableStages = %s", got)
	}
}

const targetGrubCfg = `set default="0"
menuentry 'Sunlight OS GNU/Linux' {
	linux	/boot/vmlinuz-6.18 root=UUID=abcd ro quiet
	initrd	/boot/initramfs-6.18.img
}
`

// With power-state: kexec the installed kernel is loaded while the target
// is still there to read it from. The install stands whatever comes of it.
func TestFinishLoadsTheKernelForKexec(t *testing.T) {
	cfg := twoPartConfig()
	cfg.PowerState = "kexec"
	var r recorder
	in := stagedInstaller(t, cfg, &r)
	boot := mkTarget(t, in, "boot", "grub")
	os.WriteFile(filepath.Join(boot, "grub.cfg"), []byte(targetGrubCfg), 0o644)
	os.WriteFile(filepath.Join(in.opts.Root, "boot", "vmlinuz-6.18"), nil, 0o644)
	os.WriteFile(filepath.Join(in.opts.Root, "boot", "initramfs-6.18.img"), nil, 0o644)
	if err := in.finish(); err != nil {
		t.Fatal(err)
	}
	want := "kexec -l " + in.opts.Root + "/boot/vmlinuz-6.18 --initrd=" + in.opts.Root + "/boot/initramfs-6.18.img --append=root=UUID=abcd ro quiet\n"
	if r.flat() != want {
		t.Errorf("kexec load:\n%s\nwant:\n%s", r.flat(), want)
	}
	// A kernel that cannot be loaded: said as what it is — the system is
	// installed, and a normal reboot will start it.
	in.opts.Runner = r.failOn("kexec")
	wantErr(t, in.finish(), "the system is installed, but its kernel could not be loaded for kexec")
	in = stagedInstaller(t, cfg, &r) // no grub.cfg at all
	wantErr(t, in.finish(), "reboot instead")
	// Any other ending loads nothing.
	cfg.PowerState = "reboot"
	r = recorder{}
	in = stagedInstaller(t, cfg, &r)
	if err := in.finish(); err != nil || len(r.cmds) != 0 {
		t.Errorf("finish with a reboot: %v, ran %s", err, r.flat())
	}
}

// system-upgrade brings the target up to date once the base system is in:
// xbps first, then everything, through the config's proxy, and before any
// kernel is removed.
func TestSystemUpgrade(t *testing.T) {
	cfg := twoPartConfig()
	cfg.SystemUpgrade, cfg.HTTPSProxy, cfg.KernelRemove = true, "http://proxy:3128", []string{"linux6.18"}
	cfg.Image = "/img/base.tar"
	var r recorder
	in := stagedInstaller(t, cfg, &r)
	bin := mkTarget(t, in, "usr", "bin")
	os.WriteFile(filepath.Join(bin, "grub-install"), nil, 0o755)
	if err := in.baseInstall(); err != nil {
		t.Fatal(err)
	}
	flat := r.flat()
	env := "chroot " + in.opts.Root + " env https_proxy=http://proxy:3128 http_proxy=http://proxy:3128 "
	steps := []string{"tar -xpf /img/base.tar", env + "xbps-install -Syu xbps\n", env + "xbps-install -Syu\n", "xbps-remove -Ry linux6.18"}
	last := -1
	for _, s := range steps {
		i := strings.Index(flat, s)
		if i < 0 || i < last {
			t.Fatalf("step %q missing or out of order:\n%s", s, flat)
		}
		last = i
	}
	in.opts.Runner = r.failOn("-Syu xbps")
	wantErr(t, in.baseInstall(), "boom")
	// Not asked for: not done.
	cfg.SystemUpgrade = false
	r = recorder{}
	in = stagedInstaller(t, cfg, &r)
	os.WriteFile(filepath.Join(mkTarget(t, in, "usr", "bin"), "grub-install"), nil, 0o755)
	if err := in.baseInstall(); err != nil || strings.Contains(r.flat(), "-Syu") {
		t.Errorf("without system-upgrade: %v\n%s", err, r.flat())
	}
}

// The upgrade reads the target's own xbps.d, so the Sunlight mirror has to
// be recorded there before it runs; upgrading first resolved everything
// against Void alone.
func TestSystemUpgradeSeesTheMirror(t *testing.T) {
	cfg := twoPartConfig()
	cfg.SystemUpgrade = true
	var r recorder
	in := stagedInstaller(t, cfg, &r)
	in.opts.Repo, in.opts.Packages = "https://repo.example/current", []string{"sunlight-base"}
	in.opts.ExtraRepos = []string{"https://mirror.example/sunlight"}
	conf := filepath.Join(in.opts.Root, "etc", "xbps.d", "10-sunlight.conf")
	upgraded := false
	in.opts.Runner = func(args []string) error {
		if strings.Contains(strings.Join(args, " "), "xbps-install -Syu") {
			upgraded = true
			if _, err := os.Stat(conf); err != nil {
				t.Errorf("upgrade ran before the mirror was recorded: %v", err)
			}
		}
		return r.run(args)
	}
	if err := in.baseInstall(); err != nil {
		t.Fatal(err)
	}
	if !upgraded {
		t.Fatalf("no upgrade ran:\n%s", r.flat())
	}
}

// The first-boot stage leaves a random seed for the first boot.
func TestFirstBootSeedsTheRNG(t *testing.T) {
	var r recorder
	in := stagedInstaller(t, twoPartConfig(), &r)
	dir := mkTarget(t, in, "usr", "lib", "slinit.d")
	os.WriteFile(filepath.Join(dir, "boot"), nil, 0o644)
	if err := in.firstBoot(); err != nil {
		t.Fatal(err)
	}
	if seed, err := os.ReadFile(filepath.Join(in.opts.Root, "var", "lib", "seedrng", "seed.no-credit")); err != nil || len(seed) != 32 {
		t.Errorf("seed after first-boot: %v, %d bytes", err, len(seed))
	}
	in = stagedInstaller(t, twoPartConfig(), &r)
	os.WriteFile(filepath.Join(mkTarget(t, in, "usr", "lib", "slinit.d"), "boot"), nil, 0o644)
	blockDir(t, in, "var", "lib")
	wantErr(t, in.firstBoot(), "system:")
}

// The target's own resolv.conf — a link to a resolver's runtime file, or a
// file an image brought — is set aside for the install and put back, never
// written through or deleted; a resumed install's own copy is not mistaken
// for it.
func TestResolvConfOfTheTargetIsKept(t *testing.T) {
	live := filepath.Join(t.TempDir(), "resolv.conf")
	os.WriteFile(live, []byte("nameserver 10.0.0.1\n"), 0o644)

	// A link: the live file behind it is untouched, and the link returns.
	var r recorder
	in := stagedInstaller(t, twoPartConfig(), &r)
	etc := mkTarget(t, in, "etc")
	behind := filepath.Join(t.TempDir(), "runtime-resolv.conf")
	os.WriteFile(behind, []byte("nameserver 192.0.2.53\n"), 0o644)
	dst := filepath.Join(etc, "resolv.conf")
	os.Symlink(behind, dst)
	if err := in.copyResolv(live); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(behind); string(got) != "nameserver 192.0.2.53\n" {
		t.Errorf("written through the target's link: %q", got)
	}
	if got, _ := os.ReadFile(dst); string(got) != resolvMark+"nameserver 10.0.0.1\n" {
		t.Errorf("install-time resolv.conf = %q", got)
	}
	if err := removeIfPresent(in.resolvCopy); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(in.resolvSaved, in.resolvCopy); err != nil {
		t.Fatal(err)
	}
	if l, err := os.Readlink(dst); err != nil || l != behind {
		t.Errorf("the link was not put back: %q, %v", l, err)
	}

	// A file of the image's: kept through a resume, then back.
	in = stagedInstaller(t, twoPartConfig(), &r)
	etc = mkTarget(t, in, "etc")
	dst = filepath.Join(etc, "resolv.conf")
	os.WriteFile(dst, []byte("nameserver 203.0.113.1\n"), 0o644)
	if err := in.copyResolv(live); err != nil {
		t.Fatal(err)
	}
	again := stagedInstaller(t, twoPartConfig(), &r)
	again.opts.Root = in.opts.Root
	if err := again.copyResolv(live); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(again.resolvSaved); string(got) != "nameserver 203.0.113.1\n" {
		t.Errorf("the image's resolv.conf after a resume = %q", got)
	}

	// A copy a failed attempt left, with the live file changed since (the
	// DNS it failed on, fixed): still known for ours, not put back.
	in = stagedInstaller(t, twoPartConfig(), &r)
	mkTarget(t, in, "etc")
	in.copyResolv(live)
	os.WriteFile(live, []byte("nameserver 10.0.0.2\n"), 0o644)
	again = stagedInstaller(t, twoPartConfig(), &r)
	again.opts.Root = in.opts.Root
	if err := again.copyResolv(live); err != nil || again.resolvSaved != "" {
		t.Errorf("a stale copy was set aside as the target's own: %q, %v", again.resolvSaved, err)
	}
	os.WriteFile(live, []byte("nameserver 10.0.0.1\n"), 0o644)

	// No resolv.conf of its own, and a resume: nothing to put back.
	in = stagedInstaller(t, twoPartConfig(), &r)
	mkTarget(t, in, "etc")
	in.copyResolv(live)
	again = stagedInstaller(t, twoPartConfig(), &r)
	again.opts.Root = in.opts.Root
	if err := again.copyResolv(live); err != nil || again.resolvSaved != "" {
		t.Errorf("a resume set its own copy aside: %q, %v", again.resolvSaved, err)
	}
}

// Asked to stop, the install finishes the stage running, runs no further
// one, and says it was stopped.
func TestStopBetweenStages(t *testing.T) {
	stop := make(chan struct{})
	var r recorder
	var stages []string
	err := Install(twoPartConfig(), Options{Through: "partition", Runner: r.run, Devices: noDevices, Stop: stop,
		Progress: func(done, total int, stage string) {
			stages = append(stages, stage)
			if stage == "pre-install" {
				close(stop)
			}
		}})
	if !errors.Is(err, ErrStopped) || !strings.Contains(err.Error(), "before ") {
		t.Fatalf("err = %v", err)
	}
	if len(stages) != 1 || strings.Contains(r.flat(), "parted") {
		t.Errorf("went on past the stop: stages %v\n%s", stages, r.flat())
	}
	// Before partition there is nothing to resume, and it says nothing of it.
	if strings.Contains(err.Error(), "--resume-from") {
		t.Errorf("a resume offered before partitioning: %v", err)
	}
}

// A run that fails or is stopped after meta-fs puts the target's own
// resolv.conf back, as finish does: rebooted into, the disk is not left
// pinned to the live medium's DNS.
func TestCleanupPutsTheTargetsResolvConfBack(t *testing.T) {
	live := filepath.Join(t.TempDir(), "resolv.conf")
	os.WriteFile(live, []byte("nameserver 10.0.0.1\n"), 0o644)
	var r recorder
	in := stagedInstaller(t, twoPartConfig(), &r)
	dst := filepath.Join(mkTarget(t, in, "etc"), "resolv.conf")
	os.WriteFile(dst, []byte("nameserver 203.0.113.1\n"), 0o644)
	if err := in.copyResolv(live); err != nil {
		t.Fatal(err)
	}
	in.cleanup()
	if got, _ := os.ReadFile(dst); string(got) != "nameserver 203.0.113.1\n" {
		t.Errorf("after a failed run, resolv.conf = %q", got)
	}
	if _, err := os.Stat(dst + ".slinstaller-saved"); err == nil {
		t.Error("the set-aside copy was left behind")
	}
}

// A resume after a reboot finds nothing of the stack the first attempt
// built: it opens the encrypted volumes, assembles the arrays and
// activates the volume groups before mounting.
func TestResumeReassemblesTheStack(t *testing.T) {
	args := fakeCryptsetup(t)
	var r recorder
	in, _ := cryptInstaller(t, lvmConfig("/boot", true), &r)
	in.opts.ResumeFrom, in.opts.Passphrase = "bootloader", "a long passphrase"
	if err := in.reassemble(); err != nil {
		t.Fatal(err)
	}
	opened, _ := os.ReadFile(filepath.Join(args, "args"))
	if !strings.Contains(string(opened), "open /dev/sda2 cryptvg0") && !strings.Contains(string(opened), "luksOpen /dev/sda2 cryptvg0") {
		t.Errorf("the encrypted PV was not opened:\n%s", opened)
	}
	if !strings.Contains(r.flat(), "vgchange -ay vg0") {
		t.Errorf("the volume group was not activated:\n%s", r.flat())
	}
	// No passphrase, nothing can be opened: said so.
	in.opts.Passphrase = ""
	wantErr(t, in.reassemble(), "resuming needs its passphrase")

	r = recorder{}
	raid := stagedInstaller(t, raidConfig(), &r)
	raid.opts.ResumeFrom = "bootloader"
	if err := raid.reassemble(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(r.flat(), "mdadm --assemble /dev/md0 ") {
		t.Errorf("the array was not assembled:\n%s", r.flat())
	}

	// Assembled by the live system under its own name: stopped first, then
	// assembled as the install's.
	saved := sysBlock
	defer func() { sysBlock = saved }()
	sysBlock = t.TempDir()
	os.MkdirAll(filepath.Join(sysBlock, "sda2", "holders", "md127"), 0o755)
	r = recorder{}
	if err := raid.reassemble(); err != nil {
		t.Fatal(err)
	}
	flat := r.flat()
	stop, asm := strings.Index(flat, "mdadm --stop /dev/md127"), strings.Index(flat, "mdadm --assemble /dev/md0")
	if stop < 0 || asm < stop || strings.Count(flat, "--stop") != 1 {
		t.Errorf("an array held under another name:\n%s", flat)
	}
}

// What cleanup cannot do is said in the log, not hidden.
func TestCleanupWarns(t *testing.T) {
	var r recorder
	var log bytes.Buffer
	in := stagedInstaller(t, twoPartConfig(), &r)
	in.opts.Log = &log
	in.resolvCopy = filepath.Join(mkTarget(t, in, "etc"), "resolv.conf")
	os.WriteFile(in.resolvCopy, []byte(resolvMark), 0o644)
	in.resolvSaved = filepath.Join(in.opts.Root, "etc", "gone") // cannot be put back
	in.cleanup()
	if !strings.Contains(log.String(), "warning: put back") {
		t.Errorf("log = %q", log.String())
	}
	log.Reset()
	in.resolvCopy = filepath.Join(in.opts.Root, "etc", "resolv2.conf")
	in.resolvSaved = filepath.Join(in.opts.Root, "etc", "no-such-saved")
	in.cleanup()
	if !strings.Contains(log.String(), "warning: put back the target's resolv.conf") {
		t.Errorf("log = %q", log.String())
	}
}

// A resume needs the mark a first attempt leaves in the target: without it
// the disk holds no install of ours left unfinished, and nothing is done to it.
func TestResumeNeedsTheMark(t *testing.T) {
	var r recorder
	in := stagedInstaller(t, twoPartConfig(), &r)
	in.plan.Parts = nil // nothing to mount for real: the mark is what is tested
	if err := in.mount(); err != nil {
		t.Fatalf("first mount: %v", err)
	}
	mark := filepath.Join(in.opts.Root, resumeMark)
	if !exists(mark) {
		t.Fatal("no mark after the first mount")
	}
	in.opts.ResumeFrom = "bootloader"
	if err := in.mount(); err != nil {
		t.Errorf("resume with the mark: %v", err)
	}
	// A mark from an attempt with another disk layout is not this one's.
	in.cfg.TargetMedia[0].Children[1].Size = "10G"
	wantErr(t, in.mount(), "another disk layout")
	os.Remove(mark)
	wantErr(t, in.mount(), "nothing to resume")
	// A link in its place is not taken for it.
	os.Symlink("/etc/hostname", mark)
	wantErr(t, in.mount(), "nothing to resume")
}

// A resumed first-boot generates the locale its first attempt enabled but
// may not have generated.
func TestResumedFirstBootGeneratesLocale(t *testing.T) {
	var r recorder
	cfg := twoPartConfig()
	cfg.Language = "en_US.UTF-8"
	in := stagedInstaller(t, cfg, &r)
	os.MkdirAll(filepath.Join(in.opts.Root, "etc", "default"), 0o755)
	os.WriteFile(filepath.Join(in.opts.Root, "etc", "default", "libc-locales"), []byte("en_US.UTF-8 UTF-8\n"), 0o644)
	in.opts.ResumeFrom = "first-boot"
	in.firstBoot()
	if !strings.Contains(r.flat(), "xbps-reconfigure -f glibc-locales") {
		t.Errorf("not regenerated on resume:\n%s", r.flat())
	}
}

// A resume looks for the mark on the root mounted read-only before any of
// the target is mounted writable.
func TestResumeLooksReadOnlyFirst(t *testing.T) {
	var r recorder
	in := stagedInstaller(t, twoPartConfig(), &r)
	in.opts.ResumeFrom = "bootloader"
	looked := false
	in.readRoot = func(rel string) ([]byte, error) {
		looked = true
		return []byte("something else\n"), nil
	}
	// The plan's partitions stay: had MountAll run, it would fail on the
	// devices — the refusal comes first.
	wantErr(t, in.mount(), "nothing to resume")
	if !looked {
		t.Error("the root was not looked at read-only")
	}
}

// The root that cannot be looked at is said as such, not as "nothing to resume".
func TestResumeLookErrorSaid(t *testing.T) {
	var r recorder
	in := stagedInstaller(t, twoPartConfig(), &r)
	in.opts.ResumeFrom = "bootloader"
	in.readRoot = func(string) ([]byte, error) { return nil, errors.New("mount /dev/sda2: no such device") }
	err := in.mount()
	if err == nil || !strings.Contains(err.Error(), "no such device") || strings.Contains(err.Error(), "nothing to resume") {
		t.Errorf("err = %v", err)
	}
	in.readRoot = func(string) ([]byte, error) { return nil, os.ErrNotExist }
	wantErr(t, in.mount(), "nothing to resume")
}

// A resolv.conf the config wrote over the install-time copy is the
// system's own: finish keeps it, and drops the one set aside.
func TestFinishKeepsAWrittenResolvConf(t *testing.T) {
	var r recorder
	in := stagedInstaller(t, twoPartConfig(), &r)
	etc := mkTarget(t, in, "etc")
	in.resolvCopy = filepath.Join(etc, "resolv.conf")
	in.resolvSaved = filepath.Join(etc, "resolv.conf.slinstaller-saved")
	os.WriteFile(in.resolvSaved, []byte("nameserver 9.9.9.9\n"), 0o644)
	os.WriteFile(in.resolvCopy, []byte("nameserver 192.0.2.53\n"), 0o644) // write-files
	if err := in.finish(); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(in.resolvCopy); string(got) != "nameserver 192.0.2.53\n" {
		t.Errorf("resolv.conf = %q", got)
	}
	if _, err := os.Stat(in.resolvSaved); err == nil {
		t.Error("the set-aside file is left")
	}
}

// Equal to the live file counts as our copy only resuming (an installer from
// before the mark); on a fresh install it is the image's own.
func TestIsOurResolvOnlyResuming(t *testing.T) {
	var r recorder
	in := stagedInstaller(t, twoPartConfig(), &r)
	dst := filepath.Join(mkTarget(t, in, "etc"), "resolv.conf")
	live := []byte("nameserver 1.1.1.1\n")
	os.WriteFile(dst, live, 0o644)
	fi, _ := os.Lstat(dst)
	if in.isOurResolv(fi, dst, live) {
		t.Error("an image's resolv.conf equal to the live one taken for ours")
	}
	in.opts.ResumeFrom = "bootloader"
	if !in.isOurResolv(fi, dst, live) {
		t.Error("resuming, a pre-mark copy not recognised")
	}
}

// A resolv.conf the config made a link (resolvconf's) stays a link, in
// finish and in cleanup alike; cleanup keeps written content too.
func TestPutBackResolvKeepsWhatTheConfigMade(t *testing.T) {
	for _, via := range []string{"finish", "cleanup"} {
		var r recorder
		in := stagedInstaller(t, twoPartConfig(), &r)
		etc := mkTarget(t, in, "etc")
		in.resolvCopy = filepath.Join(etc, "resolv.conf")
		in.resolvSaved = filepath.Join(etc, "resolv.conf.slinstaller-saved")
		os.WriteFile(in.resolvSaved, []byte("nameserver 9.9.9.9\n"), 0o644)
		os.Symlink("/run/resolvconf/resolv.conf", in.resolvCopy) // dangling here
		if via == "finish" {
			in.finish()
		} else {
			in.cleanup()
		}
		if l, err := os.Readlink(in.resolvCopy); err != nil || l != "/run/resolvconf/resolv.conf" {
			t.Errorf("%s: the link is %q, %v", via, l, err)
		}
	}
	var r recorder
	in := stagedInstaller(t, twoPartConfig(), &r)
	etc := mkTarget(t, in, "etc")
	in.resolvCopy = filepath.Join(etc, "resolv.conf")
	os.WriteFile(in.resolvCopy, []byte("nameserver 192.0.2.53\n"), 0o644)
	in.cleanup()
	if got, _ := os.ReadFile(in.resolvCopy); string(got) != "nameserver 192.0.2.53\n" {
		t.Errorf("cleanup lost a written resolv.conf: %q", got)
	}
}
