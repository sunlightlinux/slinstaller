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
// Sunlight install is honoured and the rest is carried but unused.
package config

import (
	"fmt"
	"math"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

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

	KernelArguments *KernelArguments `yaml:"kernel-arguments,omitempty"`

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
// device file: "p" for nvme/loop/mmcblk (nvme0n1 -> nvme0n1p1), "" for the
// sd*/vd* family (sda -> sda1). This mirrors clr-installer's prefix table.
func aliasPartSuffix(file string) string {
	base := filepath.Base(file)
	for _, p := range []string{"nvme", "loop", "mmcblk"} {
		if strings.HasPrefix(base, p) {
			return "p"
		}
	}
	return ""
}

// expandVars replaces $name and ${name} in s using vars, like
// clr-installer's utils.ExpandVariables.
func expandVars(vars map[string]string, s string) string {
	for k, v := range vars {
		s = strings.ReplaceAll(s, "${"+k+"}", v)
		s = strings.ReplaceAll(s, "$"+k, v)
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
		base := filepath.Base(a.File)
		diskVars[a.Name] = base
		partVars[a.Name] = base + aliasPartSuffix(a.File)
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

	disks, err := c.installDisks()
	if err != nil {
		return err
	}

	var probs []string
	add := func(format string, a ...any) { probs = append(probs, fmt.Sprintf(format, a...)) }

	// A clr-installer alias whose file is not a device asks for an install
	// into an image file (made, loop-mounted, kept with keepImage), which
	// slinstaller does not do. Said here, rather than reading the file name
	// as a device path (/dev/release.img) and failing later.
	for _, al := range c.BlockDevices {
		if al != nil && al.File != "" && !strings.HasPrefix(al.File, "/dev/") {
			add("block-devices: %s points at %s, an image file; installing into an image is not supported (name a device such as /dev/sda)", al.Name, al.File)
		}
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
	raidMembers := map[string]int{} // members declared per array name

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
		if mnt != "" {
			if !strings.HasPrefix(mnt, "/") {
				add("%s: mountpoint %q is not absolute", where, mnt)
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
				checkFS(where, ch.FsType, ch.Mountpoint)
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
				}
				checkMin(where, ch.Size, storage.MinPartitionSize, "1 MiB", "physical-volume size")
				fills := 0
				for j, lv := range ch.Children {
					if lv == nil {
						continue
					}
					lvWhere := fmt.Sprintf("%s/%s", where, lv.Name)
					if lv.Name == "" {
						lvWhere = fmt.Sprintf("%s/lv%d", where, j+1)
					}
					checkFS(lvWhere, lv.FsType, lv.Mountpoint)
					checkMin(lvWhere, lv.Size, storage.MinPartitionSize, "1 MiB", "size")
					if lv.Mountpoint == "/" {
						checkMin(lvWhere, lv.Size, storage.MinRootSize, "2 GB", "root size")
					}
					if s := strings.TrimSpace(lv.Size); s == "" || s == "0" || strings.EqualFold(s, "rest") {
						fills++
					}
				}
				if fills > 1 {
					add("%s: more than one logical volume fills the group", where)
				}
				continue
			}

			// A RAID member carries no filesystem — the array does. Record
			// it against its array; the arrays are validated below.
			if ch.Type == "raid" {
				if ch.Raid == "" {
					add("%s: raid member names no array (raid:)", where)
				} else {
					raidMembers[ch.Raid]++
				}
				continue
			}

			checkFS(where, ch.FsType, ch.Mountpoint)
			checkMin(where, ch.Size, storage.MinPartitionSize, "1 MiB", "size")
			if ch.Mountpoint == "/" {
				checkMin(where, ch.Size, storage.MinRootSize, "2 GB", "root size")
			}

			if ch.FsType == "vfat" && (ch.Mountpoint == "/boot" || ch.Mountpoint == "/boot/efi") {
				esps++
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
		if a.FsType == "vfat" && (a.Mountpoint == "/boot" || a.Mountpoint == "/boot/efi") {
			add("raid %s: the EFI System Partition cannot be on RAID", where)
		} else {
			checkFS("raid "+where, a.FsType, a.Mountpoint)
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
	if esps == 0 {
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
		if u.Password != "" && !looksHashed(u.Password) {
			add("users[%d] (%s): password does not look like a crypt(3) hash - a config must carry a hash, not a plaintext password", i, u.Login)
		}
	}

	if c.Hostname != "" && (len(c.Hostname) > 63 || !hostnameRe.MatchString(c.Hostname)) {
		add("hostname %q must be 1-63 characters of letters, digits and hyphens", c.Hostname)
	}
	if c.SwapFileSize != "" {
		if _, err := parseSize(c.SwapFileSize); err != nil {
			add("swapFileSize: %v", err)
		}
	}

	// Network interfaces: a statically configured one must carry usable
	// addresses (and a valid gateway, if any), checked here so a typo fails
	// before any disk is touched rather than partway through the install.
	for i, ni := range c.NetworkInterfaces {
		if ni == nil {
			continue
		}
		who := ni.Name
		if who == "" {
			who = fmt.Sprintf("networkInterfaces[%d]", i)
			add("%s: no name", who)
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
		if ni.Gateway != "" && net.ParseIP(ni.Gateway) == nil {
			add("%s: gateway %q is not a valid IP", who, ni.Gateway)
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
	}

	for _, disk := range disks {
		device := "/dev/" + disk.Name
		for i, ch := range disk.Children {
			if ch == nil {
				continue
			}

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
			if encrypted {
				p.MapName = cryptMapName(ch)
			}
			if raidMember {
				p.IsRaidMember = true
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
		var members []string
		for _, p := range pl.Parts {
			if p.IsRaidMember && p.RaidName == a.Name {
				members = append(members, p.Device)
			}
		}
		pl.Parts = append(pl.Parts, storage.Partition{
			Raid:        true,
			RaidName:    a.Name,
			RaidLevel:   a.Level,
			RaidMembers: members,
			Mountpoint:  a.Mountpoint,
			Filesystem:  a.FsType,
		})
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

	// A fill physical partition must be the last physical one on its disk;
	// logical volumes and RAID arrays (which are not cut by parted) are
	// exempt.
	for _, disk := range pl.Disks {
		lastPhys := -1
		for i, p := range pl.Parts {
			if !p.LV && !p.Raid && planDiskOf(p, pl.Disk) == disk {
				lastPhys = i
			}
		}
		for i, p := range pl.Parts {
			if !p.LV && !p.Raid && planDiskOf(p, pl.Disk) == disk &&
				p.Size == storage.SizeRest && i != lastPhys {
				return nil, fmt.Errorf("config: %s fills its disk but is not the last partition on it", p.Device)
			}
		}
	}
	return pl, nil
}

// planReuse builds a plan that installs into existing partitions without
// repartitioning: Wipe is false, each declared child is matched to its
// existing device by name, formatted unless marked preserve, and mounted.
// Only plain partitions are supported — crypt, lvm and raid would need to
// create or re-create on-disk structures, which reuse exists to avoid — and
// Validate reports those; this guards again so a direct Plan call is safe.
func (c *Config) planReuse(disks []*Media) (*storage.Plan, error) {
	pl := &storage.Plan{Wipe: false}
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
			// A Create child is cut fresh in unallocated space (Safe install):
			// Start is the MiB offset, Size the length, so End = Start + Size.
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
	}

	var lvs []storage.Partition
	for _, lv := range ch.Children {
		if lv == nil {
			continue
		}
		lvSize := strings.TrimSpace(lv.Size)
		if lvSize == "" || lvSize == "0" || strings.EqualFold(lvSize, "rest") {
			lvSize = storage.SizeRest
		}
		lvs = append(lvs, storage.Partition{
			LV: true, VGName: ch.VolumeGroup, LVName: lv.Name,
			Mountpoint: lv.Mountpoint, Filesystem: lv.FsType, Size: lvSize,
			Label: lv.Label, Options: strings.Fields(lv.Options),
		})
	}
	return pv, lvs, nil
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
	if err != nil || f < 0 {
		return 0, fmt.Errorf("size %q is not a byte count or a number with a K/M/G/T unit", s)
	}
	return uint64(math.Round(f * mult)), nil
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
