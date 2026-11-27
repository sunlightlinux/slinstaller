package main

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/sunlightlinux/slinstaller/pkg/config"
	"github.com/sunlightlinux/slinstaller/pkg/system"
)

// --apply only runs as root, and what it guards is the moment before a disk
// is wiped. These tests run it as root inside a user namespace — root over
// nothing: it cannot open a real disk — and take two more precautions: PATH
// holds only stand-in tools (see onlyTools), and the target is a device that
// does not exist. Each case then checks that a refusal comes before parted
// is ever called.

const (
	applyChildEnv = "SLINSTALLER_APPLY_CHILD"
	applyBaseEnv  = "SLINSTALLER_APPLY_BASE"
)

func TestApplyAsRootInAUserNamespace(t *testing.T) {
	if os.Getenv(applyChildEnv) == "1" {
		applyChild(t)
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestApplyAsRootInAUserNamespace$", "-test.v")
	for _, a := range os.Args[1:] {
		if strings.HasPrefix(a, "-test.gocoverdir=") {
			cmd.Args = append(cmd.Args, a)
		}
	}
	cmd.Env = append(os.Environ(), applyChildEnv+"=1", applyBaseEnv+"="+t.TempDir())
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags:                 syscall.CLONE_NEWUSER | syscall.CLONE_NEWNS,
		UidMappings:                []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Getuid(), Size: 1}},
		GidMappings:                []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Getgid(), Size: 1}},
		GidMappingsEnableSetgroups: false,
	}
	out, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	if err != nil && !errors.As(err, &exit) {
		t.Skipf("cannot start a process in a user namespace: %v", err)
	}
	if err != nil {
		t.Fatalf("--apply in the namespace failed:\n%s", out)
	}
	// A child that skipped has checked nothing; do not report that as a pass.
	if strings.Contains(string(out), "--- SKIP") {
		t.Skipf("the namespace run was skipped:\n%s", out)
	}
}

// baseTools is what a plain install needs on the live system.
var baseTools = []string{"parted", "partprobe", "mkswap", "blkid", "mkfs.vfat", "mkfs.ext4", "xbps-install", "chroot", "wipefs"}

type applyEnv struct {
	t    *testing.T
	base string
	bin  string
	repo string
	args []string // flags every run gets
}

// newApplyEnv gives a fresh set of stand-in tools, a local repository and a
// target root, and the flags that point an install at them.
func newApplyEnv(t *testing.T, tools ...string) *applyEnv {
	base, err := os.MkdirTemp(os.Getenv(applyBaseEnv), "case-")
	if err != nil {
		t.Fatal(err)
	}
	e := &applyEnv{t: t, base: base, repo: filepath.Join(base, "repo")}
	e.bin = onlyTools(t, base, append(append([]string{}, baseTools...), tools...)...)
	os.MkdirAll(e.repo, 0o755)
	os.WriteFile(filepath.Join(e.repo, "x86_64-repodata"), nil, 0o644)
	e.args = []string{"--apply", "--offline", "--local-repo", e.repo,
		"--root", filepath.Join(base, "mnt"), "--log", filepath.Join(base, "install.log"),
		"--keys", filepath.Join(base, "no-keys")}
	return e
}

func (e *applyEnv) run(stdin string, cfg *config.Config, extra ...string) (string, error) {
	e.t.Helper()
	args := append(append(append([]string{}, e.args...), extra...), writeConfig(e.t, e.base, cfg))
	_, errOut, err := runWith(e.t, stdin, args...)
	return errOut, err
}

// ran reports whether the stand-in tool was called, and with what.
func (e *applyEnv) ran(tool string) string {
	data, _ := os.ReadFile(filepath.Join(e.bin, tool+".args"))
	return string(data)
}

// refused checks that the install stopped with the given reason before
// anything was written to a disk.
func (e *applyEnv) refused(what string, err error, part string) {
	e.t.Helper()
	wantRunErr(e.t, what, err, part)
	for _, tool := range []string{"parted", "wipefs", "mkfs.ext4", "mkfs.vfat", "cryptsetup"} {
		if got := e.ran(tool); got != "" {
			e.t.Errorf("%s: %s ran although the install was refused:\n%s", what, tool, got)
		}
	}
}

func applyChild(t *testing.T) {
	if os.Geteuid() != 0 || os.Getenv(applyBaseEnv) == "" {
		t.Fatalf("not in the test namespace (uid %d)", os.Geteuid())
	}
	confirmed := testDisk + "\n"
	base := os.Getenv(applyBaseEnv)
	lockPath = filepath.Join(base, "install.lock")
	diagnosticsDir = base

	if !system.IsUEFI() {
		// Not booted under UEFI: a full install is refused, whatever else
		// is right, and the rest of these cases cannot run.
		e := newApplyEnv(t)
		_, err := e.run(confirmed, plainConfig())
		e.refused("not UEFI", err, "not booted in UEFI mode")
		return
	}

	// The operator declines, or types something else: nothing happens.
	e := newApplyEnv(t)
	_, err := e.run("yes\n", plainConfig())
	e.refused("not confirmed", err, "aborted")

	// Confirmed: the install starts — the disk is released and partitioned
	// — and here stops at the mount, since there is no such disk.
	e = newApplyEnv(t)
	_, err = e.run(confirmed, plainConfig())
	wantRunErr(t, "full run", err, "controller: mount")
	if !strings.Contains(e.ran("wipefs"), "-a "+testDisk) || !strings.Contains(e.ran("parted"), testDisk+" mklabel gpt") {
		t.Errorf("a confirmed install did not partition: wipefs %q parted %q", e.ran("wipefs"), e.ran("parted"))
	}
	if log, _ := os.ReadFile(filepath.Join(e.base, "install.log")); !strings.Contains(string(log), "==> partition") {
		t.Errorf("the --log file does not carry the install:\n%s", log)
	}

	// That failure leaves a diagnostics archive behind: the error, the log,
	// the disks, and the config without its password hashes.
	e = newApplyEnv(t)
	withUser := plainConfig()
	withUser.Users = []*config.User{{Login: "ion", Password: "$6$salt$the-hash", Admin: true}}
	archive := filepath.Join(e.base, "diag.tar.gz")
	errOut, err := e.run(confirmed, withUser, "--diagnostics", archive)
	wantRunErr(t, "full run with a user", err, "controller: mount")
	if !strings.Contains(errOut, "Diagnostics for this failure are in "+archive) {
		t.Errorf("the archive is not announced:\n%s", errOut)
	}
	files := readArchive(t, archive)
	for _, want := range []string{"error.txt", "config.yaml", "install.log", "lsblk.txt"} {
		if _, ok := files[want]; !ok {
			t.Errorf("diagnostics lack %s (have %v)", want, keys(files))
		}
	}
	if !strings.Contains(files["error.txt"], "controller: mount") || !strings.Contains(files["install.log"], "==> partition") {
		t.Errorf("error.txt %q", files["error.txt"])
	}
	if strings.Contains(files["config.yaml"], "the-hash") || !strings.Contains(files["config.yaml"], "login: ion") {
		t.Errorf("config in the diagnostics:\n%s", files["config.yaml"])
	}
	// Without --diagnostics it is a dated file in the default place.
	e = newApplyEnv(t)
	diagnosticsDir = e.base
	errOut, _ = e.run(confirmed, plainConfig())
	if m, _ := filepath.Glob(filepath.Join(e.base, "slinstaller-diagnostics-*.tar.gz")); len(m) != 1 || !strings.Contains(errOut, m[0]) {
		t.Errorf("default diagnostics file: %v\n%s", m, errOut)
	}
	// An operator who declined has nothing to diagnose.
	e = newApplyEnv(t)
	diagnosticsDir = e.base
	_, _ = e.run("no\n", plainConfig())
	if m, _ := filepath.Glob(filepath.Join(e.base, "slinstaller-diagnostics-*")); len(m) != 0 {
		t.Errorf("an aborted install wrote diagnostics: %v", m)
	}

	// Only one install at a time: while another holds the lock, this one is
	// refused before it does anything.
	held, lerr := lockInstall()
	if lerr != nil {
		t.Fatalf("taking the lock: %v", lerr)
	}
	e = newApplyEnv(t)
	_, err = e.run(confirmed, plainConfig())
	e.refused("another install running", err, "another slinstaller install is running")
	held.Close()

	// --system-check runs every check and stops: no confirmation asked,
	// nothing written, and a problem is reported as it would be.
	e = newApplyEnv(t)
	errOut, err = e.run("", plainConfig(), "--system-check")
	if err != nil || !strings.Contains(errOut, "System check passed") {
		t.Errorf("--system-check: %v\n%s", err, errOut)
	}
	e.refused("--system-check", errors.New("x"), "x")
	e = newApplyEnv(t)
	os.Remove(filepath.Join(e.bin, "mkfs.vfat"))
	_, err = e.run("", plainConfig(), "--system-check")
	e.refused("--system-check with a missing tool", err, "missing tools on the live system: mkfs.vfat")
	e = newApplyEnv(t)
	_, err = e.run("", plainConfig(), "--system-check", "--through", "partition")
	e.refused("--system-check with --through", err, "drop --through")

	// Reported as it goes, and how it ended, to whoever asked.
	sink := newEventSink(t)
	e = newApplyEnv(t)
	if _, err = e.run(confirmed, plainConfig(), "--through", "partition", "--allow-insecure-http", "--report-url", sink.URL); err != nil {
		t.Errorf("reported install: %v", err)
	}
	if got := sink.summary(); got != "stage pre-install, stage partition, finish success" {
		t.Errorf("reported events = %s", got)
	}
	sink = newEventSink(t)
	e = newApplyEnv(t)
	_, _ = e.run(confirmed, plainConfig(), "--allow-insecure-http", "--report-url", sink.URL)
	if got := sink.summary(); !strings.HasSuffix(got, "stage mount, finish failure") {
		t.Errorf("reported events for a failed install = %s", got)
	}
	// An operator who declines has started nothing, and nothing is reported.
	sink = newEventSink(t)
	e = newApplyEnv(t)
	_, _ = e.run("no\n", plainConfig(), "--allow-insecure-http", "--report-url", sink.URL)
	if got := sink.summary(); got != "" {
		t.Errorf("an aborted install was reported: %s", got)
	}

	applyDiskImage(t, confirmed)

	// An installer message is shown before the operator confirms.
	msgs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, preInstallMessage) {
			io.WriteString(w, "install-msg:\n- default: Read the release notes first.\n")
			return
		}
		http.NotFound(w, r)
	}))
	e = newApplyEnv(t)
	errOut, _ = e.run("no\n", plainConfig(), "--message-url", msgs.URL)
	msgs.Close()
	if i, j := strings.Index(errOut, "Read the release notes first."), strings.Index(errOut, "Type the disk path"); i < 0 || i > j {
		t.Errorf("the message is not shown before the confirmation:\n%s", errOut)
	}

	// power-state: kexec needs kexec on the live system, checked up front.
	kx := plainConfig()
	kx.PowerState = "kexec"
	e = newApplyEnv(t)
	_, err = e.run(confirmed, kx)
	e.refused("no kexec", err, "missing tools on the live system: kexec")

	// --log-level 2: the terminal keeps the stages; the commands and what
	// they print go to the log file alone, which still has everything.
	e = newApplyEnv(t)
	errOut, err = e.run(confirmed, plainConfig(), "--through", "partition", "--log-level", "2")
	if err != nil {
		t.Errorf("quiet install: %v", err)
	}
	if !strings.Contains(errOut, "==> partition") || strings.Contains(errOut, "mklabel") {
		t.Errorf("quiet terminal output:\n%s", errOut)
	}
	if log, _ := os.ReadFile(filepath.Join(e.base, "install.log")); !strings.Contains(string(log), "mklabel gpt") || !strings.Contains(string(log), "==> partition") {
		t.Errorf("the log file of a quiet install:\n%s", log)
	}

	// --resume: no confirmation is asked (nothing is erased), nothing is
	// partitioned or formatted, and the install goes straight to the mount.
	e = newApplyEnv(t)
	errOut, err = e.run("", plainConfig(), "--resume")
	wantRunErr(t, "resume", err, "controller: mount")
	if !strings.Contains(errOut, "Resuming on "+testDisk+" from base-install: nothing is partitioned or formatted") {
		t.Errorf("resume is not announced:\n%s", errOut)
	}
	for _, tool := range []string{"parted", "wipefs", "mkfs.ext4", "mkfs.vfat", "mkswap"} {
		if got := e.ran(tool); got != "" {
			t.Errorf("a resumed install ran %s:\n%s", tool, got)
		}
	}
	// From a later stage, and not from one that cannot be resumed at.
	e = newApplyEnv(t)
	errOut, err = e.run("", plainConfig(), "--resume-from", "bootloader")
	wantRunErr(t, "resume from bootloader", err, "controller: mount")
	if !strings.Contains(errOut, "from bootloader") || e.ran("parted") != "" {
		t.Errorf("resume from bootloader:\n%s", errOut)
	}
	e = newApplyEnv(t)
	_, err = e.run("", plainConfig(), "--resume-from", "partition")
	e.refused("resume from partition", err, "cannot be resumed at \"partition\"")
	e = newApplyEnv(t)
	_, err = e.run("", plainConfig(), "--resume", "--through", "partition")
	e.refused("resume with --through", err, "--resume continues a full install")

	// --in-target wants a target that is really mounted.
	e = newApplyEnv(t)
	_, _, err = runWith(t, "", "--in-target", "true", "--root", filepath.Join(e.base, "empty"))
	wantRunErr(t, "--in-target with no target", err, "does not look like a mounted target")

	// The config's proxy is in force for the run itself.
	e = newApplyEnv(t)
	proxied := plainConfig()
	proxied.HTTPSProxy, proxied.NoProxy = "http://proxy.example:3128", "localhost"
	_, _ = e.run("", proxied, "--system-check")
	if os.Getenv("https_proxy") != "http://proxy.example:3128" || os.Getenv("HTTP_PROXY") != "http://proxy.example:3128" || os.Getenv("no_proxy") != "localhost" {
		t.Errorf("proxy environment: https_proxy=%q HTTP_PROXY=%q no_proxy=%q", os.Getenv("https_proxy"), os.Getenv("HTTP_PROXY"), os.Getenv("no_proxy"))
	}
	for _, k := range []string{"https_proxy", "HTTPS_PROXY", "http_proxy", "HTTP_PROXY", "no_proxy", "NO_PROXY"} {
		os.Unsetenv(k)
	}

	// A staged run stops where asked and says the target is left mounted.
	e = newApplyEnv(t)
	errOut, err = e.run(confirmed, plainConfig(), "--through", "partition")
	if err != nil || !strings.Contains(errOut, `(stopped after stage "partition"; target left mounted at `) {
		t.Errorf("--through partition: %v\n%s", err, errOut)
	}
	if !strings.Contains(e.ran("mkfs.ext4"), testDisk+"p3") {
		t.Errorf("the root filesystem was not made: %q", e.ran("mkfs.ext4"))
	}

	// --- refusals, each before anything is written ---

	e = newApplyEnv(t)
	_, err = e.run(confirmed, plainConfig(), "--repo", "http://repo.example/current", "--offline=false")
	e.refused("plain http", err, "is plain http")
	// A staged run fetches the same base: refused too.
	e = newApplyEnv(t)
	_, err = e.run(confirmed, plainConfig(), "--repo", "http://repo.example/current", "--offline=false", "--through", "partition")
	e.refused("plain http, staged", err, "is plain http")

	e = newApplyEnv(t)
	os.Remove(filepath.Join(e.repo, "x86_64-repodata"))
	_, err = e.run(confirmed, plainConfig(), "--packages", "grub-x86_64-efi")
	e.refused("offline with no repository", err, "offline install needs a local repository")

	// The default packages come from the live medium's repository, or the
	// Sunlight mirror: with neither, refused.
	e = newApplyEnv(t)
	os.Remove(filepath.Join(e.repo, "x86_64-repodata"))
	_, err = e.run(confirmed, plainConfig(), "--offline=false", "--repo", e.repo, "--extra-repo", "")
	e.refused("no live repository", err, "is only in the live medium's repository")

	// Kernel arguments that do not fit this machine — unless validation is
	// off, when the install goes on to ask for confirmation.
	misfit := plainConfig()
	misfit.KernelArguments = &config.KernelArguments{Add: []string{"isolcpus=100000-100003"}}
	e = newApplyEnv(t)
	_, err = e.run(confirmed, misfit)
	e.refused("kernel arguments", err, "kernel-arguments do not fit this machine")
	e = newApplyEnv(t)
	_, err = e.run("", misfit, "--skip-validation-all")
	e.refused("kernel arguments, validation off", err, "aborted")

	// A tool the install needs is not on the live system.
	e = newApplyEnv(t)
	os.Remove(filepath.Join(e.bin, "mkfs.ext4"))
	_, err = e.run(confirmed, plainConfig())
	e.refused("missing mkfs", err, "missing tools on the live system: mkfs.ext4")

	lvmCfg := &config.Config{TargetMedia: []*config.Media{{Name: testName, Type: "disk", Children: []*config.Media{
		{Name: testName + "p1", Type: "part", FsType: "vfat", Mountpoint: "/boot", Size: "512M"},
		{Name: testName + "p2", Type: "lvm", VolumeGroup: "vg0", Children: []*config.Media{
			{Name: "root", FsType: "ext4", Mountpoint: "/"}}},
	}}}}
	e = newApplyEnv(t)
	_, err = e.run(confirmed, lvmCfg)
	e.refused("LVM tools", err, "pvcreate, vgcreate, lvcreate")

	raidCfg := &config.Config{
		TargetMedia: []*config.Media{
			{Name: testName, Type: "disk", Children: []*config.Media{
				{Name: testName + "p1", Type: "part", FsType: "vfat", Mountpoint: "/boot", Size: "512M"},
				{Name: testName + "p2", Type: "raid", Raid: "md0"}}},
			{Name: "slitest1", Type: "disk", Children: []*config.Media{
				{Name: "slitest1p1", Type: "raid", Raid: "md0"}}},
		},
		Raid: []*config.RaidArray{{Name: "md0", Level: "1", FsType: "ext4", Mountpoint: "/"}},
	}
	e = newApplyEnv(t)
	_, err = e.run(confirmed, raidCfg)
	e.refused("RAID tools", err, "mdadm")

	// --- the target disk itself ---

	e = newApplyEnv(t)
	os.Remove(filepath.Join(e.bin, "lsblk.json"))
	_, err = e.run(confirmed, plainConfig())
	e.refused("no listing", err, "lsblk")

	e = newApplyEnv(t)
	setListing(t, e.bin, `{"name":"sdz","path":"/dev/sdz","size":1000,"type":"disk"}`)
	_, err = e.run(confirmed, plainConfig())
	e.refused("disk absent", err, "is not among the machine's block devices")

	e = newApplyEnv(t)
	setListing(t, e.bin, `{"name":"sdz","path":"/dev/sdz","size":1000,"type":"disk","children":[
		{"name":"slitest0","path":"`+testDisk+`","size":1000,"type":"part"}]}`)
	_, err = e.run(confirmed, plainConfig())
	e.refused("target is a partition", err, "is a part, not a whole disk")

	e = newApplyEnv(t)
	setListing(t, e.bin, `{"name":"slitest0","path":"`+testDisk+`","size":64000000000,"type":"disk","children":[
		{"name":"slitest0p1","path":"`+testDisk+`p1","size":1000,"type":"part","mountpoint":"[SWAP]"}]}`)
	_, err = e.run(confirmed, plainConfig(), "--force-destructive")
	e.refused("disk in use", err, "is in use: "+testDisk+"p1 is in use as swap")

	// A staged run cuts and formats the disk too: the same refusals, though
	// it skips the tool and network checks.
	e = newApplyEnv(t)
	setListing(t, e.bin, `{"name":"sdz","path":"/dev/sdz","size":1000,"type":"disk","children":[
		{"name":"slitest0","path":"`+testDisk+`","size":1000,"type":"part"}]}`)
	_, err = e.run(confirmed, plainConfig(), "--through", "partition")
	e.refused("staged: target is a partition", err, "is a part, not a whole disk")
	e = newApplyEnv(t)
	setListing(t, e.bin, `{"name":"slitest0","path":"`+testDisk+`","size":64000000000,"type":"disk","children":[
		{"name":"slitest0p1","path":"`+testDisk+`p1","size":1000,"type":"part","mountpoint":"[SWAP]"}]}`)
	_, err = e.run(confirmed, plainConfig(), "--through", "partition")
	e.refused("staged: disk in use", err, "is in use: "+testDisk+"p1 is in use as swap")

	// A RAID array shared with a disk the install does not wipe: refused
	// unless forced, and then the tool that stops the array is needed.
	shared := `{"name":"slitest0","path":"` + testDisk + `","size":64000000000,"type":"disk","children":[
		{"name":"slitest0p1","path":"` + testDisk + `p1","size":1000,"type":"part","children":[
			{"name":"md0","path":"/dev/md0","size":1000,"type":"raid1"}]}]},
		{"name":"sdz","path":"/dev/sdz","size":1000,"type":"disk","children":[
		{"name":"sdz1","path":"/dev/sdz1","size":1000,"type":"part","children":[
			{"name":"md0","path":"/dev/md0","size":1000,"type":"raid1"}]}]}`
	e = newApplyEnv(t)
	setListing(t, e.bin, shared)
	_, err = e.run(confirmed, plainConfig())
	e.refused("shared array", err, "would break other disks")
	wantRunErr(t, "shared array", err, "--force-destructive")
	e = newApplyEnv(t)
	setListing(t, e.bin, shared)
	_, err = e.run(confirmed, plainConfig(), "--force-destructive")
	e.refused("no tool to stop the array", err, "missing tools on the live system to release the target disk: mdadm")

	// A layout larger than the disk.
	small := `{"name":"slitest0","path":"` + testDisk + `","size":1073741824,"type":"disk"}`
	e = newApplyEnv(t)
	setListing(t, e.bin, small)
	_, err = e.run(confirmed, plainConfig())
	e.refused("disk too small", err, "but the disk is only")
	e = newApplyEnv(t)
	setListing(t, e.bin, small)
	_, err = e.run("", plainConfig(), "--skip-validation-size")
	e.refused("disk too small, size check off", err, "aborted")

	// Room for the fixed partitions is not enough: the root that fills
	// the rest needs its minimum too (3 GB less 512M and 2G is not 2 GB).
	e = newApplyEnv(t)
	setListing(t, e.bin, `{"name":"slitest0","path":"`+testDisk+`","size":3000000000,"type":"disk"}`)
	_, err = e.run(confirmed, plainConfig())
	e.refused("fill root too small", err, "below its 2 GB minimum")
	e = newApplyEnv(t)
	setListing(t, e.bin, `{"name":"slitest0","path":"`+testDisk+`","size":5000000000,"type":"disk"}`)
	_, err = e.run("", plainConfig())
	e.refused("fill root big enough", err, "aborted")

	// A disk that reports no size (an empty card reader) is no target,
	// whether or not sizes are checked.
	for _, flags := range [][]string{nil, {"--skip-validation-size"}} {
		e = newApplyEnv(t)
		setListing(t, e.bin, `{"name":"slitest0","path":"`+testDisk+`","size":0,"type":"disk"}`)
		_, err = e.run(confirmed, plainConfig(), flags...)
		e.refused("disk of size 0", err, "has no capacity (size 0)")
	}

	// Reusing partitions: they have to be there, and be partitions.
	reuse := plainConfig()
	reuse.ReusePartitions = true
	e = newApplyEnv(t)
	_, err = e.run(confirmed, reuse)
	e.refused("reuse, partitions absent", err, "reusePartitions: "+testDisk+"p1 does not exist")
	e = newApplyEnv(t)
	setListing(t, e.bin, `{"name":"slitest0","path":"`+testDisk+`","size":64000000000,"type":"disk","children":[
		{"name":"slitest0p1","path":"`+testDisk+`p1","size":1000,"type":"lvm"}]}`)
	_, err = e.run(confirmed, reuse)
	e.refused("reuse, not a partition", err, "is a lvm, not a partition")

	// Shrinking a partition: only an ext filesystem that is really there,
	// and only to something smaller. Otherwise the operator sees what will
	// be shrunk and created before confirming.
	shrink := &config.Config{ReusePartitions: true, TargetMedia: []*config.Media{{Name: testName, Type: "disk", Children: []*config.Media{
		{Name: testName + "p1", Type: "part", FsType: "vfat", Mountpoint: "/boot/efi", Preserve: true},
		{Name: testName + "p2", Type: "part", FsType: "ext4", Preserve: true, Resize: "40G"},
		{Name: testName + "p3", Type: "part", FsType: "ext4", Mountpoint: "/", Create: true},
	}}}}
	existing := func(fs string, size string) string {
		return `{"name":"slitest0","path":"` + testDisk + `","size":128000000000,"type":"disk","children":[
		{"name":"slitest0p1","path":"` + testDisk + `p1","size":536870912,"type":"part","fstype":"vfat"},
		{"name":"slitest0p2","path":"` + testDisk + `p2","size":` + size + `,"type":"part","fstype":"` + fs + `"}]}`
	}
	shrinkTools := []string{"e2fsck", "resize2fs", "sh"}
	e = newApplyEnv(t, shrinkTools...)
	setListing(t, e.bin, existing("ntfs", "107374182400"))
	_, err = e.run(confirmed, shrink)
	e.refused("a kept partition that is not what the config says", err, "is to be kept as ext4, but holds ntfs")
	// A kept partition with no filesystem at all is not what was described either.
	e = newApplyEnv(t, shrinkTools...)
	setListing(t, e.bin, existing("", "107374182400"))
	_, err = e.run(confirmed, shrink)
	e.refused("a kept partition that is empty", err, "is to be kept as ext4, but holds -")
	// An ntfs partition described as one can be shrunk, with its own tool.
	ntfs := &config.Config{ReusePartitions: true, TargetMedia: []*config.Media{{Name: testName, Type: "disk", Children: []*config.Media{
		{Name: testName + "p1", Type: "part", FsType: "vfat", Mountpoint: "/boot/efi", Preserve: true},
		{Name: testName + "p2", Type: "part", FsType: "ntfs", Preserve: true, Resize: "40G"},
		{Name: testName + "p3", Type: "part", FsType: "ext4", Mountpoint: "/", Create: true},
	}}}}
	e = newApplyEnv(t, "sh")
	setListing(t, e.bin, existing("ntfs", "107374182400"))
	_, err = e.run(confirmed, ntfs)
	e.refused("no ntfsresize", err, "missing tools on the live system: ntfsresize")
	e = newApplyEnv(t, shrinkTools...)
	setListing(t, e.bin, existing("ext4", "21474836480"))
	_, err = e.run(confirmed, shrink)
	e.refused("resize that would grow", err, "would not shrink it")
	e = newApplyEnv(t)
	setListing(t, e.bin, existing("ext4", "107374182400"))
	_, err = e.run(confirmed, shrink)
	e.refused("no resize tools", err, "missing tools on the live system: e2fsck, resize2fs")
	e = newApplyEnv(t, shrinkTools...)
	setListing(t, e.bin, existing("ext4", "107374182400"))
	errOut, err = e.run("no\n", shrink)
	e.refused("shrink not confirmed", err, "aborted")
	for _, want := range []string{
		"SHRINK " + testDisk + "p2 to 40.0G", "KEEP   " + testDisk + "p1", "CREATE " + testDisk + "p3 as ext4 -> /",
		"A SHRINK that is interrupted can lose what is on the partition",
	} {
		if !strings.Contains(errOut, want) {
			t.Errorf("confirmation missing %q:\n%s", want, errOut)
		}
	}

	// Formatting an existing partition: the operator is shown what is on it now.
	reformat := &config.Config{ReusePartitions: true, TargetMedia: []*config.Media{{Name: testName, Type: "disk", Children: []*config.Media{
		{Name: testName + "p1", Type: "part", FsType: "vfat", Mountpoint: "/boot/efi", Preserve: true},
		{Name: testName + "p2", Type: "part", FsType: "ext4", Mountpoint: "/"},
	}}}}
	e = newApplyEnv(t)
	setListing(t, e.bin, `{"name":"slitest0","path":"`+testDisk+`","size":128000000000,"type":"disk","children":[
		{"name":"slitest0p1","path":"`+testDisk+`p1","size":536870912,"type":"part","fstype":"vfat"},
		{"name":"slitest0p2","path":"`+testDisk+`p2","size":107374182400,"type":"part","fstype":"ntfs","label":"Windows"}]}`)
	errOut, err = e.run("no\n", reformat)
	e.refused("format not confirmed", err, "aborted")
	if !strings.Contains(errOut, `FORMAT `+testDisk+`p2 as ext4 -> /  (now ntfs, labelled "Windows")`) {
		t.Errorf("the confirmation does not say what is erased:\n%s", errOut)
	}

	// --- the LUKS passphrase ---

	crypt := &config.Config{TargetMedia: []*config.Media{{Name: testName, Type: "disk", Children: []*config.Media{
		{Name: testName + "p1", Type: "part", FsType: "vfat", Mountpoint: "/boot", Size: "512M"},
		{Name: testName + "p2", Type: "crypt", FsType: "ext4", Mountpoint: "/", Size: "0"},
	}}}}
	const secret = "open sesame 42"

	e = newApplyEnv(t)
	_, err = e.run(confirmed, crypt)
	e.refused("no cryptsetup", err, "cryptsetup")

	e = newApplyEnv(t, "cryptsetup")
	_, err = e.run(confirmed, crypt, "--crypt-file", filepath.Join(e.base, "absent"))
	e.refused("missing --crypt-file", err, "--crypt-file:")

	// Nothing given anywhere: refused rather than encrypted with nothing.
	e = newApplyEnv(t, "cryptsetup")
	_, err = e.run("", crypt)
	e.refused("no passphrase", err, "the layout is encrypted but no passphrase was given")

	// From a file (its trailing newline is not part of it), from the
	// environment, and from the input ahead of the confirmation. It never
	// reaches a command line, the terminal or the log.
	for name, tc := range map[string]struct {
		stdin string
		setup func(e *applyEnv) []string
	}{
		"file": {confirmed, func(e *applyEnv) []string {
			p := filepath.Join(e.base, "pass")
			os.WriteFile(p, []byte(secret+"\r\n"), 0o600)
			return []string{"--crypt-file", p}
		}},
		"environment": {confirmed, func(e *applyEnv) []string {
			t.Setenv("SLINSTALLER_LUKS_PASSPHRASE", secret)
			return nil
		}},
		"input": {secret + "\n" + confirmed, func(e *applyEnv) []string {
			t.Setenv("SLINSTALLER_LUKS_PASSPHRASE", "")
			return nil
		}},
	} {
		e = newApplyEnv(t, "cryptsetup")
		errOut, err := e.run(tc.stdin, crypt, append(tc.setup(e), "--through", "partition")...)
		if err != nil {
			t.Errorf("passphrase from %s: %v\n%s", name, err, errOut)
		}
		if got := e.ran("cryptsetup"); !strings.Contains(got, "luksFormat "+testDisk+"p2") || strings.Contains(got, secret) {
			t.Errorf("passphrase from %s: cryptsetup calls:\n%s", name, got)
		}
		log, _ := os.ReadFile(filepath.Join(e.base, "install.log"))
		if strings.Contains(errOut, secret) || strings.Contains(string(log), secret) {
			t.Errorf("passphrase from %s was shown or logged", name)
		}
	}

	// --- no config: the prompt installer ---

	// With no terminal the plain prompts are used; input that ends before
	// they are answered ends the install, with nothing touched.
	e = newApplyEnv(t)
	_, errOut2, err := runWith(t, "", "--text", "--log", filepath.Join(e.base, "install.log"))
	if err == nil {
		t.Errorf("the prompt installer went on without input:\n%s", errOut2)
	}
	e.refused("prompt installer without input", err, "")
}
