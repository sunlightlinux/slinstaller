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
	"os"
	"regexp"
	"strconv"
	"strings"

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
}

// Address is one IP on an interface. version is 0 for IPv4, 1 for IPv6,
// as clr-installer encodes it.
type Address struct {
	IP      string `yaml:"ip"`
	Netmask string `yaml:"netmask,omitempty"`
	Version int    `yaml:"version,omitempty"`
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

// Validate checks what must hold for the config to describe a bootable
// UEFI install, before any disk is touched. It reports every problem it
// finds at once, so an operator fixing a config by hand does not have to
// run it once per mistake.
//
// It stays within clr-installer compatibility: it does not require fields
// a valid clr config may omit (a login account, a hostname), only
// rejects layouts that cannot install or boot.
func (c *Config) Validate() error {
	disk, err := c.installDisk()
	if err != nil {
		return err
	}

	var probs []string
	add := func(format string, a ...any) { probs = append(probs, fmt.Sprintf(format, a...)) }

	roots, esps := 0, 0
	seen := map[string]bool{}
	for i, ch := range disk.Children {
		if ch == nil {
			continue
		}
		where := ch.Name
		if where == "" {
			where = fmt.Sprintf("partition %d", i+1)
		}

		switch {
		case ch.FsType == "":
			add("%s: no filesystem", where)
		case !storage.KnownFilesystem(ch.FsType):
			add("%s: filesystem %q is not supported", where, ch.FsType)
		}

		if ch.Mountpoint != "" {
			if !strings.HasPrefix(ch.Mountpoint, "/") {
				add("%s: mountpoint %q is not absolute", where, ch.Mountpoint)
			}
			if seen[ch.Mountpoint] {
				add("%s: mountpoint %q is used more than once", where, ch.Mountpoint)
			}
			seen[ch.Mountpoint] = true
		}
		if ch.Mountpoint == "/" {
			roots++
		}

		if ch.FsType == "vfat" && (ch.Mountpoint == "/boot" || ch.Mountpoint == "/boot/efi") {
			esps++
			if ch.Type == "crypt" {
				add("%s: the EFI System Partition cannot be encrypted", where)
			}
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

	if c.Hostname != "" && (len(c.Hostname) > 63 || !hostnameRe.MatchString(c.Hostname)) {
		add("hostname %q must be 1-63 characters of letters, digits and hyphens", c.Hostname)
	}
	if c.SwapFileSize != "" {
		if _, err := parseSize(c.SwapFileSize); err != nil {
			add("swapFileSize: %v", err)
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
	disk, err := c.installDisk()
	if err != nil {
		return nil, err
	}

	device := "/dev/" + disk.Name
	pl := &storage.Plan{Disk: device, Wipe: true}

	for i, ch := range disk.Children {
		if ch == nil {
			continue
		}
		// A child is a partition ("part" or unset) or an encrypted
		// partition ("crypt"), the way clr-installer marks LUKS.
		var encrypted bool
		switch ch.Type {
		case "", "part":
		case "crypt":
			encrypted = true
		default:
			return nil, fmt.Errorf("config: %s child %q has type %q; expected part or crypt",
				disk.Name, ch.Name, ch.Type)
		}

		// A missing size, 0, or "rest" means "fill the remaining space",
		// the way clr-installer uses size 0. Otherwise parse to bytes.
		var size string
		if s := strings.TrimSpace(ch.Size); s == "" || s == "0" || strings.EqualFold(s, "rest") {
			size = storage.SizeRest
		} else {
			bytes, err := parseSize(s)
			if err != nil {
				return nil, fmt.Errorf("config: %s/%s: %w", disk.Name, ch.Name, err)
			}
			size = strconv.FormatUint(bytes, 10)
		}

		dev, num := device, i+1
		if ch.Name != "" {
			dev = "/dev/" + ch.Name
			num = partNumber(ch.Name, i+1)
		} else {
			dev = storage.PartitionName(device, num)
		}

		p := storage.Partition{
			Number:     num,
			Device:     dev,
			Mountpoint: ch.Mountpoint,
			Filesystem: ch.FsType,
			Size:       size,
			Encrypted:  encrypted,
		}
		if encrypted {
			p.MapName = cryptMapName(ch)
		}
		pl.Parts = append(pl.Parts, p)
	}

	if len(pl.Parts) == 0 {
		return nil, fmt.Errorf("config: target disk %s declares no partitions", disk.Name)
	}

	// A fill partition must be the last one, or the partitions after it
	// would have nowhere to go.
	for i, p := range pl.Parts {
		if p.Size == storage.SizeRest && i != len(pl.Parts)-1 {
			return nil, fmt.Errorf("config: %s fills the disk but is not the last partition", p.Device)
		}
	}
	return pl, nil
}

// cryptMapName is the LUKS mapper name for an encrypted partition,
// derived from its role so it reads clearly in /dev/mapper and crypttab.
func cryptMapName(ch *Media) string {
	switch {
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

// installDisk finds the disk to install to: the one disk-type media that
// carries a partition layout. slinstaller installs to a single disk, so
// more than one is an error rather than a silent choice.
func (c *Config) installDisk() (*Media, error) {
	var found *Media
	for _, m := range c.TargetMedia {
		if m == nil || (m.Type != "" && m.Type != "disk") || len(m.Children) == 0 {
			continue
		}
		if found != nil {
			return nil, fmt.Errorf("config: targetMedia has more than one disk with a layout; slinstaller installs to a single disk")
		}
		found = m
	}
	if found == nil {
		return nil, fmt.Errorf("config: targetMedia has no disk with a partition layout")
	}
	return found, nil
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
