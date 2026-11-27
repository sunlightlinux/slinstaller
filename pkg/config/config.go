// Package config is slinstaller's installation config, in the shape
// clr-installer writes.
//
// The goal is compatibility at the file level: a clr-installer
// config.yaml should load here and describe the same install. So the
// YAML field names are clr-installer's — targetMedia, bundles, users,
// networkInterfaces, kernel-arguments — not a translation of them. The
// code behind them is slinstaller's own; only the schema is shared.
//
// Parsing is deliberately tolerant. A real clr-installer config carries
// fields slinstaller has no use for: lsblk probe output on each device
// (model, majMin, ro, rm, state) and Clear Linux specifics (bundles,
// telemetry, swupd*). Unknown fields are ignored rather than rejected,
// so a config meant for clr-installer still loads; what maps to a
// Sunlight install is honoured and the rest is carried but unused. Keys
// slinstaller does not know at all are listed by UnknownKeys, so a typo
// (encryptd: true) is warned about rather than silently dropped.
package config

import (
	"fmt"
	"math"
	"net"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/sunlightlinux/slinstaller/pkg/mdadm"
	"github.com/sunlightlinux/slinstaller/pkg/network"
	"github.com/sunlightlinux/slinstaller/pkg/storage"
	"gopkg.in/yaml.v3"
)

// Config is a whole installation as clr-installer serialises it.
//
// Only TargetMedia drives anything today (it produces the partition
// plan); the other fields are the surface slinstaller commits to
// understanding as the identity, account, network and software
// subsystems are ported.
type Config struct {
	TargetMedia       []*Media     `yaml:"targetMedia"`
	NetworkInterfaces []*Interface `yaml:"networkInterfaces,omitempty"`
	Users             []*User      `yaml:"users,omitempty"`

	Keyboard string `yaml:"keyboard,omitempty"`
	Language string `yaml:"language,omitempty"`
	Hostname string `yaml:"hostname,omitempty"`
	Timezone string `yaml:"timezone,omitempty"`
	Kernel   string `yaml:"kernel,omitempty"`

	// KernelProfile records which Kernel Command Line profile the arguments
	// came from (e.g. "Server", "Telco/RAN", "Custom"). KernelArguments holds
	// the expanded arguments that apply, except for what a profile derives
	// from the hardware, which an install recomputes for the machine it runs
	// on: the vendor-specific arguments (IOMMU, nested KVM, P-State mode) of
	// any profile, and the CPU lists and hugepage count of one that isolates
	// CPUs (Realtime, Telco/RAN, …). "Custom", or no profile, keeps every
	// argument as written. A Sunlight extension.
	KernelProfile string `yaml:"kernel-profile,omitempty"`

	KernelArguments *KernelArguments `yaml:"kernel-arguments,omitempty"`

	// Bootloader holds the choices about how GRUB is installed; without it
	// the loader is installed and registered with the firmware, as before.
	Bootloader *Bootloader `yaml:"bootloader,omitempty"`

	// ConsoleLogLevel sets the kernel console log verbosity (0 emergency … 7
	// debug) in two places: loglevel=N on the kernel command line, and
	// kernel.printk = N 4 1 7 via /etc/sysctl.d on the installed system. nil
	// leaves the kernel default untouched. This is a Sunlight extension, not a
	// clr-installer field, and is ignored when absent.
	ConsoleLogLevel *int `yaml:"console-log-level,omitempty"`

	Bundles     []string `yaml:"bundles,omitempty"`
	SwupdMirror string   `yaml:"swupdMirror,omitempty"`
	HTTPSProxy  string   `yaml:"httpsProxy,omitempty"`

	// Packages are extra xbps packages to install into the target, on top of
	// the base set. This is slinstaller's functional stand-in for
	// clr-installer's bundles: Clear's bundle names (os-core, editors, …) do
	// not map to Sunlight packages, so bundles is carried-but-ignored, while
	// a Sunlight config names real packages here.
	Packages []string `yaml:"packages,omitempty"`

	// Offline installs without fetching from a remote repository: the base
	// system must come from a local repository on the live medium
	// (clr-installer's offline). The network-readiness precheck is skipped
	// and the remote default repo is not used.
	Offline bool `yaml:"offline,omitempty"`

	// AllowInsecureHTTP permits a plain http:// package mirror
	// (clr-installer's allowInsecureHTTP). Without it a non-https remote repo
	// is refused; https and local repositories are always allowed.
	AllowInsecureHTTP bool `yaml:"allowInsecureHTTP,omitempty"`

	// SkipValidationSize turns off the minimum-size and disk-fit checks
	// (clr-installer's skipValidationSize); SkipValidationAll turns off
	// Validate entirely (clr's skipValidationAll). Both exist so a
	// deliberately unusual layout can be forced through, and both are off by
	// default.
	SkipValidationSize bool `yaml:"skipValidationSize,omitempty"`
	SkipValidationAll  bool `yaml:"skipValidationAll,omitempty"`

	// SwapFileSize, if set, creates a swap file of that size on root
	// instead of (or besides) a swap partition — clr-installer's
	// swapFileSize. A human size such as "2G".
	SwapFileSize string `yaml:"swapFileSize,omitempty"`

	// Raid declares software RAID arrays. Each array's members are the
	// `type: raid` partitions (across the disks) that carry its name; the
	// array holds a filesystem at /dev/md/<name>, like any other mount.
	Raid []*RaidArray `yaml:"raid,omitempty"`

	PreInstall  []*Hook `yaml:"pre-install,omitempty"`
	PostInstall []*Hook `yaml:"post-install,omitempty"`

	// Environment is a set of variables made available to pre/post-install
	// hook commands (clr-installer's env), on top of the $chrootDir and
	// $chrooted the installer always provides.
	Environment map[string]string `yaml:"env,omitempty"`

	// CopyNetwork copies the live system's network configuration into the
	// installed system (clr-installer's copyNetwork), so a machine set up on
	// the live medium — notably its Wi-Fi credentials — keeps working after
	// the first boot.
	CopyNetwork bool `yaml:"copyNetwork,omitempty"`

	// AutoUpdate keeps the installed system updated from its xbps
	// repositories once a day (clr-installer's autoUpdate, which leaves
	// swupd's update timer on or turns it off). Unset means on, as in
	// clr-installer; false installs the update service disabled.
	AutoUpdate *bool `yaml:"autoUpdate,omitempty"`

	// PostReboot reboots the machine when the install finishes
	// (clr-installer's postReboot), for an unattended install that should
	// come up into the new system without an operator at the console.
	PostReboot bool `yaml:"postReboot,omitempty"`

	// PowerState is what the machine does when a full install finishes:
	// "reboot" (the same as postReboot), "poweroff", "halt", or "kexec" —
	// the installed kernel is loaded and started directly, without going
	// back through the firmware. Empty leaves it running.
	PowerState string `yaml:"power-state,omitempty"`

	// HTTPProxy and NoProxy complete httpsProxy: the proxy for plain-http
	// repositories (httpsProxy when unset) and the hosts reached directly.
	HTTPProxy string `yaml:"httpProxy,omitempty"`
	NoProxy   string `yaml:"noProxy,omitempty"`

	// DiskImage writes a whole-disk image — partition table and all — onto
	// the one disk named in targetMedia, in place of an install: nothing is
	// partitioned, configured or made bootable by slinstaller, because the
	// image already is all of that. Compressed images (.zst, .xz, .gz) are
	// unpacked on the way. A relative path is taken from the config file's
	// directory.
	DiskImage string `yaml:"disk-image,omitempty"`

	// Image installs the base system by unpacking a root filesystem image
	// — a tar archive (compressed or not) or a squashfs — instead of
	// installing packages. The image has to hold a complete system: its
	// kernel, GRUB, and whatever the layout needs to boot (cryptsetup, lvm2,
	// mdadm). A relative path is taken from the config file's directory.
	Image string `yaml:"image,omitempty"`

	// SystemUpgrade brings the installed system up to date from its
	// repositories before the bootloader is set up — for an install from
	// an image or a local repository that has aged.
	SystemUpgrade bool `yaml:"system-upgrade,omitempty"`

	// KernelRemove lists kernel packages to remove once the base system is
	// in place — the kernel an image came with, when the config installs or
	// keeps another.
	KernelRemove []string `yaml:"kernel-remove,omitempty"`

	// KernelFallback is the kernel package to install when the configured
	// one cannot be: the base install is tried once more with it.
	KernelFallback string `yaml:"kernel-fallback,omitempty"`

	// MessageURL is where an installer message is looked for, as
	// clr-installer fetches one from its distribution: pre-install-msg.yaml
	// under it is shown before the install is confirmed and
	// post-install-msg.yaml after it succeeds. A message that is not there
	// is simply not shown.
	MessageURL string `yaml:"message-url,omitempty"`

	// StageHooks are commands run after a named stage of the install
	// (partition, mount, meta-fs, base-install, fstab, swapfile, bootloader,
	// first-boot, network, save-config), in the shape of pre-install and
	// post-install hooks. A chroot hook needs the target mounted, so it
	// belongs after meta-fs or later.
	StageHooks map[string][]*Hook `yaml:"stage-hooks,omitempty"`

	// PowerStateDelay is how many seconds to wait before power-state (or
	// postReboot) takes effect, to leave time to read the last messages.
	PowerStateDelay int `yaml:"power-state-delay,omitempty"`

	// InstallType raises the smallest root the config may ask for, as
	// clr-installer does for what it installs: "server" needs 4 GB,
	// "desktop" 20 GB. Unset, the minimum is 2 GB.
	InstallType string `yaml:"install-type,omitempty"`

	// ReportURL, when set, is sent the install's progress: a JSON POST as
	// each stage begins and one when the install ends, with its result.
	ReportURL string `yaml:"report-url,omitempty"`

	// WriteFiles are files written into the installed system once it is
	// configured, before the post-install hooks run.
	WriteFiles []*WriteFile `yaml:"write-files,omitempty"`

	// ReusePartitions installs into the disk's existing partitions instead
	// of repartitioning it (clr-installer's advanced/manual mode): the
	// partition table is left untouched, each declared partition is matched
	// to the existing device by name, formatted unless it is marked
	// preserve, then mounted. It is how an install shares an existing ESP or
	// keeps a /home across a reinstall, and the only way slinstaller writes
	// to a disk without wiping it.
	ReusePartitions bool `yaml:"reusePartitions,omitempty"`

	// BlockDevices are clr-installer's storage aliases: a name bound to a
	// device file, so a targetMedia entry can be written as `${alias}` /
	// `${alias}1` instead of a hard-coded `nvme0n1` / `nvme0n1p1`. They are
	// expanded into the targetMedia names at load time (expandAliases).
	BlockDevices []*StorageAlias `yaml:"block-devices,omitempty"`

	Version uint `yaml:"version,omitempty"`

	// unknown are the keys of the loaded file no field reads (UnknownKeys).
	unknown []string
}

// UnknownKeys lists the keys of the loaded file that slinstaller does not
// read, as paths (targetMedia[0].children[1].encryptd), leaving out the
// clr-installer keys it knows and ignores. They do not stop the install —
// a clr-installer config has to keep loading — but one is usually a typo.
func (c *Config) UnknownKeys() []string { return c.unknown }

// clrIgnored are keys a clr-installer config carries that slinstaller
// knows and deliberately ignores: the lsblk probe fields on each device
// and Clear's telemetry settings.
var clrIgnored = map[string]bool{
	"model": true, "majMin": true, "ro": true, "rm": true, "state": true,
	"telemetry": true, "telemetryURL": true, "telemetryTID": true, "telemetryPolicy": true,
}

// unknownKeys walks a YAML node against the type it is decoded into and
// appends the mapping keys no field of that type reads.
func unknownKeys(n *yaml.Node, t reflect.Type, at string, out *[]string) {
	for n.Kind == yaml.AliasNode && n.Alias != nil {
		n = n.Alias
	}
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	switch {
	case n.Kind == yaml.DocumentNode:
		for _, x := range n.Content {
			unknownKeys(x, t, at, out)
		}
	case n.Kind == yaml.SequenceNode && t.Kind() == reflect.Slice:
		for i, x := range n.Content {
			unknownKeys(x, t.Elem(), fmt.Sprintf("%s[%d]", at, i), out)
		}
	case n.Kind == yaml.MappingNode && t.Kind() == reflect.Map:
		for i := 0; i+1 < len(n.Content); i += 2 {
			unknownKeys(n.Content[i+1], t.Elem(), at+"."+n.Content[i].Value, out)
		}
	case n.Kind == yaml.MappingNode && t.Kind() == reflect.Struct:
		fields := map[string]reflect.Type{}
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			name, _, _ := strings.Cut(f.Tag.Get("yaml"), ",")
			if !f.IsExported() || name == "-" {
				continue
			}
			if name == "" {
				name = strings.ToLower(f.Name)
			}
			fields[name] = f.Type
		}
		for i := 0; i+1 < len(n.Content); i += 2 {
			key, path := n.Content[i].Value, strings.TrimPrefix(at+"."+n.Content[i].Value, ".")
			switch ft, ok := fields[key]; {
			case ok:
				unknownKeys(n.Content[i+1], ft, path, out)
			case !clrIgnored[key] && key != "<<":
				*out = append(*out, path)
			}
		}
	}
}

// AutoUpdates reports whether the installed system updates itself: the
// config's autoUpdate, on when it is not set.
func (c *Config) AutoUpdates() bool { return c.AutoUpdate == nil || *c.AutoUpdate }

// StorageAlias binds a name to a device file so targetMedia can refer to a
// disk by alias. In clr-installer a disk is named `${alias}` and its
// partitions `${alias}1`, `${alias}2`; the alias expands to the device's
// base name (/dev/nvme0n1 -> nvme0n1) for the disk, and to that name plus
// the kernel's partition suffix (nvme0n1p) for the partitions, so a layout
// can be written once and pointed at different disks by changing the file.
type StorageAlias struct {
	Name string `yaml:"name,omitempty"`
	File string `yaml:"file,omitempty"`
}

// Media is one entry in targetMedia: a disk (type "disk") carrying its
// partitions, or a partition (type "part"). The lsblk probe fields a
// clr-installer config includes on each device are intentionally not
// declared — they load and are ignored.
type Media struct {
	Name       string   `yaml:"name"`
	Type       string   `yaml:"type,omitempty"`
	Size       string   `yaml:"size,omitempty"`
	FsType     string   `yaml:"fstype,omitempty"`
	Mountpoint string   `yaml:"mountpoint,omitempty"`
	Children   []*Media `yaml:"children,omitempty"`

	// Label is the filesystem label mkfs gives the partition or logical
	// volume (truncated to what the filesystem allows), as clr-installer
	// does. Mounts still go by UUID.
	Label string `yaml:"label,omitempty"`

	// Options are extra mkfs options, space-separated, added after
	// slinstaller's own and before the device (clr-installer's options),
	// e.g. "-O ^metadata_csum" or "-m 1".
	Options string `yaml:"options,omitempty"`

	// VolumeGroup names the LVM volume group a `type: lvm` partition
	// hosts; its children are the logical volumes.
	VolumeGroup string `yaml:"volumeGroup,omitempty"`

	// Encrypted puts a `type: lvm` partition's physical volume inside a
	// LUKS container (LVM-on-LUKS): the partition is luksFormat'd and the
	// PV, group and logical volumes live on the mapper. A plain partition
	// uses `type: crypt` instead; this is for the LVM case, where the type
	// is already taken by `lvm`.
	Encrypted bool `yaml:"encrypted,omitempty"`

	// Raid names the RAID array a `type: raid` partition is a member of.
	// The array itself is declared in the config's top-level raid list;
	// several partitions, usually on different disks, carry the same name.
	Raid string `yaml:"raid,omitempty"`

	// Preserve, in a reusePartitions install, keeps this existing partition's
	// data: it is mounted but not reformatted. Ignored for a normal
	// (wiping) install, where every partition is made fresh.
	Preserve bool `yaml:"preserve,omitempty"`

	// Create and Start describe a NEW partition to cut in unallocated space
	// during a reusePartitions (Safe/alongside) install — the Sunlight
	// partition added to a disk's free space without wiping the existing
	// ones. Start is the MiB offset of the free region; Size is its length.
	Create bool   `yaml:"create,omitempty"`
	Start  string `yaml:"start,omitempty"`

	// CryptOptions are extra /etc/crypttab options for an encrypted volume,
	// comma-separated (e.g. "discard"); for the root volume, which the
	// initramfs opens, they go on the kernel command line instead.
	CryptOptions string `yaml:"crypt-options,omitempty"`

	// PartitionType, PartitionUUID and PartitionName set the partition's
	// entry in the GPT: its type GUID, its own GUID, and its name. Without
	// them the type follows the partition's use and the rest is generated.
	PartitionType string `yaml:"partition-type,omitempty"`
	PartitionUUID string `yaml:"partition-uuid,omitempty"`
	PartitionName string `yaml:"partition-name,omitempty"`

	// UUID is the filesystem's UUID (for vfat, a volume id such as
	// 1A2B-3C4D), for a layout that has to be reproducible.
	UUID string `yaml:"uuid,omitempty"`

	// Passno and Freq are the fstab entry's fsck pass and dump frequency.
	// Without Passno the root is checked first (1) and the rest after (2).
	Passno *int `yaml:"passno,omitempty"`
	Freq   int  `yaml:"freq,omitempty"`

	// ESP marks a vfat partition with no mountpoint as a spare EFI System
	// Partition: on a second disk, it is given a copy of the bootloader, so
	// a machine whose root is on RAID still boots when the disk holding the
	// mounted ESP has failed.
	ESP bool `yaml:"esp,omitempty"`

	// Resize, in a reusePartitions install, shrinks this existing ext2/3/4
	// or ntfs partition to the given size, filesystem first, keeping what is on it
	// (it has to be preserve: true). The space it gives up can take a
	// `create: true` partition.
	Resize string `yaml:"resize,omitempty"`

	// Spare makes a `type: raid` partition a hot spare of its array: it
	// holds no data until a member fails, when the array rebuilds onto it.
	Spare bool `yaml:"spare,omitempty"`

	// Wipe, on a disk, is how much of what is on it is erased before it is
	// partitioned: "superblock" (the default: old signatures, enough for the
	// install), "zero" (zeros over the whole disk), "random" (random data
	// over the whole disk) or "discard" (the whole disk discarded, as for
	// an SSD). The last three take as long as the disk is large.
	Wipe string `yaml:"wipe,omitempty"`

	// MountOptions are the fstab options for this filesystem (or swap),
	// comma-separated as in fstab, e.g. "noatime,compress=zstd". Without
	// them the entry says "defaults".
	MountOptions string `yaml:"mount-options,omitempty"`

	// Subvolumes splits a btrfs filesystem into subvolumes, each mounted at
	// its own mountpoint. One of them carries this entry's mountpoint.
	Subvolumes []*Subvolume `yaml:"subvolumes,omitempty"`

	// Match, on a disk, names it by what does not change between boots —
	// its serial number, WWN, model or a /dev/disk/by-* path — instead of
	// by its kernel name, which can. ResolveDisks turns it into the name.
	Match *DiskMatch `yaml:"match,omitempty"`
}

// StageNames are the install's stages, in order. Hooks may be attached to
// any of them but the first and last, which have pre-install and
// post-install.
var StageNames = []string{"pre-install", "partition", "mount", "meta-fs", "base-install", "fstab",
	"swapfile", "bootloader", "first-boot", "network", "save-config", "post-install"}

// minRoot is the smallest root filesystem the config may ask for, and how
// to say it.
func (c *Config) minRoot() (uint64, string) {
	switch c.InstallType {
	case "server":
		return 4 * 1000 * 1000 * 1000, "4 GB"
	case "desktop":
		return 20 * 1000 * 1000 * 1000, "20 GB"
	}
	return storage.MinRootSize, "2 GB"
}

// RootMinimum is the smallest root filesystem the config may ask for, and
// how to say it: what a root that fills its disk has to be left with.
func (c *Config) RootMinimum() (uint64, string) { return c.minRoot() }

// autoSize is the swap size that means "work it out from the memory".
const autoSize = "auto"

// swapEntries calls f on every swap partition and swap logical volume.
func (c *Config) swapEntries(f func(*Media)) {
	for _, d := range c.TargetMedia {
		if d == nil {
			continue
		}
		for _, ch := range d.Children {
			if ch == nil {
				continue
			}
			if ch.FsType == "swap" {
				f(ch)
			}
			for _, lv := range ch.Children {
				if lv != nil && lv.FsType == "swap" {
					f(lv)
				}
			}
		}
	}
}

// autoSwap reports whether any swap is still sized "auto".
func (c *Config) autoSwap() bool {
	auto := strings.EqualFold(strings.TrimSpace(c.SwapFileSize), autoSize)
	c.swapEntries(func(m *Media) {
		if strings.EqualFold(strings.TrimSpace(m.Size), autoSize) {
			auto = true
		}
	})
	return auto
}

// ResolveAutoSwap gives every swap sized "auto" — the swap file, a swap
// partition, a swap logical volume — the size SuggestedSwap works out for a
// machine with mem bytes of memory.
func (c *Config) ResolveAutoSwap(mem uint64) {
	size := fmt.Sprintf("%dM", SuggestedSwap(mem)>>20)
	if strings.EqualFold(strings.TrimSpace(c.SwapFileSize), autoSize) {
		c.SwapFileSize = size
	}
	c.swapEntries(func(m *Media) {
		if strings.EqualFold(strings.TrimSpace(m.Size), autoSize) {
			m.Size = size
		}
	})
}

// SuggestedSwap is a swap size for a machine with mem bytes of memory: twice
// the memory on a very small machine, about as much as the memory up to
// 4 GiB, then a fixed 4 GiB, and 8 GiB from 64 GiB up. Swap this size is for
// riding out memory pressure, not for hibernating; it is never more than
// 8 GiB, nor less than 1 GiB (which is also the answer when the memory
// could not be read).
func SuggestedSwap(mem uint64) uint64 {
	const gib = uint64(1) << 30
	var size uint64
	switch {
	case mem == 0:
		size = gib
	case mem < gib:
		size = 2 * mem
	case mem < 2*gib:
		size = 2 * gib
	case mem < 4*gib:
		size = mem
	case mem < 64*gib:
		size = 4 * gib
	default:
		size = 8 * gib
	}
	// Whole mebibytes, as a partition is cut.
	size = (size + (1<<20 - 1)) >> 20 << 20
	return min(max(size, gib), 8*gib)
}

// WriteFile is a file to write into the installed system.
type WriteFile struct {
	// Path is the file's absolute path in the installed system.
	Path string `yaml:"path"`
	// Content is what the file holds.
	Content string `yaml:"content"`
	// Permissions is its mode in octal, e.g. "0600". Empty means 0644.
	Permissions string `yaml:"permissions,omitempty"`
}

// Mode is the file's permission bits.
func (w *WriteFile) Mode() (os.FileMode, error) {
	if w.Permissions == "" {
		return 0o644, nil
	}
	n, err := strconv.ParseUint(w.Permissions, 8, 32)
	if err != nil || n > 0o7777 {
		return 0, fmt.Errorf("permissions %q are not an octal mode such as 0644", w.Permissions)
	}
	return os.FileMode(n&0o777) | modeBits(n), nil
}

// modeBits maps the octal setuid, setgid and sticky digits to Go's bits.
func modeBits(n uint64) os.FileMode {
	var m os.FileMode
	if n&0o4000 != 0 {
		m |= os.ModeSetuid
	}
	if n&0o2000 != 0 {
		m |= os.ModeSetgid
	}
	if n&0o1000 != 0 {
		m |= os.ModeSticky
	}
	return m
}

// ProxyEnv is the proxy settings as environment assignments, for the
// commands that download: the https proxy, the http proxy (the https one
// when no other is given, as before there were two) and the direct hosts.
func (c *Config) ProxyEnv() []string {
	var env []string
	httpProxy := c.HTTPProxy
	if httpProxy == "" {
		httpProxy = c.HTTPSProxy
	}
	if c.HTTPSProxy != "" {
		env = append(env, "https_proxy="+c.HTTPSProxy)
	}
	if httpProxy != "" {
		env = append(env, "http_proxy="+httpProxy)
	}
	if c.NoProxy != "" && len(env) > 0 {
		env = append(env, "no_proxy="+c.NoProxy)
	}
	return env
}

// PowerAction is what to do with the machine after a full install:
// "reboot", "poweroff", "halt" or "" — power-state, or postReboot.
func (c *Config) PowerAction() string {
	if c.PowerState != "" {
		return c.PowerState
	}
	if c.PostReboot {
		return "reboot"
	}
	return ""
}

// Bootloader is how GRUB is installed.
type Bootloader struct {
	// Terminal is where the boot menu shows: "" or "console" for the
	// screen only, "serial" for the screen and the serial console the
	// kernel uses (console=ttyS… among the kernel arguments).
	Terminal string `yaml:"terminal,omitempty"`

	// Removable also installs the loader at the removable-media path
	// (EFI/BOOT/BOOTX64.EFI), which the firmware boots when it has no
	// entry of its own for the disk.
	Removable bool `yaml:"removable,omitempty"`

	// UpdateNVRAM, when false, leaves the firmware's boot entries alone;
	// the system then boots from the removable-media path, so Removable
	// has to be set. Unset means true.
	UpdateNVRAM *bool `yaml:"update-nvram,omitempty"`

	// ProbeOtherOS adds the other systems found on the machine to the
	// boot menu (installs os-prober in the target).
	ProbeOtherOS bool `yaml:"probe-other-os,omitempty"`
}

var (
	uuidRe   = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	vfatIDRe = regexp.MustCompile(`^[0-9a-fA-F]{4}-?[0-9a-fA-F]{4}$`)
)

// details carries a partition's optional particulars into the plan.
func details(p *storage.Partition, m *Media) {
	p.CryptOptions = strings.TrimSpace(m.CryptOptions)
	p.PartType, p.PartUUID, p.PartName = m.PartitionType, m.PartitionUUID, m.PartitionName
	p.FsUUID, p.Passno, p.Freq = m.UUID, m.Passno, m.Freq
}

// Subvolume is one btrfs subvolume and where it is mounted.
type Subvolume struct {
	Name       string `yaml:"name"`
	Mountpoint string `yaml:"mountpoint"`
}

// mountExtras carries a filesystem's mount options and subvolumes into the
// plan. main is the subvolume mounted at the filesystem's own mountpoint.
func mountExtras(mnt, opts string, svs []*Subvolume) (string, string, []storage.Subvolume) {
	var out []storage.Subvolume
	main := ""
	for _, sv := range svs {
		if sv == nil {
			continue
		}
		out = append(out, storage.Subvolume{Name: sv.Name, Mountpoint: sv.Mountpoint})
		if sv.Mountpoint == mnt {
			main = sv.Name
		}
	}
	return strings.TrimSpace(opts), main, out
}

// DiskMatch identifies a disk by its hardware. Every field given has to
// agree, and exactly one disk has to fit.
type DiskMatch struct {
	Serial string `yaml:"serial,omitempty"`
	WWN    string `yaml:"wwn,omitempty"`
	Model  string `yaml:"model,omitempty"`
	// Path is a device path, usually a /dev/disk/by-id or by-path link.
	Path string `yaml:"path,omitempty"`
}

func (m *DiskMatch) String() string {
	var parts []string
	for _, f := range [][2]string{{"serial", m.Serial}, {"wwn", m.WWN}, {"model", m.Model}, {"path", m.Path}} {
		if f[1] != "" {
			parts = append(parts, f[0]+" "+f[1])
		}
	}
	return strings.Join(parts, ", ")
}

// DiskInfo is what ResolveDisks needs to know about a disk on this machine.
type DiskInfo struct {
	Name, Path, Serial, WWN, Model string
}

// NeedsDisks reports whether any disk is named by a match, so the machine's
// disks have to be listed before the config can be planned.
func (c *Config) NeedsDisks() bool {
	for _, m := range c.TargetMedia {
		if m != nil && m.Match != nil {
			return true
		}
	}
	return false
}

// ResolveDisks gives every disk that carries a match the kernel name of the
// one disk that fits it. A match that fits no disk, or more than one, is an
// error: the install must not guess which disk to erase. The disk's
// partitions follow it — a child named after the disk's old name keeps its
// number (sda2 becomes nvme0n1p2), any other is named by its position.
func (c *Config) ResolveDisks(disks []DiskInfo) error {
	taken := map[string]string{}
	for _, m := range c.TargetMedia {
		if m == nil || m.Match == nil {
			continue
		}
		mt := m.Match
		if mt.String() == "" {
			return fmt.Errorf("config: disk %q has an empty match", m.Name)
		}
		path := ""
		if mt.Path != "" {
			p, err := filepath.EvalSymlinks(mt.Path)
			if err != nil {
				return fmt.Errorf("config: disk match path: %w", err)
			}
			path = p
		}
		var found []DiskInfo
		for _, d := range disks {
			if (mt.Serial == "" || strings.TrimSpace(d.Serial) == mt.Serial) &&
				(mt.WWN == "" || strings.EqualFold(strings.TrimSpace(d.WWN), mt.WWN)) &&
				(mt.Model == "" || strings.TrimSpace(d.Model) == mt.Model) &&
				(path == "" || d.Path == path) {
				found = append(found, d)
			}
		}
		switch len(found) {
		case 0:
			return fmt.Errorf("config: no disk on this machine matches %s", mt)
		case 1:
		default:
			var names []string
			for _, d := range found {
				names = append(names, d.Path)
			}
			return fmt.Errorf("config: %s matches several disks (%s); add a serial or a path to name one", mt, strings.Join(names, ", "))
		}
		name := found[0].Name
		if other, dup := taken[name]; dup {
			return fmt.Errorf("config: %s and %s both match /dev/%s", other, mt, name)
		}
		taken[name] = mt.String()
		old := m.Name
		m.Name = name
		// Counted as Plan counts them: an empty entry is not a partition.
		pos := 0
		for _, ch := range m.Children {
			if ch == nil {
				continue
			}
			pos++
			n := pos
			if old != "" && strings.HasPrefix(ch.Name, old) {
				n = partNumber(strings.TrimPrefix(ch.Name, old), n)
			}
			ch.Name = strings.TrimPrefix(storage.PartitionName("/dev/"+name, n), "/dev/")
		}
	}
	return nil
}

// RaidArray is one software RAID array: a name (its device is /dev/md/<name>,
// or /dev/mdN for an "mdN" name), a level (0, 1, 5, 6 or 10, with or without
// a "raid" prefix), and the filesystem it carries. Its members are the
// `type: raid` partitions whose `raid` field is this name.
type RaidArray struct {
	Name       string `yaml:"name"`
	Level      string `yaml:"level"`
	FsType     string `yaml:"fstype,omitempty"`
	Mountpoint string `yaml:"mountpoint,omitempty"`

	// As on a partition (see Media).
	MountOptions string       `yaml:"mount-options,omitempty"`
	Subvolumes   []*Subvolume `yaml:"subvolumes,omitempty"`

	// Encrypted puts a LUKS container on the array: its filesystem, or its
	// volume group, is then on the mapper.
	Encrypted bool `yaml:"encrypted,omitempty"`

	// As on a partition (see Media).
	CryptOptions string `yaml:"crypt-options,omitempty"`
	UUID         string `yaml:"uuid,omitempty"`
	Passno       *int   `yaml:"passno,omitempty"`
	Freq         int    `yaml:"freq,omitempty"`

	// Metadata is the array's superblock format: 1.2 (the default), 1.1,
	// 1.0 or 0.90.
	Metadata string `yaml:"metadata,omitempty"`

	// Partitions gives the array a partition table of its own, in place of
	// one filesystem: each entry is a partition cut on the array (its
	// devices are /dev/md0p1, /dev/md0p2, … or /dev/md/name1, …).
	Partitions []*Media `yaml:"partitions,omitempty"`

	// VolumeGroup makes the array an LVM physical volume of that group, in
	// place of a filesystem; Children are then the group's logical volumes
	// (which may also be declared on another physical volume of the group).
	VolumeGroup string   `yaml:"volumeGroup,omitempty"`
	Children    []*Media `yaml:"children,omitempty"`
}

// User is an account to create, in clr-installer's shape.
type User struct {
	Login    string   `yaml:"login,omitempty"`
	UserName string   `yaml:"username,omitempty"`
	Password string   `yaml:"password,omitempty"`
	Admin    bool     `yaml:"admin,omitempty"`
	SSHKeys  []string `yaml:"ssh-keys,omitempty"`
}

// Interface is one network interface, in clr-installer's shape. dhcp is
// a string ("true"/"false") because that is how clr-installer writes it.
type Interface struct {
	Name    string     `yaml:"name"`
	Addrs   []*Address `yaml:"addrs,omitempty"`
	DHCP    string     `yaml:"dhcp,omitempty"`
	Gateway string     `yaml:"gateway,omitempty"`
	DNS     string     `yaml:"dns,omitempty"`
	Domain  string     `yaml:"domain,omitempty"`

	// Bond, VLAN and Bridge make this a virtual interface created at boot
	// on top of others (at most one of the three), and Routes are static
	// routes out of it. These are slinstaller's own.
	Bond   *Bond    `yaml:"bond,omitempty"`
	VLAN   *VLAN    `yaml:"vlan,omitempty"`
	Bridge *Bridge  `yaml:"bridge,omitempty"`
	Routes []*Route `yaml:"routes,omitempty"`
}

// Bond joins several interfaces into one.
type Bond struct {
	Members []string `yaml:"members"`
	// Mode is the bonding mode: active-backup (the default), 802.3ad,
	// balance-rr, balance-xor, broadcast, balance-tlb or balance-alb.
	Mode string `yaml:"mode,omitempty"`
}

// VLAN is a tagged network on another interface.
type VLAN struct {
	ID   int    `yaml:"id"`
	Link string `yaml:"link"`
}

// Bridge joins interfaces at layer 2.
type Bridge struct {
	Members []string `yaml:"members,omitempty"`
}

// Route is a static route: a destination in CIDR form and its gateway.
type Route struct {
	To  string `yaml:"to"`
	Via string `yaml:"via"`
}

// Address is one IP on an interface. version is 0 for IPv4, 1 for IPv6,
// as clr-installer encodes it.
type Address struct {
	IP      string `yaml:"ip"`
	Netmask string `yaml:"netmask,omitempty"`
	Version int    `yaml:"version,omitempty"`
}

// Static reports whether the interface is statically configured: it has
// addresses and is not explicitly DHCP. clr-installer marks DHCP with
// dhcp:"true" and treats an interface with addresses as static otherwise,
// so an unset dhcp alongside addresses still means static.
func (i *Interface) Static() bool {
	return len(i.Addrs) > 0 && !strings.EqualFold(i.DHCP, "true")
}

// KernelArguments are command-line arguments to add or remove.
type KernelArguments struct {
	Add    []string `yaml:"add,omitempty"`
	Remove []string `yaml:"remove,omitempty"`
}

// Hook is a command run at a named point. chroot selects whether it runs
// inside the target or on the live system.
type Hook struct {
	Chroot bool   `yaml:"chroot,omitempty"`
	Cmd    string `yaml:"cmd,omitempty"`
}

// Load reads and parses a config file.
func Load(path string) (*Config, error) { return LoadWith(path, nil) }

// LoadWith is Load with storage aliases given outside the file
// (clr-installer's --block-device name:file): each replaces the file's
// alias of the same name, or adds one, before targetMedia is expanded, so
// one config can be pointed at another disk without editing it.
func LoadWith(path string, aliases []*StorageAlias) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var c Config
	if err := yaml.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err == nil {
		unknownKeys(&doc, reflect.TypeOf(c), "", &c.unknown)
	}
next:
	for _, a := range aliases {
		for _, have := range c.BlockDevices {
			if have != nil && have.Name == a.Name {
				have.File = a.File
				continue next
			}
		}
		c.BlockDevices = append(c.BlockDevices, a)
	}
	c.expandAliases()
	return &c, nil
}

// Save writes the config to path as YAML, in the clr-installer-compatible
// shape it was read in. A file written here loads back with Load and drives
// the same install. It never contains a LUKS passphrase — that is prompted
// or taken from the environment, never stored.
func Save(path string, c *Config) error {
	data, err := yaml.Marshal(c)
	if err != nil {
		return fmt.Errorf("config: marshal: %w", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("config: write %s: %w", path, err)
	}
	return nil
}

// KernelPackage maps the configured kernel to the xbps package that
// provides it, or "" to leave the base system's default kernel in place.
// clr-installer names a kernel by its Clear bundle (kernel-native,
// kernel-lts, kernel-kvm); those map to Sunlight's mainline (linux) or LTS
// (linux-lts) kernel. Any other value is taken as a literal package name,
// so a Sunlight-native config can request a specific kernel package.
func (c *Config) KernelPackage() string {
	k := strings.TrimSpace(c.Kernel)
	switch {
	case k == "":
		return ""
	case strings.Contains(k, "lts"):
		return "linux-lts"
	case strings.HasPrefix(k, "kernel-") || k == "kernel":
		return "linux"
	default:
		return k
	}
}

// aliasPartSuffix returns the partition-name suffix the kernel uses for a
// device file: "p" after a name that ends in a digit (nvme0n1 -> nvme0n1p1),
// "" otherwise (sda -> sda1). clr-installer keeps a table of prefixes for
// this (nvme, loop, mmcblk); the kernel's rule covers those and the rest.
func aliasPartSuffix(file string) string {
	// The kernel's own rule: a disk whose name ends in a digit (nvme0n1,
	// loop0, mmcblk0, md0) has its partitions set off by a "p".
	base := filepath.Base(file)
	if base != "" && base[len(base)-1] >= '0' && base[len(base)-1] <= '9' {
		return "p"
	}
	return ""
}

// expandVars replaces $name and ${name} in s using vars, like
// clr-installer's utils.ExpandVariables.
func expandVars(vars map[string]string, s string) string {
	// Longest name first: with aliases "disk" and "disk2", an unbraced
	// $disk2 must not be read as $disk followed by a 2 — which, in map
	// order, it sometimes was.
	names := make([]string, 0, len(vars))
	for k := range vars {
		names = append(names, k)
	}
	sort.Slice(names, func(i, j int) bool {
		if len(names[i]) != len(names[j]) {
			return len(names[i]) > len(names[j])
		}
		return names[i] < names[j]
	})
	for _, k := range names {
		s = strings.ReplaceAll(s, "${"+k+"}", vars[k])
		s = strings.ReplaceAll(s, "$"+k, vars[k])
	}
	return s
}

// expandAliases rewrites ${alias}/$alias references in targetMedia names
// using the block-devices declarations, so a clr-installer config written
// against aliases loads into concrete device names. A disk's name expands
// to the device's base name; its direct partition children expand to that
// base plus the kernel's partition suffix — the convention clr uses, where
// a disk is `${a}` and its partitions `${a}1`, `${a}2`.
func (c *Config) expandAliases() {
	if len(c.BlockDevices) == 0 {
		return
	}
	diskVars := map[string]string{} // alias -> nvme0n1
	partVars := map[string]string{} // alias -> nvme0n1p (base + suffix)
	for _, a := range c.BlockDevices {
		if a == nil || a.Name == "" || a.File == "" {
			continue
		}
		// A /dev/disk/by-id or by-path link names the disk it points at.
		file := a.File
		if real, err := filepath.EvalSymlinks(file); err == nil {
			file = real
		}
		base := filepath.Base(file)
		diskVars[a.Name] = base
		partVars[a.Name] = base + aliasPartSuffix(file)
	}
	if len(diskVars) == 0 {
		return
	}
	for _, m := range c.TargetMedia {
		if m == nil {
			continue
		}
		m.Name = expandVars(diskVars, m.Name)
		for _, ch := range m.Children {
			if ch == nil {
				continue
			}
			ch.Name = expandVars(partVars, ch.Name)
		}
	}
}

// hostnameRe is the conservative intersection of what Linux and DNS
// accept: letters, digits and hyphens, not starting or ending with one.
// lvmNameRe is a volume group or logical volume name LVM accepts; the
// name also goes into /dev/<vg>/<lv> paths and fstab, where a space
// breaks the line.
var lvmNameRe = regexp.MustCompile(`^[a-zA-Z0-9+_.][a-zA-Z0-9+_.-]{0,126}$`)

// validLVMName reports whether LVM takes name for a volume group, or with
// lv for a logical volume: lvmNameRe, not "." or "..", and for a logical
// volume none of the names LVM keeps for its own (lvm2's
// lib/misc/lvm-string.c).
func validLVMName(name string, lv bool) bool {
	if !lvmNameRe.MatchString(name) || name == "." || name == ".." {
		return false
	}
	if !lv {
		return true
	}
	if strings.HasPrefix(name, "snapshot") || strings.HasPrefix(name, "pvmove") {
		return false
	}
	for _, r := range []string{"_cdata", "_cmeta", "_corig", "_cpool", "_cvol", "_wcorig", "_mimage", "_mlog",
		"_rimage", "_rmeta", "_tdata", "_tmeta", "_vdata", "_imeta", "_iorig", "_pmspare", "_vorigin"} {
		if strings.Contains(name, r) {
			return false
		}
	}
	return true
}

// domainRe is a DNS domain: dot-separated labels of letters, digits and
// hyphens.
var domainRe = regexp.MustCompile(`^([a-zA-Z0-9]([a-zA-Z0-9-]*[a-zA-Z0-9])?\.)*[a-zA-Z0-9]([a-zA-Z0-9-]*[a-zA-Z0-9])?\.?$`)

var hostnameRe = regexp.MustCompile(`^[a-zA-Z0-9]([a-zA-Z0-9-]*[a-zA-Z0-9])?$`)

// loginRe is what useradd accepts for a login name.
var loginRe = regexp.MustCompile(`^[a-z_][a-z0-9_-]*$`)

// Limits on accounts, clr-installer's: a login of at most 31 characters,
// and a password of 8 to 255.
const (
	MaxLoginLength    = 31
	MinPasswordLength = 8
	MaxPasswordLength = 255

	// A LUKS passphrase: 8 to 94 characters, clr-installer's limits.
	MinPassphraseLength = 8
	MaxPassphraseLength = 94
)

// reservedLogins are the names the base system already uses for an
// account or a group (base-files' passwd and group). useradd makes a group
// named after each new account, so a login taking one of these fails
// partway through the install; clr-installer refuses such names too.
var reservedLogins = map[string]bool{}

func init() {
	for _, n := range strings.Fields(`root nobody bin sys kmem wheel tty tape daemon floppy disk lp
		dialout audio video utmp adm cdrom optical mail storage scanner network kvm input plugdev
		usbmon sgx nogroup users xbuilder`) {
		reservedLogins[n] = true
	}
}

// ValidLogin checks a login name as clr-installer does: present, a valid
// Unix name, no longer than MaxLoginLength, and not one the base system
// already uses.
// ValidHostname checks a host name the way Validate does, for a front-end
// to ask again rather than fail after it has asked everything.
func ValidHostname(h string) error {
	if len(h) > 63 || !hostnameRe.MatchString(h) {
		return fmt.Errorf("hostname %q must be 1-63 characters of letters, digits and hyphens", h)
	}
	return nil
}

// SizeBytes parses a size as the config does ("60G", "59.5G", "512M"),
// for a front-end to read one the same way the install will.
func SizeBytes(s string) (uint64, error) { return parseSize(s) }

// ValidSwapSize checks a swap size a front-end was given: "auto", or a
// size with a unit, more than nothing. ("0" means "fill the disk" for a partition,
// which a swap before the root cannot.)
func ValidSwapSize(s string) error {
	if strings.EqualFold(strings.TrimSpace(s), autoSize) { // from the memory (ResolveAutoSwap)
		return nil
	}
	n, err := parseSize(s)
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("a swap of size 0 is no swap; choose none instead")
	}
	return nil
}

func ValidLogin(login string) error {
	switch {
	case login == "":
		return fmt.Errorf("no login")
	case !loginRe.MatchString(login):
		return fmt.Errorf("login %q must be a valid Unix name (lowercase letter or _ then letters, digits, - or _)", login)
	case len(login) > MaxLoginLength:
		return fmt.Errorf("login %q is longer than %d characters", login, MaxLoginLength)
	case reservedLogins[login]:
		return fmt.Errorf("login %q is already a system account or group", login)
	}
	return nil
}

// ValidPassphrase checks a new LUKS passphrase as clr-installer does: its
// length, and printable 7-bit characters only, as it is typed at boot, before
// a keyboard layout or a character set is loaded.
func ValidPassphrase(p string) error {
	switch n := len(p); {
	case n < MinPassphraseLength:
		return fmt.Errorf("passphrase must be at least %d characters", MinPassphraseLength)
	case n > MaxPassphraseLength:
		return fmt.Errorf("passphrase must be at most %d characters", MaxPassphraseLength)
	}
	for _, r := range p {
		if r < 0x20 || r > 0x7e {
			return fmt.Errorf("passphrase may only contain printable ASCII characters")
		}
	}
	return nil
}

// ValidPassword checks a plaintext password against clr-installer's length
// limits, before it is hashed (the TUI's account form, --genpass).
func ValidPassword(pw string) error {
	switch n := len(pw); {
	case n < MinPasswordLength:
		return fmt.Errorf("password must be at least %d characters", MinPasswordLength)
	case n > MaxPasswordLength:
		return fmt.Errorf("password must be at most %d characters", MaxPasswordLength)
	}
	return nil
}

// looksHashed reports whether s is a crypt(3) hash ($id$...$...) rather
// than a plaintext password, so a config cannot carry a password in the
// clear even by accident.
func looksHashed(s string) bool {
	return strings.HasPrefix(s, "$") && strings.Count(s, "$") >= 3
}

// Validate checks what must hold for the config to describe a bootable
// UEFI install, before any disk is touched. It reports every problem it
// finds at once, so an operator fixing a config by hand does not have to
// run it once per mistake.
//
// It stays within clr-installer compatibility: it does not require fields
// a valid clr config may omit (a login account, a hostname), only
// rejects layouts that cannot install or boot.
func (c *Config) Validate() error {
	if c.SkipValidationAll {
		return nil
	}
	if c.DiskImage != "" {
		_, err := c.DiskImageTarget()
		return err
	}

	disks, err := c.installDisks()
	if err != nil {
		return err
	}

	var probs []string
	add := func(format string, a ...any) { probs = append(probs, fmt.Sprintf(format, a...)) }
	rootMin, rootMinLabel := c.minRoot()

	// A clr-installer alias whose file is not a device asks for an install
	// into an image file (made, loop-mounted, kept with keepImage), which
	// slinstaller does not do. Said here, rather than reading the file name
	// as a device path (/dev/release.img) and failing later.
	for _, al := range c.BlockDevices {
		if al != nil && al.File != "" && !strings.HasPrefix(al.File, "/dev/") {
			add("block-devices: %s points at %s, an image file; installing into an image is not supported (name a device such as /dev/sda)", al.Name, al.File)
		}
	}

	checkDetails := func(where string, m *Media, physical bool) {
		if strings.ContainsAny(m.CryptOptions, " \t") {
			add("%s: crypt-options %q must be comma-separated, without spaces", where, m.CryptOptions)
		}
		if m.CryptOptions != "" && m.Type != "crypt" && !m.Encrypted {
			add("%s: crypt-options on a volume that is not encrypted", where)
		}
		if !physical && (m.PartitionType != "" || m.PartitionUUID != "" || m.PartitionName != "") {
			add("%s: partition-type, partition-uuid and partition-name are for partitions, not logical volumes", where)
		}
		for what, v := range map[string]string{"partition-type": m.PartitionType, "partition-uuid": m.PartitionUUID} {
			if v != "" && !uuidRe.MatchString(v) {
				add("%s: %s %q is not a GUID", where, what, v)
			}
		}
		if strings.ContainsAny(m.PartitionName, "\"'\n") || len(m.PartitionName) > 36 {
			add("%s: partition-name %q is not a name a GPT entry can carry (36 characters, no quotes)", where, m.PartitionName)
		}
		switch {
		case m.UUID == "":
		case m.FsType == "vfat" && !vfatIDRe.MatchString(m.UUID):
			add("%s: uuid %q is not a vfat volume id (e.g. 1A2B-3C4D)", where, m.UUID)
		case m.FsType != "vfat" && !uuidRe.MatchString(m.UUID):
			add("%s: uuid %q is not a UUID", where, m.UUID)
		}
		if m.Passno != nil && (*m.Passno < 0 || *m.Passno > 2) || m.Freq < 0 || m.Freq > 9 {
			add("%s: passno is 0, 1 or 2 and freq 0 to 9", where)
		}
	}
	for _, d := range c.TargetMedia {
		if d == nil {
			continue
		}
		for _, ch := range d.Children {
			if ch == nil {
				continue
			}
			checkDetails(ch.Name, ch, true)
			// `encrypted` is read on an lvm partition only. Anywhere else it
			// was dropped without a word and the volume installed in the
			// clear — refused, with what does encrypt it.
			if ch.Encrypted && !c.ReusePartitions {
				switch ch.Type {
				case "lvm", "crypt":
				case "raid":
					add("%s: encrypted is not read on a RAID member; set encrypted on raid %q instead", ch.Name, ch.Raid)
				default:
					add("%s: encrypted is read only on an lvm partition; use type: crypt to encrypt this partition", ch.Name)
				}
			}
			for _, lv := range ch.Children {
				if lv != nil {
					checkDetails(ch.Name+"/"+lv.Name, lv, false)
					if lv.Encrypted {
						add("%s/%s: a logical volume is not encrypted on its own; set encrypted on the lvm partition %s", ch.Name, lv.Name, ch.Name)
					}
				}
			}
		}
	}
	for _, a := range c.Raid {
		if a == nil {
			continue
		}
		switch a.Metadata {
		case "", "1.2", "1.1", "1.0", "0.90":
		default:
			add("raid %s: metadata %q is not 1.2, 1.1, 1.0 or 0.90", a.Name, a.Metadata)
		}
		checkDetails("raid "+a.Name, &Media{FsType: a.FsType, UUID: a.UUID, Passno: a.Passno, Freq: a.Freq,
			CryptOptions: a.CryptOptions, Encrypted: a.Encrypted}, true)
		for _, lv := range a.Children {
			if lv != nil {
				checkDetails("raid "+a.Name+"/"+lv.Name, lv, false)
			}
		}
	}
	for _, d := range c.TargetMedia {
		if d == nil {
			continue
		}
		inFree := 0
		for _, ch := range d.Children {
			if ch == nil {
				continue
			}
			if ch.Create && strings.TrimSpace(ch.Start) == "" {
				if inFree++; inFree == 2 {
					add("%s: only one partition per disk can be created without a start (in the largest free region); give the others a start", d.Name)
				}
			}
			if ch.Resize == "" {
				continue
			}
			switch {
			case !c.ReusePartitions:
				add("%s: resize is for a reusePartitions install; a normal install repartitions the disk", ch.Name)
			case !ch.Preserve || ch.Create:
				add("%s: resize keeps what is on the partition, so it has to be an existing one with preserve: true", ch.Name)
			case !storage.Resizable(ch.FsType):
				add("%s: only ext2, ext3, ext4, ntfs and btrfs can be resized, not %q", ch.Name, ch.FsType)
			}
			if mib, err := offsetMiB(ch.Resize); err != nil || mib == 0 {
				add("%s: resize %q is not a size", ch.Name, ch.Resize)
			}
		}
	}
	if c.MessageURL != "" && !strings.HasPrefix(c.MessageURL, "http://") && !strings.HasPrefix(c.MessageURL, "https://") {
		add("message-url: %s is not an http or https URL", c.MessageURL)
	}
	if c.ReportURL != "" && !strings.HasPrefix(c.ReportURL, "http://") && !strings.HasPrefix(c.ReportURL, "https://") {
		add("report-url: %s is not an http or https URL", c.ReportURL)
	} else if strings.HasPrefix(c.ReportURL, "http://") && !c.AllowInsecureHTTP {
		// A report carries the hostname and the install's errors.
		add("report-url: %s is plain http; use https, or set allowInsecureHTTP", c.ReportURL)
	}
	if err := c.checkRemote("image", c.Image); err != nil {
		add("%v", err)
	} else if IsRemote(c.Image) {
		switch strings.ToLower(filepath.Ext(c.Image)) {
		case ".squashfs", ".sqfs", ".sfs":
			add("image: a squashfs cannot be unpacked as it downloads; fetch %s first, or use a tar image", c.Image)
		}
	}
	for _, m := range c.TargetMedia {
		if m == nil {
			continue
		}
		switch m.Wipe {
		case "", "superblock":
		case "zero", "random", "discard":
			if c.ReusePartitions {
				add("%s: wipe: %s would erase the partitions reusePartitions keeps", m.Name, m.Wipe)
			}
		default:
			add("%s: wipe %q is not superblock, zero, random or discard", m.Name, m.Wipe)
		}
	}
	if c.autoSwap() {
		add("a swap size of \"auto\" has to be worked out from this machine's memory first (ResolveAutoSwap)")
	}

	for _, k := range c.KernelRemove {
		if k == "" || strings.ContainsAny(k, " \t/") || strings.HasPrefix(k, "-") {
			add("kernel-remove: %q is not a package name", k)
		}
		if k != "" && k == c.KernelPackage() {
			add("kernel-remove: %s is the kernel the config installs", k)
		}
	}
	if c.KernelFallback != "" && c.KernelPackage() == "" {
		add("kernel-fallback: there is no kernel named for %s to fall back from", c.KernelFallback)
	}
	switch c.InstallType {
	case "", "server", "desktop":
	default:
		add("install-type: %q is not server or desktop", c.InstallType)
	}
	if c.PowerStateDelay < 0 || c.PowerStateDelay > 3600 {
		add("power-state-delay: %d is not between 0 and 3600 seconds", c.PowerStateDelay)
	}
	for stage := range c.StageHooks {
		known := false
		for _, n := range StageNames[1 : len(StageNames)-1] {
			known = known || n == stage
		}
		if !known {
			add("stage-hooks: %q is not a stage hooks can follow (%s)", stage, strings.Join(StageNames[1:len(StageNames)-1], ", "))
		}
	}
	switch c.PowerState {
	case "", "reboot", "poweroff", "halt", "kexec":
	default:
		add("power-state: %q is not reboot, poweroff, halt or kexec", c.PowerState)
	}
	if c.SystemUpgrade && c.Offline {
		add("system-upgrade needs the repositories; it cannot be done offline")
	}
	wrote := map[string]bool{}
	for _, w := range c.WriteFiles {
		if w == nil {
			continue
		}
		clean := filepath.Clean(w.Path)
		switch {
		case !strings.HasPrefix(w.Path, "/") || clean == "/":
			add("write-files: %q is not an absolute file path", w.Path)
		case wrote[clean]:
			add("write-files: %s is written twice", clean)
		}
		wrote[clean] = true
		if _, err := w.Mode(); err != nil {
			add("write-files: %s: %v", w.Path, err)
		}
	}

	if b := c.Bootloader; b != nil {
		switch b.Terminal {
		case "", "console", "serial":
		default:
			add("bootloader: terminal %q is not console or serial", b.Terminal)
		}
		if b.UpdateNVRAM != nil && !*b.UpdateNVRAM && !b.Removable {
			add("bootloader: update-nvram: false leaves the firmware nothing to boot; set removable: true as well")
		}
	}

	// Disks and partitions go by their bare kernel name (sda, nvme0n1p2); a
	// path here would be planned as /dev//dev/sda. A disk named by a match
	// has to have been resolved (ResolveDisks) before it can be checked.
	for _, m := range c.TargetMedia {
		if m == nil {
			continue
		}
		if m.Match != nil && m.Type != "" && m.Type != "disk" {
			add("%s: match names a disk, not a %s", m.Name, m.Type)
		}
		for _, x := range append([]*Media{m}, m.Children...) {
			if x == nil {
				continue
			}
			if strings.Contains(x.Name, "/") {
				add("%s: give the device's name (e.g. sda), not a path; to name a disk by path use match: {path: ...}", x.Name)
			}
			if x != m && x.Match != nil {
				add("%s: match is for a disk; a partition follows its disk", x.Name)
			}
		}
	}

	// The console log level is written to sysctl.d and the kernel command
	// line as given, so a value outside the kernel's 0-7 range is refused here.
	if lv := c.ConsoleLogLevel; lv != nil && (*lv < 0 || *lv > 7) {
		add("console-log-level: %d is not a kernel log level (0 emergency to 7 debug)", *lv)
	}

	// checkMin flags a fixed partition/volume size below a floor, unless size
	// validation is turned off. A missing size, 0 or "rest" (fill) is not a
	// fixed size and is left to the disk-fit check at install time.
	checkMin := func(where, sizeStr string, min uint64, minLabel, what string) {
		// Sizes are meaningless in a reuse install (the partitions already
		// exist), and off when size validation is skipped.
		if c.SkipValidationSize || c.ReusePartitions {
			return
		}
		s := strings.TrimSpace(sizeStr)
		if s == "" || s == "0" || strings.EqualFold(s, "rest") {
			return
		}
		b, err := parseSize(s)
		if err != nil {
			return // the size format is reported elsewhere
		}
		if b < min {
			add("%s: %s %q is below the %s minimum", where, what, s, minLabel)
		}
	}

	roots, esps := 0, 0
	seen := map[string]bool{}
	spareESPs, espAtBoot := 0, false
	raidMembers := map[string]int{} // members declared per array name
	raidSpares := map[string]int{}

	// checkFS validates a filesystem-bearing entry (a plain partition, a
	// logical volume or a RAID array): the filesystem is known, the
	// mountpoint is absolute and unique, and a root mount is counted.
	checkFS := func(where, fsType, mnt string) {
		switch {
		case fsType == "":
			add("%s: no filesystem", where)
		case !storage.KnownFilesystem(fsType):
			add("%s: filesystem %q is not supported", where, fsType)
		}
		switch {
		case fsType == "swap" && mnt != "":
			// A swap area is not mounted: counted as / it would pass for
			// the root the install needs and leave none.
			add("%s: swap is not mounted; drop mountpoint %q", where, mnt)
			return
		case fsType == "vfat" && mnt == "/":
			add("%s: vfat cannot hold the root filesystem (no owners, modes or links)", where)
		}
		if mnt != "" {
			if !strings.HasPrefix(mnt, "/") {
				add("%s: mountpoint %q is not absolute", where, mnt)
			} else if p := mountpointProblem(mnt); p != "" {
				add("%s: %s", where, p)
			}
			if seen[mnt] {
				add("%s: mountpoint %q is used more than once", where, mnt)
			}
			seen[mnt] = true
		}
		if mnt == "/" {
			roots++
		}
	}

	// checkLVs checks the logical volumes declared on one physical volume of
	// a group. A group can have several physical volumes; its volumes may be
	// declared on any of them, so names and "fill" are counted per group.
	lvNames := map[string]bool{}
	lvFills := map[string]int{}
	// pvCrypt records, per volume group, whether its physical volumes are
	// encrypted: a group with both kinds spreads its volumes — the root,
	// /boot, the keyfile the initramfs carries — over a plain one too.
	pvCrypt := map[string]map[bool]string{}
	notePV := func(vg string, encrypted bool, where string) {
		if vg == "" {
			return
		}
		if pvCrypt[vg] == nil {
			pvCrypt[vg] = map[bool]string{}
		}
		if _, ok := pvCrypt[vg][encrypted]; !ok {
			pvCrypt[vg][encrypted] = where
		}
	}
	var checkLVs func(where, vg string, children []*Media)

	// checkMount checks a filesystem's fstab options and btrfs subvolumes.
	// The subvolume at the filesystem's own mountpoint was counted by
	// checkFS; the others are mountpoints in their own right.
	checkMount := func(where, fsType, mnt, opts string, svs []*Subvolume, preserve bool) {
		if strings.ContainsAny(opts, " \t\n") {
			add("%s: mount-options %q must be comma-separated, without spaces", where, opts)
		}
		if opts != "" && mnt == "" && fsType != "swap" {
			add("%s: mount-options on a filesystem that is not mounted", where)
		}
		if len(svs) == 0 {
			return
		}
		if fsType != "btrfs" {
			add("%s: subvolumes need a btrfs filesystem, not %q", where, fsType)
			return
		}
		if preserve {
			add("%s: subvolumes cannot be made on a preserved filesystem", where)
		}
		names, main := map[string]bool{}, false
		for _, sv := range svs {
			if sv == nil {
				continue
			}
			switch {
			case sv.Name == "" || strings.ContainsAny(sv.Name, "/, \t"):
				add("%s: subvolume name %q must be a single path component", where, sv.Name)
			case names[sv.Name]:
				add("%s: subvolume %q is listed twice", where, sv.Name)
			}
			names[sv.Name] = true
			switch {
			case sv.Mountpoint == mnt && mnt != "":
				main = true
			case !strings.HasPrefix(sv.Mountpoint, "/"):
				add("%s: subvolume %q: mountpoint %q is not absolute", where, sv.Name, sv.Mountpoint)
			case mountpointProblem(sv.Mountpoint) != "":
				add("%s: subvolume %q: %s", where, sv.Name, mountpointProblem(sv.Mountpoint))
			case seen[sv.Mountpoint]:
				add("%s: mountpoint %q is used more than once", where, sv.Mountpoint)
			default:
				seen[sv.Mountpoint] = true
				if sv.Mountpoint == "/" {
					roots++
				}
			}
		}
		if !main {
			add("%s: one subvolume must be mounted at the filesystem's own mountpoint (%q)", where, mnt)
		}
	}

	checkLVs = func(where, vg string, children []*Media) {
		for j, lv := range children {
			if lv == nil {
				continue
			}
			lvWhere := fmt.Sprintf("%s/%s", where, lv.Name)
			if lv.Name == "" {
				lvWhere = fmt.Sprintf("%s/lv%d", where, j+1)
			} else if lvNames[vg+"/"+lv.Name] {
				add("%s: logical volume %q is declared twice in group %s", where, lv.Name, vg)
			}
			lvNames[vg+"/"+lv.Name] = true
			if lv.Name != "" && !validLVMName(lv.Name, true) {
				add("%s: logical volume name %q is not one LVM takes (letters, digits and + _ . -, not starting with -, not a name LVM keeps)", where, lv.Name)
			}
			checkFS(lvWhere, lv.FsType, lv.Mountpoint)
			checkMount(lvWhere, lv.FsType, lv.Mountpoint, lv.MountOptions, lv.Subvolumes, false)
			if sz := strings.TrimSpace(lv.Size); sz != "" && sz != "0" && !strings.EqualFold(sz, "rest") {
				if _, err := parseSize(sz); err != nil {
					add("%s: size %q is not a byte count or a number with a K/M/G/T unit", lvWhere, sz)
				}
			}
			checkMin(lvWhere, lv.Size, storage.MinPartitionSize, "1 MiB", "size")
			if lv.Mountpoint == "/" {
				checkMin(lvWhere, lv.Size, rootMin, rootMinLabel, "root size")
			}
			if s := strings.TrimSpace(lv.Size); s == "" || s == "0" || strings.EqualFold(s, "rest") {
				if lvFills[vg]++; lvFills[vg] == 2 {
					add("%s: more than one logical volume fills the group", where)
				}
			}
		}
	}

	for _, disk := range disks {
		for i, ch := range disk.Children {
			if ch == nil {
				continue
			}
			where := ch.Name
			if where == "" {
				where = fmt.Sprintf("partition %d", i+1)
			}

			// A reuse install writes into existing partitions, so it cannot
			// create the on-disk structures crypt/lvm/raid need: only plain
			// partitions are allowed, and the rest is validated as usual
			// (filesystem, mountpoint, ESP/root counting) so the same checks
			// apply whether or not the disk is wiped.
			if c.ReusePartitions {
				if (ch.Type != "" && ch.Type != "part") || ch.Encrypted {
					add("%s: reusePartitions supports only plain partitions (no crypt, lvm, raid or encrypted)", where)
					continue
				}
				// A partition that is kept and not mounted is only being
				// stepped around (or shrunk): what it holds need not be a
				// filesystem slinstaller can make.
				if !(ch.Preserve && ch.Mountpoint == "") {
					checkFS(where, ch.FsType, ch.Mountpoint)
				}
				checkMount(where, ch.FsType, ch.Mountpoint, ch.MountOptions, ch.Subvolumes, ch.Preserve)
				if ch.FsType == "vfat" && (ch.Mountpoint == "/boot" || ch.Mountpoint == "/boot/efi") {
					esps++
				}
				continue
			}

			// An lvm partition carries no filesystem itself; its logical
			// volumes do. Validate those, and the group it names.
			if ch.Type == "lvm" {
				if ch.VolumeGroup == "" {
					add("%s: lvm partition has no volumeGroup", where)
				} else if !validLVMName(ch.VolumeGroup, false) {
					add("%s: volume group name %q is not one LVM takes (letters, digits and + _ . -, not starting with -)", where, ch.VolumeGroup)
				}
				checkMin(where, ch.Size, storage.MinPartitionSize, "1 MiB", "physical-volume size")
				checkLVs(where, ch.VolumeGroup, ch.Children)
				notePV(ch.VolumeGroup, ch.Encrypted, where)
				continue
			}

			// A RAID member carries no filesystem — the array does. Record
			// it against its array; the arrays are validated below.
			if ch.Type == "raid" {
				if ch.Raid == "" {
					add("%s: raid member names no array (raid:)", where)
				} else {
					if ch.Spare {
						raidSpares[ch.Raid]++
					} else {
						raidMembers[ch.Raid]++
					}
				}
				continue
			}

			if ch.ESP {
				spareESPs++
				if ch.FsType != "vfat" || ch.Mountpoint != "" || ch.Type == "crypt" {
					add("%s: a spare ESP (esp: true) is a plain vfat partition with no mountpoint", where)
				}
				checkMin(where, ch.Size, storage.MinESPSize, "100 MB", "ESP size")
				continue
			}
			checkFS(where, ch.FsType, ch.Mountpoint)
			checkMount(where, ch.FsType, ch.Mountpoint, ch.MountOptions, ch.Subvolumes, ch.Preserve)
			checkMin(where, ch.Size, storage.MinPartitionSize, "1 MiB", "size")
			if ch.Mountpoint == "/" {
				checkMin(where, ch.Size, rootMin, rootMinLabel, "root size")
			}

			if ch.FsType == "vfat" && (ch.Mountpoint == "/boot" || ch.Mountpoint == "/boot/efi") {
				esps++
				espAtBoot = espAtBoot || ch.Mountpoint == "/boot"
				checkMin(where, ch.Size, storage.MinESPSize, "100 MB", "ESP size")
				if ch.Type == "crypt" {
					add("%s: the EFI System Partition cannot be encrypted", where)
				}
			}
		}
	}

	// RAID arrays: a known level with enough members, a supported
	// filesystem, and not the ESP (firmware reads a plain partition).
	declared := map[string]bool{}
	for i, a := range c.Raid {
		if a == nil {
			continue
		}
		where := a.Name
		if where == "" {
			where = fmt.Sprintf("raid[%d]", i)
			add("%s: array has no name", where)
		}
		declared[a.Name] = true
		if !mdadm.KnownLevel(a.Level) {
			add("raid %s: level %q is not supported (use 0, 1, 5, 6 or 10)", where, a.Level)
		} else if n := raidMembers[a.Name]; n < mdadm.MinMembers(a.Level) {
			add("raid %s: level %s needs at least %d members, has %d",
				where, a.Level, mdadm.MinMembers(a.Level), n)
		}
		if raidSpares[a.Name] > 0 && strings.TrimPrefix(a.Level, "raid") == "0" {
			add("raid %s: level 0 has no redundancy to rebuild onto a spare", where)
		}
		if len(a.Partitions) > 0 {
			if a.FsType != "" || a.Mountpoint != "" || a.VolumeGroup != "" || a.Encrypted {
				add("raid %s: an array with partitions carries nothing else itself: the filesystems go on the partitions", where)
			}
			for j, ch := range a.Partitions {
				if ch == nil {
					continue
				}
				pw := fmt.Sprintf("raid %s partition %d", where, j+1)
				if ch.Type != "" && ch.Type != "part" || ch.Encrypted || len(ch.Children) > 0 {
					add("%s: a partition on an array is a plain one (no crypt, lvm or raid)", pw)
				}
				if ch.FsType == "vfat" && (ch.Mountpoint == "/boot" || ch.Mountpoint == "/boot/efi") {
					add("%s: the EFI System Partition cannot be on RAID", pw)
					continue
				}
				checkFS(pw, ch.FsType, ch.Mountpoint)
				checkMount(pw, ch.FsType, ch.Mountpoint, ch.MountOptions, ch.Subvolumes, false)
				checkMin(pw, ch.Size, storage.MinPartitionSize, "1 MiB", "size")
				if ch.Mountpoint == "/" {
					checkMin(pw, ch.Size, rootMin, rootMinLabel, "root size")
				}
			}
			continue
		}
		if a.VolumeGroup != "" && !validLVMName(a.VolumeGroup, false) {
			add("raid %s: volume group name %q is not one LVM takes", where, a.VolumeGroup)
		}
		if a.VolumeGroup != "" {
			if a.FsType != "" || a.Mountpoint != "" {
				add("raid %s: an array that is a physical volume (volumeGroup) carries no filesystem of its own; put it on a logical volume", where)
			}
			checkLVs("raid "+where, a.VolumeGroup, a.Children)
			notePV(a.VolumeGroup, a.Encrypted, "raid "+where)
		} else if len(a.Children) > 0 {
			add("raid %s: children are logical volumes and need a volumeGroup", where)
		} else if a.FsType == "vfat" && (a.Mountpoint == "/boot" || a.Mountpoint == "/boot/efi") {
			add("raid %s: the EFI System Partition cannot be on RAID", where)
		} else {
			checkFS("raid "+where, a.FsType, a.Mountpoint)
			checkMount("raid "+where, a.FsType, a.Mountpoint, a.MountOptions, a.Subvolumes, false)
		}
	}
	// A spare ESP gets a copy of the bootloader, not of the kernels. With
	// the ESP at /boot the kernels live on it and a copy would go stale at
	// the first kernel update; at /boot/efi it holds GRUB alone, and /boot
	// is on the (redundant) root.
	if spareESPs > 0 && c.ReusePartitions {
		add("spare ESPs (esp: true) are for an install that partitions the disks, not reusePartitions")
	}
	if spareESPs > 0 && espAtBoot {
		add("spare ESPs (esp: true) need the main ESP at /boot/efi, not /boot: at /boot the kernels would be on one disk only")
	}

	for vg, kinds := range pvCrypt {
		if len(kinds) == 2 {
			add("volume group %s has an encrypted physical volume (%s) and a plain one (%s): what is on it — the root, the key the initramfs carries — can land on the plain one; encrypt all of them or none", vg, kinds[true], kinds[false])
		}
	}

	for name := range raidMembers {
		if !declared[name] {
			add("raid member references undeclared array %q", name)
		}
	}

	switch roots {
	case 0:
		add("no partition mounts /")
	case 1:
	default:
		add("%d partitions mount /", roots)
	}
	switch {
	case esps == 0:
		add("no EFI System Partition (vfat at /boot or /boot/efi); Sunlight Linux is UEFI-only")
	}

	logins := map[string]bool{}
	for i, u := range c.Users {
		if u == nil {
			continue
		}
		who := u.Login
		if who == "" {
			add("users[%d]: no login", i)
			continue
		}
		if err := ValidLogin(u.Login); err != nil {
			add("users[%d]: %v", i, err)
		}
		if logins[u.Login] {
			add("users[%d]: login %q is used more than once", i, u.Login)
		}
		logins[u.Login] = true
		// The full name goes to useradd -c, which refuses a colon or a
		// line break ("invalid comment") — at first boot, after the disk
		// was wiped and the base installed.
		if strings.ContainsAny(u.UserName, ":\n\r") {
			add("users[%d] (%s): the full name %q cannot hold a colon or a line break", i, u.Login, u.UserName)
		}
		if u.Password != "" && !looksHashed(u.Password) {
			add("users[%d] (%s): password does not look like a crypt(3) hash - a config must carry a hash, not a plaintext password", i, u.Login)
		}
	}

	// An administrator makes the install lock root's password and give
	// wheel sudo — with a password. When no administrator has one, nobody
	// can become root on the installed system: root is locked, and sudo
	// asks every admin for a password they do not have (an SSH key logs
	// them in, it does not answer sudo).
	admins, adminWithPassword := []string{}, false
	for _, u := range c.Users {
		if u != nil && u.Admin && u.Login != "" {
			admins = append(admins, u.Login)
			adminWithPassword = adminWithPassword || u.Password != ""
		}
	}
	// A config that sees to sudo itself — a sudoers file of its own, or a
	// hook that can write one — is trusted to: a key-only administrator
	// with NOPASSWD is the usual headless setup.
	ownSudo := len(c.PostInstall) > 0 || len(c.StageHooks) > 0
	for _, w := range c.WriteFiles {
		if w != nil && strings.HasPrefix(path.Clean("/"+w.Path), "/etc/sudoers") {
			ownSudo = true
		}
	}
	if len(admins) > 0 && !adminWithPassword && !ownSudo {
		add("no administrator has a password (%s): root is locked once there is an administrator, and sudo asks for one - give an administrator a password hash", strings.Join(admins, ", "))
	}

	if c.Hostname != "" {
		if err := ValidHostname(c.Hostname); err != nil {
			add("%v", err)
		}
	}
	if c.SwapFileSize != "" {
		if _, err := parseSize(c.SwapFileSize); err != nil {
			add("swapFileSize: %v", err)
		}
	}

	// Network interfaces: a statically configured one must carry usable
	// addresses (and a valid gateway, if any), checked here so a typo fails
	// before any disk is touched rather than partway through the install.
	memberOf := map[string]string{}
	for i, ni := range c.NetworkInterfaces {
		if ni == nil {
			continue
		}
		who := ni.Name
		if who == "" {
			who = fmt.Sprintf("networkInterfaces[%d]", i)
			add("%s: no name", who)
		}
		kinds := 0
		taken := func(members []string, what string) {
			if len(members) == 0 && what == "bond" {
				add("%s: a bond needs at least one member", who)
			}
			for _, m := range members {
				if !network.ValidIfName(m) {
					add("%s: %s member %q is not an interface name", who, what, m)
				}
				if memberOf[m] != "" {
					add("%s: %s is already a member of %s", who, m, memberOf[m])
				}
				memberOf[m] = ni.Name
			}
		}
		if ni.Bond != nil {
			kinds++
			taken(ni.Bond.Members, "bond")
			if !network.ValidBondMode(ni.Bond.Mode) {
				add("%s: bond mode %q is not one of %s", who, ni.Bond.Mode, strings.Join(network.BondModes, ", "))
			}
		}
		if ni.Bridge != nil {
			kinds++
			taken(ni.Bridge.Members, "bridge")
		}
		if ni.VLAN != nil {
			kinds++
			if ni.VLAN.ID < 1 || ni.VLAN.ID > 4094 {
				add("%s: VLAN id %d is not between 1 and 4094", who, ni.VLAN.ID)
			}
			if !network.ValidIfName(ni.VLAN.Link) {
				add("%s: a VLAN needs the interface it is on (link)", who)
			}
		}
		if kinds > 1 {
			add("%s: an interface is a bond, a VLAN or a bridge, not several", who)
		}
		if ni.Name != "" && !network.ValidIfName(ni.Name) {
			add("%s: not an interface name (1 to 15 letters, digits and . _ : -)", who)
		}
		for _, r := range ni.Routes {
			if r == nil {
				continue
			}
			if _, _, err := net.ParseCIDR(r.To); err != nil {
				add("%s: route to %q is not in CIDR form (e.g. 10.1.0.0/16)", who, r.To)
			}
			if net.ParseIP(r.Via) == nil {
				add("%s: route via %q is not a valid IP", who, r.Via)
			}
			// dhcpcd's static routes are IPv4 only; an IPv6 one was dropped
			// at boot ("invalid IP address") — IPv6 routes come from router
			// advertisements.
			if ip, _, err := net.ParseCIDR(r.To); err == nil && ip.To4() == nil {
				add("%s: route to %s is IPv6; dhcpcd takes static routes for IPv4 only (IPv6 routes come from router advertisements)", who, r.To)
			}
		}
		if len(ni.Routes) > 0 && !ni.Static() {
			add("%s: static routes need a static address on the interface", who)
		}
		if !ni.Static() {
			continue
		}
		for j, a := range ni.Addrs {
			if a == nil {
				continue
			}
			if err := network.ValidAddr(a.IP, a.Netmask, a.Version == 1); err != nil {
				add("%s: address %d: %v", who, j+1, err)
			}
		}
		if ni.Gateway != "" {
			if ip := net.ParseIP(ni.Gateway); ip == nil {
				add("%s: gateway %q is not a valid IP", who, ni.Gateway)
			} else if ip.To4() == nil || strings.Contains(ni.Gateway, ":") {
				add("%s: gateway %s is IPv6; dhcpcd's static routers are IPv4 only (an IPv6 default route comes from router advertisements)", who, ni.Gateway)
			}
		}
		// Written into dhcpcd.conf and the live resolv.conf as given: a
		// newline would start a stanza of its own, a comma-separated list
		// is not a server.
		for _, d := range strings.Fields(ni.DNS) {
			if net.ParseIP(d) == nil {
				add("%s: dns %q is not an IP address (separate several with spaces)", who, d)
			}
		}
		if strings.ContainsAny(ni.DNS, "\n\r") {
			add("%s: dns is one line of addresses", who)
		}
		for _, d := range strings.Fields(ni.Domain) {
			if len(d) > 253 || !domainRe.MatchString(d) {
				add("%s: domain %q is not a domain name (separate several with spaces)", who, d)
			}
		}
		if strings.ContainsAny(ni.Domain, "\n\r") {
			add("%s: domain is one line of names", who)
		}
	}

	// A member of a bond or bridge carries no address of its own.
	for _, ni := range c.NetworkInterfaces {
		if ni != nil && memberOf[ni.Name] != "" && len(ni.Addrs) > 0 {
			add("%s: is a member of %s, so the addresses belong on %s", ni.Name, memberOf[ni.Name], memberOf[ni.Name])
		}
	}

	switch len(probs) {
	case 0:
		return nil
	case 1:
		return fmt.Errorf("config: %s", probs[0])
	default:
		return fmt.Errorf("config: %d problems:\n  - %s", len(probs), strings.Join(probs, "\n  - "))
	}
}

// Plan turns the target disk's layout into a storage plan.
//
// Declaring a layout in targetMedia means repartitioning the disk, so
// the plan wipes it — the same meaning clr-installer gives a config that
// lists children. Partition device names and numbers come from the
// child names a clr-installer config carries (sda1, nvme0n1p2); sizes
// are resolved to bytes here so the storage layer sees one form.
func (c *Config) Plan() (*storage.Plan, error) {
	disks, err := c.installDisks()
	if err != nil {
		return nil, err
	}

	if c.ReusePartitions {
		return c.planReuse(disks)
	}

	pl := &storage.Plan{Wipe: true}
	for _, d := range disks {
		pl.Disks = append(pl.Disks, "/dev/"+d.Name)
		if d.Wipe != "" && d.Wipe != "superblock" {
			if pl.DiskWipe == nil {
				pl.DiskWipe = map[string]string{}
			}
			pl.DiskWipe["/dev/"+d.Name] = d.Wipe
		}
	}

	for _, disk := range disks {
		device := "/dev/" + disk.Name
		// i counts the partitions, not the list's entries: an empty entry
		// is not cut, so it must not leave a gap in the numbers parted
		// gives the rest.
		i := -1
		for _, ch := range disk.Children {
			if ch == nil {
				continue
			}
			i++

			// An lvm partition is a physical volume: it is cut like any
			// other, and its children become logical volumes on top.
			if ch.Type == "lvm" {
				pv, lvs, err := lvmParts(device, i, ch)
				if err != nil {
					return nil, err
				}
				pv.Disk = device
				pl.Parts = append(pl.Parts, pv)
				pl.Parts = append(pl.Parts, lvs...)
				continue
			}

			// A child is a plain partition ("part"/unset), an encrypted one
			// ("crypt", clr's LUKS marker), or a RAID member ("raid").
			var encrypted, raidMember bool
			switch ch.Type {
			case "", "part":
			case "crypt":
				encrypted = true
			case "raid":
				raidMember = true
			default:
				return nil, fmt.Errorf("config: %s child %q has type %q; expected part, crypt, lvm or raid",
					disk.Name, ch.Name, ch.Type)
			}

			size, err := physSize(ch.Size)
			if err != nil {
				return nil, fmt.Errorf("config: %s/%s: %w", disk.Name, ch.Name, err)
			}
			dev, num := partDev(device, i, ch.Name)

			p := storage.Partition{
				Number:     num,
				Device:     dev,
				Disk:       device,
				Mountpoint: ch.Mountpoint,
				Filesystem: ch.FsType,
				Label:      ch.Label,
				Options:    strings.Fields(ch.Options),
				Size:       size,
				Encrypted:  encrypted,
			}
			p.MountOptions, p.Subvol, p.Subvolumes = mountExtras(ch.Mountpoint, ch.MountOptions, ch.Subvolumes)
			details(&p, ch)
			if encrypted {
				p.MapName = cryptMapName(ch)
			}
			p.SpareESP = ch.ESP
			if raidMember {
				p.IsRaidMember = true
				p.RaidSpare = ch.Spare
				p.RaidName = ch.Raid
				p.Mountpoint, p.Filesystem = "", "" // the array carries those
			}
			pl.Parts = append(pl.Parts, p)
		}
	}

	// RAID arrays: gather each array's member devices (in plan order) and
	// append the array as a pseudo-partition carrying the filesystem, the
	// way an LV is appended for LVM.
	for _, a := range c.Raid {
		if a == nil {
			continue
		}
		var members, spares []string
		for _, p := range pl.Parts {
			switch {
			case !p.IsRaidMember || p.RaidName != a.Name:
			case p.RaidSpare:
				spares = append(spares, p.Device)
			default:
				members = append(members, p.Device)
			}
		}
		rp := storage.Partition{
			Raid:        true,
			RaidName:    a.Name,
			RaidLevel:   a.Level,
			RaidMembers: members,
			RaidSpares:  spares,
			Mountpoint:  a.Mountpoint,
			Filesystem:  a.FsType,
		}
		rp.MountOptions, rp.Subvol, rp.Subvolumes = mountExtras(a.Mountpoint, a.MountOptions, a.Subvolumes)
		rp.CryptOptions, rp.FsUUID, rp.Passno, rp.Freq, rp.RaidMetadata = strings.TrimSpace(a.CryptOptions), a.UUID, a.Passno, a.Freq, a.Metadata
		if a.Encrypted {
			// LUKS on the array: the array device is what is formatted and
			// opened, and everything above it is on the mapper.
			rp.Encrypted, rp.Device, rp.MapName = true, storage.RaidDevice(a.Name), raidMapName(a.Name)
		}
		if len(a.Partitions) > 0 {
			// A partitioned array: it is a disk in its own right, cut after
			// it is assembled, and its partitions carry the filesystems.
			dev := storage.RaidDevice(a.Name)
			rp.Mountpoint, rp.Filesystem = "", ""
			pl.Parts = append(pl.Parts, rp)
			pl.RaidDisks = append(pl.RaidDisks, dev)
			n := 0
			for _, ch := range a.Partitions {
				if ch == nil {
					continue
				}
				n++
				size, err := physSize(ch.Size)
				if err != nil {
					return nil, fmt.Errorf("config: raid %s partition %d: %w", a.Name, n, err)
				}
				p := storage.Partition{
					Number: n, Device: storage.PartitionName(dev, n), Disk: dev,
					Mountpoint: ch.Mountpoint, Filesystem: ch.FsType, Label: ch.Label,
					Options: strings.Fields(ch.Options), Size: size,
				}
				p.MountOptions, p.Subvol, p.Subvolumes = mountExtras(ch.Mountpoint, ch.MountOptions, ch.Subvolumes)
				details(&p, ch)
				pl.Parts = append(pl.Parts, p)
			}
			continue
		}
		if a.VolumeGroup != "" {
			// LVM on the array: it is a physical volume and carries no
			// filesystem of its own.
			rp.IsPV, rp.VGName = true, a.VolumeGroup
			rp.Mountpoint, rp.Filesystem = "", ""
			pl.Parts = append(pl.Parts, rp)
			pl.Parts = append(pl.Parts, lvParts(a.VolumeGroup, a.Children)...)
			continue
		}
		pl.Parts = append(pl.Parts, rp)
	}

	if len(pl.Parts) == 0 {
		return nil, fmt.Errorf("config: targetMedia declares no partitions")
	}

	// The primary disk — where the bootloader and ESP go — is the disk that
	// holds the ESP; the first disk otherwise.
	pl.Disk = pl.Disks[0]
	for _, p := range pl.Parts {
		if p.Filesystem == "vfat" && (p.Mountpoint == "/boot" || p.Mountpoint == "/boot/efi") && p.Disk != "" {
			pl.Disk = p.Disk
			break
		}
	}

	if err := checkFreshNumbering(pl); err != nil {
		return nil, err
	}
	uniqueMapNames(pl)

	// A fill physical partition must be the last physical one on its disk —
	// in on-disk order, which is the number order parted cuts them in, not
	// the order the config lists them; logical volumes and RAID arrays
	// (which are not cut by parted) are exempt.
	for _, disk := range append(append([]string{}, pl.Disks...), pl.RaidDisks...) {
		phys := pl.PhysOn(disk)
		for k, p := range phys {
			if p.Size == storage.SizeRest && k != len(phys)-1 {
				return nil, fmt.Errorf("config: %s fills its disk but is not the last partition on it", p.Device)
			}
		}
	}
	return pl, nil
}

// checkFreshNumbering refuses a new partition table whose partition names
// do not match what parted will make. Device names and numbers come from
// the config's child names, but on a fresh table parted numbers the
// partitions itself, 1, 2, 3… in the order they are cut — so a child named
// for another disk (mkfs on a disk nobody confirmed), a gap (sda1, sda5:
// mkfs on a partition that does not exist), or a duplicate (two sda1: the
// second mkfs over the first) all went ahead, after the table was wiped.
// Each disk's partitions must be its own and numbered 1..n.
func checkFreshNumbering(pl *storage.Plan) error {
	seen := map[string]bool{}
	for _, disk := range append(append([]string{}, pl.Disks...), pl.RaidDisks...) {
		for k, p := range pl.PhysOn(disk) {
			if want := storage.PartitionName(disk, p.Number); p.Device != want {
				return fmt.Errorf("config: partition %s is not a partition of %s (it would be %s)", p.Device, disk, want)
			}
			if seen[p.Device] {
				return fmt.Errorf("config: partition %s is declared twice", p.Device)
			}
			seen[p.Device] = true
			if p.Number != k+1 {
				return fmt.Errorf("config: %s is partition %d of %s, but a new table numbers its partitions 1, 2, 3…; %s would be %s",
					p.Device, p.Number, disk, p.Device, storage.PartitionName(disk, k+1))
			}
		}
	}
	return nil
}

// planReuse builds a plan that installs into existing partitions without
// repartitioning: Wipe is false, each declared child is matched to its
// existing device by name, formatted unless marked preserve, and mounted.
// Only plain partitions are supported — crypt, lvm and raid would need to
// create or re-create on-disk structures, which reuse exists to avoid — and
// Validate reports those; this guards again so a direct Plan call is safe.
func (c *Config) planReuse(disks []*Media) (*storage.Plan, error) {
	pl := &storage.Plan{Wipe: false}
	seen := map[string]bool{}
	for _, d := range disks {
		pl.Disks = append(pl.Disks, "/dev/"+d.Name)
	}
	for _, disk := range disks {
		device := "/dev/" + disk.Name
		for i, ch := range disk.Children {
			if ch == nil {
				continue
			}
			if (ch.Type != "" && ch.Type != "part") || ch.Encrypted {
				return nil, fmt.Errorf("config: reusePartitions supports only plain partitions; %s/%s is %q",
					disk.Name, ch.Name, ch.Type)
			}
			dev, num := partDev(device, i, ch.Name)
			// Formatted or kept, it must be on the disk the install was
			// confirmed for, and listed once.
			if want := storage.PartitionName(device, num); dev != want {
				return nil, fmt.Errorf("config: partition %s is not a partition of %s (it would be %s)", dev, device, want)
			}
			if seen[dev] {
				return nil, fmt.Errorf("config: partition %s is declared twice", dev)
			}
			seen[dev] = true
			p := storage.Partition{
				Number:     num,
				Device:     dev,
				Disk:       device,
				Mountpoint: ch.Mountpoint,
				Filesystem: ch.FsType,
				Label:      ch.Label,
				Options:    strings.Fields(ch.Options),
				Preserve:   ch.Preserve,
			}
			p.MountOptions, p.Subvol, p.Subvolumes = mountExtras(ch.Mountpoint, ch.MountOptions, ch.Subvolumes)
			details(&p, ch)
			// A Create child is cut fresh in unallocated space (Safe install):
			// Start is the MiB offset, Size the length, so End = Start + Size.
			if ch.Resize != "" {
				mib, err := offsetMiB(ch.Resize)
				if err != nil || mib == 0 {
					return nil, fmt.Errorf("config: %s/%s resize: %q is not a size", disk.Name, ch.Name, ch.Resize)
				}
				p.ResizeMiB = mib
			}
			// A Create child with no start goes in the disk's largest free
			// region, found when the install gets there — after any resize
			// has made the room. Its size, if given, is an upper bound.
			if ch.Create && strings.TrimSpace(ch.Start) == "" {
				p.Create, p.CreateInFree = true, true
				if sz := strings.TrimSpace(ch.Size); sz != "" && sz != "0" && !strings.EqualFold(sz, "rest") {
					mib, err := offsetMiB(sz)
					if err != nil {
						return nil, fmt.Errorf("config: %s/%s size: %w", disk.Name, ch.Name, err)
					}
					p.EndMiB = mib // the bound, until the region is known
				}
				pl.Parts = append(pl.Parts, p)
				continue
			}
			if ch.Create {
				startMiB, err := offsetMiB(ch.Start)
				if err != nil {
					return nil, fmt.Errorf("config: %s/%s start: %w", disk.Name, ch.Name, err)
				}
				sizeMiB, err := offsetMiB(ch.Size)
				if err != nil {
					return nil, fmt.Errorf("config: %s/%s size: %w", disk.Name, ch.Name, err)
				}
				p.Create = true
				p.StartMiB = startMiB
				p.EndMiB = startMiB + sizeMiB
			}
			pl.Parts = append(pl.Parts, p)
		}
	}
	if len(pl.Parts) == 0 {
		return nil, fmt.Errorf("config: targetMedia declares no partitions")
	}
	pl.Disk = pl.Disks[0]
	for _, p := range pl.Parts {
		if p.Filesystem == "vfat" && (p.Mountpoint == "/boot" || p.Mountpoint == "/boot/efi") && p.Disk != "" {
			pl.Disk = p.Disk
			break
		}
	}
	return pl, nil
}

// offsetMiB parses a size/offset string to whole MiB, for a Safe install's
// start offset and length.
func offsetMiB(s string) (uint64, error) {
	bytes, err := parseSize(s)
	if err != nil {
		return 0, err
	}
	return bytes >> 20, nil
}

// planDiskOf is the disk a physical partition sits on, defaulting to the
// plan's primary disk when the partition does not name one.
func planDiskOf(p storage.Partition, primary string) string {
	if p.Disk != "" {
		return p.Disk
	}
	return primary
}

// physSize resolves a physical partition's size to a byte count, or
// SizeRest for a missing size / 0 / "rest".
func physSize(s string) (string, error) {
	if s = strings.TrimSpace(s); s == "" || s == "0" || strings.EqualFold(s, "rest") {
		return storage.SizeRest, nil
	}
	bytes, err := parseSize(s)
	if err != nil {
		return "", err
	}
	return strconv.FormatUint(bytes, 10), nil
}

// partDev is the device path and number for a physical partition: from
// its config name when it has one, else derived from its position.
func partDev(disk string, i int, name string) (string, int) {
	if name != "" {
		return "/dev/" + name, partNumber(name, i+1)
	}
	return storage.PartitionName(disk, i+1), i + 1
}

// lvmParts turns a `type: lvm` partition into the physical volume and its
// logical volumes. The PV is sized on the disk; each LV keeps its human
// size (lvcreate's), or SizeRest to fill the group.
func lvmParts(disk string, i int, ch *Media) (storage.Partition, []storage.Partition, error) {
	if ch.VolumeGroup == "" {
		return storage.Partition{}, nil, fmt.Errorf("config: lvm partition %q has no volumeGroup", ch.Name)
	}
	size, err := physSize(ch.Size)
	if err != nil {
		return storage.Partition{}, nil, fmt.Errorf("config: %s: %w", ch.Name, err)
	}
	dev, num := partDev(disk, i, ch.Name)
	pv := storage.Partition{Number: num, Device: dev, Size: size, IsPV: true, VGName: ch.VolumeGroup}
	// LVM-on-LUKS: the physical volume is a LUKS container. pvcreate then
	// targets the mapper (storage.Partition.EffectiveDevice), not the raw
	// partition; the controller luksFormat/luksOpens it before lvm runs.
	if ch.Encrypted {
		pv.Encrypted = true
		pv.MapName = cryptMapName(ch)
		pv.CryptOptions = strings.TrimSpace(ch.CryptOptions)
	}

	return pv, lvParts(ch.VolumeGroup, ch.Children), nil
}

// lvSizeRe is a size lvcreate reads as the config does: a plain or
// decimal number with a K/M/G/T unit.
var lvSizeRe = regexp.MustCompile(`^[0-9]+(\.[0-9]+)?[KkMmGgTt]$`)

// lvcreateSize is a logical volume's size as lvcreate -L must be given it.
// A size with a unit is passed as written. Anything else — a bare number,
// bytes in a config — would be read by lvcreate as MiB (1073741824 asked
// for a pebibyte, 2e9 and 1048576.5 likewise), and a byte count it takes
// only in multiples of 512: such a size is converted to whole MiB, rounded
// up (lvcreate rounds up to an extent anyway).
func lvcreateSize(s string) string {
	if lvSizeRe.MatchString(s) {
		return s
	}
	b, err := parseSize(s)
	if err != nil {
		return s // Validate reports it
	}
	mib := b >> 20
	if b&(1<<20-1) != 0 {
		mib++ // rounded up, without b+MiB overflowing for a huge size
	}
	return fmt.Sprintf("%dm", mib)
}

// lvParts turns a group's declared children into its logical volumes.
func lvParts(vg string, children []*Media) []storage.Partition {
	var lvs []storage.Partition
	for _, lv := range children {
		if lv == nil {
			continue
		}
		lvSize := strings.TrimSpace(lv.Size)
		if lvSize == "" || lvSize == "0" || strings.EqualFold(lvSize, "rest") {
			lvSize = storage.SizeRest
		} else {
			lvSize = lvcreateSize(lvSize)
		}
		lp := storage.Partition{
			LV: true, VGName: vg, LVName: lv.Name,
			Mountpoint: lv.Mountpoint, Filesystem: lv.FsType, Size: lvSize,
			Label: lv.Label, Options: strings.Fields(lv.Options),
		}
		lp.MountOptions, lp.Subvol, lp.Subvolumes = mountExtras(lv.Mountpoint, lv.MountOptions, lv.Subvolumes)
		details(&lp, lv)
		lvs = append(lvs, lp)
	}
	return lvs
}

// uniqueMapNames gives every LUKS container of the plan a mapper name of
// its own. Names come from roles (cryptswap, cryptvar-lib, crypt<vg>), and
// two volumes can share one: two encrypted swaps, /var/lib beside
// /var-lib, two encrypted physical volumes of one group, an array named
// root beside an encrypted root. The second could not be opened ("already
// exists") — after the disks were wiped. The first keeps its name; each
// later one gets its device's name added (cryptswap-sdb2).
func uniqueMapNames(pl *storage.Plan) {
	used := map[string]bool{}
	for i := range pl.Parts {
		p := &pl.Parts[i]
		if !p.Encrypted || p.MapName == "" {
			continue
		}
		if used[p.MapName] {
			base := p.MapName + "-" + filepath.Base(p.Device)
			name := base
			for n := 2; used[name]; n++ {
				name = fmt.Sprintf("%s-%d", base, n)
			}
			p.MapName = name
		}
		used[p.MapName] = true
	}
}

// raidMapName is the LUKS mapper name for an encrypted RAID array.
func raidMapName(array string) string { return "crypt" + array }

// mountpointProblem says what is wrong with an absolute mountpoint, or "".
// Mountpoints are compared and written to fstab as given, so each has to
// be one clean path fstab can carry (/home, not /home/ or /boot/../home);
// the API filesystems are mounted by the system itself.
func mountpointProblem(m string) string {
	if clean := filepath.Clean(m); clean != m {
		return fmt.Sprintf("mountpoint %q is not a clean path (write %q)", m, clean)
	}
	for _, r := range m {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return fmt.Sprintf("mountpoint %q has a space or control character, which fstab cannot carry", m)
		}
	}
	for _, api := range []string{"/proc", "/sys", "/dev", "/run"} {
		if m == api || strings.HasPrefix(m, api+"/") {
			return fmt.Sprintf("mountpoint %q is under %s, which the system mounts itself", m, api)
		}
	}
	return ""
}

// cryptMapName is the LUKS mapper name for an encrypted partition,
// derived from its role so it reads clearly in /dev/mapper and crypttab.
func cryptMapName(ch *Media) string {
	switch {
	case ch.Type == "lvm" && ch.VolumeGroup != "":
		// LVM-on-LUKS: the container holds the whole group, so name it
		// after the group (cryptvg0), not a mountpoint it has none of.
		return "crypt" + ch.VolumeGroup
	case ch.Mountpoint == "/":
		return "cryptroot"
	case ch.FsType == "swap":
		return "cryptswap"
	case ch.Mountpoint != "":
		return "crypt" + strings.ReplaceAll(strings.Trim(ch.Mountpoint, "/"), "/", "-")
	default:
		return "crypt" + ch.Name
	}
}

// installDisks finds the disks to install to: the disk-type media that
// carry a partition layout. One disk is the common case; several are a
// RAID (or otherwise multi-disk) layout.
// IsRemote reports whether an image is named by a URL rather than a path.
func IsRemote(image string) bool { return strings.Contains(image, "://") }

// checkRemote accepts an image given as an https URL — it is then unpacked
// as it downloads, never stored — and refuses anything else with "://" in
// it. Plain http needs allowInsecureHTTP, as a repository does: the image
// is the whole system.
func (c *Config) checkRemote(key, image string) error {
	switch {
	case !IsRemote(image), strings.HasPrefix(image, "https://"):
		return nil
	case strings.HasPrefix(image, "http://"):
		if c.AllowInsecureHTTP {
			return nil
		}
		return fmt.Errorf("%s: %s is plain http; use https, or set allowInsecureHTTP", key, image)
	}
	return fmt.Errorf("%s: %s is not an https URL or a file", key, image)
}

// CurlFetch is the shell command that writes the URL in "$1" to stdout. For
// an https URL a redirect is held to https unless plain http is allowed, as
// checkRemote holds the URL itself: curl would otherwise follow an https
// server on to plain http.
func CurlFetch(url string, allowInsecureHTTP bool) string {
	if strings.HasPrefix(url, "https://") && !allowInsecureHTTP {
		return `curl -fsSL --proto-redir =https "$1"`
	}
	return `curl -fsSL "$1"`
}

// DiskImageTarget is the one disk a disk-image is written to. A config that
// also describes a layout, or names several disks, is refused: the image
// replaces the whole disk, and which disk has to be beyond doubt.
func (c *Config) DiskImageTarget() (*Media, error) {
	if err := c.checkRemote("disk-image", c.DiskImage); err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	var disks []*Media
	for _, m := range c.TargetMedia {
		if m != nil {
			disks = append(disks, m)
		}
	}
	switch {
	case len(disks) != 1 || (disks[0].Type != "" && disks[0].Type != "disk"):
		return nil, fmt.Errorf("config: disk-image is written to one disk; targetMedia names %d entries", len(disks))
	case len(disks[0].Children) > 0 || c.ReusePartitions:
		return nil, fmt.Errorf("config: disk-image replaces the whole of %s, partition table included; a layout cannot be given with it", disks[0].Name)
	case disks[0].Name == "" || strings.Contains(disks[0].Name, "/"):
		return nil, fmt.Errorf("config: disk-image: the disk needs a name (e.g. sda) or a match that was resolved")
	case c.Image != "":
		return nil, fmt.Errorf("config: give image (a root filesystem to install) or disk-image (a whole disk to write), not both")
	}
	return disks[0], nil
}

func (c *Config) installDisks() ([]*Media, error) {
	var disks []*Media
	for _, m := range c.TargetMedia {
		if m == nil || (m.Type != "" && m.Type != "disk") || len(m.Children) == 0 {
			continue
		}
		disks = append(disks, m)
	}
	if len(disks) == 0 {
		return nil, fmt.Errorf("config: targetMedia has no disk with a partition layout")
	}
	return disks, nil
}

// parseSize parses a clr-installer size to bytes. It appears three ways:
// a byte count ("157286400"), an integer with a unit ("150M"), and a
// decimal with a unit ("1.364G"). Units are powers of 1024.
func parseSize(s string) (uint64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("missing size")
	}

	mult := 1.0
	num := s
	switch s[len(s)-1] {
	case 'K', 'k':
		mult = 1 << 10
	case 'M', 'm':
		mult = 1 << 20
	case 'G', 'g':
		mult = 1 << 30
	case 'T', 't':
		mult = 1 << 40
	case 'B', 'b':
		mult = 1
	}
	if mult != 1 || s[len(s)-1] == 'B' || s[len(s)-1] == 'b' {
		num = s[:len(s)-1]
	}

	f, err := strconv.ParseFloat(num, 64)
	if err != nil || f < 0 || math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, fmt.Errorf("size %q is not a byte count or a number with a K/M/G/T unit", s)
	}
	b := math.Round(f * mult)
	if b >= float64(1<<64) {
		return 0, fmt.Errorf("size %q is too large", s)
	}
	return uint64(b), nil
}

// partNumber extracts the trailing partition number from a device name
// (sda1 -> 1, nvme0n1p2 -> 2), falling back when there is none.
func partNumber(name string, fallback int) int {
	i := len(name)
	for i > 0 && name[i-1] >= '0' && name[i-1] <= '9' {
		i--
	}
	if i == len(name) {
		return fallback
	}
	n, err := strconv.Atoi(name[i:])
	if err != nil {
		return fallback
	}
	return n
}
