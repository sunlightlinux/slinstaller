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

	Version uint `yaml:"version,omitempty"`
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
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var c Config
	if err := yaml.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	return &c, nil
}

// hostnameRe is the conservative intersection of what Linux and DNS
// accept: letters, digits and hyphens, not starting or ending with one.
var hostnameRe = regexp.MustCompile(`^[a-zA-Z0-9]([a-zA-Z0-9-]*[a-zA-Z0-9])?$`)

// loginRe is what useradd accepts for a login name.
var loginRe = regexp.MustCompile(`^[a-z_][a-z0-9_-]*$`)

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
	disks, err := c.installDisks()
	if err != nil {
		return err
	}

	var probs []string
	add := func(format string, a ...any) { probs = append(probs, fmt.Sprintf(format, a...)) }

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

			// An lvm partition carries no filesystem itself; its logical
			// volumes do. Validate those, and the group it names.
			if ch.Type == "lvm" {
				if ch.VolumeGroup == "" {
					add("%s: lvm partition has no volumeGroup", where)
				}
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

			if ch.FsType == "vfat" && (ch.Mountpoint == "/boot" || ch.Mountpoint == "/boot/efi") {
				esps++
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
		if !loginRe.MatchString(u.Login) {
			add("users[%d]: login %q must be a valid Unix name (lowercase letter or _ then letters, digits, - or _)", i, u.Login)
		}
		if logins[u.Login] {
			add("users[%d]: login %q is used more than once", i, u.Login)
		}
		logins[u.Login] = true
		if u.Password != "" && !looksHashed(u.Password) {
			add("users[%d] (%s): password does not look like a crypt(3) hash — a config must carry a hash, not a plaintext password", i, u.Login)
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
