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
	"bytes"
	"crypto/rand"
	"errors"
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
	// its start. Encrypted volumes, arrays and volume groups the first
	// attempt made are brought back first if they are not open (after a
	// reboot of the live system; see reassemble). Empty means a normal
	// install.
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

	// Stop, when closed, asks the install to stop before its next stage.
	// The stage running finishes first — a command cut off half-way
	// (mkfs, xbps) leaves worse than one let finish — then the target is
	// unmounted and Install returns an error wrapping ErrStopped.
	Stop <-chan struct{}

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
	// resolvSaved is where the target's own resolv.conf (a file an image
	// brought, or a link to a resolver's runtime file) waits meanwhile, to
	// be put back by finish; empty when the target had none.
	resolvSaved string

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

// ErrStopped is what an install asked to stop (Options.Stop) returns.
var ErrStopped = errors.New("stopped by the operator")

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
		select {
		case <-in.opts.Stop:
			in.cleanup()
			return fmt.Errorf("controller: %w before %s", ErrStopped, s.name)
		default:
		}
		if in.opts.Progress != nil {
			in.opts.Progress(i, limit, s.name)
		}
		if in.opts.Log != nil {
			fmt.Fprintf(in.opts.Log, "==> %s\n", s.name)
		}
		if err := s.run(); err != nil {
			// Best-effort cleanup: release whatever got mounted so a
			// failed run does not leave the target half-assembled.
			in.cleanup()
			return fmt.Errorf("controller: %s: %w", s.name, err)
		}
		// The config's own commands for after this stage, if any.
		if err := in.runHooks(in.cfg.StageHooks[s.name]); err != nil {
			in.cleanup()
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

// cleanup is what a failed or stopped run does before it returns: the
// target's own resolv.conf goes back in place of the live system's copy
// (only finish did that, so a run that failed after meta-fs left the
// installed disk pinned to the live medium's DNS), and the target is
// unmounted. Nothing here hides the error the run returns; what goes
// wrong is said in the log, where it explains a re-run that then finds
// the disk busy.
func (in *installer) cleanup() {
	warn := func(format string, a ...any) {
		if in.opts.Log != nil {
			fmt.Fprintf(in.opts.Log, "warning: "+format+"\n", a...)
		}
	}
	// Only when there is a copy to take the place of: an image unpack takes
	// it out while the target's own is still aside (a failed unpack leaves
	// it there for the next attempt, which finds and keeps it).
	if err := removeIfPresent(in.resolvCopy); err != nil {
		warn("%v", err)
	} else if in.resolvSaved != "" && in.resolvCopy != "" {
		if err := os.Rename(in.resolvSaved, in.resolvCopy); err != nil {
			warn("put back the target's resolv.conf: %v", err)
		}
	}
	if err := in.mnt.UnmountAll(); err != nil {
		warn("%v", err)
	}
	if len(in.mnt.Lazy) > 0 {
		warn("still in use, detached instead of unmounted: %s", strings.Join(in.mnt.Lazy, ", "))
	}
}

// finish ends a full run: it takes the install-time resolv.conf back out of
// the target, saves the install log there, and unmounts everything.
func (in *installer) finish() error {
	// The live system's resolv.conf must not outlive the install. Left
	// in place it pins the installed system to the live medium's DNS:
	// dhcpcd hands DNS to resolvconf, and resolvconf will not replace a
	// resolv.conf it did not write ("signature mismatch"). Removed, it
	// is written fresh at first boot.
	// Failing that, the target is still unmounted before the error is
	// returned: left mounted, it held the disk and kept its data unsynced.
	resolvErr := removeIfPresent(in.resolvCopy)
	if resolvErr == nil && in.resolvSaved != "" {
		if err := os.Rename(in.resolvSaved, in.resolvCopy); err != nil {
			resolvErr = fmt.Errorf("put back the target's resolv.conf: %w", err)
		}
	}
	if resolvErr != nil {
		return errors.Join(resolvErr, in.mnt.UnmountAll())
	}
	if err := in.archiveLog(); err != nil && in.opts.Log != nil {
		fmt.Fprintf(in.opts.Log, "warning: the install log was not saved on the target: %v\n", err)
	}
	// To go straight into the installed system, its kernel is loaded now,
	// while its files can still be read. The system is installed whatever
	// comes of this, so the target is unmounted either way.
	var kexecErr error
	if in.cfg.PowerAction() == "kexec" {
		cmd, err := grub.KexecCommand(in.opts.Root)
		if err == nil {
			err = storage.RunCommands([][]string{cmd}, in.opts.Runner)
		}
		if err != nil {
			kexecErr = fmt.Errorf("the system is installed, but its kernel could not be loaded for kexec (reboot instead): %w", err)
		}
	}
	if err := in.mnt.UnmountAll(); err != nil {
		return err
	}
	if len(in.mnt.Lazy) > 0 && in.opts.Log != nil {
		fmt.Fprintf(in.opts.Log, "warning: still in use, detached instead of unmounted (synced first): %s\n", strings.Join(in.mnt.Lazy, ", "))
	}
	return kexecErr
}

func (in *installer) partition() error {
	// A Safe install's new partitions must be the devices it then formats;
	// checked before anything — a resize included — touches the disk.
	if !in.plan.Wipe {
		if err := in.plan.CheckCreateNumbers(); err != nil {
			return err
		}
	}
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
	// An array with a partition table of its own is cut now that it exists.
	if cmds := in.plan.ArrayTableCommands(); len(cmds) > 0 {
		if err := storage.RunCommands(cmds, in.opts.Runner); err != nil {
			return err
		}
		in.settle()
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

// reassemble brings back, for a resumed install, the stack the first
// attempt built under its filesystems, where it is not there any more —
// after a reboot of the live system, nothing is: encrypted partitions
// opened, arrays assembled from their members, encrypted arrays opened,
// volume groups activated, in that order. Without it a resume after a
// reboot failed at the mount with "no such file or directory".
func (in *installer) reassemble() error {
	gone := func(dev string) bool {
		_, err := os.Stat(dev)
		return err != nil
	}
	open := func(onArrays bool) error {
		for _, p := range in.plan.Parts {
			if !p.Encrypted || p.Raid != onArrays || !gone(p.EffectiveDevice()) {
				continue
			}
			if in.opts.Passphrase == "" {
				return fmt.Errorf("%s is encrypted: resuming needs its passphrase", p.Device)
			}
			if err := luks.Open(p.Device, p.MapName, in.opts.Passphrase, in.output()); err != nil {
				return err
			}
		}
		return nil
	}
	if err := open(false); err != nil {
		return err
	}
	var cmds [][]string
	for _, p := range in.plan.Parts {
		if p.Raid && gone(storage.RaidDevice(p.RaidName)) {
			members := append(append([]string{}, p.RaidMembers...), p.RaidSpares...)
			// The live system may have assembled the array by itself at
			// boot, under a name of its own (md127, <host>:<name>): its
			// members are then held, and assembling them again fails. That
			// array is stopped first, to be assembled as the install's.
			stopped := map[string]bool{}
			for _, m := range members {
				for _, h := range mdHolders(m) {
					if !stopped[h] {
						stopped[h] = true
						cmds = append(cmds, []string{"mdadm", "--stop", "/dev/" + h})
					}
				}
			}
			cmds = append(cmds, append([]string{"mdadm", "--assemble", storage.RaidDevice(p.RaidName)}, members...))
		}
	}
	if err := storage.RunCommands(cmds, in.opts.Runner); err != nil {
		return err
	}
	if len(cmds) > 0 {
		in.settle()
	}
	if err := open(true); err != nil {
		return err
	}
	cmds = nil
	seen := map[string]bool{}
	for _, p := range in.plan.Parts {
		if p.LV && !seen[p.VGName] && gone(p.EffectiveDevice()) {
			seen[p.VGName] = true
			cmds = append(cmds, []string{"vgchange", "-ay", p.VGName})
		}
	}
	if err := storage.RunCommands(cmds, in.opts.Runner); err != nil {
		return err
	}
	if len(cmds) > 0 {
		in.settle()
	}
	return nil
}

// sysBlock is sysfs's block-device directory (a variable for tests).
var sysBlock = "/sys/class/block"

// mdHolders returns the md arrays holding the device at path (sda2 →
// md127), from sysfs.
func mdHolders(path string) []string {
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		real = path
	}
	ents, err := os.ReadDir(filepath.Join(sysBlock, filepath.Base(real), "holders"))
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), "md") {
			out = append(out, e.Name())
		}
	}
	return out
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
	var disks []blockdev.Device
	var names []string
	for _, disk := range in.plan.DiskList() {
		d := blockdev.FindByPath(devs, disk)
		if d == nil {
			continue // a loop device or image the listing does not show
		}
		disks, names = append(disks, *d), append(names, disk)
	}
	// All at once: an array or volume spanning the disks is released once.
	if err := storage.RunCommands(blockdev.TeardownAll(disks), in.opts.Runner); err != nil {
		return fmt.Errorf("release %s: %w", strings.Join(names, ", "), err)
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
	} else if err := in.reassemble(); err != nil {
		return err
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

// rootDisk is the disk (or partitioned array) the root partition is cut on,
// or "" when the root is not a plain partition.
func (in *installer) rootDisk() string {
	for _, p := range in.plan.Parts {
		if p.Mountpoint == "/" && !p.LV && !p.Raid {
			return p.Disk
		}
	}
	return ""
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
	return in.copyResolv("/etc/resolv.conf")
}

// copyResolv puts the live system's resolv.conf (src) in the target for
// the install, setting the target's own aside.
func (in *installer) copyResolv(src string) error {
	if data, err := os.ReadFile(src); err == nil {
		dst := filepath.Join(in.opts.Root, "etc", "resolv.conf")
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return fmt.Errorf("mkdir for resolv.conf: %w", err)
		}
		// What the target has there is its own: moved aside, not written
		// through (a link to /run/... landed on the LIVE system's file) or
		// over (an image's resolv.conf was overwritten, then deleted).
		// A resumed install finds it already aside and keeps it.
		saved := dst + ".slinstaller-saved"
		if _, err := os.Lstat(saved); err == nil {
			in.resolvSaved = saved
			if err := removeIfPresent(dst); err != nil {
				return err
			}
		} else if fi, err := os.Lstat(dst); err == nil && !in.isOurResolv(fi, dst, data) {
			if err := os.Rename(dst, saved); err != nil {
				return fmt.Errorf("set aside the target's resolv.conf: %w", err)
			}
			in.resolvSaved = saved
		}
		if err := os.WriteFile(dst, append([]byte(resolvMark), data...), 0o644); err != nil {
			return fmt.Errorf("copy resolv.conf: %w", err)
		}
		in.resolvCopy = dst
	}
	return nil
}

// resolvMark heads the install-time copy of resolv.conf, so a resumed
// install knows the copy a failed attempt left for its own. Its contents
// cannot tell: the live file may have changed since (the DNS the first
// attempt failed on, fixed), and the stale copy was then set aside as
// the target's own and put back into the installed system.
const resolvMark = "# slinstaller: the live system's resolv.conf, for the install only\n"

// isOurResolv reports whether the file at dst is a copy this installer
// left there (a resumed install's first attempt never got to finish).
func (in *installer) isOurResolv(fi os.FileInfo, dst string, live []byte) bool {
	if !fi.Mode().IsRegular() {
		return false
	}
	got, err := os.ReadFile(dst)
	// Marked, or — left by an installer from before the mark — exactly the
	// live file.
	return err == nil && (bytes.HasPrefix(got, []byte(resolvMark)) || bytes.Equal(got, live))
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
func ImageCommand(image, root string, allowInsecureHTTP bool) []string {
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
		return []string{"sh", "-c", config.CurlFetch(image, allowInsecureHTTP) + ` | tar -xpf - ` + flag + `-C "$2" --numeric-owner`, "sh", image, root}
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
	return []string{ImageCommand(image, "/", false)[0]}
}

// forgetImageIdentity removes what makes the system the image was taken
// from that one machine: its machine-id, the random seed it saved and its
// SSH host keys. Kept, every machine installed from one image shared them
// — one machine-id for slinit's journal and logind, one seed for every
// first boot's RNG, one host key, so one machine could pass for another.
// The first-boot stage writes a machine-id and a seed of this machine's
// own; the host keys are made anew here when the image has ssh-keygen.
func (in *installer) forgetImageIdentity() error {
	root := in.opts.Root
	files := []string{"etc/machine-id", "var/lib/dbus/machine-id",
		"var/lib/seedrng/seed.credit", "var/lib/seedrng/seed.no-credit"}
	keys, _ := filepath.Glob(filepath.Join(root, "etc", "ssh", "ssh_host_*"))
	for _, f := range files {
		keys = append(keys, filepath.Join(root, f))
	}
	for _, f := range keys {
		// Lstat, not Stat: a link (dbus's machine-id often is one) is
		// removed itself; and only in a directory that is in the target —
		// an image whose /etc/ssh is an absolute link would otherwise have
		// had the LIVE system's host keys removed.
		if !insideTarget(root, filepath.Dir(f)) {
			continue
		}
		if fi, err := os.Lstat(f); err == nil && !fi.IsDir() {
			if err := os.Remove(f); err != nil {
				return fmt.Errorf("image: %w", err)
			}
		}
	}
	if exists(filepath.Join(root, "usr", "bin", "ssh-keygen")) && exists(filepath.Join(root, "etc", "ssh")) {
		return storage.RunCommands([][]string{{"chroot", root, "ssh-keygen", "-A"}}, in.opts.Runner)
	}
	return nil
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
	// Unpacked with the live system's /proc, /sys and /dev bound in, the
	// image's own dev/, proc/ and sys/ entries landed on the LIVE ones (a
	// container export's empty dev/console replaced the live console), and
	// its etc/resolv.conf over the install-time copy. They come off for the
	// unpack and go back on after, setting the image's resolv.conf aside.
	bound := in.mnt.HasMeta()
	if bound {
		if err := in.mnt.UnmountMeta(); err != nil {
			return err
		}
	}
	if err := removeIfPresent(in.resolvCopy); err != nil {
		return err
	}
	in.resolvCopy = ""
	if err := storage.RunCommands([][]string{ImageCommand(image, in.opts.Root, in.cfg.AllowInsecureHTTP)}, in.opts.Runner); err != nil {
		return err
	}
	if bound {
		if err := in.mountMeta(); err != nil {
			return err
		}
	}
	if err := in.forgetImageIdentity(); err != nil {
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
	// The mirror is recorded first: the upgrade below runs the target's own
	// xbps-install, which reads only the target's xbps.d. Upgrading before
	// the mirror was there resolved every package against Void alone — the
	// very skew (dbus vs dbus-libs) this file exists to prevent.
	if len(in.opts.ExtraRepos) > 0 {
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
	}
	// The base system is in place: the kernels the config asked to be rid
	// of go now, before the bootloader lists what is there.
	// Brought up to date first, as the installed system's own update does:
	// xbps itself, which refuses a system update while a newer xbps is
	// available, then everything.
	if in.cfg.SystemUpgrade {
		env := append([]string{"chroot", in.opts.Root, "env"}, in.cfg.ProxyEnv()...)
		upgrade := [][]string{
			append(append([]string{}, env...), "xbps-install", "-Syu", "xbps"),
			append(append([]string{}, env...), "xbps-install", "-Syu"),
		}
		if err := storage.RunCommands(upgrade, in.opts.Runner); err != nil {
			return err
		}
	}
	if len(in.cfg.KernelRemove) > 0 {
		remove := append([]string{"chroot", in.opts.Root, "xbps-remove", "-Ry"}, in.cfg.KernelRemove...)
		if err := storage.RunCommands([][]string{remove}, in.opts.Runner); err != nil {
			return err
		}
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
	// Resuming, the first attempt's keyfile is already in the target and,
	// mostly, in the volumes' key slots: a second key would only use up
	// another slot. But that attempt may have failed between two volumes,
	// so each one the keyfile does not open yet gets it now — skipped
	// whole, a volume left without it did not open at boot.
	if in.opts.ResumeFrom != "" && exists(full) {
		for _, p := range enc {
			if luks.KeyOpens(p.Device, full) {
				continue
			}
			if err := luks.AddKey(p.Device, full, in.opts.Passphrase, in.output()); err != nil {
				return err
			}
		}
	} else {
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
	// A link there (a reused root, an image) would have the commands below
	// truncate and mkswap whatever it points at; it is replaced.
	if fi, err := os.Lstat(filepath.Join(in.opts.Root, "swapfile")); err == nil && fi.Mode()&os.ModeSymlink != 0 {
		if err := os.Remove(filepath.Join(in.opts.Root, "swapfile")); err != nil {
			return fmt.Errorf("replace the link at /swapfile: %w", err)
		}
	}
	rootFS := ""
	for _, p := range in.plan.Parts {
		if p.Mountpoint == "/" && !p.IsPV && !p.IsRaidMember {
			rootFS = p.Filesystem
		}
	}
	if err := storage.RunCommands(
		storage.SwapFileCommands(in.opts.Root, in.cfg.SwapFileSize, rootFS), in.opts.Runner); err != nil {
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
	// An init= the config adds itself is the one it means: appended after
	// it, slinit's would have won (the kernel takes the last).
	ownInit := false
	for _, a := range kargs {
		ownInit = ownInit || strings.HasPrefix(a, "init=")
	}
	if !ownInit && bootsSlinit(in.opts.Root, remove) {
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
					// dracut's own crypt module (no systemd here) reads no
					// rd.luks.options; the one option it has a switch for is
					// discard, which it takes per volume.
					for _, o := range strings.Split(p.CryptOptions, ",") {
						if o = strings.TrimSpace(o); o == "discard" || o == "allow-discards" {
							kargs = append(kargs, "rd.luks.allow-discards="+uuid)
							break
						}
					}
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
	// The ":/" names the initramfs itself as where the key is: with no key
	// device dracut probes every block device for the path instead, never
	// finds it (it is only in the initramfs), and asks for the passphrase
	// a second time.
	if in.keyfile != "" && in.bootOnEncrypted() {
		kargs = append(kargs, "rd.luks.key="+in.keyfile+":/")
	}

	// A root on a RAID array must have the array assembled by the initramfs
	// before it can be mounted — rd.md.uuid points dracut at it (alongside
	// the /etc/mdadm.conf written below).
	for _, p := range in.plan.Parts {
		// … whether the root is on it directly, or on a volume group or a
		// LUKS container that is.
		if p.Raid && (p.Mountpoint == "/" || in.isRootCrypt(p) || (p.IsPV && p.VGName != "" && p.VGName == in.rootVGName()) ||
			in.rootDisk() == storage.RaidDevice(p.RaidName)) {
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
		return storage.RunCommands([][]string{in.initramfsCommand()}, in.opts.Runner)
	}
	return nil
}

// initramfsCommand rebuilds the target's initramfs for every kernel it
// has. With dracut there, that is all it runs: `xbps-reconfigure -fa`
// re-ran every package's configure script (a full pass, and a third
// dracut build on top of base-install's two) for the one thing needed
// here. Without dracut (an image built otherwise), it falls back to that.
func (in *installer) initramfsCommand() []string {
	if exists(filepath.Join(in.opts.Root, "usr", "bin", "dracut")) {
		return []string{"chroot", in.opts.Root, "dracut", "--regenerate-all", "--force"}
	}
	return []string{"chroot", in.opts.Root, "xbps-reconfigure", "-fa"}
}

// fillSpareESPs gives each spare EFI System Partition what the firmware
// needs to boot from it: a copy of the ESP's EFI directory (GRUB, which
// finds /boot by its filesystem UUID, so the copy works from any disk), the
// loader at the removable-media path as well, and — unless the firmware's
// entries are to be left alone — a boot entry of its own. efibootmgr puts a
// new entry first in the boot order, so the spares end up ahead of the entry
// grub-install made; every one of them starts the same GRUB, so which is
// tried first makes no difference while the disks are all there.
// efiBootList is the firmware's boot entries as `efibootmgr` lists them;
// tests replace it. A machine where it cannot be read has none to clean.
var efiBootList = func() string {
	out, _ := exec.Command("efibootmgr").Output()
	return string(out)
}

// efiEntriesLabelled returns the numbers of the boot entries whose label is
// exactly label ("Boot0003* Sunlight (sdb)\tHD(...)" → "0003").
func efiEntriesLabelled(label string) []string {
	var nums []string
	for _, line := range strings.Split(efiBootList(), "\n") {
		rest, ok := strings.CutPrefix(line, "Boot")
		if !ok || len(rest) < 5 || strings.Trim(rest[:4], "0123456789ABCDEFabcdef") != "" {
			continue
		}
		name := strings.TrimLeft(rest[4:], "* ")
		name, _, _ = strings.Cut(name, "\t")
		if strings.TrimSpace(name) == label {
			nums = append(nums, rest[:4])
		}
	}
	return nums
}

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
		label := grub.DefaultID + " (" + filepath.Base(p.Disk) + ")"
		// grub-install replaces its own entry; nothing replaced these, so
		// every reinstall and resume added another set, the new ones first
		// in BootOrder and the old ones pointing at partitions since
		// recreated. An entry with this label is removed first.
		var cmds [][]string
		for _, num := range efiEntriesLabelled(label) {
			cmds = append(cmds, []string{"chroot", in.opts.Root, "efibootmgr", "--bootnum", num, "--delete-bootnum"})
		}
		cmds = append(cmds, []string{"chroot", in.opts.Root, "efibootmgr", "--create",
			"--disk", p.Disk, "--part", fmt.Sprintf("%d", p.Number),
			"--label", label,
			"--loader", `\EFI\` + grub.DefaultID + `\grubx64.efi`})
		if err := storage.RunCommands(cmds, in.opts.Runner); err != nil {
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
	// The install's own arrays only. --scan lists every array the live
	// system has assembled — a data array on a disk the install does not
	// touch, an old install's — and the installed system then assembled
	// those at boot too, under the live system's names.
	args := []string{"mdadm", "--detail", "--brief"}
	for _, p := range in.plan.Parts {
		if p.Raid {
			args = append(args, storage.RaidDevice(p.RaidName))
		}
	}
	out, err := exec.Command(args[0], args[1:]...).Output()
	if err != nil {
		return fmt.Errorf("%s: %w", strings.Join(args, " "), storage.WithStderr(err))
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
	// And a random seed for the first boot, which has no previous boot to
	// have saved one.
	if err := system.SeedRNG(root); err != nil {
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

// insideTarget reports whether dir, its links followed, is root or under
// it: links in an image or a reused root point where they would on that
// system, and from here an absolute one points into the live system.
func insideTarget(root, dir string) bool {
	r, err := filepath.EvalSymlinks(root)
	if err != nil {
		r = root
	}
	d, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return false
	}
	return d == r || strings.HasPrefix(d, r+string(os.PathSeparator))
}

// ownFile makes path, a file the install is about to read and rewrite in
// the target, an ordinary file in the target. A link there is resolved as
// the installed system would resolve it (an absolute target taken inside
// root), its content kept, and replaced by a file of that content: read
// and written through, an absolute link reached the live system's file.
// A directory that leads out of the target is refused.
func ownFile(root, path string) error {
	if !insideTarget(root, filepath.Dir(path)) {
		return fmt.Errorf("%s leads outside the installed system", path)
	}
	fi, err := os.Lstat(path)
	if err != nil || fi.Mode()&os.ModeSymlink == 0 {
		return nil
	}
	link, err := os.Readlink(path)
	if err != nil {
		return fmt.Errorf("read link %s: %w", path, err)
	}
	target := filepath.Join(filepath.Dir(path), link)
	if filepath.IsAbs(link) {
		target = filepath.Join(root, link)
	}
	var data []byte
	if st, err := os.Stat(target); err == nil && st.Mode().IsRegular() && insideTarget(root, filepath.Dir(target)) {
		data, _ = os.ReadFile(target)
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("replace link %s: %w", path, err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// writeDhcpcdConf adds conf (DhcpcdConf: the global part, then stanzas)
// to the target's dhcpcd.conf, each part once, so a resumed install does
// not write them twice. The global part goes before the file's first
// interface block, not at its end: dhcpcd ignores a global option inside a
// block, and a dhcpcd.conf carried over from the live system (copyNetwork)
// may well end in one — the members of a bond would then ask for leases.
func writeDhcpcdConf(path, global, conf string) error {
	stanzas := strings.TrimPrefix(conf, global)
	// Only an ordinary file is read: a path that leads to a device has no
	// end to read to. Anything else in the way fails the write below.
	var data []byte
	if fi, err := os.Stat(path); err == nil && fi.Mode().IsRegular() {
		if data, err = os.ReadFile(path); err != nil {
			return fmt.Errorf("read %s: %w", path, err)
		}
	}
	text := string(data)
	// Compared and written without the parts' leading blank line, so a
	// second run (a resume) finds what the first one wrote.
	g := strings.TrimPrefix(global, "\n")
	if g != "" && !strings.Contains(text, g) {
		at := len(text)
		off := 0
		for _, l := range strings.SplitAfter(text, "\n") {
			if f := strings.Fields(l); len(f) > 0 && (f[0] == "interface" || f[0] == "ssid" || f[0] == "profile") {
				at = off
				break
			}
			off += len(l)
		}
		before := text[:at]
		if before != "" && !strings.HasSuffix(before, "\n") {
			before += "\n" // a last line with no newline is not glued to
		}
		text = before + g + "\n" + text[at:]
	}
	if st := strings.TrimPrefix(stanzas, "\n"); st != "" && !strings.Contains(text, st) {
		if text != "" && !strings.HasSuffix(text, "\n") {
			text += "\n"
		}
		text += "\n" + st
	}
	if text == string(data) {
		return nil
	}
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
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
	if conf != "" {
		path := filepath.Join(in.opts.Root, "etc", "dhcpcd.conf")
		if err := ownFile(in.opts.Root, path); err != nil {
			return err
		}
		if err := writeDhcpcdConf(path, network.DhcpcdGlobal(ifaces), conf); err != nil {
			return err
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
	if err != nil || !st.Mode().IsRegular() {
		return false
	}
	// The binary alone is not a system slinit can boot: with no boot
	// service (a Void base with the slinit package added) init=slinit came
	// up to a failed boot, while every other stage set the system up for
	// runit. The service set's or the distribution's boot service has to
	// be there.
	// Where slinit itself looks (its /lib is /usr/lib on a merged-/usr
	// Void, but not on every image).
	for _, dir := range []string{"usr/lib/slinit.d", "etc/slinit.d", "lib/slinit.d", "usr/local/lib/slinit.d"} {
		if _, err := os.Stat(filepath.Join(root, dir, "boot")); err == nil {
			return true
		}
	}
	return false
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
