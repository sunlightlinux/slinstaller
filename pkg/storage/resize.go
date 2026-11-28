package storage

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// Resizable reports whether a partition holding this filesystem can be
// shrunk in place.
func Resizable(fs string) bool {
	switch fs {
	case "ext2", "ext3", "ext4", "ntfs", "btrfs":
		return true
	}
	return false
}

// ResizeCommands returns the commands that shrink each partition asking for
// it, in the only safe order: the filesystem is checked, then shrunk, and
// only then is the partition cut down to it. resize2fs refuses a size the
// data does not fit in, and a failure at any step stops before the next —
// so the partition is never made smaller than the filesystem inside it.
//
// The partition is resized with sfdisk, which changes one entry and leaves
// its start, type and identifiers as they are, and takes its input on
// stdin — hence the shell.
func (pl *Plan) ResizeCommands() [][]string {
	var cmds [][]string
	// Every ntfs trial first, before any partition is changed: run in each
	// partition's turn, a hibernated Windows listed second was refused
	// after the first partition had been shrunk.
	for _, p := range pl.Parts {
		if p.ResizeMiB != 0 && p.Filesystem == "ntfs" {
			cmds = append(cmds, []string{"ntfsresize", "--no-action", "--no-progress-bar", "-s", fmt.Sprintf("%d", p.ResizeMiB<<20), p.Device})
		}
	}
	for _, p := range pl.Parts {
		if p.ResizeMiB == 0 {
			continue
		}
		disk := p.Disk
		if disk == "" {
			disk = pl.Disk
		}
		if p.Filesystem == "btrfs" {
			// btrfs is shrunk while mounted; Resize mounts it for the
			// purpose. Shown here by the device it is done on.
			cmds = append(cmds, detached([]string{"btrfs", "filesystem", "resize", fmt.Sprintf("%dM", p.ResizeMiB), "[" + p.Device + "]"}))
		} else if p.Filesystem == "ntfs" {
			// Its trial ran above. The real run asks for confirmation on
			// its input.
			bytes := fmt.Sprintf("%d", p.ResizeMiB<<20)
			cmds = append(cmds,
				detached([]string{"sh", "-c", fmt.Sprintf("echo y | ntfsresize --no-progress-bar -f -s %s %s", bytes, p.Device)}))
		} else {
			cmds = append(cmds,
				[]string{"e2fsck", "-f", "-p", p.Device},
				detached([]string{"resize2fs", p.Device, fmt.Sprintf("%dM", p.ResizeMiB)}))
		}
		cmds = append(cmds,
			detached([]string{"sh", "-c", fmt.Sprintf("echo ',%dMiB' | sfdisk --no-reread -N %d %s", p.ResizeMiB, p.Number, disk)}),
			[]string{"partprobe", disk},
		)
	}
	return cmds
}

// detached runs a command that moves a kept filesystem's data or rewrites
// its partition in a session of its own (setsid -w waits and passes its
// exit status on): a Ctrl+C on the terminal goes to the terminal's
// foreground process group, and killed resize2fs or ntfsresize part-way
// through moving the other system's data - a filesystem left corrupt,
// where the installer promises to wind down after the step.
func detached(c []string) []string {
	return append([]string{"setsid", "-w"}, c...)
}

// command is the program a ResizeCommands line runs, under setsid or not.
func command(c []string) []string {
	if len(c) > 2 && c[0] == "setsid" {
		return c[2:]
	}
	return c
}

// WithMountedFS is the filesystem type Resize mounts with; tests substitute
// one they can mount.
var WithMountedFS = func(fs string) string { return fs }

// Resize carries out ResizeCommands. They are run as they are, except that a
// btrfs filesystem is mounted on a directory of its own for its resize, as
// the tool needs.
func (pl *Plan) Resize(run Runner) error {
	for _, c := range pl.ResizeCommands() {
		if c[0] == "e2fsck" {
			if err := run(c); err != nil && !e2fsckFixed(err) {
				return fmt.Errorf("storage: %s: %w", ShowCommand(c), err)
			}
			continue
		}
		b := command(c)
		if b[0] != "btrfs" {
			if err := RunCommands([][]string{c}, run); err != nil {
				return err
			}
			continue
		}
		device := b[4][1 : len(b[4])-1]
		err := WithMounted(device, WithMountedFS("btrfs"), func(dir string) error {
			return RunCommands([][]string{detached([]string{"btrfs", "filesystem", "resize", b[3], dir})}, run)
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// e2fsckFixed reports whether an e2fsck failure is one it recovered from:
// exit 1 (errors corrected) or 2 (corrected, reboot advised). Per e2fsck(8)
// 4 and up mean errors were left. The reboot advice is for a mounted root;
// this filesystem is unmounted, so after a fix it is safe to shrink.
func e2fsckFixed(err error) bool {
	var ee *exec.ExitError
	if !errors.As(err, &ee) {
		return false
	}
	code := ee.ExitCode()
	return code == 1 || code == 2
}

// PlaceInFree gives every CreateInFree partition its place: the largest
// unallocated region of its disk as it is now — after any resize — less a
// mebibyte of slack at the end, or as much of it as the partition's size
// allows. A disk with no room, or less than was asked for, is an error.
func (pl *Plan) PlaceInFree() error {
	for i := range pl.Parts {
		p := &pl.Parts[i]
		if !p.CreateInFree {
			continue
		}
		disk := p.Disk
		if disk == "" {
			disk = pl.Disk
		}
		region, ok, err := LargestFree(disk, 2)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("storage: %s has no unallocated space for %s", disk, p.Device)
		}
		size := region.SizeMiB - 1
		if want := p.EndMiB; want > 0 {
			if want > size {
				return fmt.Errorf("storage: %s needs %d MiB but the largest free region on %s is %d MiB", p.Device, want, disk, size)
			}
			size = want
		}
		p.StartMiB, p.EndMiB, p.CreateInFree = region.StartMiB, region.StartMiB+size, false
	}
	return nil
}

// CheckCreateNumbers refuses a Safe install whose new partitions would not
// be the devices the plan formats. parted gives a new partition the lowest
// number free in the table, and nothing more of the plan follows it: the
// mkfs, the esp flag and fstab use the number the config named (or the
// child's position, for a nameless one). On a disk holding sda1..sda4, a
// partition the plan calls sda2 is cut as sda5 — and mkfs then formatted
// the existing sda2. Run before the table is touched.
func (pl *Plan) CheckCreateNumbers() error {
	for _, disk := range pl.DiskList() {
		var creates []Partition
		for _, p := range pl.physOn(disk) {
			if p.Create {
				creates = append(creates, p)
			}
		}
		if len(creates) == 0 {
			continue
		}
		out, err := partedPrintFree(disk)
		if err != nil {
			return err
		}
		// The partitions are cut as on GPT (mkpart with a name, which msdos
		// takes for primary/logical, and the lowest free number, which
		// msdos's logical partitions do not take): on another table the
		// first mkpart failed, after the resize had shrunk the other
		// system's partition.
		if table := partedTable(out); table != "gpt" {
			return fmt.Errorf("storage: %s is to be created, but %s has %s; a Safe install adds partitions only to a GPT disk - nothing was changed",
				creates[0].Device, disk, tableName(table))
		}
		parts, _ := parsePartedFree(out)
		used := map[int]bool{}
		for _, p := range parts {
			used[p.Number] = true
		}
		next := 1
		for _, p := range creates {
			for used[next] {
				next++
			}
			used[next] = true
			if p.Number != next {
				return fmt.Errorf("storage: %s is to be created, but parted will number it %d (%s)%s; name it %s in the config - nothing was changed",
					p.Device, next, PartitionName(disk, next), existsNote(parts, p.Number), PartitionName(disk, next))
			}
		}
	}
	return nil
}

// partedTable is the partition table type on parted's disk line ("gpt",
// "msdos", "loop"), or "" when the output has none.
func partedTable(out string) string {
	for _, line := range strings.Split(out, "\n") {
		f := strings.Split(strings.TrimSuffix(strings.TrimSpace(line), ";"), ":")
		if len(f) >= 6 && strings.HasPrefix(f[0], "/") {
			return f[5]
		}
	}
	return ""
}

func tableName(t string) string {
	switch t {
	case "":
		return "no partition table parted could name"
	case "loop":
		// parted's name for a disk that is one filesystem, no table.
		return "no partition table (a filesystem on the whole disk)"
	case "msdos":
		return "an msdos (MBR) partition table"
	}
	return "a " + t + " partition table"
}

// existsNote says when the number the config named is an existing
// partition — the one its mkfs would have destroyed.
func existsNote(parts []existingPart, n int) string {
	for _, p := range parts {
		if p.Number == n {
			return fmt.Sprintf(", and partition %d already exists", n)
		}
	}
	return ""
}
