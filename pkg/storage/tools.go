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
		if p.Filesystem == "" || p.Filesystem == "swap" {
			continue
		}
		mk := "mkfs." + p.Filesystem
		if !seen[mk] {
			seen[mk] = true
			tools = append(tools, mk)
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
