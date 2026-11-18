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
	"crypto/rand"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/sunlightlinux/slinstaller/pkg/blockdev"
	"github.com/sunlightlinux/slinstaller/pkg/config"
	"github.com/sunlightlinux/slinstaller/pkg/grub"
	"github.com/sunlightlinux/slinstaller/pkg/luks"
	"github.com/sunlightlinux/slinstaller/pkg/lvm"
	"github.com/sunlightlinux/slinstaller/pkg/mdadm"
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

	// LocalRepos are xbps repositories on the live medium, searched ahead
	// of Repo — where slinit and slinit-services come from.
	LocalRepos []string

	// ExtraRepos are additional remote xbps repositories searched ahead of
	// Repo — the Sunlight mirror layered over Void's.
	ExtraRepos []string

	// Passphrase unlocks the LUKS partitions. Required when the config
	// has any encrypted partition; ignored otherwise.
	Passphrase string

	// Through names the last stage to run, inclusive. Empty runs the
	// whole install; a stage name stops after it, for a staged VM run.
	Through string

	// ResumeFrom continues an install that failed, without partitioning
	// again: the pre-install hooks and the partition stage are skipped, what
	// was made is mounted once more, and the install carries on from the
	// named stage (base-install or any later one), which is run again from
	// its start. It is for the same boot of the live system, in which the
	// encrypted volumes, arrays and volume groups made the first time are
	// still open. Empty means a normal install.
	ResumeFrom string

	// Log, if set, receives a line announcing each stage as it starts, so
	// an operator can see progress. nil is silent.
	Log io.Writer

	// ConfigDir is the directory of the config file the install came from,
	// given to hooks as $yamlDir (clr-installer's), so a hook can reach
	// files kept beside its config. Empty for an interactive install.
	ConfigDir string

	// Devices lists the machine's block devices, for releasing what the
	// live system runs on a disk before it is repartitioned. nil uses
	// blockdev.List; tests substitute their own.
	Devices func() ([]blockdev.Device, error)

	// LogFile, if set, is a file on the live system holding the install's
	// log. A full run copies it to /root/slinstaller.log on the target
	// before unmounting, as clr-installer archives its log into the
	// installed system; failing to copy it is reported to Log, not fatal.
	LogFile string

	// Output, if set, receives the output of sub-commands that cannot go
	// through Runner because they need stdin — namely cryptsetup, which is
	// fed the LUKS passphrase. A full-screen front-end points this at its
	// own view so the output does not leak onto the terminal; nil sends it
	// to stderr.
	Output io.Writer

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

	if opts.ResumeFrom != "" {
		ok := false
		for _, s := range ResumableStages() {
			ok = ok || s == opts.ResumeFrom
		}
		if !ok {
			return fmt.Errorf("controller: an install cannot be resumed at %q; it can at: %s",
				opts.ResumeFrom, strings.Join(ResumableStages(), ", "))
		}
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

	// resolvCopy is the live system's resolv.conf copied into the target
	// for install-time DNS, removed again when the install completes.
	resolvCopy string

	// keyfile is the initramfs LUKS keyfile path, set by the crypttab stage
	// when /boot is encrypted and read by the bootloader stage; empty means
	// the passphrase is entered at boot.
	keyfile string
}

type stage struct {
	name string
	run  func() error
}

// stages is the whole install, in order.
func (in *installer) stages() []stage {
	all := in.allStages()
	if in.opts.ResumeFrom == "" {
		return all
	}
	// Nothing is partitioned or formatted again: the target is mounted as it
	// was left, and the install goes on from the stage asked for.
	for i := resumeFirst; i < len(all); i++ {
		if all[i].name == in.opts.ResumeFrom {
			return append(all[2:resumeFirst:resumeFirst], all[i:]...)
		}
	}
	return all[2:] // a name Install refused; kept safe all the same
}

// resumeFirst is the index of the first stage an install can be resumed
// at: base-install. The two before it (mount, meta-fs) are always run again.
const resumeFirst = 4

// ResumableStages are the stages --resume-from accepts, in order.
func ResumableStages() []string { return StageNames()[resumeFirst:] }

func (in *installer) allStages() []stage {
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

// StageNames lists the install's stages in order, for a front-end that
// shows them all before they run.
func StageNames() []string {
	var in installer
	var names []string
	for _, s := range in.allStages() {
		names = append(names, s.name)
	}
	return names
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
		// The config's own commands for after this stage, if any.
		if err := in.runHooks(in.cfg.StageHooks[s.name]); err != nil {
			_ = in.mnt.UnmountAll()
			return fmt.Errorf("controller: hooks after %s: %w", s.name, err)
		}
	}
	if in.opts.Progress != nil {
		in.opts.Progress(limit, limit, "complete")
	}

	// A full run unmounts the finished target; a staged run leaves it
	// mounted so the result can be inspected.
	if in.opts.Through == "" {
		return in.finish()
	}
	return nil
}

// finish ends a full run: it takes the install-time resolv.conf back out of
// the target, saves the install log there, and unmounts everything.
func (in *installer) finish() error {
	// The live system's resolv.conf must not outlive the install. Left
	// in place it pins the installed system to the live medium's DNS:
	// dhcpcd hands DNS to resolvconf, and resolvconf will not replace a
	// resolv.conf it did not write ("signature mismatch"). Removed, it
	// is written fresh at first boot.
	if err := removeIfPresent(in.resolvCopy); err != nil {
		return err
	}
	if err := in.archiveLog(); err != nil && in.opts.Log != nil {
		fmt.Fprintf(in.opts.Log, "warning: the install log was not saved on the target: %v\n", err)
	}
	return in.mnt.UnmountAll()
}

func (in *installer) partition() error {
	// A disk about to be repartitioned is released first: arrays and
	// volumes the live system brought up from an earlier install are
	// stopped, and old signatures erased (blockdev.Teardown).
	if err := in.releaseDisks(); err != nil {
		return err
	}
	// A disk asked to be erased whole is, now that nothing holds it.
	if err := storage.RunCommands(in.plan.WipeCommands(), in.opts.Runner); err != nil {
		return err
	}
	// An existing partition asked to give up space does so now, and a new
	// partition waiting for that space is given its place.
	if len(in.plan.ResizeCommands()) > 0 {
		if err := in.plan.Resize(in.opts.Runner); err != nil {
			return err
		}
		in.settle()
	}
	if err := in.plan.PlaceInFree(); err != nil {
		return err
	}
	// The partition table first; then, on an encrypted disk, LUKS on each
	// crypt partition before any filesystem is made — mkfs targets the
	// mapper, not the raw partition.
	if err := storage.RunCommands(in.plan.TableCommands(), in.opts.Runner); err != nil {
		return err
	}
	// Wait for udev to create the new partition device nodes before anything
	// opens them. Right after partprobe a device may not exist yet — e.g.
	// mdadm opening /dev/vdb1 the instant it was partitioned — so settle
	// before LUKS, RAID and LVM, which open partitions by path.
	in.settle()

	// The LUKS volume GRUB must open itself (an encrypted /boot) is formatted
	// with a GRUB-readable key derivation; the rest keep cryptsetup's
	// stronger argon2id default.
	grubBoot := in.bootCryptDevice()
	// encrypt sets up LUKS on the partitions (onArrays false) or on the
	// arrays (true): an array has to exist before it can be encrypted, and
	// a partition has to be encrypted before it joins anything above it.
	encrypt := func(onArrays bool) error {
		for _, p := range in.plan.Parts {
			if !p.Encrypted || p.Raid != onArrays {
				continue
			}
			if in.opts.Passphrase == "" {
				return fmt.Errorf("%s is encrypted but no passphrase was given", p.Device)
			}
			if err := luks.Format(p.Device, in.opts.Passphrase, in.output(), p.Device == grubBoot); err != nil {
				return err
			}
			if err := luks.Open(p.Device, p.MapName, in.opts.Passphrase, in.output()); err != nil {
				return err
			}
			in.settle()
		}
		return nil
	}
	if err := encrypt(false); err != nil {
		return err
	}

	// RAID, on top of the (partitioned, possibly encrypted) members, so the
	// array device exists before its filesystem is made.
	if mdadm.Used(in.plan) {
		if err := storage.RunCommands(mdadm.Commands(in.plan), in.opts.Runner); err != nil {
			return err
		}
		in.settle()
	}
	if err := encrypt(true); err != nil {
		return err
	}

	// LVM, on top of the (possibly encrypted) physical volumes, before
	// the filesystems are made on the logical volumes.
	if lvm.Used(in.plan) {
		if err := storage.RunCommands(lvm.Commands(in.plan), in.opts.Runner); err != nil {
			return err
		}
		in.settle()
	}

	mkfs, err := in.plan.MkfsCommands()
	if err != nil {
		return err
	}
	return storage.RunCommands(mkfs, in.opts.Runner)
}

// output is where sub-command output that cannot go through Runner (the
// stdin-fed cryptsetup) is written: the configured Output, or stderr.
func (in *installer) output() io.Writer {
	if in.opts.Output != nil {
		return in.opts.Output
	}
	return os.Stderr
}

// settle waits for udev to finish creating device nodes, so a device that
// was just partitioned or assembled exists before the next step opens it.
// Best-effort: a live system without udevadm just skips it (and relies on
// the kernel having created the nodes synchronously).
// releaseDisks runs blockdev.Teardown on each disk a wiping install
// repartitions. A reuse install keeps the table, and what is on it, as is.
func (in *installer) releaseDisks() error {
	if !in.plan.Wipe {
		return nil
	}
	list := in.opts.Devices
	if list == nil {
		list = blockdev.List
	}
	devs, err := list()
	if err != nil {
		return err
	}
	for _, disk := range in.plan.DiskList() {
		d := blockdev.FindByPath(devs, disk)
		if d == nil {
			continue // a loop device or image the listing does not show
		}
		if err := storage.RunCommands(blockdev.Teardown(*d), in.opts.Runner); err != nil {
			return fmt.Errorf("release %s: %w", disk, err)
		}
	}
	in.settle()
	return nil
}

func (in *installer) settle() {
	if _, err := exec.LookPath("udevadm"); err != nil {
		return
	}
	_ = in.opts.Runner([]string{"udevadm", "settle"})
}

func (in *installer) mount() error {
	// Resuming, the subvolumes are there from the first attempt.
	if in.opts.ResumeFrom == "" {
		if err := storage.CreateSubvolumes(in.plan, in.opts.Runner); err != nil {
			return err
		}
	}
	if err := in.mnt.MountAll(in.plan); err != nil {
		return err
	}
	// Resuming past the stage that made it, the keyfile the first attempt
	// put in the target is the one the bootloader stage has to point at.
	if in.opts.ResumeFrom != "" && exists(filepath.Join(in.opts.Root, keyfilePath)) {
		in.keyfile = keyfilePath
	}
	return nil
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// hasAccount reports whether the target's passwd already has login.
func hasAccount(root, login string) bool {
	data, err := os.ReadFile(filepath.Join(root, "etc", "passwd"))
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, login+":") {
			return true
		}
	}
	return false
}

// holds reports whether the file at path already contains text — what a
// stage run a second time, on resuming, has already written there.
func holds(path, text string) bool {
	// Only an ordinary file is read: a path that leads to a device has no
	// end to read to.
	if fi, err := os.Stat(path); err != nil || !fi.Mode().IsRegular() {
		return false
	}
	data, err := os.ReadFile(path)
	return err == nil && text != "" && strings.Contains(string(data), text)
}

func (in *installer) mountMeta() error {
	if err := in.mnt.MountMeta(); err != nil {
		return err
	}
	// Give chrooted commands (and hooks) working DNS during the install.
	// It is only for install time: run removes it once the install is
	// done (see resolvCopy). A missing host file is not fatal.
	if data, err := os.ReadFile("/etc/resolv.conf"); err == nil {
		dst := filepath.Join(in.opts.Root, "etc", "resolv.conf")
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return fmt.Errorf("mkdir for resolv.conf: %w", err)
		}
		if err := os.WriteFile(dst, data, 0o644); err != nil {
			return fmt.Errorf("copy resolv.conf: %w", err)
		}
		in.resolvCopy = dst
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
	if in.cfg.Image != "" {
		return in.unpackImage()
	}
	install := func(packages []string) error {
		cmds, err := xbps.Options{
			Root:       in.opts.Root,
			Repo:       in.opts.Repo,
			LocalRepos: in.opts.LocalRepos,
			ExtraRepos: in.opts.ExtraRepos,
			KeysDir:    in.opts.KeysDir,
			Arch:       in.opts.Arch,
			ProxyEnv:   in.cfg.ProxyEnv(),
			Packages:   packages,
		}.Commands()
		if err != nil {
			return err
		}
		return storage.RunCommands(cmds, in.opts.Runner)
	}
	err := install(in.opts.Packages)
	// The configured kernel could not be installed and another was named:
	// one more attempt, with that one in its place. (A failure that had
	// nothing to do with the kernel fails again, and is what is reported.)
	if kernel, fallback := in.cfg.KernelPackage(), in.cfg.KernelFallback; err != nil && fallback != "" && kernel != "" {
		var packages []string
		swapped := false
		for _, p := range in.opts.Packages {
			if p == kernel {
				p, swapped = fallback, true
			}
			packages = append(packages, p)
		}
		if swapped {
			if in.opts.Log != nil {
				fmt.Fprintf(in.opts.Log, "warning: the base install failed with %s (%v); trying once more with %s\n", kernel, err, fallback)
			}
			err = install(packages)
		}
	}
	if err != nil {
		return err
	}
	return in.writeTargetRepos()
}

// ImageCommand is the command that unpacks a root filesystem image into
// root: unsquashfs for a squashfs, tar for anything else (tar works out the
// compression itself). Ownership is kept by number, as the image's users
// are the installed system's, not the live one's.
//
// An image named by a URL is unpacked as it downloads, so it never has to
// fit anywhere but the target. tar cannot tell a stream's compression for
// itself, so it is told from the name. The URL and the root reach the shell
// as arguments, never as part of its command line.
func ImageCommand(image, root string) []string {
	ext := strings.ToLower(filepath.Ext(image))
	if config.IsRemote(image) {
		flag := ""
		switch ext {
		case ".zst":
			flag = "--zstd "
		case ".xz", ".txz":
			flag = "-J "
		case ".gz", ".tgz":
			flag = "-z "
		case ".bz2":
			flag = "-j "
		}
		return []string{"sh", "-c", `curl -fsSL "$1" | tar -xpf - ` + flag + `-C "$2" --numeric-owner`, "sh", image, root}
	}
	switch ext {
	case ".squashfs", ".sqfs", ".sfs":
		return []string{"unsquashfs", "-f", "-d", root, image}
	}
	return []string{"tar", "-xpf", image, "-C", root, "--numeric-owner"}
}

// ImageTools are the programs ImageCommand's command needs on the live
// system.
func ImageTools(image string) []string {
	if config.IsRemote(image) {
		return []string{"sh", "curl", "tar"}
	}
	return []string{ImageCommand(image, "/")[0]}
}

// unpackImage installs the base system from the config's image. What was
// unpacked has to be able to boot: without GRUB in it the install would go
// on to fail at the bootloader, so that is checked here, where the reason
// is still plain.
func (in *installer) unpackImage() error {
	image := in.cfg.Image
	if !config.IsRemote(image) && !filepath.IsAbs(image) && in.opts.ConfigDir != "" {
		image = filepath.Join(in.opts.ConfigDir, image)
	}
	if err := storage.RunCommands([][]string{ImageCommand(image, in.opts.Root)}, in.opts.Runner); err != nil {
		return err
	}
	for _, dir := range []string{"usr/bin", "usr/sbin", "bin", "sbin"} {
		if _, err := os.Stat(filepath.Join(in.opts.Root, dir, "grub-install")); err == nil {
			return in.writeTargetRepos()
		}
	}
	return fmt.Errorf("image %s holds no grub-install: it is not a system this installer can make bootable", image)
}

// writeTargetRepos records the Sunlight mirror (ExtraRepos) in the target's
// /etc/xbps.d so the installed system updates FROM Sunlight, not only Void.
// xbps.Commands passes the mirror as -R for this install and seeds its key
// into the target, but leaves no persistent repo config; a later
// `xbps-install -Su` then saw only Void and pulled Void versions — which
// skewed dbus against dbus-libs on the first installs. This closes that gap.
// (It does not stop a package Void ships at a HIGHER version from winning;
// keeping the Sunlight fork current is the separate half of that.)
func (in *installer) writeTargetRepos() error {
	if len(in.opts.ExtraRepos) == 0 {
		return nil
	}
	dir := filepath.Join(in.opts.Root, "etc", "xbps.d")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}
	content := "# Written by slinstaller: the Sunlight mirror, searched ahead of Void.\n"
	for _, r := range in.opts.ExtraRepos {
		content += fmt.Sprintf("repository=%s\n", r)
	}
	path := filepath.Join(dir, "10-sunlight.conf")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
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

// crypttab arranges the LUKS unlocks so the whole install takes one
// passphrase, splitting the work between the two things that unlock at
// boot: the dracut initramfs and Void's runit crypttab service.
//
//   - The ROOT volume is unlocked by the initramfs (rd.luks.uuid, set in the
//     bootloader stage). dracut asks for the passphrase — unless /boot is
//     encrypted, in which case GRUB already asked and a keyfile embedded in
//     the (encrypted) initramfs unlocks it instead.
//   - The NON-root volumes (a crypt /home, /data, …) are unlocked after root
//     is mounted, by runit reading /etc/crypttab, with a keyfile that lives
//     on the (now-unlocked) encrypted root — so no prompt, and the keyfile is
//     never on unencrypted storage.
//
// So /etc/crypttab lists only the non-root volumes: listing root would make
// the runit service ask for its passphrase a second time (dracut already
// opened it).
func (in *installer) crypttab() error {
	var enc, nonRoot []storage.Partition
	for _, p := range in.plan.Parts {
		if !p.Encrypted {
			continue
		}
		enc = append(enc, p)
		if !in.isRootCrypt(p) {
			nonRoot = append(nonRoot, p)
		}
	}
	if len(enc) == 0 {
		return nil
	}

	// A keyfile is needed when there are non-root volumes (for the runit
	// service) or when /boot is encrypted (for the initramfs to open root
	// without re-prompting). A lone crypt root on an unencrypted /boot needs
	// none — dracut just asks once.
	if len(nonRoot) > 0 || in.bootOnEncrypted() {
		if err := in.setupKeyfile(enc); err != nil {
			return err
		}
	}

	if len(nonRoot) == 0 {
		return nil
	}
	var tab strings.Builder
	for _, p := range nonRoot {
		uuid, err := luks.UUID(p.Device)
		if err != nil {
			return err
		}
		tab.WriteString(luks.CrypttabLineWith(p.MapName, uuid, in.keyfile, p.CryptOptions))
	}
	path := filepath.Join(in.opts.Root, "etc", "crypttab")
	if err := os.WriteFile(path, []byte(tab.String()), 0o600); err != nil {
		return fmt.Errorf("write crypttab: %w", err)
	}
	return nil
}

// rootVGName is the LVM volume group that holds the root logical volume, or
// "" when root is not on LVM.
func (in *installer) rootVGName() string {
	for _, p := range in.plan.Parts {
		if p.LV && p.Mountpoint == "/" {
			return p.VGName
		}
	}
	return ""
}

// isRootCrypt reports whether an encrypted partition is on the root's path,
// so the initramfs must open it to mount root: the crypt root itself, or
// the encrypted physical volume of root's group (LVM-on-LUKS).
func (in *installer) isRootCrypt(p storage.Partition) bool {
	if !p.Encrypted {
		return false
	}
	if p.Mountpoint == "/" {
		return true
	}
	vg := in.rootVGName()
	return p.IsPV && vg != "" && p.VGName == vg
}

// keyfilePath is where the LUKS keyfile lives on the (encrypted) target
// root: the runit service reads it there to open the non-root volumes, and
// for an encrypted /boot it is also embedded in the initramfs for dracut.
const keyfilePath = "/etc/cryptkeys/slinstaller.key"

// setupKeyfile creates a random keyfile on the encrypted root and adds it as
// a second key slot to every encrypted volume, so the volumes can be opened
// without the passphrase. It sets in.keyfile (used by crypttab and, for an
// encrypted /boot, the bootloader). The keyfile is embedded in the initramfs
// only when /boot is encrypted — there the initramfs is itself on encrypted,
// GRUB-locked storage; on an unencrypted /boot it must not ride in the
// initramfs, so it stays on the root for the post-boot runit service alone.
func (in *installer) setupKeyfile(enc []storage.Partition) error {
	full := filepath.Join(in.opts.Root, keyfilePath)
	// Resuming, the first attempt's keyfile is already in the target and in
	// the volumes' key slots: a second one would only use up another slot.
	if in.opts.ResumeFrom == "" || !exists(full) {
		key := make([]byte, 2048)
		if _, err := rand.Read(key); err != nil {
			return fmt.Errorf("generate keyfile: %w", err)
		}
		if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
			return fmt.Errorf("mkdir %s: %w", filepath.Dir(full), err)
		}
		_ = os.Remove(full) // read-only once written
		if err := os.WriteFile(full, key, 0o400); err != nil {
			return fmt.Errorf("write keyfile: %w", err)
		}
		for _, p := range enc {
			if err := luks.AddKey(p.Device, full, in.opts.Passphrase, in.output()); err != nil {
				return err
			}
		}
	}

	if in.bootOnEncrypted() {
		// Embed the keyfile in the initramfs dracut builds later, so it can
		// open the root without re-prompting after GRUB.
		dir := filepath.Join(in.opts.Root, "etc", "dracut.conf.d")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("mkdir %s: %w", dir, err)
		}
		conf := "# Written by slinstaller.\ninstall_items+=\" " + keyfilePath + " \"\n"
		if err := os.WriteFile(filepath.Join(dir, "10-slinstaller-keyfile.conf"), []byte(conf), 0o644); err != nil {
			return fmt.Errorf("write dracut keyfile conf: %w", err)
		}
	}

	in.keyfile = keyfilePath
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
	if holds(path, storage.SwapFileFstab) {
		return nil
	}
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

// applyLogLevel makes loglevel= on the kernel command line reflect the
// configured console log level: any loglevel= already present (e.g. from a
// kernel profile) is dropped and loglevel=N appended, so the Kernel Log Level
// section is the single authority. A nil level leaves kargs unchanged.
func applyLogLevel(kargs []string, level *int) []string {
	if level == nil {
		return kargs
	}
	kept := kargs[:0]
	for _, a := range kargs {
		if !strings.HasPrefix(a, "loglevel=") {
			kept = append(kept, a)
		}
	}
	return append(kept, fmt.Sprintf("loglevel=%d", *level))
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
	remove := map[string]bool{}
	if in.cfg.KernelArguments != nil {
		for _, r := range in.cfg.KernelArguments.Remove {
			remove[r] = true
		}
		for _, a := range in.cfg.KernelArguments.Add {
			if !remove[a] {
				kargs = append(kargs, a)
			}
		}
	}

	// Console log level: loglevel=N is the authority. The matching
	// kernel.printk is written to sysctl.d in firstBoot.
	kargs = applyLogLevel(kargs, in.cfg.ConsoleLogLevel)

	// slinit is Sunlight's init. sunlight-base points /usr/bin/init at it
	// already; naming it on the kernel command line as well makes the
	// choice explicit and keeps it when a base from elsewhere owns
	// /usr/bin/init. A config opts out by listing it under
	// kernel-arguments: remove.
	if bootsSlinit(in.opts.Root, remove) {
		kargs = append(kargs, slinitInitArg)
	}

	// The initramfs unlocks only the ROOT LUKS volume (rd.luks.uuid) — the
	// non-root ones are opened after boot from /etc/crypttab (by
	// slinit-services' filesystems step, or runit's core services), so
	// naming them here too would make dracut prompt for them as well. A root
	// on LVM also needs its group activated — rd.lvm.lv.
	encrypted := false
	for _, p := range in.plan.Parts {
		if p.Encrypted {
			encrypted = true
			if in.isRootCrypt(p) {
				uuid, err := luks.UUID(p.Device)
				if err != nil {
					return err
				}
				kargs = append(kargs, "rd.luks.uuid="+uuid)
				// The initramfs opens the root volume, so its options go
				// where the initramfs reads them.
				if p.CryptOptions != "" {
					kargs = append(kargs, "rd.luks.options="+p.CryptOptions)
				}
			}
		}
		if p.LV && p.Mountpoint == "/" {
			kargs = append(kargs, "rd.lvm.lv="+p.VGName+"/"+p.LVName)
		}
	}
	if encrypted {
		// Drive the initramfs unlock from rd.luks.uuid alone and ignore the
		// embedded /etc/crypttab: dracut would otherwise process both and ask
		// for the passphrase twice for the root volume.
		kargs = append(kargs, "rd.luks.crypttab=0")
	}
	// When /boot is encrypted the keyfile is in the initramfs; point dracut
	// at it so it opens root without a prompt (GRUB already asked once).
	if in.keyfile != "" && in.bootOnEncrypted() {
		kargs = append(kargs, "rd.luks.key="+in.keyfile)
	}

	// A root on a RAID array must have the array assembled by the initramfs
	// before it can be mounted — rd.md.uuid points dracut at it (alongside
	// the /etc/mdadm.conf written below).
	for _, p := range in.plan.Parts {
		// … whether the root is on it directly, or on a volume group or a
		// LUKS container that is.
		if p.Raid && (p.Mountpoint == "/" || in.isRootCrypt(p) || (p.IsPV && p.VGName != "" && p.VGName == in.rootVGName())) {
			uuid, err := mdadm.UUID(storage.RaidDevice(p.RaidName))
			if err != nil {
				return err
			}
			kargs = append(kargs, "rd.md.uuid="+uuid)
		}
	}

	g := grub.Options{Root: in.opts.Root, ESPDir: esp, KernelArgs: kargs}
	if b := in.cfg.Bootloader; b != nil {
		g.Removable = b.Removable
		g.NoNVRAM = b.UpdateNVRAM != nil && !*b.UpdateNVRAM
		g.SerialTerminal = b.Terminal == "serial"
		g.ProbeOtherOS = b.ProbeOtherOS
	}

	// Extra /etc/default/grub lines, written before grub-install and
	// grub-mkconfig both read it: the name in the boot menu, the kernel
	// command line, and — when /boot
	// is on an encrypted device (no unencrypted ESP mounted at /boot) —
	// GRUB_ENABLE_CRYPTODISK=y, without which grub-install refuses to set up
	// a loader that must unlock LUKS to read the kernel.
	var grubDefault strings.Builder
	grubDefault.WriteString("\n# boot menu name set by slinstaller\n" + grub.DistributorConfig())
	if line := g.CmdlineConfig(); line != "" {
		grubDefault.WriteString("\n# kernel command line set by slinstaller\n" + line)
	}
	if lines := g.TerminalConfig(); lines != "" {
		grubDefault.WriteString("\n# boot menu options set by slinstaller\n" + lines)
	}
	if in.bootOnEncrypted() {
		grubDefault.WriteString("\n# /boot is encrypted — let GRUB unlock it\nGRUB_ENABLE_CRYPTODISK=y\n")
	}
	if path := filepath.Join(in.opts.Root, "etc", "default", "grub"); grubDefault.Len() > 0 && !holds(path, grubDefault.String()) {
		f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			return fmt.Errorf("open %s: %w", path, err)
		}
		_, werr := f.WriteString(grubDefault.String())
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
	if err := in.fillSpareESPs(esp, g.NoNVRAM); err != nil {
		return err
	}

	// On a RAID install, record the arrays in /etc/mdadm.conf so the
	// initramfs (and the booted system) assemble them by UUID, and force
	// dracut to include its mdraid module — in the chroot dracut cannot see
	// that the target boots from an array, so it would otherwise leave the
	// module out and the system would hang unable to find root. Both must be
	// written before the initramfs is rebuilt below.
	if mdadm.Used(in.plan) {
		if err := in.writeMdadmConf(); err != nil {
			return err
		}
		if err := in.forceDracutModule("mdraid"); err != nil {
			return err
		}
	}

	// Rebuild the initramfs now that /etc/crypttab, /etc/mdadm.conf and the
	// rd.luks / rd.lvm command line exist, so dracut includes the crypt,
	// lvm and mdraid modules and the system can assemble the root at boot.
	// The one built during base-install predates them.
	if encrypted || lvm.Used(in.plan) || mdadm.Used(in.plan) {
		return storage.RunCommands(
			[][]string{{"chroot", in.opts.Root, "xbps-reconfigure", "-fa"}},
			in.opts.Runner)
	}
	return nil
}

// fillSpareESPs gives each spare EFI System Partition what the firmware
// needs to boot from it: a copy of the ESP's EFI directory (GRUB, which
// finds /boot by its filesystem UUID, so the copy works from any disk), the
// loader at the removable-media path as well, and — unless the firmware's
// entries are to be left alone — a boot entry of its own. efibootmgr puts a
// new entry first in the boot order, so the spares end up ahead of the entry
// grub-install made; every one of them starts the same GRUB, so which is
// tried first makes no difference while the disks are all there.
func (in *installer) fillSpareESPs(esp string, noNVRAM bool) error {
	src := filepath.Join(in.opts.Root, esp, "EFI")
	for _, p := range in.plan.SpareESPs() {
		err := storage.WithMounted(p.Device, p.Filesystem, func(dir string) error {
			return storage.RunCommands([][]string{
				{"cp", "-a", src, dir + "/"},
				{"mkdir", "-p", filepath.Join(dir, "EFI", "BOOT")},
				{"cp", filepath.Join(src, grub.DefaultID, "grubx64.efi"), filepath.Join(dir, "EFI", "BOOT", "BOOTX64.EFI")},
			}, in.opts.Runner)
		})
		if err != nil {
			return fmt.Errorf("spare ESP %s: %w", p.Device, err)
		}
		if noNVRAM {
			continue
		}
		entry := []string{"chroot", in.opts.Root, "efibootmgr", "--create",
			"--disk", p.Disk, "--part", fmt.Sprintf("%d", p.Number),
			"--label", grub.DefaultID + " (" + filepath.Base(p.Disk) + ")",
			"--loader", `\EFI\` + grub.DefaultID + `\grubx64.efi`}
		if err := storage.RunCommands([][]string{entry}, in.opts.Runner); err != nil {
			return err
		}
	}
	return nil
}

// bootOnEncrypted reports whether /boot lives on an encrypted device, so
// GRUB itself must unlock it to read the kernel.
func (in *installer) bootOnEncrypted() bool { return in.bootCryptDevice() != "" }

// bootCryptDevice returns the raw partition of the LUKS volume GRUB must
// open to read /boot, or "" when /boot is not encrypted. /boot is on its
// own mount when one exists (e.g. an unencrypted ESP at /boot), otherwise
// it is part of the root filesystem; for LVM-on-LUKS it is the group's
// encrypted physical volume.
func (in *installer) bootCryptDevice() string {
	var boot *storage.Partition
	for i := range in.plan.Parts {
		if in.plan.Parts[i].Mountpoint == "/boot" {
			boot = &in.plan.Parts[i]
		}
	}
	if boot == nil {
		for i := range in.plan.Parts {
			if in.plan.Parts[i].Mountpoint == "/" {
				boot = &in.plan.Parts[i]
			}
		}
	}
	if boot == nil {
		return ""
	}
	if boot.Encrypted {
		return boot.Device
	}
	if boot.LV {
		for _, p := range in.plan.Parts {
			if p.IsPV && p.Encrypted && p.VGName == boot.VGName {
				return p.Device
			}
		}
	}
	return ""
}

// writeMdadmConf records the assembled arrays in the target's
// /etc/mdadm.conf, so dracut assembles them in the initramfs and the
// booted system knows them. It reads the live array details (the arrays
// were just created, so they are running) with `mdadm --detail --scan`.
func (in *installer) writeMdadmConf() error {
	out, err := exec.Command("mdadm", "--detail", "--scan").Output()
	if err != nil {
		return fmt.Errorf("mdadm --detail --scan: %w", err)
	}
	dir := filepath.Join(in.opts.Root, "etc")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}
	header := "# Written by slinstaller — the RAID arrays this system assembles at boot.\n"
	if err := os.WriteFile(filepath.Join(dir, "mdadm.conf"), append([]byte(header), out...), 0o644); err != nil {
		return fmt.Errorf("write mdadm.conf: %w", err)
	}
	return nil
}

// forceDracutModule tells dracut to include a module in the initramfs it
// builds, with a /etc/dracut.conf.d drop-in. It is for modules dracut
// cannot infer in the chroot (it inspects the live system, not the target),
// such as mdraid for a root on RAID.
func (in *installer) forceDracutModule(module string) error {
	dir := filepath.Join(in.opts.Root, "etc", "dracut.conf.d")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}
	content := fmt.Sprintf("# Written by slinstaller.\nadd_dracutmodules+=\" %s \"\n", module)
	path := filepath.Join(dir, "10-slinstaller-"+module+".conf")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
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
	// slinit reads the machine ID as PID 1 before any service runs, so it
	// has to exist before the first boot, not be made during it.
	if err := system.MachineID(root); err != nil {
		return err
	}
	if err := system.Timezone(root, in.cfg.Timezone); err != nil {
		return err
	}
	if err := system.Keymap(root, in.cfg.Keyboard); err != nil {
		return err
	}
	if in.cfg.ConsoleLogLevel != nil {
		if err := system.PrintkSysctl(root, *in.cfg.ConsoleLogLevel); err != nil {
			return err
		}
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
		// An account the first attempt already made is not made again.
		if !hasAccount(root, cu.Login) {
			if err := storage.RunCommands([][]string{u.AddCommand(root)}, in.opts.Runner); err != nil {
				return err
			}
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
	// wheel sudo (with a password). With an administrator to reach it
	// through, root's own password login is locked, as clr-installer does.
	if anyAdmin {
		if err := system.WheelSudo(root); err != nil {
			return err
		}
		if err := storage.RunCommands(system.LockRootCommands(root, time.Now()), in.opts.Runner); err != nil {
			return err
		}
	}
	return in.writeFiles()
}

// writeFiles writes the config's write-files into the target. A path is
// taken inside the target whatever links it crosses: the directory it lands
// in must itself be in the target, and a link at the file's own name is
// replaced, not followed — an installed /etc/resolv.conf is often a link to
// somewhere that, from here, is the live system.
func (in *installer) writeFiles() error {
	root, err := filepath.EvalSymlinks(in.opts.Root)
	if err != nil {
		root = in.opts.Root
	}
	for _, w := range in.cfg.WriteFiles {
		if w == nil {
			continue
		}
		mode, err := w.Mode()
		if err != nil {
			return fmt.Errorf("write-files: %s: %w", w.Path, err)
		}
		dst := filepath.Join(root, filepath.Clean("/"+w.Path))
		dir := filepath.Dir(dst)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("write-files: %w", err)
		}
		real, err := filepath.EvalSymlinks(dir)
		if err != nil {
			return fmt.Errorf("write-files: %w", err)
		}
		if real != root && !strings.HasPrefix(real, root+string(os.PathSeparator)) {
			return fmt.Errorf("write-files: %s leads outside the installed system (to %s)", w.Path, real)
		}
		if fi, err := os.Lstat(dst); err == nil && fi.Mode()&os.ModeSymlink != 0 {
			if err := os.Remove(dst); err != nil {
				return fmt.Errorf("write-files: %w", err)
			}
		}
		if err := os.WriteFile(dst, []byte(w.Content), mode.Perm()); err != nil {
			return fmt.Errorf("write-files: %w", err)
		}
		// WriteFile's mode passes through the umask and only applies to a
		// new file; set it outright, with any setuid/setgid/sticky bit.
		if err := os.Chmod(dst, mode); err != nil {
			return fmt.Errorf("write-files: %w", err)
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
	// copyNetwork: carry the live system's own network config into the
	// target, so an install done over Wi-Fi (or with a hand-edited dhcpcd
	// config) stays reachable after reboot. Best-effort — missing files are
	// not a failure — and done before the config's own stanzas are appended,
	// so an explicit networkInterfaces still takes effect.
	if in.cfg.CopyNetwork {
		if err := in.copyLiveNetwork(); err != nil {
			return err
		}
	}

	ifaces := interfacesFromConfig(in.cfg.NetworkInterfaces)
	conf, err := network.DhcpcdConf(ifaces)
	if err != nil {
		return err
	}
	// Bonds, VLANs and bridges are made at each boot by a service of their
	// own; dhcpcd then finds the new interfaces and addresses them.
	if err := system.NetLinks(in.opts.Root, network.LinkScript(ifaces)); err != nil {
		return err
	}
	if path := filepath.Join(in.opts.Root, "etc", "dhcpcd.conf"); conf != "" && !holds(path, conf) {
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

	// A slinit system brings DHCP up from slinit-services' network
	// service, which boot already depends on; runit's dhcpcd service is
	// for a runit system. It gets sshd enabled too, so a machine
	// installed for remote use (a VM, a headless box) can be reached
	// after its first boot without a trip to the console. Its daily
	// update service is installed here as well, enabled unless the config
	// turns autoUpdate off.
	if system.IsSlinitSystem(in.opts.Root) {
		if err := system.EnableSlinitService(in.opts.Root, "sshd"); err != nil {
			return err
		}
		return system.AutoUpdate(in.opts.Root, in.cfg.AutoUpdates())
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
		if ni.Bond != nil {
			iface.Bond = &network.Bond{Members: ni.Bond.Members, Mode: ni.Bond.Mode}
		}
		if ni.VLAN != nil {
			iface.VLAN = &network.VLAN{ID: ni.VLAN.ID, Link: ni.VLAN.Link}
		}
		if ni.Bridge != nil {
			iface.Bridge = &network.Bridge{Members: ni.Bridge.Members}
		}
		for _, r := range ni.Routes {
			if r != nil {
				iface.Routes = append(iface.Routes, network.Route{To: r.To, Via: r.Via})
			}
		}
		ifaces = append(ifaces, iface)
	}
	return ifaces
}

// liveNetworkPaths are the live system's network-config locations
// copyNetwork carries into the target: Wi-Fi credentials (which the
// per-interface config cannot express) and a hand-edited dhcpcd.conf. Each
// is copied only if it exists on the live system.
var liveNetworkPaths = []string{
	"/etc/wpa_supplicant",
	"/etc/dhcpcd.conf",
}

// copyLiveNetwork copies the live system's network configuration into the
// target, preserving each path's location under the target root. It is
// best-effort: a path that is absent on the live system is skipped.
func (in *installer) copyLiveNetwork() error {
	for _, src := range liveNetworkPaths {
		fi, err := os.Lstat(src)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return fmt.Errorf("copyNetwork: stat %s: %w", src, err)
		}
		dst := filepath.Join(in.opts.Root, src)
		if err := copyPath(src, dst, fi); err != nil {
			return fmt.Errorf("copyNetwork: %s: %w", src, err)
		}
	}
	return nil
}

// copyPath copies a file or a directory tree from src to dst, creating
// parent directories and preserving file permissions.
func copyPath(src, dst string, fi os.FileInfo) error {
	if fi.IsDir() {
		entries, err := os.ReadDir(src)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(dst, fi.Mode().Perm()); err != nil {
			return err
		}
		for _, e := range entries {
			cfi, err := e.Info()
			if err != nil {
				return err
			}
			if err := copyPath(filepath.Join(src, e.Name()), filepath.Join(dst, e.Name()), cfi); err != nil {
				return err
			}
		}
		return nil
	}
	// Regular files only; skip sockets, devices and dangling symlinks that
	// have no meaning in the target.
	if !fi.Mode().IsRegular() {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, fi.Mode().Perm())
}

func (in *installer) preHooks() error  { return in.runHooks(in.cfg.PreInstall) }
func (in *installer) postHooks() error { return in.runHooks(in.cfg.PostInstall) }

// runHooks runs each hook command, chrooted into the target when the
// hook asks for it, on the live system otherwise, the way clr-installer
// runs them: the hook variables ($chrootDir, $chrooted, $yamlDir and the
// config's env) are substituted into the command, in both $name and
// ${name} forms, and also set in its environment, and the command runs
// under bash as a login shell (sh where there is no bash), so a hook
// written for clr-installer, bash syntax and all, runs unchanged.
//
// One difference is kept on purpose: inside a chroot hook $chrootDir is
// "/", the target's root as the hook sees it, where clr-installer passes
// the live system's mount path, which does not exist inside the chroot.
func (in *installer) runHooks(hooks []*config.Hook) error {
	for _, h := range hooks {
		if h == nil || h.Cmd == "" {
			continue
		}
		vars := map[string]string{"yamlDir": in.opts.ConfigDir}
		for k, v := range in.cfg.Environment {
			vars[k] = v
		}
		cmd := []string{}
		if h.Chroot {
			vars["chrootDir"], vars["chrooted"] = "/", "1"
			cmd = append(cmd, "chroot", in.opts.Root)
		} else {
			vars["chrootDir"], vars["chrooted"] = in.opts.Root, "0"
		}
		// Sorted, for a deterministic command line.
		keys := make([]string, 0, len(vars))
		for k := range vars {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		cmd = append(cmd, "env")
		for _, k := range keys {
			cmd = append(cmd, k+"="+vars[k])
		}
		cmd = append(cmd, in.hookShell(h.Chroot)...)
		cmd = append(cmd, expandHookVars(vars, h.Cmd))
		if err := storage.RunCommands([][]string{cmd}, in.opts.Runner); err != nil {
			return err
		}
	}
	return nil
}

// hookShell is how a hook command is run: "bash -l -c" as clr-installer
// does, where bash exists (in the target for a chroot hook, on the live
// system otherwise), "sh -c" where it does not.
func (in *installer) hookShell(chroot bool) []string {
	has := false
	if chroot {
		for _, p := range []string{"usr/bin/bash", "bin/bash"} {
			if _, err := os.Stat(filepath.Join(in.opts.Root, p)); err == nil {
				has = true
			}
		}
	} else {
		_, err := exec.LookPath("bash")
		has = err == nil
	}
	if has {
		return []string{"bash", "-l", "-c"}
	}
	return []string{"sh", "-c"}
}

// expandHookVars substitutes the hook variables into a command, $name and
// ${name}, as clr-installer does before handing it to the shell. Longer
// names go first, so one name that begins another is not cut short.
func expandHookVars(vars map[string]string, cmd string) string {
	keys := make([]string, 0, len(vars))
	for k := range vars {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(a, b int) bool {
		if len(keys[a]) != len(keys[b]) {
			return len(keys[a]) > len(keys[b])
		}
		return keys[a] < keys[b]
	})
	for _, k := range keys {
		cmd = strings.ReplaceAll(cmd, "${"+k+"}", vars[k])
		cmd = strings.ReplaceAll(cmd, "$"+k, vars[k])
	}
	return cmd
}

func stageNames(stages []stage) string {
	names := make([]string, len(stages))
	for i, s := range stages {
		names[i] = s.name
	}
	return strings.Join(names, ", ")
}

// slinitInitArg makes slinit PID 1.
const slinitInitArg = "init=/usr/bin/slinit"

// bootsSlinit reports whether the installed system should boot slinit:
// the base install put it in the target, and the config did not remove
// the argument.
func bootsSlinit(root string, remove map[string]bool) bool {
	if remove[slinitInitArg] {
		return false
	}
	st, err := os.Stat(filepath.Join(root, "usr", "bin", "slinit"))
	return err == nil && st.Mode().IsRegular()
}

// removeIfPresent removes path; an empty path or a file already gone is
// not an error.
// archiveLog copies Options.LogFile to /root/slinstaller.log on the target,
// beside the saved config. It is private to root: it lists every command
// the install ran.
func (in *installer) archiveLog() error {
	if in.opts.LogFile == "" {
		return nil
	}
	data, err := os.ReadFile(in.opts.LogFile)
	if err != nil {
		return err
	}
	dir := filepath.Join(in.opts.Root, "root")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "slinstaller.log"), data, 0o600)
}

func removeIfPresent(path string) error {
	if path == "" {
		return nil
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove %s: %w", path, err)
	}
	return nil
}
