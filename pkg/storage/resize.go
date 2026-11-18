package storage

import "fmt"

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
		cmds = append(cmds,
			[]string{"e2fsck", "-f", "-p", p.Device},
			[]string{"resize2fs", p.Device, fmt.Sprintf("%dM", p.ResizeMiB)},
			[]string{"sh", "-c", fmt.Sprintf("echo ',%dMiB' | sfdisk --no-reread -N %d %s", p.ResizeMiB, p.Number, disk)},
			[]string{"partprobe", disk},
		)
	}
	return cmds
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
