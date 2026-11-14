package controller

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sunlightlinux/slinstaller/pkg/config"
	"github.com/sunlightlinux/slinstaller/pkg/storage"
)

// These tests run the stages after partitioning one at a time against a
// temporary target tree, with the commands recorded rather than run. mount
// and fstab are left out: they make real mount(2) and blkid calls, which only
// a VM install exercises honestly.

// stagedInstaller builds an installer over a temp root for cfg, the way
// Install does, without running anything.
func stagedInstaller(t *testing.T, cfg *config.Config, r *recorder) *installer {
	t.Helper()
	plan, err := cfg.Plan()
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	root := t.TempDir()
	return &installer{
		cfg:  cfg,
		plan: plan,
		opts: Options{Root: root, Runner: r.run},
		mnt:  storage.NewMounter(root),
	}
}

// readTarget returns a file from the target tree, failing the test if it is
// missing.
func readTarget(t *testing.T, in *installer, rel ...string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(append([]string{in.opts.Root}, rel...)...))
	if err != nil {
		t.Fatalf("read %s: %v", filepath.Join(rel...), err)
	}
	return string(data)
}

// mkTarget creates a directory in the target tree, as the base install would
// have.
func mkTarget(t *testing.T, in *installer, rel ...string) string {
	t.Helper()
	dir := filepath.Join(append([]string{in.opts.Root}, rel...)...)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

// grubCmdline pulls the kernel command line out of the target's
// /etc/default/grub.
func grubCmdline(t *testing.T, in *installer) string {
	t.Helper()
	for _, line := range strings.Split(readTarget(t, in, "etc", "default", "grub"), "\n") {
		if strings.HasPrefix(line, "GRUB_CMDLINE_LINUX_DEFAULT=") {
			return strings.Trim(strings.TrimPrefix(line, "GRUB_CMDLINE_LINUX_DEFAULT="), `"`)
		}
	}
	return ""
}

func TestBootloaderWritesKernelCommandLine(t *testing.T) {
	cfg := twoPartConfig()
	cfg.KernelArguments = &config.KernelArguments{
		Add:    []string{"quiet", "loglevel=7", "nomodeset", "console=ttyS0,115200"},
		Remove: []string{"nomodeset"},
	}
	lv := 3
	cfg.ConsoleLogLevel = &lv

	var r recorder
	in := stagedInstaller(t, cfg, &r)
	mkTarget(t, in, "etc", "default")
	// The base install put slinit in the target.
	bin := mkTarget(t, in, "usr", "bin")
	if err := os.WriteFile(filepath.Join(bin, "slinit"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := in.bootloader(); err != nil {
		t.Fatalf("bootloader: %v", err)
	}

	args := strings.Fields(grubCmdline(t, in))
	has := func(a string) bool {
		for _, x := range args {
			if x == a {
				return true
			}
		}
		return false
	}
	for _, want := range []string{"quiet", "console=ttyS0,115200", "loglevel=3", slinitInitArg} {
		if !has(want) {
			t.Errorf("kernel command line missing %q: %v", want, args)
		}
	}
	// A removed argument is not written, and the configured log level
	// replaces the one among the arguments.
	for _, not := range []string{"nomodeset", "loglevel=7"} {
		if has(not) {
			t.Errorf("kernel command line still has %q: %v", not, args)
		}
	}
	// A plain layout has nothing for the initramfs to unlock or assemble.
	for _, a := range args {
		if strings.HasPrefix(a, "rd.") {
			t.Errorf("plain layout got initramfs argument %q", a)
		}
	}

	flat := r.flat()
	for _, want := range []string{"grub-install", "grub-mkconfig"} {
		if !strings.Contains(flat, want) {
			t.Errorf("bootloader commands missing %q\n%s", want, flat)
		}
	}
	// No crypt, LVM or RAID: the initramfs from base-install stands.
	if strings.Contains(flat, "xbps-reconfigure") {
		t.Errorf("plain layout rebuilt the initramfs:\n%s", flat)
	}
}

func TestBootloaderLeavesSlinitOutWhenAbsentOrRemoved(t *testing.T) {
	// No slinit in the target: the argument is not forced.
	var r recorder
	in := stagedInstaller(t, twoPartConfig(), &r)
	mkTarget(t, in, "etc", "default")
	if err := in.bootloader(); err != nil {
		t.Fatalf("bootloader: %v", err)
	}
	if got := grubCmdline(t, in); strings.Contains(got, "init=") {
		t.Errorf("init= written without slinit in the target: %q", got)
	}

	// slinit present, but the config removes the argument.
	cfg := twoPartConfig()
	cfg.KernelArguments = &config.KernelArguments{Add: []string{"quiet"}, Remove: []string{slinitInitArg}}
	in = stagedInstaller(t, cfg, &r)
	mkTarget(t, in, "etc", "default")
	bin := mkTarget(t, in, "usr", "bin")
	if err := os.WriteFile(filepath.Join(bin, "slinit"), nil, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := in.bootloader(); err != nil {
		t.Fatalf("bootloader: %v", err)
	}
	if got := grubCmdline(t, in); strings.Contains(got, "init=") || !strings.Contains(got, "quiet") {
		t.Errorf("removed init= argument was written, or quiet lost: %q", got)
	}
}

func TestBootloaderAppendsToThePackagedGrubDefault(t *testing.T) {
	// /etc/default/grub comes from the grub package; the installer's lines
	// are appended so they win, and the package's are kept.
	var r recorder
	cfg := twoPartConfig()
	cfg.KernelArguments = &config.KernelArguments{Add: []string{"quiet"}}
	in := stagedInstaller(t, cfg, &r)
	dir := mkTarget(t, in, "etc", "default")
	if err := os.WriteFile(filepath.Join(dir, "grub"), []byte("GRUB_TIMEOUT=5\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := in.bootloader(); err != nil {
		t.Fatalf("bootloader: %v", err)
	}
	got := readTarget(t, in, "etc", "default", "grub")
	if !strings.HasPrefix(got, "GRUB_TIMEOUT=5\n") {
		t.Errorf("packaged grub default was not kept at the top:\n%s", got)
	}
	if !strings.Contains(got, "GRUB_DISTRIBUTOR=") || !strings.Contains(got, `GRUB_CMDLINE_LINUX_DEFAULT="quiet"`) {
		t.Errorf("installer lines missing:\n%s", got)
	}
}

func TestBaseInstallPassesRepoPackagesAndProxy(t *testing.T) {
	cfg := twoPartConfig()
	cfg.HTTPSProxy = "http://proxy.example:3128"
	var r recorder
	in := stagedInstaller(t, cfg, &r)
	in.opts.Repo = "https://repo.example/current"
	in.opts.Packages = []string{"sunlight-base", "grub-x86_64-efi"}

	if err := in.baseInstall(); err != nil {
		t.Fatalf("baseInstall: %v", err)
	}
	flat := r.flat()
	for _, want := range []string{
		"xbps-install", in.opts.Root, "https://repo.example/current",
		"sunlight-base", "grub-x86_64-efi", "proxy.example:3128",
	} {
		if !strings.Contains(flat, want) {
			t.Errorf("base install commands missing %q\n%s", want, flat)
		}
	}
}

func TestSaveConfigWritesAPrivateReplayableCopy(t *testing.T) {
	cfg := twoPartConfig()
	cfg.Hostname = "sunlight"
	cfg.KernelProfile = "Server"
	var r recorder
	in := stagedInstaller(t, cfg, &r)

	if err := in.saveConfig(); err != nil {
		t.Fatalf("saveConfig: %v", err)
	}
	path := filepath.Join(in.opts.Root, "root", "slinstaller.yaml")
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	// It names the accounts' password hashes: root only.
	if st.Mode().Perm() != 0o600 {
		t.Errorf("saved config mode = %v, want 0600", st.Mode().Perm())
	}
	got := readTarget(t, in, "root", "slinstaller.yaml")
	for _, want := range []string{"# Written by slinstaller", "hostname: sunlight", "kernel-profile: Server", "name: sda"} {
		if !strings.Contains(got, want) {
			t.Errorf("saved config missing %q\n%s", want, got)
		}
	}
	// It loads back as a config, which is the point of saving it.
	if _, err := config.Load(path); err != nil {
		t.Errorf("saved config does not load: %v", err)
	}
}

func TestSwapfileStage(t *testing.T) {
	// No swap file asked for: nothing runs and fstab is not touched.
	var r recorder
	in := stagedInstaller(t, twoPartConfig(), &r)
	if err := in.swapfile(); err != nil {
		t.Fatalf("swapfile: %v", err)
	}
	if len(r.cmds) != 0 {
		t.Errorf("swapfile ran commands with no swapFileSize: %s", r.flat())
	}
	if _, err := os.Stat(filepath.Join(in.opts.Root, "etc", "fstab")); err == nil {
		t.Error("fstab created with no swap file")
	}

	// With a size: the file is made, and fstab gains its line after what
	// the fstab stage wrote.
	cfg := twoPartConfig()
	cfg.SwapFileSize = "2G"
	r = recorder{}
	in = stagedInstaller(t, cfg, &r)
	etc := mkTarget(t, in, "etc")
	if err := os.WriteFile(filepath.Join(etc, "fstab"), []byte("UUID=abc / ext4 defaults 0 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := in.swapfile(); err != nil {
		t.Fatalf("swapfile: %v", err)
	}
	if flat := r.flat(); !strings.Contains(flat, "mkswap") || !strings.Contains(flat, in.opts.Root) {
		t.Errorf("swap file commands wrong:\n%s", flat)
	}
	fstab := readTarget(t, in, "etc", "fstab")
	if !strings.HasPrefix(fstab, "UUID=abc / ext4") || !strings.Contains(fstab, storage.SwapFileFstab) {
		t.Errorf("fstab after swapfile:\n%s", fstab)
	}
}

func TestCrypttabStageIsANoOpWithoutEncryption(t *testing.T) {
	var r recorder
	in := stagedInstaller(t, twoPartConfig(), &r)
	if err := in.crypttab(); err != nil {
		t.Fatalf("crypttab: %v", err)
	}
	if _, err := os.Stat(filepath.Join(in.opts.Root, "etc", "crypttab")); err == nil {
		t.Error("crypttab written for an unencrypted layout")
	}
	if in.keyfile != "" || len(r.cmds) != 0 {
		t.Errorf("unencrypted layout set up a keyfile (%q) or ran commands: %s", in.keyfile, r.flat())
	}
}

func TestStageNamesMatchTheStages(t *testing.T) {
	names := StageNames()
	if len(names) == 0 || names[0] != "pre-install" || names[len(names)-1] != "post-install" {
		t.Fatalf("stage names = %v", names)
	}
	// The destructive step comes before anything is mounted or installed.
	idx := map[string]int{}
	for i, n := range names {
		idx[n] = i
	}
	for _, pair := range [][2]string{
		{"partition", "mount"}, {"mount", "base-install"}, {"base-install", "fstab"},
		{"fstab", "bootloader"}, {"bootloader", "first-boot"}, {"network", "save-config"},
	} {
		a, aok := idx[pair[0]]
		b, bok := idx[pair[1]]
		if !aok || !bok || a >= b {
			t.Errorf("stage %q must come before %q: %v", pair[0], pair[1], names)
		}
	}
}

func TestFirstBootConfiguresTheTarget(t *testing.T) {
	cfg := twoPartConfig()
	cfg.Hostname = "ceres"
	cfg.Timezone = "Europe/Bucharest"
	cfg.Keyboard = "us"
	cfg.Language = "en_US.UTF-8"
	lv := 3
	cfg.ConsoleLogLevel = &lv
	cfg.Users = []*config.User{
		{Login: "ion", UserName: "Ion", Password: "$6$hash", Admin: true, SSHKeys: []string{"ssh-ed25519 AAAA ion@host"}},
		{Login: "guest", Password: "$6$other"},
		nil,         // tolerated, as a hand-written config may carry one
		{Login: ""}, // skipped: Validate reports it
	}

	var r recorder
	in := stagedInstaller(t, cfg, &r)
	// A glibc target lists its locales commented out.
	def := mkTarget(t, in, "etc", "default")
	if err := os.WriteFile(filepath.Join(def, "libc-locales"), []byte("#en_US.UTF-8 UTF-8\n#ro_RO.UTF-8 UTF-8\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := in.firstBoot(); err != nil {
		t.Fatalf("firstBoot: %v", err)
	}

	if got := readTarget(t, in, "etc", "hostname"); got != "ceres\n" {
		t.Errorf("hostname = %q", got)
	}
	if id := strings.TrimSpace(readTarget(t, in, "etc", "machine-id")); len(id) != 32 {
		t.Errorf("machine-id = %q, want 32 hex digits", id)
	}
	if link, err := os.Readlink(filepath.Join(in.opts.Root, "etc", "localtime")); err != nil || link != "/usr/share/zoneinfo/Europe/Bucharest" {
		t.Errorf("localtime -> %q, %v", link, err)
	}
	if got := readTarget(t, in, "etc", "rc.conf"); !strings.Contains(got, `KEYMAP="us"`) {
		t.Errorf("rc.conf:\n%s", got)
	}
	if got := readTarget(t, in, "etc", "locale.conf"); got != "LANG=en_US.UTF-8\n" {
		t.Errorf("locale.conf = %q", got)
	}
	// Only the chosen locale is enabled.
	if got := readTarget(t, in, "etc", "default", "libc-locales"); got != "en_US.UTF-8 UTF-8\n#ro_RO.UTF-8 UTF-8\n" {
		t.Errorf("libc-locales:\n%s", got)
	}
	if got := readTarget(t, in, "etc", "sysctl.d", "10-printk.conf"); !strings.Contains(got, "kernel.printk = 3 4 1 7") {
		t.Errorf("printk sysctl:\n%s", got)
	}
	if got := readTarget(t, in, "home", "ion", ".ssh", "authorized_keys"); got != "ssh-ed25519 AAAA ion@host\n" {
		t.Errorf("authorized_keys = %q", got)
	}
	if got := readTarget(t, in, "etc", "sudoers.d", "wheel"); !strings.Contains(got, "%wheel") {
		t.Errorf("sudoers wheel:\n%s", got)
	}

	flat := r.flat()
	root := in.opts.Root
	for _, want := range []string{
		// The enabled locale has to be generated.
		"chroot " + root + " xbps-reconfigure -f glibc-locales",
		"chroot " + root + " useradd -m -G wheel -c Ion -p $6$hash ion",
		"chroot " + root + " useradd -m -p $6$other guest",
		"chroot " + root + " chown -R ion:ion /home/ion/.ssh",
		// An administrator exists, so root's password login is locked.
		"chroot " + root + " usermod --lock root",
	} {
		if !strings.Contains(flat, want) {
			t.Errorf("first-boot commands missing %q\n%s", want, flat)
		}
	}
	if n := strings.Count(flat, "useradd"); n != 2 {
		t.Errorf("useradd ran %d times, want 2 (nil and nameless users skipped)\n%s", n, flat)
	}
}

func TestFirstBootWithoutAnAdminKeepsRootUsable(t *testing.T) {
	// With no administrator there is nobody to reach root through sudo, so
	// root must not be locked and no sudoers drop-in is written.
	cfg := twoPartConfig()
	cfg.Users = []*config.User{{Login: "guest", Password: "$6$x"}}
	var r recorder
	in := stagedInstaller(t, cfg, &r)
	if err := in.firstBoot(); err != nil {
		t.Fatalf("firstBoot: %v", err)
	}
	if flat := r.flat(); strings.Contains(flat, "usermod --lock root") {
		t.Errorf("root locked with no administrator:\n%s", flat)
	}
	if _, err := os.Stat(filepath.Join(in.opts.Root, "etc", "sudoers.d", "wheel")); err == nil {
		t.Error("sudoers wheel written with no administrator")
	}
	// Nothing was configured, so nothing optional is written either.
	for _, rel := range []string{"etc/hostname", "etc/localtime", "etc/locale.conf", "etc/sysctl.d/10-printk.conf"} {
		if _, err := os.Lstat(filepath.Join(in.opts.Root, rel)); err == nil {
			t.Errorf("%s written though the config left it unset", rel)
		}
	}
}

func TestNetworkStageOnARunitTarget(t *testing.T) {
	cfg := twoPartConfig()
	cfg.NetworkInterfaces = []*config.Interface{{
		Name:    "eth0",
		Addrs:   []*config.Address{{IP: "192.168.1.10", Netmask: "255.255.255.0"}},
		Gateway: "192.168.1.1",
		DNS:     "1.1.1.1",
	}}
	var r recorder
	in := stagedInstaller(t, cfg, &r)
	etc := mkTarget(t, in, "etc")
	if err := os.WriteFile(filepath.Join(etc, "dhcpcd.conf"), []byte("# packaged default\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := in.network(); err != nil {
		t.Fatalf("network: %v", err)
	}
	conf := readTarget(t, in, "etc", "dhcpcd.conf")
	if !strings.HasPrefix(conf, "# packaged default\n") {
		t.Errorf("packaged dhcpcd.conf was not kept:\n%s", conf)
	}
	for _, want := range []string{"interface eth0", "192.168.1.10", "192.168.1.1", "1.1.1.1"} {
		if !strings.Contains(conf, want) {
			t.Errorf("dhcpcd.conf missing %q\n%s", want, conf)
		}
	}
	// No slinit boot services in the target: runit's dhcpcd is enabled.
	link, err := os.Readlink(filepath.Join(in.opts.Root, "etc", "runit", "runsvdir", "default", "dhcpcd"))
	if err != nil || link != "/etc/sv/dhcpcd" {
		t.Errorf("dhcpcd service link -> %q, %v", link, err)
	}
}

func TestNetworkStageOnASlinitTarget(t *testing.T) {
	enabled := func(in *installer, name string) bool {
		_, err := os.Lstat(filepath.Join(in.opts.Root, "etc", "slinit.d", "boot.d", name))
		return err == nil
	}
	slinitTarget := func(cfg *config.Config) *installer {
		var r recorder
		in := stagedInstaller(t, cfg, &r)
		dir := mkTarget(t, in, "usr", "lib", "slinit.d")
		for _, svc := range []string{"boot", "sshd"} {
			if err := os.WriteFile(filepath.Join(dir, svc), []byte("type = internal\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		return in
	}

	// Default: sshd and the daily update are enabled; runit is not touched.
	in := slinitTarget(twoPartConfig())
	if err := in.network(); err != nil {
		t.Fatalf("network: %v", err)
	}
	if !enabled(in, "sshd") || !enabled(in, "sunlight-autoupdate") {
		t.Error("sshd or the update service not enabled on a slinit target")
	}
	if _, err := os.Lstat(filepath.Join(in.opts.Root, "etc", "runit")); err == nil {
		t.Error("runit service enabled on a slinit target")
	}

	// autoUpdate: false installs the service but leaves it disabled.
	cfg := twoPartConfig()
	off := false
	cfg.AutoUpdate = &off
	in = slinitTarget(cfg)
	if err := in.network(); err != nil {
		t.Fatalf("network: %v", err)
	}
	if enabled(in, "sunlight-autoupdate") {
		t.Error("update service enabled though autoUpdate is false")
	}
	if _, err := os.Stat(filepath.Join(in.opts.Root, "etc", "slinit.d", "sunlight-autoupdate")); err != nil {
		t.Errorf("update service not installed: %v", err)
	}
}
