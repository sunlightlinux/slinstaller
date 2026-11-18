package storage

import "os/exec"

// RequiredTools lists the external commands the live system must have to
// carry out this plan's disk work: partitioning, making filesystems, and
// reading UUIDs back. Tools that run inside the target chroot
// (grub-install, xbps-reconfigure, useradd) are not here — they come from
// the base system once it is installed, so they cannot be checked up
// front.
func (pl *Plan) RequiredTools() []string {
	tools := []string{"parted", "partprobe", "mkswap", "blkid"}
	seen := map[string]bool{}
	for _, p := range pl.Parts {
		if len(p.Subvolumes) > 0 && !seen["btrfs"] {
			seen["btrfs"] = true
			tools = append(tools, "btrfs")
		}
		// No mkfs is needed for what is not made: swap has its own tool,
		// and a kept partition keeps the filesystem it has.
		if p.Filesystem == "" || p.Filesystem == "swap" || p.Preserve {
			continue
		}
		mk := "mkfs." + p.Filesystem
		if !seen[mk] {
			seen[mk] = true
			tools = append(tools, mk)
		}
	}
	for _, c := range append(pl.ResizeCommands(), pl.WipeCommands()...) {
		if !seen[c[0]] {
			seen[c[0]] = true
			tools = append(tools, c[0])
		}
	}
	return tools
}

// MissingTools returns the names not found in PATH, keeping their order.
// It is how a front-end checks the live system before a destructive run.
func MissingTools(names []string) []string {
	var missing []string
	for _, n := range names {
		if _, err := exec.LookPath(n); err != nil {
			missing = append(missing, n)
		}
	}
	return missing
}
