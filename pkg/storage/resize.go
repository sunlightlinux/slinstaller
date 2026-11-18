package storage

import "fmt"

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
			cmds = append(cmds, []string{"btrfs", "filesystem", "resize", fmt.Sprintf("%dM", p.ResizeMiB), "[" + p.Device + "]"})
		} else if p.Filesystem == "ntfs" {
			// A trial first: it changes nothing and fails if the data does
			// not fit. The real run asks for confirmation on its input.
			bytes := fmt.Sprintf("%d", p.ResizeMiB<<20)
			cmds = append(cmds,
				[]string{"ntfsresize", "--no-action", "--no-progress-bar", "-s", bytes, p.Device},
				[]string{"sh", "-c", fmt.Sprintf("echo y | ntfsresize --no-progress-bar -f -s %s %s", bytes, p.Device)})
		} else {
			cmds = append(cmds,
				[]string{"e2fsck", "-f", "-p", p.Device},
				[]string{"resize2fs", p.Device, fmt.Sprintf("%dM", p.ResizeMiB)})
		}
		cmds = append(cmds,
			[]string{"sh", "-c", fmt.Sprintf("echo ',%dMiB' | sfdisk --no-reread -N %d %s", p.ResizeMiB, p.Number, disk)},
			[]string{"partprobe", disk},
		)
	}
	return cmds
}

// WithMountedFS is the filesystem type Resize mounts with; tests substitute
// one they can mount.
var WithMountedFS = func(fs string) string { return fs }

// Resize carries out ResizeCommands. They are run as they are, except that a
// btrfs filesystem is mounted on a directory of its own for its resize, as
// the tool needs.
func (pl *Plan) Resize(run Runner) error {
	for _, c := range pl.ResizeCommands() {
		if c[0] != "btrfs" {
			if err := RunCommands([][]string{c}, run); err != nil {
				return err
			}
			continue
		}
		device := c[4][1 : len(c[4])-1]
		err := WithMounted(device, WithMountedFS("btrfs"), func(dir string) error {
			return RunCommands([][]string{{"btrfs", "filesystem", "resize", c[3], dir}}, run)
		})
		if err != nil {
			return err
		}
	}
	return nil
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
