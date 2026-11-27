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
	"strings"
)

// Options is a base-system install: the target tree, the repository to
// pull from, the packages to install, and the signing keys and
// architecture needed to do it.
type Options struct {
	// Root is the target mount tree the system is installed into.
	Root string

	// Repo is the xbps repository URL.
	Repo string

	// LocalRepos are repositories on the live medium, passed to
	// xbps-install ahead of Repo. They carry what Repo does not — slinit
	// and its services, which no remote repository has yet. xbps checks
	// signatures only for remote repositories, so these need no key.
	LocalRepos []string

	// ExtraRepos are additional REMOTE repositories searched ahead of Repo
	// — the Sunlight mirror layered over Void's. Like Repo they are
	// signature-checked, so their signing key must be in KeysDir. A package
	// they carry wins over Repo's whatever the versions: xbps takes a
	// package from the first repository listed that has it.
	ExtraRepos []string

	// KeysDir holds the repository signing keys to seed into the target
	// before installing; without them xbps cannot verify what it
	// fetches. On the live medium this is /var/db/xbps/keys. Empty skips
	// the step, for a target that already has its keys.
	KeysDir string

	// Arch is XBPS_TARGET_ARCH, for installing a target whose
	// architecture differs from the host's. Empty — the usual case —
	// installs the host's architecture.
	Arch string

	// ProxyEnv are the proxy settings for the download, as environment
	// assignments (https_proxy=…, http_proxy=…, no_proxy=…).
	ProxyEnv []string

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
	// A repository is needed, but a local one is enough: an offline install
	// pulls everything from the live medium's repository with no remote Repo.
	if o.Repo == "" && len(o.LocalRepos) == 0 && len(o.ExtraRepos) == 0 {
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
	envs = append(envs, o.ProxyEnv...)

	install := []string{}
	if len(envs) > 0 {
		install = append(install, "env")
		install = append(install, envs...)
	}
	install = append(install, "xbps-install", "-Sy")
	for _, r := range o.LocalRepos {
		install = append(install, "-R", r)
	}
	// Extra remote repositories (the Sunlight mirror) before the default
	// remote: xbps takes each package from the first repository that has
	// it (no bestmatching), so these override Repo for what they carry —
	// and the local repositories, listed first, override them.
	for _, r := range o.ExtraRepos {
		install = append(install, "-R", r)
	}
	// The remote repository, when there is one. Offline installs have none:
	// every -R is a local repository and xbps never reaches the network.
	if o.Repo != "" {
		install = append(install, "-R", o.Repo)
	}
	install = append(install, "-r", o.Root)
	install = append(install, o.Packages...)
	cmds = append(cmds, install)

	cmds = append(cmds, []string{"chroot", o.Root, "xbps-reconfigure", "-fa"})

	return cmds, nil
}

// ResolveCommands returns the commands that check, installing nothing, that
// the packages resolve from the repositories: the keys are seeded into
// scratch, a throwaway root; the repositories are synced into it (xbps-install
// -S with no packages syncs and stops); then xbps-install runs there as a dry
// run (-n). Two runs, because xbps does not sync under -n (bin/xbps-install
// main.c: "if (syncf && !drun)"): every remote repository would have had no
// index in the scratch root and been dropped, and a package from one
// reported "not found". A typo, a kernel no repository carries or an offline
// repository missing part of the base would otherwise fail at base-install,
// after the wipe.
func (o Options) ResolveCommands(scratch string) ([][]string, error) {
	o.Root = scratch
	cmds, err := o.Commands()
	if err != nil {
		return nil, err
	}
	cmds = cmds[:len(cmds)-1] // no reconfigure
	install := cmds[len(cmds)-1]
	cmds = cmds[:len(cmds)-1]
	var sync, dry []string
	for i, a := range install {
		switch {
		case a == "-Sy":
			sync = append(sync, "-S")
			dry = append(dry, "-n")
		case a == "-r":
			sync = append(sync, "-r", scratch) // and no packages: sync only
			dry = append(dry, install[i:]...)
			return append(cmds, sync, dry), nil
		default:
			sync = append(sync, a)
			dry = append(dry, a)
		}
	}
	return nil, fmt.Errorf("xbps: no -r in %v", install)
}

// Unresolved reports a dry run's output when it says the package set
// cannot be installed — a package not found in the repository pool, or the
// transaction aborted over unresolved dependencies, shlibs or conflicts
// (xbps prints the culprits on the lines before) — and nothing otherwise:
// any other failure (a network blip, a lock) is not a verdict on the
// package set. At most the last 20 lines.
func Unresolved(output string) []string {
	if !strings.Contains(output, "not found in repository pool") &&
		!strings.Contains(output, "Transaction aborted due to unresolved") &&
		!strings.Contains(output, "Transaction aborted due to conflicting") {
		return nil
	}
	var lines []string
	for _, l := range strings.Split(output, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			lines = append(lines, l)
		}
	}
	if len(lines) > 20 {
		lines = lines[len(lines)-20:]
	}
	return lines
}
