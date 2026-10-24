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
// This is for ext4, the default root filesystem; btrfs needs a
// no-copy-on-write file, which is a separate case if a btrfs root ever
// wants a swap file.
func SwapFileCommands(root, size string) [][]string {
	path := filepath.Join(root, "swapfile")
	return [][]string{
		{"fallocate", "-l", size, path},
		{"chmod", "600", path},
		{"mkswap", path},
	}
}
