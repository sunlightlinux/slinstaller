// Package controller sequences an installation from a config.
//
// It is the one place that knows the order of the install: partition the
// disk, make its filesystems, mount the target, bind the meta
// filesystems, lay down the base system, write fstab, install the
// bootloader, configure first boot. Each step is a subsystem under
// pkg/; the controller holds the order and the error handling, nothing
// more.
//
// The order is also a safety boundary. A full install needs steps that
// are not implemented yet (base-system install, bootloader, first-boot
// config). Rather than wipe a disk and stop halfway, a run must say how
// far to go — Options.Through — and the controller refuses a full
// install until it can actually finish one.
package controller

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/sunlightlinux/slinstaller/pkg/config"
	"github.com/sunlightlinux/slinstaller/pkg/grub"
	"github.com/sunlightlinux/slinstaller/pkg/storage"
	"github.com/sunlightlinux/slinstaller/pkg/xbps"
)

// Options controls an install run.
type Options struct {
	// Root is the target mount tree (e.g. /mnt). Defaults to /mnt.
	Root string

	// Runner runs the partitioning and package commands. Defaults to
	// storage.DefaultRunner; tests substitute their own.
	Runner storage.Runner

	// Repo, KeysDir, Arch and Packages are the base-system install: the
	// xbps repository, the signing keys to seed into the target (empty =
	// the live medium's), the architecture (empty = host), and the
	// package set.
	Repo     string
	KeysDir  string
	Arch     string
	Packages []string

	// KernelArgs are added to the kernel command line via the bootloader.
	KernelArgs []string

	// Through names the last stage to run, inclusive. It is required
	// until a full install is possible, so a run cannot begin a
	// destructive install it has no way to complete.
	Through string
}

// Install carries out the config as far as Options.Through allows.
func Install(cfg *config.Config, opts Options) error {
	if opts.Root == "" {
		opts.Root = "/mnt"
	}
	if opts.Runner == nil {
		opts.Runner = storage.DefaultRunner
	}

	plan, err := cfg.Plan()
	if err != nil {
		return err
	}

	in := &installer{plan: plan, opts: opts, mnt: storage.NewMounter(opts.Root)}
	return in.run()
}

type installer struct {
	plan *storage.Plan
	opts Options
	mnt  *storage.Mounter
}

type stage struct {
	name string
	run  func() error
}

// stages are the install steps implemented today, in order. First-boot
// config comes after these and is not sequenced yet — it joins as it
// lands.
func (in *installer) stages() []stage {
	return []stage{
		{"partition", in.partition},
		{"mount", in.mount},
		{"meta-fs", in.mountMeta},
		{"base-install", in.baseInstall},
		{"fstab", in.fstab},
		{"bootloader", in.bootloader},
	}
}

func (in *installer) run() error {
	stages := in.stages()

	if in.opts.Through == "" {
		return fmt.Errorf("controller: a full install also needs first-boot config, "+
			"which is not implemented yet; "+
			"pass Through to run the available stages (%s) in a VM", stageNames(stages))
	}

	limit := -1
	for i, s := range stages {
		if s.name == in.opts.Through {
			limit = i
			break
		}
	}
	if limit < 0 {
		return fmt.Errorf("controller: unknown stage %q; known: %s", in.opts.Through, stageNames(stages))
	}

	for _, s := range stages[:limit+1] {
		if err := s.run(); err != nil {
			// Best-effort cleanup: release whatever got mounted so a
			// failed run does not leave the target half-assembled.
			_ = in.mnt.UnmountAll()
			return fmt.Errorf("controller: %s: %w", s.name, err)
		}
	}
	return nil
}

func (in *installer) partition() error {
	cmds, err := in.plan.Commands()
	if err != nil {
		return err
	}
	return storage.RunCommands(cmds, in.opts.Runner)
}

func (in *installer) mount() error     { return in.mnt.MountAll(in.plan) }
func (in *installer) mountMeta() error { return in.mnt.MountMeta() }

func (in *installer) baseInstall() error {
	cmds, err := xbps.Options{
		Root:     in.opts.Root,
		Repo:     in.opts.Repo,
		KeysDir:  in.opts.KeysDir,
		Arch:     in.opts.Arch,
		Packages: in.opts.Packages,
	}.Commands()
	if err != nil {
		return err
	}
	return storage.RunCommands(cmds, in.opts.Runner)
}

// fstab writes the target's /etc/fstab after the base system is in
// place, so the base does not overwrite it. UUIDs are read back from the
// freshly made filesystems.
func (in *installer) fstab() error {
	uuids, err := storage.ProbeUUIDs(in.plan)
	if err != nil {
		return err
	}
	return storage.WriteFstab(in.opts.Root, in.plan.Fstab(uuids))
}

// bootloader installs GRUB onto the ESP and registers the firmware boot
// entry. Any kernel arguments are written to /etc/default/grub before
// grub-mkconfig reads it.
func (in *installer) bootloader() error {
	esp := in.plan.ESPMountpoint()
	if esp == "" {
		return fmt.Errorf("no EFI System Partition in the plan")
	}

	g := grub.Options{Root: in.opts.Root, ESPDir: esp, KernelArgs: in.opts.KernelArgs}

	if line := g.CmdlineConfig(); line != "" {
		path := filepath.Join(in.opts.Root, "etc", "default", "grub")
		f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			return fmt.Errorf("open %s: %w", path, err)
		}
		_, werr := f.WriteString("\n# kernel command line set by slinstaller\n" + line)
		cerr := f.Close()
		if werr != nil {
			return werr
		}
		if cerr != nil {
			return cerr
		}
	}

	cmds, err := g.Commands()
	if err != nil {
		return err
	}
	return storage.RunCommands(cmds, in.opts.Runner)
}

func stageNames(stages []stage) string {
	names := make([]string, len(stages))
	for i, s := range stages {
		names[i] = s.name
	}
	return strings.Join(names, ", ")
}
