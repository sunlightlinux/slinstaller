// Package controller sequences an installation from a config.
//
// It is the one place that knows the order of the install: run any
// pre-install hooks, partition the disk, make its filesystems, mount the
// target, bind the meta filesystems, lay down the base system, write
// fstab, install the bootloader, configure first boot, run any
// post-install hooks. Each step is a subsystem under pkg/; the
// controller holds the order and the error handling, nothing more.
//
// A run touches the disk. By default it runs every stage — a full
// install — gated by the front-end (root, and the operator confirming
// the target). Options.Through stops after a named stage instead, to
// exercise part of the sequence in a VM. A stage that fails after the
// target is mounted triggers a best-effort unmount, so a failed run does
// not leave the target half-assembled.
package controller

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/sunlightlinux/slinstaller/pkg/config"
	"github.com/sunlightlinux/slinstaller/pkg/grub"
	"github.com/sunlightlinux/slinstaller/pkg/network"
	"github.com/sunlightlinux/slinstaller/pkg/storage"
	"github.com/sunlightlinux/slinstaller/pkg/system"
	"github.com/sunlightlinux/slinstaller/pkg/xbps"
)

// Options controls an install run.
type Options struct {
	// Root is the target mount tree (e.g. /mnt). Defaults to /mnt.
	Root string

	// Runner runs the installation commands. Defaults to
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

	// Through names the last stage to run, inclusive. Empty runs the
	// whole install; a stage name stops after it, for a staged VM run.
	Through string

	// Log, if set, receives a line announcing each stage as it starts, so
	// an operator can see progress. nil is silent.
	Log io.Writer
}

// Install carries out the config, to the end or as far as
// Options.Through allows.
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

	in := &installer{cfg: cfg, plan: plan, opts: opts, mnt: storage.NewMounter(opts.Root)}
	return in.run()
}

type installer struct {
	cfg  *config.Config
	plan *storage.Plan
	opts Options
	mnt  *storage.Mounter
}

type stage struct {
	name string
	run  func() error
}

// stages is the whole install, in order.
func (in *installer) stages() []stage {
	return []stage{
		{"pre-install", in.preHooks},
		{"partition", in.partition},
		{"mount", in.mount},
		{"meta-fs", in.mountMeta},
		{"base-install", in.baseInstall},
		{"fstab", in.fstab},
		{"bootloader", in.bootloader},
		{"first-boot", in.firstBoot},
		{"network", in.network},
		{"post-install", in.postHooks},
	}
}

func (in *installer) run() error {
	stages := in.stages()

	// Where to stop: the whole list by default, or through a named stage.
	limit := len(stages)
	if in.opts.Through != "" {
		limit = -1
		for i, s := range stages {
			if s.name == in.opts.Through {
				limit = i + 1
				break
			}
		}
		if limit < 0 {
			return fmt.Errorf("controller: unknown stage %q; known: %s", in.opts.Through, stageNames(stages))
		}
	}

	for _, s := range stages[:limit] {
		if in.opts.Log != nil {
			fmt.Fprintf(in.opts.Log, "==> %s\n", s.name)
		}
		if err := s.run(); err != nil {
			// Best-effort cleanup: release whatever got mounted so a
			// failed run does not leave the target half-assembled.
			_ = in.mnt.UnmountAll()
			return fmt.Errorf("controller: %s: %w", s.name, err)
		}
	}

	// A full run unmounts the finished target; a staged run leaves it
	// mounted so the result can be inspected.
	if in.opts.Through == "" {
		return in.mnt.UnmountAll()
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
// entry. Any kernel arguments from the config are written to
// /etc/default/grub before grub-mkconfig reads it.
func (in *installer) bootloader() error {
	esp := in.plan.ESPMountpoint()
	if esp == "" {
		return fmt.Errorf("no EFI System Partition in the plan")
	}

	var kargs []string
	if in.cfg.KernelArguments != nil {
		kargs = in.cfg.KernelArguments.Add
	}
	g := grub.Options{Root: in.opts.Root, ESPDir: esp, KernelArgs: kargs}

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

// firstBoot sets the installed system's hostname, timezone, console
// keymap, locale and user accounts. Networking is its own stage.
func (in *installer) firstBoot() error {
	root := in.opts.Root
	if err := system.Hostname(root, in.cfg.Hostname); err != nil {
		return err
	}
	if err := system.Timezone(root, in.cfg.Timezone); err != nil {
		return err
	}
	if err := system.Keymap(root, in.cfg.Keyboard); err != nil {
		return err
	}
	if generated, err := system.Locale(root, in.cfg.Language); err != nil {
		return err
	} else if generated {
		// Regenerate glibc locales so the one just enabled exists.
		if err := storage.RunCommands(
			[][]string{{"chroot", root, "xbps-reconfigure", "-f", "glibc-locales"}},
			in.opts.Runner); err != nil {
			return err
		}
	}

	anyAdmin := false
	for _, cu := range in.cfg.Users {
		if cu == nil || cu.Login == "" {
			continue
		}
		if cu.Admin {
			anyAdmin = true
		}
		u := system.User{
			Login:        cu.Login,
			PasswordHash: cu.Password,
			Admin:        cu.Admin,
			SSHKeys:      cu.SSHKeys,
		}
		if err := storage.RunCommands([][]string{u.AddCommand(root)}, in.opts.Runner); err != nil {
			return err
		}
		chown, err := u.WriteSSHKeys(root)
		if err != nil {
			return err
		}
		if chown != nil {
			if err := storage.RunCommands([][]string{chown}, in.opts.Runner); err != nil {
				return err
			}
		}
	}

	// An admin account is added to wheel; this is what actually gives
	// wheel sudo (with a password).
	if anyAdmin {
		if err := system.WheelSudo(root); err != nil {
			return err
		}
	}
	return nil
}

// network configures the installed system's networking: static
// interfaces from the config become /etc/dhcpcd.conf stanzas, and dhcpcd
// is enabled so the system comes up with DHCP on the rest. dhcpcd is
// enabled even with no interfaces declared, so a plain install still has
// network.
func (in *installer) network() error {
	var ifaces []network.Interface
	for _, ni := range in.cfg.NetworkInterfaces {
		if ni == nil || ni.Name == "" {
			continue
		}
		iface := network.Interface{
			Name:    ni.Name,
			Static:  strings.EqualFold(ni.DHCP, "false") && len(ni.Addrs) > 0,
			Gateway: ni.Gateway,
			DNS:     ni.DNS,
		}
		for _, a := range ni.Addrs {
			if a == nil {
				continue
			}
			iface.Addrs = append(iface.Addrs, network.Addr{IP: a.IP, Netmask: a.Netmask, V6: a.Version == 1})
		}
		ifaces = append(ifaces, iface)
	}

	conf, err := network.DhcpcdConf(ifaces)
	if err != nil {
		return err
	}
	if conf != "" {
		path := filepath.Join(in.opts.Root, "etc", "dhcpcd.conf")
		f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			return fmt.Errorf("open %s: %w", path, err)
		}
		_, werr := f.WriteString(conf)
		cerr := f.Close()
		if werr != nil {
			return werr
		}
		if cerr != nil {
			return cerr
		}
	}

	return system.EnableService(in.opts.Root, "dhcpcd")
}

func (in *installer) preHooks() error  { return in.runHooks(in.cfg.PreInstall) }
func (in *installer) postHooks() error { return in.runHooks(in.cfg.PostInstall) }

// runHooks runs each hook command, chrooted into the target when the
// hook asks for it, on the live system otherwise.
func (in *installer) runHooks(hooks []*config.Hook) error {
	for _, h := range hooks {
		if h == nil || h.Cmd == "" {
			continue
		}
		var cmd []string
		if h.Chroot {
			cmd = []string{"chroot", in.opts.Root, "sh", "-c", h.Cmd}
		} else {
			cmd = []string{"sh", "-c", h.Cmd}
		}
		if err := storage.RunCommands([][]string{cmd}, in.opts.Runner); err != nil {
			return err
		}
	}
	return nil
}

func stageNames(stages []stage) string {
	names := make([]string, len(stages))
	for i, s := range stages {
		names[i] = s.name
	}
	return strings.Join(names, ", ")
}
