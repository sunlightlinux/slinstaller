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
	"github.com/sunlightlinux/slinstaller/pkg/luks"
	"github.com/sunlightlinux/slinstaller/pkg/lvm"
	"github.com/sunlightlinux/slinstaller/pkg/network"
	"github.com/sunlightlinux/slinstaller/pkg/storage"
	"github.com/sunlightlinux/slinstaller/pkg/system"
	"github.com/sunlightlinux/slinstaller/pkg/xbps"
	"gopkg.in/yaml.v3"
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

	// Passphrase unlocks the LUKS partitions. Required when the config
	// has any encrypted partition; ignored otherwise.
	Passphrase string

	// Through names the last stage to run, inclusive. Empty runs the
	// whole install; a stage name stops after it, for a staged VM run.
	Through string

	// Log, if set, receives a line announcing each stage as it starts, so
	// an operator can see progress. nil is silent.
	Log io.Writer

	// Progress, if set, is called as each stage starts with the number of
	// stages already completed, the total to run, and the stage's name —
	// and once more with done == total and stage "complete" after the last
	// one — so a front-end can drive a progress bar. nil disables it.
	Progress func(done, total int, stage string)
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
		{"swapfile", in.swapfile},
		{"bootloader", in.bootloader},
		{"first-boot", in.firstBoot},
		{"network", in.network},
		{"save-config", in.saveConfig},
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

	for i, s := range stages[:limit] {
		if in.opts.Progress != nil {
			in.opts.Progress(i, limit, s.name)
		}
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
	if in.opts.Progress != nil {
		in.opts.Progress(limit, limit, "complete")
	}

	// A full run unmounts the finished target; a staged run leaves it
	// mounted so the result can be inspected.
	if in.opts.Through == "" {
		return in.mnt.UnmountAll()
	}
	return nil
}

func (in *installer) partition() error {
	// The partition table first; then, on an encrypted disk, LUKS on each
	// crypt partition before any filesystem is made — mkfs targets the
	// mapper, not the raw partition.
	if err := storage.RunCommands(in.plan.TableCommands(), in.opts.Runner); err != nil {
		return err
	}

	for _, p := range in.plan.Parts {
		if !p.Encrypted {
			continue
		}
		if in.opts.Passphrase == "" {
			return fmt.Errorf("%s is encrypted but no passphrase was given", p.Device)
		}
		if err := luks.Format(p.Device, in.opts.Passphrase); err != nil {
			return err
		}
		if err := luks.Open(p.Device, p.MapName, in.opts.Passphrase); err != nil {
			return err
		}
	}

	// LVM, on top of the (possibly encrypted) physical volumes, before
	// the filesystems are made on the logical volumes.
	if lvm.Used(in.plan) {
		if err := storage.RunCommands(lvm.Commands(in.plan), in.opts.Runner); err != nil {
			return err
		}
	}

	mkfs, err := in.plan.MkfsCommands()
	if err != nil {
		return err
	}
	return storage.RunCommands(mkfs, in.opts.Runner)
}

func (in *installer) mount() error { return in.mnt.MountAll(in.plan) }

func (in *installer) mountMeta() error {
	if err := in.mnt.MountMeta(); err != nil {
		return err
	}
	// Give chrooted commands (and hooks) working DNS during the install.
	// The installed system gets its own resolv.conf from dhcpcd at boot,
	// so this is only for install time; a missing host file is not fatal.
	if data, err := os.ReadFile("/etc/resolv.conf"); err == nil {
		dst := filepath.Join(in.opts.Root, "etc", "resolv.conf")
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return fmt.Errorf("mkdir for resolv.conf: %w", err)
		}
		if err := os.WriteFile(dst, data, 0o644); err != nil {
			return fmt.Errorf("copy resolv.conf: %w", err)
		}
	}
	return nil
}

// saveConfig records, on the installed system, the config it was built
// from — a replayable account, like clr-installer's saved results. It is
// re-marshalled from the loaded config, so it reflects what actually ran.
func (in *installer) saveConfig() error {
	data, err := yaml.Marshal(in.cfg)
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	dir := filepath.Join(in.opts.Root, "root")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}
	header := "# Written by slinstaller — the config this system was installed from.\n"
	path := filepath.Join(dir, "slinstaller.yaml")
	if err := os.WriteFile(path, append([]byte(header), data...), 0o600); err != nil {
		return fmt.Errorf("save config: %w", err)
	}
	return nil
}

func (in *installer) baseInstall() error {
	cmds, err := xbps.Options{
		Root:     in.opts.Root,
		Repo:     in.opts.Repo,
		KeysDir:  in.opts.KeysDir,
		Arch:     in.opts.Arch,
		Proxy:    in.cfg.HTTPSProxy,
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
	if err := storage.WriteFstab(in.opts.Root, in.plan.Fstab(uuids)); err != nil {
		return err
	}
	return in.crypttab()
}

// crypttab writes /etc/crypttab for the encrypted partitions, so the
// installed system unlocks them at boot by their LUKS UUID.
func (in *installer) crypttab() error {
	var tab strings.Builder
	for _, p := range in.plan.Parts {
		if !p.Encrypted {
			continue
		}
		uuid, err := luks.UUID(p.Device)
		if err != nil {
			return err
		}
		tab.WriteString(luks.CrypttabLine(p.MapName, uuid))
	}
	if tab.Len() == 0 {
		return nil
	}
	path := filepath.Join(in.opts.Root, "etc", "crypttab")
	if err := os.WriteFile(path, []byte(tab.String()), 0o600); err != nil {
		return fmt.Errorf("write crypttab: %w", err)
	}
	return nil
}

// swapfile creates a swap file on root and adds it to fstab, when the
// config asks for one. It runs after fstab so the entry is appended to a
// file that already exists, and after base-install so root is populated.
func (in *installer) swapfile() error {
	if in.cfg.SwapFileSize == "" {
		return nil
	}
	if err := storage.RunCommands(
		storage.SwapFileCommands(in.opts.Root, in.cfg.SwapFileSize), in.opts.Runner); err != nil {
		return err
	}

	path := filepath.Join(in.opts.Root, "etc", "fstab")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	_, werr := f.WriteString(storage.SwapFileFstab)
	cerr := f.Close()
	if werr != nil {
		return werr
	}
	return cerr
}

// bootloader installs GRUB onto the ESP and registers the firmware boot
// entry. Any kernel arguments from the config are written to
// /etc/default/grub before grub-mkconfig reads it.
func (in *installer) bootloader() error {
	esp := in.plan.ESPMountpoint()
	if esp == "" {
		return fmt.Errorf("no EFI System Partition in the plan")
	}

	// Kernel arguments: the config's additions, minus any it also asks to
	// remove. GRUB has no "remove" directive and slinstaller writes the
	// whole default command line itself, so a removed argument is simply
	// one slinstaller does not write.
	var kargs []string
	if in.cfg.KernelArguments != nil {
		remove := map[string]bool{}
		for _, r := range in.cfg.KernelArguments.Remove {
			remove[r] = true
		}
		for _, a := range in.cfg.KernelArguments.Add {
			if !remove[a] {
				kargs = append(kargs, a)
			}
		}
	}

	// An encrypted root must be unlocked by the initramfs before the
	// kernel can mount it, so the LUKS UUID on the root's path goes on the
	// command line for dracut's rd.luks to pick up. A root on LVM needs its
	// group activated the same way — rd.lvm.lv.
	//
	// The root's path may be: an encrypted root partition directly; or (for
	// LVM-on-LUKS) the encrypted physical volume of the group that holds the
	// root logical volume — found by matching the root LV's group.
	rootVG := ""
	for _, p := range in.plan.Parts {
		if p.LV && p.Mountpoint == "/" {
			rootVG = p.VGName
		}
	}

	encrypted := false
	for _, p := range in.plan.Parts {
		if p.Encrypted {
			encrypted = true
			onRootPath := p.Mountpoint == "/" || (p.IsPV && rootVG != "" && p.VGName == rootVG)
			if onRootPath {
				uuid, err := luks.UUID(p.Device)
				if err != nil {
					return err
				}
				kargs = append(kargs, "rd.luks.uuid="+uuid)
			}
		}
		if p.LV && p.Mountpoint == "/" {
			kargs = append(kargs, "rd.lvm.lv="+p.VGName+"/"+p.LVName)
		}
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
	if err := storage.RunCommands(cmds, in.opts.Runner); err != nil {
		return err
	}

	// Rebuild the initramfs now that /etc/crypttab and the rd.luks /
	// rd.lvm command line exist, so dracut includes the crypt and lvm
	// modules and the system can assemble the root at boot. The one built
	// during base-install predates them.
	if encrypted || lvm.Used(in.plan) {
		return storage.RunCommands(
			[][]string{{"chroot", in.opts.Root, "xbps-reconfigure", "-fa"}},
			in.opts.Runner)
	}
	return nil
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
			FullName:     cu.UserName,
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
	conf, err := network.DhcpcdConf(interfacesFromConfig(in.cfg.NetworkInterfaces))
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

// interfacesFromConfig maps the config's network interfaces to
// network.Interface. An interface is static when it has addresses and is
// not explicitly DHCP (config.Interface.Static); a DHCP interface produces
// no stanza and is left to dhcpcd's default. Nameless interfaces are
// skipped — Validate has already reported them.
func interfacesFromConfig(cfgIfaces []*config.Interface) []network.Interface {
	var ifaces []network.Interface
	for _, ni := range cfgIfaces {
		if ni == nil || ni.Name == "" {
			continue
		}
		iface := network.Interface{
			Name:    ni.Name,
			Static:  ni.Static(),
			Gateway: ni.Gateway,
			DNS:     ni.DNS,
			Domain:  ni.Domain,
		}
		for _, a := range ni.Addrs {
			if a == nil {
				continue
			}
			iface.Addrs = append(iface.Addrs, network.Addr{IP: a.IP, Netmask: a.Netmask, V6: a.Version == 1})
		}
		ifaces = append(ifaces, iface)
	}
	return ifaces
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
		// clr-installer expands $chrootDir and $chrooted in hook commands;
		// pass them as environment so the shell does the same. Inside the
		// chroot the target root is /; outside it is the mount tree.
		var cmd []string
		if h.Chroot {
			cmd = []string{"chroot", in.opts.Root, "env", "chrootDir=/", "chrooted=1", "sh", "-c", h.Cmd}
		} else {
			cmd = []string{"env", "chrootDir=" + in.opts.Root, "chrooted=0", "sh", "-c", h.Cmd}
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
