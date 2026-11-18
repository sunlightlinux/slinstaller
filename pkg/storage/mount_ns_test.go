package storage

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// The mounts are real mount(2) calls, which need root. Rather than leave them
// to the VM alone, this runs them in a child process inside its own user and
// mount namespace: the child is root there, can mount tmpfs and rebind /proc,
// /sys and /dev, and nothing it mounts is visible outside or outlives it. A
// tmpfs stands in for a disk's filesystem — what is tested is the mounting,
// the order and the teardown, not a filesystem driver.
//
// Where unprivileged user namespaces are not allowed the test skips.

const (
	mountChildEnv   = "SLINSTALLER_MOUNT_CHILD"
	mountRootEnv    = "SLINSTALLER_MOUNT_ROOT"
	mountSkipMarker = "MOUNT-NAMESPACE-UNAVAILABLE"
)

func TestMountsInAUserNamespace(t *testing.T) {
	if os.Getenv(mountChildEnv) == "1" {
		mountChild(t)
		return
	}
	// The child never removes its mount root: a recursive delete through a
	// bind of /dev would reach the host's. The directory is removed here,
	// after the child and its private mounts are gone.
	root := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=^TestMountsInAUserNamespace$", "-test.v")
	for _, a := range os.Args[1:] {
		if strings.HasPrefix(a, "-test.gocoverdir=") {
			cmd.Args = append(cmd.Args, a) // count the child's coverage too
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
		t.Fatalf("mounts in the namespace failed:\n%s", out)
	}
}

// mountChild runs inside the namespace.
func mountChild(t *testing.T) {
	root := os.Getenv(mountRootEnv)
	if root == "" || os.Getuid() != 0 {
		t.Fatalf("not in the test namespace (root=%q uid=%d)", root, os.Getuid())
	}
	m := NewMounter(root)
	// Whatever happens below, nothing stays mounted when the test returns.
	defer m.UnmountAll()

	pl := &Plan{Parts: []Partition{
		{Device: "tmpfs", Filesystem: "tmpfs", Mountpoint: "/boot"}, // listed child-first on purpose
		{Device: "tmpfs", Filesystem: "tmpfs", Mountpoint: "/"},
		{Device: "tmpfs", Filesystem: "swap"}, // no mountpoint: not mounted
	}}
	if err := m.MountAll(pl); err != nil {
		if errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.EACCES) {
			t.Log(mountSkipMarker)
			return
		}
		t.Fatalf("MountAll: %v", err)
	}
	// Root first, then what is beneath it — or /boot would be hidden under
	// the root mounted over it.
	if got := strings.Join(m.mounted, ","); got != root+","+filepath.Join(root, "boot") {
		t.Fatalf("mounted = %s", got)
	}
	dev := func(p string) uint64 {
		var st syscall.Stat_t
		if err := syscall.Stat(p, &st); err != nil {
			t.Fatalf("stat %s: %v", p, err)
		}
		return uint64(st.Dev)
	}
	if dev(root) == dev(filepath.Dir(root)) || dev(filepath.Join(root, "boot")) == dev(root) {
		t.Error("the target and /boot are not separate mounts")
	}
	if err := os.WriteFile(filepath.Join(root, "marker"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Subvolumes: the filesystem is mounted on its own, `btrfs subvolume
	// create` runs once per subvolume inside it, and it is unmounted again.
	// (tmpfs stands in for btrfs; the command is recorded, not run.)
	var made []string
	var during uint64
	svPlan := &Plan{Parts: []Partition{{Device: "tmpfs", Filesystem: "tmpfs", Mountpoint: "/",
		Subvolumes: []Subvolume{{"@", "/"}, {"@home", "/home"}}}}}
	err := CreateSubvolumes(svPlan, func(args []string) error {
		made = append(made, strings.Join(args[:3], " ")+" "+filepath.Base(args[3]))
		during = dev(filepath.Dir(args[3]))
		return nil
	})
	if err != nil {
		t.Fatalf("CreateSubvolumes: %v", err)
	}
	if strings.Join(made, ";") != "btrfs subvolume create @;btrfs subvolume create @home" {
		t.Errorf("subvolume commands = %v", made)
	}
	if during == dev(os.TempDir()) {
		t.Error("the subvolumes were made outside the filesystem")
	}
	// A failing command stops it, and the filesystem is still unmounted.
	boom := errors.New("btrfs: no space")
	var top string
	err = CreateSubvolumes(svPlan, func(args []string) error { top = filepath.Dir(args[3]); return boom })
	if !errors.Is(err, boom) {
		t.Errorf("CreateSubvolumes error = %v", err)
	}
	if _, statErr := os.Stat(top); statErr == nil {
		t.Errorf("the temporary mount %s was left behind", top)
	}

	// What a staged install leaves mounted is found from the mount table and
	// unmounted, deepest first, without the list the installer kept.
	tree := filepath.Join(os.TempDir(), "unmount-tree")
	os.MkdirAll(filepath.Join(tree, "boot"), 0o755)
	defer os.Remove(tree)
	if err := syscall.Mount("tmpfs", tree, "tmpfs", 0, ""); err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(filepath.Join(tree, "boot"), 0o755)
	if err := syscall.Mount("tmpfs", filepath.Join(tree, "boot"), "tmpfs", 0, ""); err != nil {
		t.Fatal(err)
	}
	gone, uerr := UnmountTree(tree)
	if uerr != nil || strings.Join(gone, ",") != filepath.Join(tree, "boot")+","+tree {
		t.Errorf("UnmountTree = %v, %v", gone, uerr)
	}
	if dev(tree) != dev(filepath.Dir(tree)) {
		t.Error("the tree is still mounted")
	}

	if err := m.MountMeta(); err != nil {
		t.Fatalf("MountMeta: %v", err)
	}
	// The live kernel filesystems are there for chrooted commands …
	for _, p := range []string{"proc/self/status", "sys/kernel", "dev/null"} {
		if _, err := os.Stat(filepath.Join(root, p)); err != nil {
			t.Errorf("%s missing in the target: %v", p, err)
		}
	}
	// … with their submounts: the bind is recursive, which is what brings
	// efivarfs along for grub-install on a real machine.
	for _, sub := range []string{"dev/pts", "dev/shm"} {
		if dev("/"+sub) != dev("/dev") && dev(filepath.Join(root, sub)) == dev(filepath.Join(root, "dev")) {
			t.Errorf("submount /%s was not carried into the target", sub)
		}
	}
	if len(m.mounted) != 5 {
		t.Errorf("mounted %d things, want 5: %v", len(m.mounted), m.mounted)
	}

	if err := m.UnmountAll(); err != nil {
		t.Fatalf("UnmountAll: %v", err)
	}
	// Everything is gone: the tmpfs with its marker, and the bound /proc.
	if _, err := os.Stat(filepath.Join(root, "marker")); err == nil {
		t.Error("the target is still mounted after UnmountAll")
	}
	if _, err := os.Stat(filepath.Join(root, "proc", "self")); err == nil {
		t.Error("/proc is still bound after UnmountAll")
	}
	if len(m.mounted) != 0 {
		t.Errorf("mounted list not cleared: %v", m.mounted)
	}
}

func TestMountFailuresAreReported(t *testing.T) {
	// A device that does not exist: the error names what could not be
	// mounted where, and nothing is recorded as mounted.
	root := t.TempDir()
	m := NewMounter(root)
	err := m.MountAll(&Plan{Parts: []Partition{{Device: "/dev/slinstaller-no-such-device", Filesystem: "ext4", Mountpoint: "/"}}})
	if err == nil || !strings.Contains(err.Error(), "/dev/slinstaller-no-such-device") || !strings.Contains(err.Error(), root) {
		t.Errorf("MountAll error = %v", err)
	}
	if len(m.mounted) != 0 {
		t.Errorf("a failed mount was recorded: %v", m.mounted)
	}

	// The target directory cannot be made (a file is in the way).
	file := filepath.Join(t.TempDir(), "file")
	os.WriteFile(file, nil, 0o644)
	blocked := NewMounter(file)
	if err := blocked.MountAll(&Plan{Parts: []Partition{{Device: "tmpfs", Filesystem: "tmpfs", Mountpoint: "/boot"}}}); err == nil || !strings.Contains(err.Error(), "mkdir") {
		t.Errorf("MountAll under a file: %v", err)
	}
	if err := blocked.MountMeta(); err == nil || !strings.Contains(err.Error(), "mkdir") {
		t.Errorf("MountMeta under a file: %v", err)
	}

	// Unmount reports every mount it could not release, then forgets them
	// so a second call does not repeat the failures.
	stale := &Mounter{mounted: []string{"/slinstaller/not/mounted/a", "/slinstaller/not/mounted/b"}}
	err = stale.UnmountAll()
	if err == nil || !strings.Contains(err.Error(), "not/mounted/a") || !strings.Contains(err.Error(), "not/mounted/b") {
		t.Errorf("UnmountAll error = %v", err)
	}
	if err := stale.UnmountAll(); err != nil {
		t.Errorf("second UnmountAll = %v", err)
	}
}

func TestMountMetaWithoutPrivilege(t *testing.T) {
	if os.Geteuid() == 0 {
		// As root the binds would succeed, onto the host's /proc, /sys and
		// /dev; the namespace test covers the success path safely.
		t.Skip("needs an unprivileged user")
	}
	m := NewMounter(t.TempDir())
	err := m.MountMeta()
	if err == nil || !strings.Contains(err.Error(), "bind /proc") {
		t.Errorf("MountMeta without privilege = %v", err)
	}
	if len(m.mounted) != 0 {
		t.Errorf("a failed bind was recorded: %v", m.mounted)
	}
}
