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
	"strings"

	"github.com/sunlightlinux/slinstaller/pkg/config"
	"github.com/sunlightlinux/slinstaller/pkg/storage"
)

// Options controls an install run.
type Options struct {
	// Root is the target mount tree (e.g. /mnt). Defaults to /mnt.
	Root string

	// Runner runs the partitioning commands. Defaults to
	// storage.DefaultRunner; tests substitute their own.
	Runner storage.Runner

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

// stages are the install steps implemented today, in order. Writing
// fstab, installing the bootloader and configuring first boot come after
// base-system install, which is not implemented yet, so they are not
// sequenced here — they join as base-system install lands.
func (in *installer) stages() []stage {
	return []stage{
		{"partition", in.partition},
		{"mount", in.mount},
		{"meta-fs", in.mountMeta},
	}
}

func (in *installer) run() error {
	stages := in.stages()

	if in.opts.Through == "" {
		return fmt.Errorf("controller: a full install also needs base-system install, "+
			"bootloader and first-boot config, which are not implemented yet; "+
			"pass Through to run the disk steps (%s) in a VM", stageNames(stages))
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

func stageNames(stages []stage) string {
	names := make([]string, len(stages))
	for i, s := range stages {
		names[i] = s.name
	}
	return strings.Join(names, ", ")
}
