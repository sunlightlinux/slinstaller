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
	extra := append(pl.ResizeCommands(), pl.WipeCommands()...)
	for _, disk := range pl.DiskList() {
		extra = append(extra, pl.entryCommands(disk, !pl.Wipe)...)
	}
	// The resize cuts the partition with sfdisk, under sh (it reads
	// stdin): sh alone was counted, and without sfdisk the filesystem was
	// shrunk before the install stopped.
	for _, p := range pl.Parts {
		if p.ResizeMiB > 0 {
			extra = append(extra, []string{"sfdisk"})
			break
		}
	}
	for _, c := range extra {
		// setsid and the program it runs (a resize's): both must be there.
		for _, name := range []string{c[0], command(c)[0]} {
			if !seen[name] {
				seen[name] = true
				tools = append(tools, name)
			}
		}
	}
	return tools
}

// MissingTools returns the names not found in PATH, keeping their order.
// It is how a front-end checks the live system before a destructive run.
func MissingTools(names []string) []string {
	var missing []string
	seen := map[string]bool{}
	for _, n := range names {
		if seen[n] {
			continue // each named once (partprobe came up twice)
		}
		seen[n] = true
		if _, err := exec.LookPath(n); err != nil {
			missing = append(missing, n)
		}
	}
	return missing
}
