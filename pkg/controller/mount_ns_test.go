package controller

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/sunlightlinux/slinstaller/pkg/config"
	"github.com/sunlightlinux/slinstaller/pkg/storage"
)

// The mount stages are real mount(2) calls. As in pkg/storage, they run here
// in a child process inside its own user and mount namespace, with tmpfs
// standing in for the disk's filesystems; nothing it mounts is visible
// outside or outlives it. Skipped where user namespaces cannot mount.

const (
	mountChildEnv   = "SLINSTALLER_CTL_MOUNT_CHILD"
	mountRootEnv    = "SLINSTALLER_CTL_MOUNT_ROOT"
	mountSkipMarker = "MOUNT-NAMESPACE-UNAVAILABLE"
)

func TestMountStagesInAUserNamespace(t *testing.T) {
	if os.Getenv(mountChildEnv) == "1" {
		mountStagesChild(t)
		return
	}
	// The child never deletes under its mount roots (a recursive delete
	// through a bind of /dev would reach the host's); they are removed here
	// once the child and its private mounts are gone.
	root := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=^TestMountStagesInAUserNamespace$", "-test.v")
	for _, a := range os.Args[1:] {
		if strings.HasPrefix(a, "-test.gocoverdir=") {
			cmd.Args = append(cmd.Args, a)
		}
	}
	cmd.Env = append(os.Environ(), mountChildEnv+"=1", mountRootEnv+"="+root)
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
		t.Fatalf("mount stages in the namespace failed:\n%s", out)
	}
}

// nsInstaller is an installer over dir whose "disk" is two tmpfs mounts.
func nsInstaller(dir string) *installer {
	return &installer{
		cfg: &config.Config{},
		plan: &storage.Plan{Parts: []storage.Partition{
			{Device: "tmpfs", Filesystem: "tmpfs", Mountpoint: "/"},
			{Device: "tmpfs", Filesystem: "tmpfs", Mountpoint: "/boot"},
		}},
		opts: Options{Root: dir},
		mnt:  storage.NewMounter(dir),
	}
}

func mountStagesChild(t *testing.T) {
	base := os.Getenv(mountRootEnv)
	if base == "" || os.Getuid() != 0 {
		t.Fatalf("not in the test namespace (root=%q uid=%d)", base, os.Getuid())
	}
	sub := func(name string) string {
		d := filepath.Join(base, name)
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatal(err)
		}
		return d
	}

	// The normal run: mount the target, bind the kernel filesystems, then
	// finish.
	in := nsInstaller(sub("a"))
	defer in.mnt.UnmountAll()
	if err := in.mount(); err != nil {
		if errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.EACCES) {
			t.Log(mountSkipMarker)
			return
		}
		t.Fatalf("mount: %v", err)
	}
	if err := in.mountMeta(); err != nil {
		t.Fatalf("mountMeta: %v", err)
	}
	if _, err := os.Stat(filepath.Join(in.opts.Root, "proc", "self")); err != nil {
		t.Errorf("/proc not bound into the target: %v", err)
	}
	// Chrooted commands get the live system's DNS for the install …
	live, liveErr := os.ReadFile("/etc/resolv.conf")
	copied := filepath.Join(in.opts.Root, "etc", "resolv.conf")
	if liveErr == nil {
		got, err := os.ReadFile(copied)
		if err != nil || !bytes.Equal(got, append([]byte(resolvMark), live...)) || in.resolvCopy != copied {
			t.Errorf("resolv.conf not copied for the install: %v (resolvCopy=%q)", err, in.resolvCopy)
		}
	}
	// … and it does not stay behind: finish removes it, then unmounts.
	if err := in.finish(); err != nil {
		t.Fatalf("finish: %v", err)
	}
	if _, err := os.Stat(filepath.Join(in.opts.Root, "proc", "self")); err == nil {
		t.Error("the target is still mounted after finish")
	}

	fillSpareESPsOnTmpfs(t, base)
	unpackImageWithBinds(t, sub("img"))

	// A resolv.conf that cannot be taken out fails the finish, but the
	// target is unmounted all the same.
	r := nsInstaller(sub("r"))
	defer r.mnt.UnmountAll()
	if err := r.mount(); err != nil {
		t.Fatalf("mount: %v", err)
	}
	os.WriteFile(filepath.Join(r.opts.Root, "boot", "marker"), nil, 0o644)
	r.resolvCopy = filepath.Join(r.opts.Root, "etc", "resolv.conf")
	os.MkdirAll(filepath.Join(r.resolvCopy, "occupied"), 0o755)
	if err := r.finish(); err == nil || !strings.Contains(err.Error(), "remove") {
		t.Errorf("finish with resolv.conf stuck: %v", err)
	}
	if _, err := os.Stat(filepath.Join(r.opts.Root, "boot", "marker")); err == nil {
		t.Error("the target was left mounted after a failed finish")
	}

	if liveErr != nil {
		return // nothing to copy, so the copy cannot be made to fail
	}
	// The kernel filesystems are bound, but the DNS copy cannot be made.
	b := nsInstaller(sub("b"))
	defer b.mnt.UnmountAll()
	os.WriteFile(filepath.Join(b.opts.Root, "etc"), nil, 0o644)
	if err := b.mountMeta(); err == nil || !strings.Contains(err.Error(), "mkdir for resolv.conf") {
		t.Errorf("mountMeta with /etc blocked: %v", err)
	}
	c := nsInstaller(sub("c"))
	defer c.mnt.UnmountAll()
	os.MkdirAll(filepath.Join(c.opts.Root, "etc"), 0o755)
	// A link there is set aside, not a failure; a directory in the way of
	// the copy is.
	os.MkdirAll(filepath.Join(c.opts.Root, "etc", "resolv.conf.slinstaller-saved"), 0o755)
	os.MkdirAll(filepath.Join(c.opts.Root, "etc", "resolv.conf", "occupied"), 0o755)
	if err := c.mountMeta(); err == nil || !strings.Contains(err.Error(), "resolv.conf") {
		t.Errorf("mountMeta with resolv.conf unwritable: %v", err)
	}
}

func TestMountStagesReportFailure(t *testing.T) {
	// A device that is not there, and a target that cannot be created: both
	// come back as errors, with nothing left mounted. (No mount succeeds
	// here, so this is safe to run as root too.)
	in := nsInstaller(t.TempDir())
	in.plan.Parts[0].Device, in.plan.Parts[0].Filesystem = "/dev/slinstaller-no-such-device", "ext4"
	if err := in.mount(); err == nil || !strings.Contains(err.Error(), "slinstaller-no-such-device") {
		t.Errorf("mount: %v", err)
	}
	file := filepath.Join(t.TempDir(), "file")
	os.WriteFile(file, nil, 0o644)
	if err := nsInstaller(file).mountMeta(); err == nil {
		t.Error("mountMeta under a file succeeded")
	}
}

func TestFinish(t *testing.T) {
	var r recorder
	var log bytes.Buffer
	in := stagedInstaller(t, twoPartConfig(), &r)
	in.opts.Log = &log

	// The install-time resolv.conf is removed and the log is saved, private
	// to root, beside the saved config.
	etc := mkTarget(t, in, "etc")
	in.resolvCopy = filepath.Join(etc, "resolv.conf")
	os.WriteFile(in.resolvCopy, []byte("nameserver 10.0.0.1\n"), 0o644)
	in.opts.LogFile = filepath.Join(t.TempDir(), "install.log")
	os.WriteFile(in.opts.LogFile, []byte("+ parted ...\n"), 0o644)
	if err := in.finish(); err != nil {
		t.Fatalf("finish: %v", err)
	}
	if _, err := os.Stat(in.resolvCopy); err == nil {
		t.Error("the live system's resolv.conf was left in the target")
	}
	if got := readTarget(t, in, "root", "slinstaller.log"); got != "+ parted ...\n" {
		t.Errorf("archived log = %q", got)
	}
	if st, _ := os.Stat(filepath.Join(in.opts.Root, "root", "slinstaller.log")); st.Mode().Perm() != 0o600 {
		t.Errorf("archived log mode = %v, want 0600", st.Mode().Perm())
	}

	// A log that cannot be saved is a warning, not a failed install: the
	// system is already installed.
	in = stagedInstaller(t, twoPartConfig(), &r)
	in.opts.Log = &log
	in.opts.LogFile = filepath.Join(t.TempDir(), "missing.log")
	log.Reset()
	if err := in.finish(); err != nil {
		t.Errorf("finish with an unsaveable log: %v", err)
	}
	if !strings.Contains(log.String(), "warning: the install log was not saved") {
		t.Errorf("no warning about the log: %q", log.String())
	}

	// A resolv.conf that cannot be removed is an error: left behind, it
	// would pin the installed system to the live medium's DNS.
	in = stagedInstaller(t, twoPartConfig(), &r)
	in.resolvCopy = filepath.Join(mkTarget(t, in, "etc"), "resolv.conf")
	os.MkdirAll(filepath.Join(in.resolvCopy, "occupied"), 0o755)
	wantErr(t, in.finish(), "remove")
}

// realRun runs a command for real; for the harmless ones (cp, mkdir) a test
// needs done inside its namespace.
func realRun(args []string) error {
	out, err := exec.Command(args[0], args[1:]...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s: %v: %s", strings.Join(args, " "), err, out)
	}
	return nil
}

// unpackImageWithBinds unpacks an image holding dev/, proc/ and an
// etc/resolv.conf into a target that has the live kernel filesystems bound
// in: nothing of it may land on the live /dev, and the binds and the
// install-time resolv.conf are back afterwards, with the image's own kept.
func unpackImageWithBinds(t *testing.T, dir string) {
	src := filepath.Join(dir, "src")
	for _, d := range []string{"dev", "proc", "etc", "usr/bin"} {
		os.MkdirAll(filepath.Join(src, d), 0o755)
	}
	os.WriteFile(filepath.Join(src, "dev", "slinstaller-probe"), nil, 0o644)
	os.WriteFile(filepath.Join(src, "etc", "resolv.conf"), []byte("nameserver 203.0.113.9\n"), 0o644)
	os.WriteFile(filepath.Join(src, "usr", "bin", "grub-install"), nil, 0o755)
	tarball := filepath.Join(dir, "image.tar")
	if out, err := exec.Command("tar", "-cf", tarball, "-C", src, ".").CombinedOutput(); err != nil {
		t.Fatalf("tar: %v %s", err, out)
	}
	root := filepath.Join(dir, "root")
	os.MkdirAll(root, 0o755)
	in := nsInstaller(root)
	in.cfg.Image = tarball
	in.opts.Runner = realRun
	defer in.mnt.UnmountAll()
	if err := in.mount(); err != nil {
		t.Fatalf("mount: %v", err)
	}
	if err := in.mountMeta(); err != nil {
		t.Fatalf("mountMeta: %v", err)
	}
	if err := in.unpackImage(); err != nil {
		t.Fatalf("unpacking an image with dev/ in it: %v", err)
	}
	if _, err := os.Stat("/dev/slinstaller-probe"); err == nil {
		os.Remove("/dev/slinstaller-probe")
		t.Error("the image's dev/ was unpacked onto the live /dev")
	}
	if _, err := os.Stat(filepath.Join(root, "proc", "self", "status")); err != nil {
		t.Errorf("/proc is not bound again after the unpack: %v", err)
	}
	if live, err := os.ReadFile("/etc/resolv.conf"); err == nil {
		got, _ := os.ReadFile(filepath.Join(root, "etc", "resolv.conf"))
		if string(got) != resolvMark+string(live) {
			t.Errorf("install-time resolv.conf = %q", got)
		}
		if kept, _ := os.ReadFile(in.resolvSaved); string(kept) != "nameserver 203.0.113.9\n" {
			t.Errorf("the image's resolv.conf was not kept aside: %q", kept)
		}
		if err := in.finish(); err != nil {
			t.Fatalf("finish: %v", err)
		}
	}
}
