package controller

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"

	"github.com/sunlightlinux/slinstaller/pkg/blockdev"
	"github.com/sunlightlinux/slinstaller/pkg/config"
	"github.com/sunlightlinux/slinstaller/pkg/storage"
)

// An install from the first stage to the last, unmount included, as close
// to a real one as a machine without root or a VM gets. It runs in a child
// process in its own user and mount namespace, as the mount stages' own
// test does: the mount stages make real mount(2) calls, with tmpfs
// standing in for the disk's filesystems; the live /proc, /sys and /dev
// are really bound in; the files the stages write land on those tmpfs
// mounts. The commands that would touch a disk or need the target's own
// programs (parted, mkfs, xbps-install, grub-install, useradd, ...) are
// recorded by the Runner, and the ones whose output later stages read are
// given that output: xbps-install lays down a small base tree, grub-mkconfig
// writes a grub.cfg from /etc/default/grub. Those the controller runs
// itself (blkid, cryptsetup, udevadm) are stand-ins first on PATH.
//
// What it cannot do is go through Install itself: Install takes its plan
// from the config, and a config's filesystems are ones the install can make
// (vfat, ext4), which the kernel does not let a user namespace mount. So
// the installer is built the way Install builds it, and for the mount stage
// alone — from its start to the next stage's — the plan's filesystems read
// tmpfs. Everything before and after (mkfs, fstab, the bootloader's ESP)
// sees the configured ones.

const (
	e2eChildEnv = "SLINSTALLER_CTL_E2E_CHILD"
	e2eBaseEnv  = "SLINSTALLER_CTL_E2E_BASE"
)

func TestFullInstallInAUserNamespace(t *testing.T) {
	if os.Getenv(e2eChildEnv) == "1" {
		e2eChild(t)
		return
	}
	// As in the mount test, the child deletes nothing under the targets it
	// mounted on; they are removed here once it and its mounts are gone.
	cmd := exec.Command(os.Args[0], "-test.run=^TestFullInstallInAUserNamespace$", "-test.v")
	for _, a := range os.Args[1:] {
		if strings.HasPrefix(a, "-test.gocoverdir=") {
			cmd.Args = append(cmd.Args, a)
		}
	}
	cmd.Env = append(os.Environ(), e2eChildEnv+"=1", e2eBaseEnv+"="+t.TempDir())
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags:                 syscall.CLONE_NEWUSER | syscall.CLONE_NEWNS,
		UidMappings:                []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Getuid(), Size: 1}},
		GidMappings:                []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Getgid(), Size: 1}},
		GidMappingsEnableSetgroups: false,
	}
	out, err := cmd.CombinedOutput()
	if strings.Contains(string(out), mountSkipMarker) {
		t.Skip("user namespaces cannot mount here")
	}
	var exit *exec.ExitError
	if err != nil && !errors.As(err, &exit) {
		t.Skipf("cannot start a process in a user namespace: %v", err)
	}
	if err != nil {
		t.Fatalf("the install in the namespace failed:\n%s", out)
	}
	// A child that skipped has checked nothing; that is not a pass.
	if strings.Contains(string(out), "--- SKIP") {
		t.Skipf("the namespace run was skipped:\n%s", out)
	}
	for _, l := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(strings.TrimSpace(l), "--- PASS: TestFullInstallInAUserNamespace/") {
			t.Log(strings.TrimSpace(l))
		}
	}
}

func e2eChild(t *testing.T) {
	base := os.Getenv(e2eBaseEnv)
	if base == "" || os.Getuid() != 0 {
		t.Fatalf("not in the test namespace (base=%q uid=%d)", base, os.Getuid())
	}
	// Can this namespace mount at all? If not, the whole test is skipped.
	probe := filepath.Join(base, "probe")
	os.Mkdir(probe, 0o755)
	if err := syscall.Mount("tmpfs", probe, "tmpfs", 0, ""); err != nil {
		t.Log(mountSkipMarker)
		return
	}
	syscall.Unmount(probe, 0)

	t.Run("plain", func(t *testing.T) { e2ePlain(t, filepath.Join(base, "plain")) })
	t.Run("encrypted", func(t *testing.T) { e2eEncrypted(t, filepath.Join(base, "crypt")) })
}

// e2e is one install run: its target, its stand-ins and what it saw.
type e2e struct {
	t      *testing.T
	dir    string // the case's own directory, outside the target
	root   string // the target (Options.Root)
	events string // the file stand-ins append their calls to
	seen   int    // how much of events has been read into log
	log    []string
	stages []string
	in     *installer

	// atComplete runs when the last stage is done, the target still fully
	// mounted; atKexec when finish loads the kernel, after it took the
	// install-time resolv.conf out and saved the log, before it unmounts.
	atComplete func()
	atKexec    func()
}

func newE2E(t *testing.T, dir string) *e2e {
	e := &e2e{t: t, dir: dir, root: filepath.Join(dir, "mnt"), events: filepath.Join(dir, "events")}
	if err := os.MkdirAll(e.root, 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(e.events, nil, 0o644)
	// blkid answers with a UUID for each filesystem the install made;
	// cryptsetup with one for each LUKS volume, after reading the passphrase
	// as the real one does; udevadm settles nothing. Each says it was called.
	note := `echo "$(basename "$0") $*" >> "` + e.events + `"`
	fakeBin(t, map[string]string{
		"blkid": note + `
case "$6" in
/dev/sda1) echo 7A3B-19C2 ;;
/dev/sda2) echo 3f1c0a52-8d2e-4c4b-9e0e-5b8f1a2c3d4e ;;
*) echo "fs-$(basename "$6")" ;;
esac`,
		"cryptsetup": note + `
if [ "$1" = luksUUID ]; then echo "luks-$(basename "$2")"; exit 0; fi
cat > "` + dir + `/cryptsetup.stdin"`,
		"udevadm": "",
	})
	return e
}

// sync reads what the stand-ins wrote since last time into the log, so
// their calls sit in it in the order they happened.
func (e *e2e) sync() {
	data, _ := os.ReadFile(e.events)
	if len(data) > e.seen {
		for _, l := range strings.Split(strings.TrimSuffix(string(data[e.seen:]), "\n"), "\n") {
			e.log = append(e.log, "exec "+l)
		}
		e.seen = len(data)
	}
}

// install builds the installer as Install does and runs it to the end.
func (e *e2e) install(cfg *config.Config, opts Options) error {
	e.t.Helper()
	opts.Root, opts.Runner = e.root, e.run
	plan, err := cfg.Plan()
	if err != nil {
		e.t.Fatalf("Plan: %v", err)
	}
	e.in = &installer{cfg: cfg, plan: plan, opts: opts, mnt: storage.NewMounter(opts.Root)}

	var saved []storage.Partition
	e.in.opts.Progress = func(done, total int, stage string) {
		e.sync()
		e.log = append(e.log, "== "+stage)
		e.stages = append(e.stages, stage)
		switch stage {
		case "mount":
			// The one substitution: tmpfs for the filesystems, for the
			// mount stage alone (see the top of this file).
			saved = append([]storage.Partition(nil), plan.Parts...)
			for i := range plan.Parts {
				if plan.Parts[i].Filesystem != "" && plan.Parts[i].Filesystem != "swap" {
					plan.Parts[i].Filesystem, plan.Parts[i].MountOptions = "tmpfs", ""
				}
			}
		case "meta-fs":
			copy(plan.Parts, saved)
		case "complete":
			if e.atComplete != nil {
				e.atComplete()
			}
		}
	}
	err = e.in.run()
	e.sync()
	return err
}

// run is the install's Runner.
func (e *e2e) run(args []string) error {
	e.sync()
	line := strings.Join(args, " ")
	e.log = append(e.log, "run "+line)
	root := e.root

	// The hooks run for real, under sh (bash -l would read the login
	// profile of whoever runs the tests); a chroot hook cannot, there being
	// no shell in the stand-in base.
	if args[0] == "env" && !contains(args, "xbps-install") {
		cmd := append([]string{}, args...)
		for i := range cmd {
			if i+2 < len(cmd) && cmd[i] == "bash" && cmd[i+1] == "-l" && cmd[i+2] == "-c" {
				cmd = append(append(cmd[:i:i], "sh", "-c"), cmd[i+3:]...)
				break
			}
		}
		return realRun(cmd)
	}
	switch {
	case args[0] == "mkdir" || args[0] == "cp":
		return realRun(args) // the signing keys, into the target
	case args[0] == "xbps-install" || (args[0] == "env" && contains(args, "xbps-install")):
		if !contains(args, "-r") {
			return fmt.Errorf("xbps-install without -r: %s", line)
		}
		if r := args[indexOf(args, "-r")+1]; r != root {
			return fmt.Errorf("xbps-install into %s, not the target %s", r, root)
		}
		e.baseTree()
	case args[0] == "kexec":
		if e.atKexec != nil {
			e.atKexec()
		}
	case args[0] == "chroot":
		if len(args) < 3 || args[1] != root {
			return fmt.Errorf("chroot outside the target: %s", line)
		}
		switch args[2] {
		case "grub-install":
			esp := ""
			for _, a := range args {
				if v, ok := strings.CutPrefix(a, "--efi-directory="); ok {
					esp = v
				}
			}
			name := filepath.Join("EFI", "Sunlight", "grubx64.efi")
			if contains(args, "--removable") {
				name = filepath.Join("EFI", "BOOT", "BOOTX64.EFI")
			}
			e.write(filepath.Join(esp, name), "GRUB", 0o644)
		case "grub-mkconfig":
			e.write(args[len(args)-1], e.grubCfg(), 0o600)
		case "useradd":
			login := args[len(args)-1]
			e.appendTo("etc/passwd", login+":x:1000:1000::/home/"+login+":/bin/sh\n")
			e.appendTo("etc/shadow", login+":"+flagValue(args, "-p")+":20000::::::\n")
			e.appendTo("etc/group", login+":x:1000:\n")
			os.MkdirAll(filepath.Join(root, "home", login), 0o700)
		}
	}
	return nil
}

// baseTree is what xbps-install leaves in the target: a little of the
// packages the later stages look for or rewrite.
func (e *e2e) baseTree() {
	for _, f := range []struct {
		path, content string
		mode          os.FileMode
	}{
		{"usr/bin/slinit", "ELF", 0o755},
		{"usr/bin/grub-install", "ELF", 0o755},
		{"usr/bin/grub-mkconfig", "#!/bin/sh", 0o755},
		{"usr/bin/dracut", "#!/bin/sh", 0o755},
		{"usr/bin/xbps-install", "ELF", 0o755},
		// slinit-services' service files: boot (what makes the system an
		// slinit one), sshd (which the network stage enables), and others.
		{"usr/lib/slinit.d/boot", "type = internal\nwaits-for.d: /etc/slinit.d/boot.d\n", 0o644},
		{"usr/lib/slinit.d/sshd", "type = process\ncommand = /usr/bin/sshd -D\n", 0o644},
		{"usr/lib/slinit.d/network", "type = scripted\n", 0o644},
		{"usr/lib/slinit.d/filesystems", "type = scripted\n", 0o644},
		{"etc/default/grub", "GRUB_DEFAULT=0\nGRUB_TIMEOUT=5\nGRUB_DISTRIBUTOR=\"Void\"\nGRUB_CMDLINE_LINUX_DEFAULT=\"loglevel=4\"\n", 0o644},
		{"etc/default/libc-locales", "#de_DE.UTF-8 UTF-8\n#en_US.UTF-8 UTF-8\n#en_US ISO-8859-1\n", 0o644},
		{"etc/passwd", "root:x:0:0:root:/root:/bin/sh\n", 0o644},
		{"etc/group", "root:x:0:\nwheel:x:4:\n", 0o644},
		{"etc/shadow", "root::19000:0:99999:7:::\n", 0o600},
		{"etc/rc.conf", "# /etc/rc.conf\n", 0o644},
		{"usr/share/zoneinfo/UTC", "TZif2", 0o644},
		{"boot/vmlinuz-7.3.0_1", "KERNEL", 0o644},
		{"boot/initramfs-7.3.0_1.img", "INITRD", 0o644},
	} {
		e.write("/"+f.path, f.content, f.mode)
	}
	os.MkdirAll(filepath.Join(e.root, "root"), 0o700)
}

// grubCfg is grub-mkconfig's output, enough of it for kexec to read: one
// entry whose command line is /etc/default/grub's, as 10_linux makes it.
func (e *e2e) grubCfg() string {
	data, _ := os.ReadFile(filepath.Join(e.root, "etc", "default", "grub"))
	var always, normal string
	for _, l := range strings.Split(string(data), "\n") {
		if v, ok := strings.CutPrefix(l, "GRUB_CMDLINE_LINUX="); ok {
			always = strings.Trim(v, `"`)
		}
		if v, ok := strings.CutPrefix(l, "GRUB_CMDLINE_LINUX_DEFAULT="); ok {
			normal = strings.Trim(v, `"`)
		}
	}
	// The kernel's path is relative to the filesystem /boot is on.
	dir := "/boot"
	if e.in.plan.ESPMountpoint() == "/boot" {
		dir = ""
	}
	return "set default=0\nmenuentry 'Sunlight OS' {\n" +
		"\tlinux " + dir + "/vmlinuz-7.3.0_1 root=UUID=x ro " + always + " " + normal + "\n" +
		"\tinitrd " + dir + "/initramfs-7.3.0_1.img\n}\n"
}

func (e *e2e) write(rel, content string, mode os.FileMode) {
	p := filepath.Join(e.root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		e.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), mode); err != nil {
		e.t.Fatal(err)
	}
}

func (e *e2e) appendTo(rel, line string) {
	f, err := os.OpenFile(filepath.Join(e.root, rel), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		e.t.Fatal(err)
	}
	f.WriteString(line)
	f.Close()
}

func (e *e2e) read(rel ...string) string {
	e.t.Helper()
	data, err := os.ReadFile(filepath.Join(append([]string{e.root}, rel...)...))
	if err != nil {
		e.t.Errorf("in the target: %v", err)
	}
	return string(data)
}

func (e *e2e) mode(rel ...string) os.FileMode {
	e.t.Helper()
	st, err := os.Stat(filepath.Join(append([]string{e.root}, rel...)...))
	if err != nil {
		e.t.Errorf("in the target: %v", err)
		return 0
	}
	return st.Mode().Perm()
}

// at is where in the log the first entry containing s is, or -1.
func (e *e2e) at(s string) int {
	for i, l := range e.log {
		if strings.Contains(l, s) {
			return i
		}
	}
	return -1
}

// inOrder checks that the log has an entry containing each of steps, each
// one after the entry found for the step before it.
func (e *e2e) inOrder(steps ...string) {
	e.t.Helper()
	from := 0
	for _, s := range steps {
		i := from
		for i < len(e.log) && !strings.Contains(e.log[i], s) {
			i++
		}
		if i == len(e.log) {
			if e.at(s) >= 0 {
				e.t.Errorf("%q ran, but not after %q\n%s", s, e.log[from-1], strings.Join(e.log, "\n"))
			} else {
				e.t.Errorf("never ran: %q\n%s", s, strings.Join(e.log, "\n"))
			}
			return
		}
		from = i + 1
	}
}

// mountsUnder lists the mount points at or under dir in this namespace.
func mountsUnder(t *testing.T, dir string) []string {
	f, err := os.Open("/proc/self/mountinfo")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) > 4 && (fields[4] == dir || strings.HasPrefix(fields[4], dir+"/")) {
			out = append(out, fields[4]+" "+fields[len(fields)-3])
		}
	}
	return out
}

func indexOf(ss []string, v string) int {
	for i, s := range ss {
		if s == v {
			return i
		}
	}
	return -1
}

func flagValue(args []string, flag string) string {
	if i := indexOf(args, flag); i >= 0 && i+1 < len(args) {
		return args[i+1]
	}
	return ""
}

// The cmdline in /etc/default/grub: what every entry gets, and the rest.
func grubLines(text string) (always, normal string) {
	for _, l := range strings.Split(text, "\n") {
		if v, ok := strings.CutPrefix(l, "GRUB_CMDLINE_LINUX="); ok {
			always = strings.Trim(v, `"`)
		}
		if v, ok := strings.CutPrefix(l, "GRUB_CMDLINE_LINUX_DEFAULT="); ok {
			normal = strings.Trim(v, `"`)
		}
	}
	return
}

var hex32 = regexp.MustCompile(`^[0-9a-f]{32}\n$`)

func e2ePlain(t *testing.T, dir string) {
	e := newE2E(t, dir)
	level := 4
	cfg := twoPartConfig()
	cfg.Hostname = "e2e-host"
	cfg.Timezone = "UTC"
	cfg.Keyboard = "us"
	cfg.Language = "en_US.UTF-8"
	cfg.ConsoleLogLevel = &level
	cfg.KernelArguments = &config.KernelArguments{Add: []string{"quiet"}}
	cfg.Bootloader = &config.Bootloader{Removable: true}
	cfg.PowerState = "kexec"
	cfg.Users = []*config.User{{Login: "ion", UserName: "Ion", Password: "$6$salt$the-hash", Admin: true,
		SSHKeys: []string{"ssh-ed25519 AAAAC3Nz e2e@test"}}}
	cfg.NetworkInterfaces = []*config.Interface{{Name: "eth0", Gateway: "192.0.2.1", DNS: "192.0.2.53",
		Addrs: []*config.Address{{IP: "192.0.2.10", Netmask: "255.255.255.0"}}}}
	cfg.WriteFiles = []*config.WriteFile{{Path: "/etc/motd", Content: "Welcome\n", Permissions: "0640"}}
	cfg.PreInstall = []*config.Hook{{Cmd: "echo pre-install ran"}}
	cfg.PostInstall = []*config.Hook{{Cmd: "touch $chrootDir/root/post-install-ran"}}
	// A hook after fstab sees the file it wrote, on the mounted target;
	// failing, it fails the install.
	cfg.StageHooks = map[string][]*config.Hook{"fstab": {{Cmd: "grep -q 'UUID=7A3B-19C2' ${chrootDir}/etc/fstab"}},
		// The install is marked as in progress from the mount on (a resume
		// requires the mark) ...
		"mount": {{Cmd: "test -f ${chrootDir}/.slinstaller-in-progress"}}}

	keys := filepath.Join(dir, "keys")
	os.MkdirAll(keys, 0o755)
	os.WriteFile(filepath.Join(keys, "d8:42:6d:5a.plist"), []byte("<plist/>"), 0o644)
	logFile := filepath.Join(dir, "install.log")
	lf, err := os.Create(logFile)
	if err != nil {
		t.Fatal(err)
	}
	defer lf.Close()

	// What the live system's listing shows on the disk: an old partition,
	// whose signature is erased before the disk is partitioned again.
	devices := func() ([]blockdev.Device, error) {
		return []blockdev.Device{{Name: "sda", Path: "/dev/sda", Type: "disk",
			Children: []blockdev.Device{{Name: "sda1", Path: "/dev/sda1", Type: "part", FsType: "ext4"}}}}, nil
	}
	opts := Options{
		Repo:       "https://repo.example.org/current",
		ExtraRepos: []string{"https://sunlight.example.org/current"},
		LocalRepos: []string{"/run/initramfs/live/repo"},
		KeysDir:    keys,
		Packages:   []string{"base-system", "slinit", "slinit-services", "grub-x86_64-efi", "linux"},
		Devices:    devices,
		Log:        lf,
		LogFile:    logFile,
		Output:     io.Discard,
	}

	_, liveResolvErr := os.ReadFile("/etc/resolv.conf")

	e.atComplete = func() {
		// Every filesystem of the install and the kernel's are mounted.
		mounts := strings.Join(mountsUnder(t, e.root), "\n")
		for _, want := range []string{e.root + " ", e.root + "/boot ", e.root + "/proc ", e.root + "/sys ", e.root + "/dev "} {
			if !strings.Contains(mounts+"\n", want) {
				t.Errorf("not mounted at the end of the stages: %s\n%s", want, mounts)
			}
		}

		// fstab: by the UUIDs blkid gave, with the configured filesystems.
		fstab := e.read("etc", "fstab")
		for _, want := range []string{
			"UUID=3f1c0a52-8d2e-4c4b-9e0e-5b8f1a2c3d4e\t/\text4\tdefaults\t0 1\n",
			"UUID=7A3B-19C2\t/boot\tvfat\tdefaults\t0 2\n",
		} {
			if !strings.Contains(fstab, want) {
				t.Errorf("fstab lacks %q:\n%s", want, fstab)
			}
		}

		// The boot menu: named Sunlight, slinit as init on every entry,
		// the rest of the command line on the normal ones only. The
		// package's own lines stay, above.
		grub := e.read("etc", "default", "grub")
		always, normal := grubLines(grub)
		if always != "init=/usr/bin/slinit" {
			t.Errorf("GRUB_CMDLINE_LINUX = %q\n%s", always, grub)
		}
		if normal != "quiet loglevel=4" {
			t.Errorf("GRUB_CMDLINE_LINUX_DEFAULT = %q\n%s", normal, grub)
		}
		if !strings.HasPrefix(grub, "GRUB_DEFAULT=0\n") || !strings.Contains(grub, `GRUB_DISTRIBUTOR="Sunlight OS"`) {
			t.Errorf("/etc/default/grub:\n%s", grub)
		}
		if !strings.Contains(e.read("boot", "grub", "grub.cfg"), "init=/usr/bin/slinit") {
			t.Error("grub.cfg was not made from the command line written")
		}
		for _, f := range []string{"EFI/Sunlight/grubx64.efi", "EFI/BOOT/BOOTX64.EFI"} {
			if _, err := os.Stat(filepath.Join(e.root, "boot", f)); err != nil {
				t.Errorf("the loader is not on the ESP: %v", err)
			}
		}

		// First boot.
		if got := e.read("etc", "hostname"); got != "e2e-host\n" {
			t.Errorf("hostname = %q", got)
		}
		if id := e.read("etc", "machine-id"); !hex32.MatchString(id) {
			t.Errorf("machine-id = %q, want 32 hex digits", id)
		}
		if seed := e.read("var", "lib", "seedrng", "seed.no-credit"); len(seed) != 32 || e.mode("var", "lib", "seedrng", "seed.no-credit") != 0o400 {
			t.Errorf("random seed: %d bytes, mode %v", len(seed), e.mode("var", "lib", "seedrng", "seed.no-credit"))
		}
		if l, err := os.Readlink(filepath.Join(e.root, "etc", "localtime")); err != nil || l != "/usr/share/zoneinfo/UTC" {
			t.Errorf("localtime -> %q (%v)", l, err)
		} else if _, err := os.Stat(filepath.Join(e.root, l)); err != nil {
			t.Errorf("localtime leads nowhere in the target: %v", err)
		}
		if rc := e.read("etc", "rc.conf"); !strings.HasPrefix(rc, "# /etc/rc.conf\n") || !strings.Contains(rc, `KEYMAP="us"`) {
			t.Errorf("rc.conf:\n%s", rc)
		}
		if got := e.read("etc", "locale.conf"); got != "LANG=en_US.UTF-8\n" {
			t.Errorf("locale.conf = %q", got)
		}
		if got := e.read("etc", "default", "libc-locales"); !strings.Contains(got, "\nen_US.UTF-8 UTF-8\n") || !strings.Contains(got, "#de_DE.UTF-8") {
			t.Errorf("libc-locales:\n%s", got)
		}
		if got := e.read("etc", "sysctl.d", "10-printk.conf"); !strings.Contains(got, "kernel.printk = 4 4 1 7") {
			t.Errorf("printk sysctl = %q", got)
		}
		if !strings.Contains(e.read("etc", "passwd"), "\nion:x:") {
			t.Error("the account is not in passwd")
		}
		if got := e.read("home", "ion", ".ssh", "authorized_keys"); got != "ssh-ed25519 AAAAC3Nz e2e@test\n" || e.mode("home", "ion", ".ssh", "authorized_keys") != 0o600 {
			t.Errorf("authorized_keys = %q mode %v", got, e.mode("home", "ion", ".ssh", "authorized_keys"))
		}
		if got := e.read("etc", "sudoers.d", "wheel"); got != "%wheel ALL=(ALL:ALL) ALL\n" || e.mode("etc", "sudoers.d", "wheel") != 0o440 {
			t.Errorf("sudoers wheel = %q mode %v", got, e.mode("etc", "sudoers.d", "wheel"))
		}
		if got := e.read("etc", "motd"); got != "Welcome\n" || e.mode("etc", "motd") != 0o640 {
			t.Errorf("motd = %q mode %v", got, e.mode("etc", "motd"))
		}

		// Network: the static stanza, and the slinit services enabled —
		// links that lead to service files in the target.
		if conf := e.read("etc", "dhcpcd.conf"); !strings.Contains(conf, "interface eth0") || !strings.Contains(conf, "static ip_address=192.0.2.10/24") {
			t.Errorf("dhcpcd.conf:\n%s", conf)
		}
		for name, want := range map[string]string{
			"sshd":                "/usr/lib/slinit.d/sshd",
			"sunlight-autoupdate": "/etc/slinit.d/sunlight-autoupdate",
		} {
			link := filepath.Join(e.root, "etc", "slinit.d", "boot.d", name)
			got, err := os.Readlink(link)
			if err != nil || got != want {
				t.Errorf("boot.d/%s -> %q (%v), want %s", name, got, err, want)
				continue
			}
			if st, err := os.Stat(filepath.Join(e.root, got)); err != nil || !st.Mode().IsRegular() {
				t.Errorf("boot.d/%s leads to no service file: %v", name, err)
			}
		}
		if ents, _ := os.ReadDir(filepath.Join(e.root, "etc", "slinit.d", "boot.d")); len(ents) != 2 {
			t.Errorf("boot.d holds %d links, want sshd and sunlight-autoupdate", len(ents))
		}
		if _, err := os.Lstat(filepath.Join(e.root, "etc", "runit", "runsvdir", "default", "dhcpcd")); err == nil {
			t.Error("runit's dhcpcd was enabled on an slinit system")
		}

		// The mirror, recorded for the installed system's own updates;
		// the signing keys, seeded.
		if got := e.read("etc", "xbps.d", "10-sunlight.conf"); !strings.Contains(got, "repository=https://sunlight.example.org/current\n") {
			t.Errorf("10-sunlight.conf = %q", got)
		}
		if _, err := os.Stat(filepath.Join(e.root, "var", "db", "xbps", "keys", "d8:42:6d:5a.plist")); err != nil {
			t.Errorf("signing key not seeded: %v", err)
		}

		// The saved config: root's alone, beside nothing it should not
		// carry, and a config that loads again.
		if e.mode("root", "slinstaller.yaml") != 0o600 || e.mode("root") != 0o700 {
			t.Errorf("saved config mode %v in /root %v, want 0600 in 0700", e.mode("root", "slinstaller.yaml"), e.mode("root"))
		}
		saved := e.read("root", "slinstaller.yaml")
		if !strings.Contains(saved, "hostname: e2e-host") || !strings.Contains(saved, "login: ion") {
			t.Errorf("saved config:\n%s", saved)
		}
		if _, err := config.Load(filepath.Join(e.root, "root", "slinstaller.yaml")); err != nil {
			t.Errorf("the saved config does not load: %v", err)
		}
		if _, err := os.Stat(filepath.Join(e.root, "root", "post-install-ran")); err != nil {
			t.Errorf("the post-install hook did not run on the mounted target: %v", err)
		}

		// The live system's DNS is there for the install, marked as such.
		if liveResolvErr == nil && !strings.HasPrefix(e.read("etc", "resolv.conf"), resolvMark) {
			t.Error("no install-time resolv.conf")
		}
	}

	kexecd := false
	e.atKexec = func() {
		kexecd = true
		// finish has taken the live resolv.conf out, and saved the log.
		if _, err := os.Lstat(filepath.Join(e.root, "etc", "resolv.conf")); err == nil {
			t.Error("the live system's resolv.conf is still in the target at finish")
		}
		got := e.read("root", "slinstaller.log")
		if !strings.Contains(got, "==> pre-install\n") || !strings.Contains(got, "==> post-install\n") {
			t.Errorf("the archived log is not the install's:\n%s", got)
		}
		if e.mode("root", "slinstaller.log") != 0o600 {
			t.Errorf("archived log mode %v", e.mode("root", "slinstaller.log"))
		}
		// Still mounted: the kernel is read from the target.
		if len(mountsUnder(t, e.root)) == 0 {
			t.Error("the target was unmounted before kexec loaded its kernel")
		}
	}

	// ... and no longer once it is complete: nothing is left to resume.
	// (Looked at when finish loads the kernel, the target still mounted.)
	markGone := false
	prevKexec := e.atKexec
	e.atKexec = func() {
		markGone = !exists(filepath.Join(e.root, resumeMark))
		if prevKexec != nil {
			prevKexec()
		}
	}
	if err := e.install(cfg, opts); err != nil {
		t.Fatalf("install: %v\n%s", err, strings.Join(e.log, "\n"))
	}
	if !markGone {
		t.Error("the resume mark is still there when the install is complete")
	}

	if got, want := strings.Join(e.stages, " "), strings.Join(append(StageNames(), "complete"), " "); got != want {
		t.Errorf("stages run: %s\nwant: %s", got, want)
	}
	if !kexecd {
		t.Error("finish did not load the kernel for kexec")
	}

	// The order the commands came in, each within its stage.
	e.inOrder(
		"== pre-install", "pre-install ran",
		"== partition", "run wipefs -a /dev/sda1", "run wipefs -a /dev/sda",
		"run parted -s /dev/sda mklabel gpt", "run partprobe /dev/sda", "run udevadm settle --timeout=30",
		"run mkfs.vfat", "run mkfs.ext4",
		"== mount", "== meta-fs",
		"== base-install", "run mkdir -p "+e.root+"/var/db/xbps/keys", "run cp -a "+keys+"/.",
		"run xbps-install -Sy -R /run/initramfs/live/repo -R https://sunlight.example.org/current -R https://repo.example.org/current -r "+e.root+" base-system slinit slinit-services grub-x86_64-efi linux",
		"run chroot "+e.root+" xbps-reconfigure -a",
		"== fstab", "exec blkid -p -s UUID -o value /dev/sda1", "exec blkid -p -s UUID -o value /dev/sda2", "grep -q 'UUID=7A3B-19C2' "+e.root+"/etc/fstab",
		"== swapfile",
		"== bootloader",
		"run chroot "+e.root+" grub-install --target=x86_64-efi --efi-directory=/boot --bootloader-id=Sunlight",
		"run chroot "+e.root+" grub-install --target=x86_64-efi --efi-directory=/boot --removable",
		"run chroot "+e.root+" grub-mkconfig -o /boot/grub/grub.cfg",
		"== first-boot",
		"run chroot "+e.root+" xbps-reconfigure -f glibc-locales",
		"run chroot "+e.root+" useradd -m -G wheel -c Ion -p $6$salt$the-hash ion",
		"run chroot "+e.root+" chown ion:ion /home/ion/.ssh /home/ion/.ssh/authorized_keys",
		"run chroot "+e.root+" usermod --lock root",
		"run chroot "+e.root+" chage --lastday",
		"== network", "== save-config",
		"== post-install", "touch "+e.root+"/root/post-install-ran",
		"== complete",
		"run kexec -l "+e.root+"/boot/vmlinuz-7.3.0_1",
	)
	// Nothing that would rebuild the initramfs: there is nothing in it to
	// change for a plain root.
	for i, l := range e.log {
		if strings.Contains(l, "dracut") || (strings.Contains(l, "xbps-reconfigure -fa") && i > e.at("== fstab")) {
			t.Errorf("an initramfs rebuild on a plain install: %s", l)
		}
	}
	if k := e.log[e.at("run kexec")]; !strings.Contains(k, "init=/usr/bin/slinit") || !strings.Contains(k, "initramfs-7.3.0_1.img") {
		t.Errorf("kexec = %s", k)
	}

	// And then nothing is mounted, and nothing was written beside the
	// "disk": the directory under the target's mounts is empty.
	if m := mountsUnder(t, e.root); len(m) > 0 {
		t.Errorf("still mounted after the install: %v", m)
	}
	if ents, _ := os.ReadDir(e.root); len(ents) != 0 {
		var names []string
		for _, d := range ents {
			names = append(names, d.Name())
		}
		t.Errorf("written under the target's mount point, outside its filesystems: %v", names)
	}
}

func e2eEncrypted(t *testing.T, dir string) {
	e := newE2E(t, dir)
	// /boot on the encrypted root (the ESP at /boot/efi), and an encrypted
	// /home: one passphrase at GRUB, a keyfile for the rest.
	cfg := cryptConfig("/boot/efi", &config.Media{Name: "sda3", Type: "crypt", FsType: "ext4", Mountpoint: "/home"})
	cfg.Hostname = "e2e-crypt"
	opts := Options{
		Repo:       "https://repo.example.org/current",
		Packages:   []string{"base-system", "slinit", "slinit-services", "grub-x86_64-efi", "cryptsetup", "dracut"},
		Passphrase: testPassphrase,
		Devices:    noDevices,
		Output:     io.Discard,
	}

	e.atComplete = func() {
		mounts := strings.Join(mountsUnder(t, e.root), "\n") + "\n"
		for _, want := range []string{e.root + " ", e.root + "/boot/efi ", e.root + "/home "} {
			if !strings.Contains(mounts, want) {
				t.Errorf("not mounted: %s\n%s", want, mounts)
			}
		}
		fstab := e.read("etc", "fstab")
		for _, want := range []string{
			"UUID=fs-cryptroot\t/\text4\tdefaults\t0 1\n",
			"UUID=7A3B-19C2\t/boot/efi\tvfat\tdefaults\t0 2\n",
			"UUID=fs-crypthome\t/home\text4\tdefaults\t0 2\n",
		} {
			if !strings.Contains(fstab, want) {
				t.Errorf("fstab lacks %q:\n%s", want, fstab)
			}
		}
		// The keyfile: on the encrypted root, root's alone, in the
		// initramfs (which is itself on the encrypted /boot).
		key := filepath.Join(e.root, keyfilePath)
		if st, err := os.Stat(key); err != nil || st.Mode().Perm() != 0o400 || st.Size() != 2048 {
			t.Errorf("keyfile: %v %v", st, err)
		}
		if got := e.read("etc", "dracut.conf.d", "10-slinstaller-keyfile.conf"); !strings.Contains(got, keyfilePath) {
			t.Errorf("dracut keyfile conf = %q", got)
		}
		// crypttab: /home only (the initramfs opens root), with the keyfile.
		tab := e.read("etc", "crypttab")
		if !strings.Contains(tab, "crypthome UUID=luks-sda3 "+keyfilePath+" luks") || strings.Contains(tab, "cryptroot") {
			t.Errorf("crypttab:\n%s", tab)
		}
		if e.mode("etc", "crypttab") != 0o600 {
			t.Errorf("crypttab mode %v", e.mode("etc", "crypttab"))
		}
		grub := e.read("etc", "default", "grub")
		always, normal := grubLines(grub)
		for _, want := range []string{"init=/usr/bin/slinit", "rd.luks.uuid=luks-sda2", "rd.luks.crypttab=0", "rd.luks.key=" + keyfilePath + ":/"} {
			if !strings.Contains(" "+always+" ", " "+want+" ") {
				t.Errorf("GRUB_CMDLINE_LINUX = %q lacks %s", always, want)
			}
		}
		if strings.Contains(always+normal, "luks-sda3") {
			t.Errorf("the initramfs is told to open /home too: %q %q", always, normal)
		}
		if !strings.Contains(grub, "GRUB_ENABLE_CRYPTODISK=y") {
			t.Errorf("GRUB cannot unlock /boot:\n%s", grub)
		}
		if _, err := os.Stat(filepath.Join(e.root, "boot", "efi", "EFI", "Sunlight", "grubx64.efi")); err != nil {
			t.Errorf("the loader is not on the ESP: %v", err)
		}
		// The passphrase reaches cryptsetup on stdin and nowhere in the
		// installed system's saved config.
		if saved := e.read("root", "slinstaller.yaml"); strings.Contains(saved, testPassphrase) {
			t.Error("the saved config holds the passphrase")
		}
	}

	if err := e.install(cfg, opts); err != nil {
		t.Fatalf("install: %v\n%s", err, strings.Join(e.log, "\n"))
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "cryptsetup.stdin")); string(got) != testPassphrase {
		t.Errorf("cryptsetup was given %q on stdin", got)
	}

	e.inOrder(
		"== partition", "run parted -s /dev/sda mklabel gpt",
		// GRUB opens the root (it holds /boot): pbkdf2 for it, not for /home.
		"exec cryptsetup --batch-mode luksFormat --pbkdf pbkdf2 /dev/sda2 -",
		"exec cryptsetup --batch-mode luksOpen /dev/sda2 cryptroot -",
		"exec cryptsetup --batch-mode luksFormat /dev/sda3 -",
		"exec cryptsetup --batch-mode luksOpen /dev/sda3 crypthome -",
		"run mkfs.vfat", "run mkfs.ext4 -v -F -b 4096 /dev/mapper/cryptroot",
		"run mkfs.ext4 -v -F -b 4096 /dev/mapper/crypthome",
		"== mount", "== base-install", "run xbps-install",
		"== fstab", "exec blkid -p -s UUID -o value /dev/mapper/cryptroot",
		"exec cryptsetup --batch-mode --key-file=- --pbkdf=pbkdf2 --pbkdf-force-iterations=1000 luksAddKey /dev/sda2 "+e.root+keyfilePath,
		"exec cryptsetup --batch-mode --key-file=- --pbkdf=pbkdf2 --pbkdf-force-iterations=1000 luksAddKey /dev/sda3 "+e.root+keyfilePath,
		"exec cryptsetup luksUUID /dev/sda3",
		"== bootloader", "exec cryptsetup luksUUID /dev/sda2",
		"run chroot "+e.root+" grub-install --target=x86_64-efi --efi-directory=/boot/efi --bootloader-id=Sunlight",
		"run chroot "+e.root+" grub-mkconfig -o /boot/grub/grub.cfg",
		// The initramfs is built again, now with the crypt setup in it.
		"run chroot "+e.root+" dracut --regenerate-all --force",
		"== first-boot", "== complete",
	)
	if strings.Contains(e.log[e.at("luksFormat /dev/sda3")], "pbkdf2") {
		t.Error("/home was formatted with GRUB's weaker key derivation")
	}
	if m := mountsUnder(t, e.root); len(m) > 0 {
		t.Errorf("still mounted after the install: %v", m)
	}
	if ents, _ := os.ReadDir(e.root); len(ents) != 0 {
		t.Errorf("written under the target's mount point, outside its filesystems: %d entries", len(ents))
	}
}
