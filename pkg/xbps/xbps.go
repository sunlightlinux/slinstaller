// Package xbps lays the base system down into the target with xbps, the
// package tooling Sunlight Linux inherits from the Void lineage.
//
// It is the counterpart to clr-installer's swupd step: where clr runs
// swupd to install bundles into the target, slinstaller runs
// xbps-install to install packages into it, then reconfigures them in a
// chroot. The mechanism lives here; which packages and which repository
// are the caller's to supply.
package xbps

import "fmt"

// Options is a base-system install: the target tree, the repository to
// pull from, the packages to install, and optionally the architecture.
type Options struct {
	// Root is the target mount tree the system is installed into.
	Root string

	// Repo is the xbps repository URL.
	Repo string

	// Arch is XBPS_ARCH; empty means the host's architecture, which is
	// right for a normal install and wrong only when cross-installing.
	Arch string

	// Packages is the set to install — the Sunlight base plus whatever
	// the install adds.
	Packages []string
}

// Commands returns the ordered commands that install the base system:
// xbps-install from the repository into the target, then a chrooted
// xbps-reconfigure so the freshly installed packages set themselves up.
// It is pure; the controller runs the list. The chroot step needs
// /proc, /sys and /dev already bound into the target.
func (o Options) Commands() ([][]string, error) {
	if o.Root == "" {
		return nil, fmt.Errorf("xbps: no target root")
	}
	if o.Repo == "" {
		return nil, fmt.Errorf("xbps: no repository")
	}
	if len(o.Packages) == 0 {
		return nil, fmt.Errorf("xbps: no packages to install")
	}

	install := []string{}
	if o.Arch != "" {
		install = append(install, "env", "XBPS_ARCH="+o.Arch)
	}
	install = append(install, "xbps-install", "-Sy", "-R", o.Repo, "-r", o.Root)
	install = append(install, o.Packages...)

	reconfigure := []string{"chroot", o.Root, "xbps-reconfigure", "-a"}

	return [][]string{install, reconfigure}, nil
}
