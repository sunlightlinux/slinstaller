// Package xbps lays the base system down into the target with xbps, the
// package tooling Sunlight Linux inherits from the Void lineage.
//
// It is the counterpart to clr-installer's swupd step: where clr runs
// swupd to install bundles into the target, slinstaller runs
// xbps-install to install packages into it, then reconfigures them in a
// chroot. The sequence follows how Void bootstraps a rootfs (see the
// void-containers reference): seed the repository signing keys into the
// target, install from the repository, reconfigure. The mechanism lives
// here; which packages, which repository and which keys are the
// caller's to supply.
package xbps

import (
	"fmt"
	"path/filepath"
)

// Options is a base-system install: the target tree, the repository to
// pull from, the packages to install, and the signing keys and
// architecture needed to do it.
type Options struct {
	// Root is the target mount tree the system is installed into.
	Root string

	// Repo is the xbps repository URL.
	Repo string

	// KeysDir holds the repository signing keys to seed into the target
	// before installing; without them xbps cannot verify what it
	// fetches. On the live medium this is /var/db/xbps/keys. Empty skips
	// the step, for a target that already has its keys.
	KeysDir string

	// Arch is XBPS_TARGET_ARCH, for installing a target whose
	// architecture differs from the host's. Empty — the usual case —
	// installs the host's architecture.
	Arch string

	// Proxy, if set, is used for the download — the config's httpsProxy.
	Proxy string

	// Packages is the set to install — the Sunlight base plus whatever
	// the install adds.
	Packages []string
}

// Commands returns the ordered commands that install the base system:
// seed the signing keys into the target, xbps-install from the
// repository, then a chrooted xbps-reconfigure so the freshly installed
// packages set themselves up. It is pure; the controller runs the list.
// The reconfigure step needs /proc, /sys and /dev already bound into the
// target.
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

	var cmds [][]string

	// Keys first: xbps verifies signatures against the keys in the
	// target, so they must be there before anything is fetched.
	if o.KeysDir != "" {
		dest := filepath.Join(o.Root, "var", "db", "xbps", "keys")
		cmds = append(cmds,
			[]string{"mkdir", "-p", dest},
			[]string{"cp", "-a", o.KeysDir + "/.", dest + "/"},
		)
	}

	var envs []string
	if o.Arch != "" {
		envs = append(envs, "XBPS_TARGET_ARCH="+o.Arch)
	}
	if o.Proxy != "" {
		envs = append(envs, "https_proxy="+o.Proxy, "http_proxy="+o.Proxy)
	}

	install := []string{}
	if len(envs) > 0 {
		install = append(install, "env")
		install = append(install, envs...)
	}
	install = append(install, "xbps-install", "-Sy", "-R", o.Repo, "-r", o.Root)
	install = append(install, o.Packages...)
	cmds = append(cmds, install)

	cmds = append(cmds, []string{"chroot", o.Root, "xbps-reconfigure", "-fa"})

	return cmds, nil
}
