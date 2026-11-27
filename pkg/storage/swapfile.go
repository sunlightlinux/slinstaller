package storage

import "path/filepath"

// SwapFileName is where a swap file is created in the installed system.
const SwapFileName = "/swapfile"

// SwapFileFstab is the fstab line that activates the swap file at boot.
const SwapFileFstab = SwapFileName + " none swap defaults 0 0\n"

// SwapFileCommands returns the commands that create a swap file of the
// given size at <root>/swapfile and make it swap: allocate it, lock down
// its mode (swap refuses a world-readable file), then mkswap. The caller
// adds SwapFileFstab to the target's fstab so it is used on boot.
//
// On btrfs (rootFS) the file must not be copy-on-write, and so not
// compressed either, or the kernel refuses it at every boot: it is made
// empty and marked No_COW (chattr +C, which only takes on an empty file)
// before it is allocated.
func SwapFileCommands(root, size, rootFS string) [][]string {
	path := filepath.Join(root, "swapfile")
	var cmds [][]string
	if rootFS == "btrfs" {
		cmds = append(cmds,
			[]string{"truncate", "-s", "0", path},
			[]string{"chattr", "+C", path})
	}
	return append(cmds,
		[]string{"fallocate", "-l", size, path},
		[]string{"chmod", "600", path},
		[]string{"mkswap", path})
}
