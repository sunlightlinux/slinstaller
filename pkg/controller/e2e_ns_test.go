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
// itself (blkid, cryptsetup, mdadm, udevadm) are stand-ins first on PATH.
//
// What it cannot do is go through Install itself: Install takes its plan
// from the config, and a config's filesystems are ones the install can make
// (vfat, ext4), which the kernel does not let a user namespace mount. So
// the installer is built the way Install builds it, and for the mount stage
// alone — from its start to the next stage's — the plan's filesystems read
// tmpfs. Everything before and after (mkfs, fstab, the bootloader's ESP)
// sees the configured ones. The one other mount is a spare ESP's, which the
// bootloader stage mounts to copy the loader onto: for that stage alone a
// spare ESP reads tmpfs too.

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
	t.Run("lvm", func(t *testing.T) { e2eLVM(t, filepath.Join(base, "lvm")) })
	t.Run("lvm-on-luks", func(t *testing.T) { e2eLVMOnLUKS(t, filepath.Join(base, "lvmluks")) })
	t.Run("raid1", func(t *testing.T) { e2eRAID1(t, filepath.Join(base, "raid1")) })
	t.Run("crypt-home-plain-root", func(t *testing.T) { e2eCryptHomePlainRoot(t, filepath.Join(base, "crypthome")) })
	t.Run("reuse", func(t *testing.T) { e2eReuse(t, filepath.Join(base, "reuse")) })
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
	// as the real one does; udevadm settles nothing. mdadm knows two running
	// arrays: md0, the one a RAID install makes, and md127, a data array of
	// the live system on a disk the install does not touch, which only a
	// scan of every array lists. Each says it was called.
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
		"mdadm": note + `
line() {
	case "$1" in
	/dev/md0) echo "ARRAY /dev/md0 metadata=1.2 name=e2e:0 UUID=` + e2eMdUUID + `" ;;
	/dev/md127) echo "ARRAY /dev/md127 metadata=1.2 name=live:data UUID=deadbeef:00000000:00000000:0000d474" ;;
	*) echo "mdadm: cannot open $1: No such file or directory" >&2; exit 1 ;;
	esac
}
case "$1 $2" in
"--detail --export") line "$3" > /dev/null && echo "MD_UUID=$(line "$3" | sed 's/.*UUID=//')" ;;
"--detail --brief") shift 2; for d in "$@"; do line "$d"; done ;;
"--detail --scan") line /dev/md0; line /dev/md127 ;;
*) exit 1 ;;
esac`,
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
	var spares []int
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
		case "bootloader":
			// A spare ESP, mounted by the stage to copy the loader onto.
			for i := range plan.Parts {
				if plan.Parts[i].SpareESP {
					spares = append(spares, i)
					plan.Parts[i].Filesystem = "tmpfs"
				}
			}
		case "first-boot":
			for _, i := range spares {
				plan.Parts[i].Filesystem = saved[i].Filesystem
			}
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

// stage is what the log holds between the start of the stage named and the
// next one, less the waits for udev, which carry no work of their own.
func (e *e2e) stage(name string) []string {
	var out []string
	in := false
	for _, l := range e.log {
		if strings.HasPrefix(l, "== ") {
			in = l == "== "+name
			continue
		}
		if in && !strings.Contains(l, "udevadm settle") {
			out = append(out, l)
		}
	}
	return out
}

// asPlanned checks that the partition stage ran what PartitionCommands —
// the dry run's account of it — shows for the plan, in its order, and
// nothing besides: run by the Runner or, cryptsetup, by the controller.
func (e *e2e) asPlanned() {
	e.t.Helper()
	cmds, err := PartitionCommands(e.in.plan)
	if err != nil {
		e.t.Fatalf("PartitionCommands: %v", err)
	}
	var want []string
	for _, c := range cmds {
		want = append(want, strings.Join(c, " "))
	}
	var got []string
	for _, l := range e.stage("partition") {
		l, _ = strings.CutPrefix(l, "run ")
		l, _ = strings.CutPrefix(l, "exec ")
		got = append(got, l)
	}
	if g, w := strings.Join(got, "\n"), strings.Join(want, "\n"); g != w {
		e.t.Errorf("the partition stage ran:\n%s\nPartitionCommands shows:\n%s", g, w)
	}
}

// packages is what xbps-install was asked to install into the target.
func (e *e2e) packages() []string {
	e.t.Helper()
	i := e.at("run xbps-install")
	if i < 0 {
		e.t.Fatal("xbps-install never ran")
	}
	f := strings.Fields(e.log[i])
	return f[indexOf(f, "-r")+2:]
}

// noKeyfile checks that no LUKS key lies anywhere in the target (the live
// /proc, /sys and /dev bound into it left out), that dracut is not told of
// one, and that no volume was given a key slot for one.
func (e *e2e) noKeyfile() {
	e.t.Helper()
	filepath.WalkDir(e.root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		switch rel, _ := filepath.Rel(e.root, path); {
		case d.IsDir() && (rel == "proc" || rel == "sys" || rel == "dev"):
			return filepath.SkipDir
		case d.Name() == "cryptkeys" || strings.HasSuffix(d.Name(), ".key") || d.Name() == "10-slinstaller-keyfile.conf":
			e.t.Errorf("a keyfile in the target: /%s", rel)
		}
		return nil
	})
	if i := e.at("luksAddKey"); i >= 0 {
		e.t.Errorf("a key slot was added for a keyfile: %s", e.log[i])
	}
}

// noCrypttab checks that the target's crypttab lists nothing.
func (e *e2e) noCrypttab() {
	e.t.Helper()
	data, err := os.ReadFile(filepath.Join(e.root, "etc", "crypttab"))
	if err == nil && strings.TrimSpace(string(data)) != "" {
		e.t.Errorf("crypttab:\n%s", data)
	}
}

// leftNothing checks, once the install is over, that nothing is mounted
// and nothing was written beside the "disk".
func (e *e2e) leftNothing() {
	e.t.Helper()
	if m := mountsUnder(e.t, e.root); len(m) > 0 {
		e.t.Errorf("still mounted after the install: %v", m)
	}
	if ents, _ := os.ReadDir(e.root); len(ents) != 0 {
		e.t.Errorf("written under the target's mount point, outside its filesystems: %d entries", len(ents))
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

// e2eMdUUID is md0's UUID as the mdadm stand-in gives it.
const e2eMdUUID = "0b1e2d3c:4f5a6b7c:8d9e0f1a:2b3c4d5e"

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
		"== base-install", "run mkdir -p "+e.root+"/var/db/xbps/keys", "run cp -a --remove-destination "+keys+"/.",
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

// lvmHomeConfig is an ESP at /boot and a volume group vg0 on the rest of the
// disk, holding the root and /home; encrypted puts the group's physical
// volume in a LUKS container.
func lvmHomeConfig(encrypted bool) *config.Config {
	return &config.Config{TargetMedia: []*config.Media{{Name: "sda", Type: "disk", Children: []*config.Media{
		{Name: "sda1", Type: "part", FsType: "vfat", Mountpoint: "/boot", Size: "512M"},
		{Name: "sda2", Type: "lvm", VolumeGroup: "vg0", Encrypted: encrypted, Children: []*config.Media{
			{Name: "root", FsType: "ext4", Mountpoint: "/", Size: "20G"},
			{Name: "home", FsType: "ext4", Mountpoint: "/home"}}}}}}}
}

// e2eConfig gives a layout the rest a config needs to pass Validate, which
// it is held to: an install of a layout the installer refuses tests nothing.
func e2eConfig(t *testing.T, cfg *config.Config, host string) *config.Config {
	t.Helper()
	cfg.Hostname = host
	cfg.Users = []*config.User{{Login: "ion", Password: "$6$salt$the-hash", Admin: true}}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("the layout does not validate: %v", err)
	}
	return cfg
}

func e2eLVM(t *testing.T, dir string) {
	e := newE2E(t, dir)
	cfg := e2eConfig(t, lvmHomeConfig(false), "e2e-lvm")
	opts := Options{
		Repo:     "https://repo.example.org/current",
		Packages: []string{"base-system", "slinit", "slinit-services", "grub-x86_64-efi", "lvm2", "dracut"},
		Devices:  noDevices,
		Output:   io.Discard,
	}

	e.atComplete = func() {
		mounts := strings.Join(mountsUnder(t, e.root), "\n") + "\n"
		for _, want := range []string{e.root + " ", e.root + "/boot ", e.root + "/home "} {
			if !strings.Contains(mounts, want) {
				t.Errorf("not mounted: %s\n%s", want, mounts)
			}
		}
		// The volumes by the UUIDs blkid gave for them, never by their
		// /dev/vg0 paths.
		fstab := e.read("etc", "fstab")
		for _, want := range []string{
			"UUID=fs-root\t/\text4\tdefaults\t0 1\n",
			"UUID=7A3B-19C2\t/boot\tvfat\tdefaults\t0 2\n",
			"UUID=fs-home\t/home\text4\tdefaults\t0 2\n",
		} {
			if !strings.Contains(fstab, want) {
				t.Errorf("fstab lacks %q:\n%s", want, fstab)
			}
		}
		if strings.Contains(fstab, "/dev/") {
			t.Errorf("fstab names a device by path:\n%s", fstab)
		}
		// The initramfs activates the root's volume, and only that one.
		always, _ := grubLines(e.read("etc", "default", "grub"))
		if always != "init=/usr/bin/slinit rd.lvm.lv=vg0/root" {
			t.Errorf("GRUB_CMDLINE_LINUX = %q", always)
		}
		e.noCrypttab()
		e.noKeyfile()
		if exists(filepath.Join(e.root, "etc", "mdadm.conf")) {
			t.Error("an mdadm.conf on an install without RAID")
		}
	}

	if err := e.install(cfg, opts); err != nil {
		t.Fatalf("install: %v\n%s", err, strings.Join(e.log, "\n"))
	}
	if !contains(e.packages(), "lvm2") {
		t.Errorf("lvm2 is not installed: %v", e.packages())
	}
	e.asPlanned()
	e.inOrder(
		"== partition", "run parted -s /dev/sda mklabel gpt", "run partprobe /dev/sda",
		// The group on the partition, the fixed-size volume before the one
		// that takes the rest, the filesystems on the volumes.
		"run pvcreate -ff -y /dev/sda2", "run vgcreate vg0 /dev/sda2",
		"run lvcreate -y -L 20G -n root vg0", "run lvcreate -y -l 100%FREE -n home vg0",
		"run mkfs.vfat -F32 /dev/sda1", "run mkfs.ext4 -v -F -b 4096 /dev/vg0/root", "run mkfs.ext4 -v -F -b 4096 /dev/vg0/home",
		"== mount", "== base-install", "run xbps-install",
		"== fstab", "exec blkid -p -s UUID -o value /dev/vg0/root", "exec blkid -p -s UUID -o value /dev/vg0/home",
		"== bootloader",
		"run chroot "+e.root+" grub-install --target=x86_64-efi --efi-directory=/boot --bootloader-id=Sunlight",
		"run chroot "+e.root+" grub-mkconfig -o /boot/grub/grub.cfg",
		// The initramfs is built again, with the lvm module in it.
		"run chroot "+e.root+" dracut --regenerate-all --force",
		"== first-boot", "== complete",
	)
	if i := e.at("blkid -p -s UUID -o value /dev/sda2"); i >= 0 {
		t.Errorf("the physical volume was probed for a filesystem: %s", e.log[i])
	}
	if i := e.at("cryptsetup"); i >= 0 {
		t.Errorf("cryptsetup on an unencrypted group: %s", e.log[i])
	}
	e.leftNothing()
}

func e2eLVMOnLUKS(t *testing.T, dir string) {
	e := newE2E(t, dir)
	// /boot on the unencrypted ESP: dracut asks for the passphrase once,
	// so no keyfile is called for.
	cfg := e2eConfig(t, lvmHomeConfig(true), "e2e-lvmluks")
	opts := Options{
		Repo:       "https://repo.example.org/current",
		Packages:   []string{"base-system", "slinit", "slinit-services", "grub-x86_64-efi", "lvm2", "cryptsetup", "dracut"},
		Passphrase: testPassphrase,
		Devices:    noDevices,
		Output:     io.Discard,
	}

	e.atComplete = func() {
		fstab := e.read("etc", "fstab")
		for _, want := range []string{
			"UUID=fs-root\t/\text4\tdefaults\t0 1\n",
			"UUID=7A3B-19C2\t/boot\tvfat\tdefaults\t0 2\n",
			"UUID=fs-home\t/home\text4\tdefaults\t0 2\n",
		} {
			if !strings.Contains(fstab, want) {
				t.Errorf("fstab lacks %q:\n%s", want, fstab)
			}
		}
		// The root's physical volume is the initramfs's to open: crypttab
		// lists nothing, the command line names the container and the
		// volume, and nothing in it is told of a key.
		e.noCrypttab()
		e.noKeyfile()
		grub := e.read("etc", "default", "grub")
		always, normal := grubLines(grub)
		if always != "init=/usr/bin/slinit rd.luks.uuid=luks-sda2 rd.lvm.lv=vg0/root rd.luks.crypttab=0" {
			t.Errorf("GRUB_CMDLINE_LINUX = %q", always)
		}
		if strings.Contains(always+normal, "rd.luks.key") || strings.Contains(grub, "GRUB_ENABLE_CRYPTODISK") {
			t.Errorf("/etc/default/grub on an unencrypted /boot:\n%s", grub)
		}
	}

	if err := e.install(cfg, opts); err != nil {
		t.Fatalf("install: %v\n%s", err, strings.Join(e.log, "\n"))
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "cryptsetup.stdin")); string(got) != testPassphrase {
		t.Errorf("cryptsetup was given %q on stdin", got)
	}
	for _, p := range []string{"lvm2", "cryptsetup"} {
		if !contains(e.packages(), p) {
			t.Errorf("%s is not installed: %v", p, e.packages())
		}
	}
	e.asPlanned()
	e.inOrder(
		"== partition", "run parted -s /dev/sda mklabel gpt", "run partprobe /dev/sda",
		// GRUB opens nothing here: cryptsetup's own key derivation.
		"exec cryptsetup --batch-mode luksFormat /dev/sda2 -",
		"exec cryptsetup --batch-mode luksOpen /dev/sda2 cryptvg0 -",
		"run pvcreate -ff -y /dev/mapper/cryptvg0", "run vgcreate vg0 /dev/mapper/cryptvg0",
		"run lvcreate -y -L 20G -n root vg0", "run lvcreate -y -l 100%FREE -n home vg0",
		"run mkfs.ext4 -v -F -b 4096 /dev/vg0/root",
		"== fstab", "exec blkid -p -s UUID -o value /dev/vg0/root",
		"== bootloader", "exec cryptsetup luksUUID /dev/sda2",
		"run chroot "+e.root+" grub-mkconfig -o /boot/grub/grub.cfg",
		"run chroot "+e.root+" dracut --regenerate-all --force",
		"== first-boot", "== complete",
	)
	e.leftNothing()
}

func e2eRAID1(t *testing.T, dir string) {
	e := newE2E(t, dir)
	// The root on md0, mirrored over sda2 and sdb2; the ESP on sda, a spare
	// on sdb for when sda is gone.
	cfg := e2eConfig(t, serverConfig(&config.RaidArray{Name: "md0", Level: "1", FsType: "ext4", Mountpoint: "/"}), "e2e-raid")
	opts := Options{
		Repo:     "https://repo.example.org/current",
		Packages: []string{"base-system", "slinit", "slinit-services", "grub-x86_64-efi", "mdadm", "dracut"},
		Devices:  noDevices,
		Output:   io.Discard,
	}
	// The firmware has an entry for the spare from an earlier install,
	// which is replaced, and others, which are left alone.
	saved := efiBootList
	defer func() { efiBootList = saved }()
	efiBootList = func() string {
		return "BootCurrent: 0001\nBootOrder: 0001,0004,0003\n" +
			"Boot0001* Sunlight\tHD(1,GPT,...)/File(\\EFI\\Sunlight\\grubx64.efi)\n" +
			"Boot0003* Windows Boot Manager\tHD(1,GPT,...)\n" +
			"Boot0004* Sunlight (sdb)\tHD(1,GPT,...)/File(\\EFI\\Sunlight\\grubx64.efi)\n"
	}

	e.atComplete = func() {
		mounts := strings.Join(mountsUnder(t, e.root), "\n") + "\n"
		for _, want := range []string{e.root + " ", e.root + "/boot/efi "} {
			if !strings.Contains(mounts, want) {
				t.Errorf("not mounted: %s\n%s", want, mounts)
			}
		}
		// The array's filesystem and the ESP; not the spare, never mounted.
		fstab := e.read("etc", "fstab")
		want := "# /etc/fstab — generated by slinstaller\n" +
			"UUID=fs-md0\t/\text4\tdefaults\t0 1\n" +
			"UUID=7A3B-19C2\t/boot/efi\tvfat\tdefaults\t0 2\n"
		if fstab != want {
			t.Errorf("fstab:\n%s\nwant:\n%s", fstab, want)
		}
		// The install's array, and not the live system's other one.
		conf := e.read("etc", "mdadm.conf")
		if want := "# Written by slinstaller — the RAID arrays this system assembles at boot.\n" +
			"ARRAY /dev/md0 metadata=1.2 name=e2e:0 UUID=" + e2eMdUUID + "\n"; conf != want {
			t.Errorf("mdadm.conf:\n%s\nwant:\n%s", conf, want)
		}
		if got := e.read("etc", "dracut.conf.d", "10-slinstaller-mdraid.conf"); !strings.Contains(got, `add_dracutmodules+=" mdraid "`) {
			t.Errorf("dracut mdraid conf = %q", got)
		}
		always, _ := grubLines(e.read("etc", "default", "grub"))
		if always != "init=/usr/bin/slinit rd.md.uuid="+e2eMdUUID {
			t.Errorf("GRUB_CMDLINE_LINUX = %q", always)
		}
		e.noCrypttab()
	}

	if err := e.install(cfg, opts); err != nil {
		t.Fatalf("install: %v\n%s", err, strings.Join(e.log, "\n"))
	}
	if !contains(e.packages(), "mdadm") {
		t.Errorf("mdadm is not installed: %v", e.packages())
	}
	e.asPlanned()
	e.inOrder(
		"== partition", "run parted -s /dev/sda mklabel gpt", "run partprobe /dev/sda",
		"run parted -s /dev/sdb mklabel gpt", "run partprobe /dev/sdb",
		// The array from its two members, once both disks are cut.
		"run mdadm --create /dev/md0 --run --force --metadata=1.2 --level=1 --raid-devices=2 /dev/sda2 /dev/sdb2",
		"run mkfs.vfat -F32 /dev/sda1", "run mkfs.vfat -F32 /dev/sdb1", "run mkfs.ext4 -v -F -b 4096 /dev/md0",
		"== mount", "== base-install", "run xbps-install",
		"== fstab", "exec blkid -p -s UUID -o value /dev/md0",
		"== bootloader", "exec mdadm --detail --export /dev/md0",
		// The firmware's entry for the ESP is grub-install's; the spare
		// gets the loader copied onto it and an entry of its own, the stale
		// one removed first.
		"run chroot "+e.root+" grub-install --target=x86_64-efi --efi-directory=/boot/efi --bootloader-id=Sunlight",
		"run chroot "+e.root+" grub-mkconfig -o /boot/grub/grub.cfg",
		"run cp -a "+e.root+"/boot/efi/EFI ",
		"run cp "+e.root+"/boot/efi/EFI/Sunlight/grubx64.efi ",
		"run chroot "+e.root+" efibootmgr --bootnum 0004 --delete-bootnum",
		"run chroot "+e.root+` efibootmgr --create --disk /dev/sdb --part 1 --label Sunlight (sdb) --loader \EFI\Sunlight\grubx64.efi`,
		"exec mdadm --detail --brief /dev/md0",
		"run chroot "+e.root+" dracut --regenerate-all --force",
		"== first-boot", "== complete",
	)
	if i := e.at("cp " + e.root + "/boot/efi/EFI/Sunlight/grubx64.efi "); i < 0 || !strings.HasSuffix(e.log[i], "/EFI/BOOT/BOOTX64.EFI") {
		t.Error("the loader is not at the spare's removable-media path")
	}
	for i, l := range e.log {
		if strings.Contains(l, "grub-install") && strings.Contains(l, "--no-nvram") {
			t.Errorf("the ESP got no firmware entry: %s", l)
		}
		if strings.Contains(l, "efibootmgr") && (strings.Contains(l, "0001") || strings.Contains(l, "0003")) {
			t.Errorf("another entry was touched: %s", l)
		}
		// Members and the spare carry no filesystem of the root's.
		for _, d := range []string{"/dev/sda2", "/dev/sdb2", "/dev/sdb1"} {
			if strings.HasPrefix(l, "exec blkid") && strings.HasSuffix(l, " "+d) {
				t.Errorf("probed for fstab: %s (line %d)", l, i)
			}
		}
	}
	if n := strings.Count(strings.Join(e.log, "\n"), "efibootmgr --create"); n != 1 {
		t.Errorf("%d efibootmgr entries made, want the spare's one", n)
	}
	e.leftNothing()
}

func e2eCryptHomePlainRoot(t *testing.T, dir string) {
	e := newE2E(t, dir)
	// A plain root and an encrypted /home: the key to /home must not lie
	// in the clear on the root beside it; /home asks for its passphrase.
	cfg := e2eConfig(t, &config.Config{TargetMedia: []*config.Media{{Name: "sda", Type: "disk", Children: []*config.Media{
		{Name: "sda1", Type: "part", FsType: "vfat", Mountpoint: "/boot", Size: "512M"},
		{Name: "sda2", Type: "part", FsType: "ext4", Mountpoint: "/", Size: "20G"},
		{Name: "sda3", Type: "crypt", FsType: "ext4", Mountpoint: "/home"}}}}}, "e2e-crypthome")
	opts := Options{
		Repo:       "https://repo.example.org/current",
		Packages:   []string{"base-system", "slinit", "slinit-services", "grub-x86_64-efi", "cryptsetup", "dracut"},
		Passphrase: testPassphrase,
		Devices:    noDevices,
		Output:     io.Discard,
	}

	e.atComplete = func() {
		fstab := e.read("etc", "fstab")
		for _, want := range []string{
			"UUID=3f1c0a52-8d2e-4c4b-9e0e-5b8f1a2c3d4e\t/\text4\tdefaults\t0 1\n",
			"UUID=fs-crypthome\t/home\text4\tdefaults\t0 2\n",
		} {
			if !strings.Contains(fstab, want) {
				t.Errorf("fstab lacks %q:\n%s", want, fstab)
			}
		}
		e.noKeyfile()
		if tab := e.read("etc", "crypttab"); tab != "crypthome UUID=luks-sda3 none luks\n" || e.mode("etc", "crypttab") != 0o600 {
			t.Errorf("crypttab (mode %v):\n%s", e.mode("etc", "crypttab"), tab)
		}
		// The initramfs opens nothing: root needs no unlocking.
		grub := e.read("etc", "default", "grub")
		always, normal := grubLines(grub)
		if always != "init=/usr/bin/slinit rd.luks.crypttab=0" {
			t.Errorf("GRUB_CMDLINE_LINUX = %q", always)
		}
		if strings.Contains(always+normal, "rd.luks.key") || strings.Contains(always+normal, "rd.luks.uuid") || strings.Contains(grub, "GRUB_ENABLE_CRYPTODISK") {
			t.Errorf("/etc/default/grub:\n%s", grub)
		}
	}

	if err := e.install(cfg, opts); err != nil {
		t.Fatalf("install: %v\n%s", err, strings.Join(e.log, "\n"))
	}
	e.asPlanned()
	e.inOrder(
		"== partition", "run parted -s /dev/sda mklabel gpt", "run partprobe /dev/sda",
		"exec cryptsetup --batch-mode luksFormat /dev/sda3 -",
		"exec cryptsetup --batch-mode luksOpen /dev/sda3 crypthome -",
		"run mkfs.vfat -F32 /dev/sda1", "run mkfs.ext4 -v -F -b 4096 /dev/sda2",
		"run mkfs.ext4 -v -F -b 4096 /dev/mapper/crypthome",
		"== mount", "== base-install",
		"== fstab", "exec blkid -p -s UUID -o value /dev/mapper/crypthome", "exec cryptsetup luksUUID /dev/sda3",
		"== bootloader", "== first-boot", "== complete",
	)
	e.leftNothing()
}

func e2eReuse(t *testing.T, dir string) {
	e := newE2E(t, dir)
	// Into the partitions already there: the ESP and /home kept as they
	// are, the root formatted afresh. The table is not touched.
	cfg := e2eConfig(t, &config.Config{ReusePartitions: true, TargetMedia: []*config.Media{{Name: "sda", Type: "disk", Children: []*config.Media{
		{Name: "sda1", Type: "part", FsType: "vfat", Mountpoint: "/boot", Preserve: true},
		{Name: "sda2", Type: "part", FsType: "ext4", Mountpoint: "/"},
		{Name: "sda3", Type: "part", FsType: "ext4", Mountpoint: "/home", Preserve: true}}}}}, "e2e-reuse")
	devices := func() ([]blockdev.Device, error) {
		return []blockdev.Device{{Name: "sda", Path: "/dev/sda", Type: "disk", Children: []blockdev.Device{
			{Name: "sda1", Path: "/dev/sda1", Type: "part", FsType: "vfat"},
			{Name: "sda2", Path: "/dev/sda2", Type: "part", FsType: "ext4"},
			{Name: "sda3", Path: "/dev/sda3", Type: "part", FsType: "ext4"}}}}, nil
	}
	opts := Options{
		Repo:     "https://repo.example.org/current",
		Packages: []string{"base-system", "slinit", "slinit-services", "grub-x86_64-efi"},
		Devices:  devices,
		Output:   io.Discard,
	}

	e.atComplete = func() {
		mounts := strings.Join(mountsUnder(t, e.root), "\n") + "\n"
		for _, want := range []string{e.root + " ", e.root + "/boot ", e.root + "/home "} {
			if !strings.Contains(mounts, want) {
				t.Errorf("not mounted: %s\n%s", want, mounts)
			}
		}
		// Kept or formatted, all three are the system's.
		fstab := e.read("etc", "fstab")
		for _, want := range []string{
			"UUID=3f1c0a52-8d2e-4c4b-9e0e-5b8f1a2c3d4e\t/\text4\tdefaults\t0 1\n",
			"UUID=7A3B-19C2\t/boot\tvfat\tdefaults\t0 2\n",
			"UUID=fs-sda3\t/home\text4\tdefaults\t0 2\n",
		} {
			if !strings.Contains(fstab, want) {
				t.Errorf("fstab lacks %q:\n%s", want, fstab)
			}
		}
		always, _ := grubLines(e.read("etc", "default", "grub"))
		if always != "init=/usr/bin/slinit" {
			t.Errorf("GRUB_CMDLINE_LINUX = %q", always)
		}
		e.noCrypttab()
	}

	if err := e.install(cfg, opts); err != nil {
		t.Fatalf("install: %v\n%s", err, strings.Join(e.log, "\n"))
	}
	// The partition stage formats the root and does nothing else: no
	// table, no signature erased, the kept partitions not formatted.
	if got := strings.Join(e.stage("partition"), "\n"); got != "run mkfs.ext4 -v -F -b 4096 /dev/sda2" {
		t.Errorf("the partition stage of a reuse install ran:\n%s", got)
	}
	e.asPlanned()
	for _, l := range e.log {
		if strings.Contains(l, "parted") || strings.Contains(l, "wipefs") || strings.Contains(l, "mkfs.vfat") || strings.Contains(l, "/dev/sda3") && strings.Contains(l, "mkfs") {
			t.Errorf("a reuse install touched what it keeps: %s", l)
		}
	}
	e.inOrder(
		"== partition", "run mkfs.ext4 -v -F -b 4096 /dev/sda2",
		"== mount", "== base-install", "run xbps-install",
		"== fstab", "exec blkid -p -s UUID -o value /dev/sda1", "exec blkid -p -s UUID -o value /dev/sda2", "exec blkid -p -s UUID -o value /dev/sda3",
		"== bootloader", "run chroot "+e.root+" grub-install --target=x86_64-efi --efi-directory=/boot --bootloader-id=Sunlight",
		"== first-boot", "== complete",
	)
	e.leftNothing()
}
