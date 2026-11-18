package storage

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMountsUnderOrdersTheUnmount(t *testing.T) {
	mounts := `/dev/sda3 / ext4 rw 0 0
proc /proc proc rw 0 0
/dev/sdb3 /mnt ext4 rw 0 0
/dev/sdb1 /mnt/boot vfat rw 0 0
/dev/sdb4 /mnt/home ext4 rw 0 0
/dev/sdb5 /mnt/home/my\040files ext4 rw 0 0
proc /mnt/proc proc rw 0 0
sysfs /mnt/sys sysfs rw 0 0
efivarfs /mnt/sys/firmware/efi/efivars efivarfs rw 0 0
devtmpfs /mnt/dev devtmpfs rw 0 0
devpts /mnt/dev/pts devpts rw 0 0
/dev/sdc1 /mnt2 ext4 rw 0 0
/dev/sdc2 /mntother/x ext4 rw 0 0
/dev/sdb3 /mnt ext4 rw 0 0
short
`
	got := strings.Join(mountsUnder("/mnt/", mounts), "\n")
	// The bound kernel filesystems go as wholes — efivars and pts are the
	// live system's own mounts and are never named — then the deepest
	// filesystem first, the root last. Nothing outside the target, however
	// alike its name.
	want := "/mnt/dev\n/mnt/proc\n/mnt/sys\n/mnt/home/my files\n/mnt/home\n/mnt/boot\n/mnt"
	if got != want {
		t.Errorf("unmount order:\n%s\nwant:\n%s", got, want)
	}
	if got := mountsUnder("/nothing/here", mounts); len(got) != 0 {
		t.Errorf("mounts under an unmounted root: %v", got)
	}
}

func TestUnmountTreeReportsWhatItCannotDo(t *testing.T) {
	defer func(p string) { mountsFile = p }(mountsFile)
	dir := t.TempDir()
	mountsFile = filepath.Join(dir, "absent")
	if _, err := UnmountTree("/mnt"); err == nil {
		t.Error("an unreadable mount table was not reported")
	}
	// A path the table lists but that is not a mount point cannot be
	// unmounted: said, with what did work.
	mountsFile = filepath.Join(dir, "mounts")
	os.WriteFile(mountsFile, []byte("none "+dir+"/not-mounted tmpfs rw 0 0\n"), 0o644)
	done, err := UnmountTree(dir)
	if err == nil || !strings.Contains(err.Error(), "failed to unmount") || len(done) != 0 {
		t.Errorf("UnmountTree = %v, %v", done, err)
	}
	// Nothing mounted there: nothing to do, and no error.
	os.WriteFile(mountsFile, []byte("/dev/sda1 / ext4 rw 0 0\n"), 0o644)
	if done, err := UnmountTree(dir); err != nil || len(done) != 0 {
		t.Errorf("empty tree: %v, %v", done, err)
	}
}
