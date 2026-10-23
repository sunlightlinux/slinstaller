// Package descriptor defines the document that describes an
// installation completely.
//
// Everything in slinstaller either produces a Descriptor or consumes
// one. Front-ends — terminal, graphical, or a file supplied on the
// command line — produce it; the controller consumes it and nothing
// else. That boundary is the architecture, not a convention: a
// front-end that performs an installation step, or that knows how
// slinit materialises a service, has crossed it.
//
// The shape is specified in sldocs, specs/installer-descriptor.md, and
// the fields are derived from what installing this distribution
// actually requires rather than from any other installer's model.
package descriptor

// CurrentVersion is the descriptor schema version this build writes.
// It is in the document so that a descriptor found on an installed
// system years later can be read by the installer that wrote it, or
// rejected clearly by one that cannot.
const CurrentVersion = 1

// Descriptor is a complete installation, serialisable to YAML.
//
// A zero Descriptor is deliberately not installable: Validate rejects
// it. Nothing here defaults to a destructive value.
type Descriptor struct {
	Version int `yaml:"version"`

	// TargetDisk is the whole device, e.g. /dev/sda.
	TargetDisk string      `yaml:"targetDisk"`
	Partitions []Partition `yaml:"partitions"`

	// Wipe must be set explicitly. A descriptor is replayable by
	// design, so one written for a machine can be run against
	// another; the guard belongs in the document and not only in
	// whatever interface produced it.
	Wipe bool `yaml:"wipe"`

	Hostname string `yaml:"hostname"`
	Timezone string `yaml:"timezone"`
	Locale   string `yaml:"locale"`
	Keymap   string `yaml:"keymap"`

	// RootPasswordHash is a crypt(3) hash, never a cleartext
	// password. A descriptor is a file people commit to repositories
	// and paste into issues; accepting plaintext here would make an
	// unattended install convenient and a disclosure inevitable.
	// Hashing is the front-end's job.
	RootPasswordHash string `yaml:"rootPasswordHash"`
	Users            []User `yaml:"users,omitempty"`

	Network []Interface `yaml:"network,omitempty"`

	Source   Source   `yaml:"source"`
	Mirror   string   `yaml:"mirror,omitempty"`
	Packages []string `yaml:"packages,omitempty"`

	Services Services `yaml:"services,omitempty"`

	Bootloader Bootloader `yaml:"bootloader"`

	Hooks Hooks `yaml:"hooks,omitempty"`

	// ProducedBy records what wrote this descriptor. The installed
	// system keeps a copy, so a machine can say which installer built
	// it — which costs a line here and answers a question that is
	// otherwise unanswerable.
	ProducedBy string `yaml:"producedBy,omitempty"`
}

// Partition is one filesystem to create and mount.
//
// The slice order is the declaration order; MountOrder sorts it for
// mounting. Callers must not rely on the declared order being the
// mount order.
type Partition struct {
	// Mountpoint is an absolute path in the installed system, or
	// empty for a partition that is created but not mounted (swap).
	Mountpoint string `yaml:"mountpoint,omitempty"`

	// Size is a human size such as "512M", or "rest" for whatever
	// remains. Exactly one partition may say "rest".
	Size string `yaml:"size"`

	Filesystem string `yaml:"filesystem"`

	// Label is optional; an empty label means the filesystem is
	// created without one.
	Label string `yaml:"label,omitempty"`
}

// SizeRest is the Size value meaning "all remaining space".
const SizeRest = "rest"

// User is an account to create in the installed system.
type User struct {
	Name string `yaml:"name"`

	// PasswordHash is a crypt(3) hash. See RootPasswordHash.
	PasswordHash string   `yaml:"passwordHash"`
	Groups       []string `yaml:"groups,omitempty"`
	Shell        string   `yaml:"shell,omitempty"`
}

// NetMode is how one interface is configured.
type NetMode string

const (
	NetDHCP   NetMode = "dhcp"
	NetStatic NetMode = "static"
	NetNone   NetMode = "none"
)

// Interface is the configuration for one network interface.
type Interface struct {
	Name string  `yaml:"interface"`
	Mode NetMode `yaml:"mode"`

	// Address is CIDR, e.g. 192.168.1.50/24. Static mode only.
	Address     string   `yaml:"address,omitempty"`
	Gateway     string   `yaml:"gateway,omitempty"`
	Nameservers []string `yaml:"nameservers,omitempty"`

	WiFi *WiFi `yaml:"wifi,omitempty"`
}

// WiFi is the credentials for a wireless interface.
type WiFi struct {
	SSID string `yaml:"ssid"`

	// PSK is a pre-shared key. It is secret, and the warning on
	// RootPasswordHash applies: a descriptor travels.
	PSK string `yaml:"psk,omitempty"`
}

// Source is where the installed system's files come from.
type Source string

const (
	// SourceRootfs copies the live image's root filesystem. This is
	// the normal path and it needs no network.
	SourceRootfs Source = "rootfs"

	// SourceNetwork fetches packages from a mirror.
	SourceNetwork Source = "network"
)

// Services names what to enable and disable in the installed system.
//
// Names only. On this system, enabling a service means materialising a
// waits-for edge — a symlink under the boot bundle's waits-for.d — and
// that mechanism belongs to the controller. A front-end that builds
// symlinks has crossed the boundary this package exists to hold.
type Services struct {
	Enable []string `yaml:"enable,omitempty"`

	// Disable matters because the installed system is not the live
	// one: a getty that logs a user in automatically is correct on
	// live media and wrong on disk.
	Disable []string `yaml:"disable,omitempty"`
}

// Bootloader is where and how to install the bootloader.
type Bootloader struct {
	// Target is the device to install to, or BootloaderNone to skip —
	// which is for an image being built for something else to boot.
	Target string `yaml:"target"`

	// KernelArgs are appended to the kernel command line. These reach
	// a bootloader config, so an error here produces a machine that
	// does not boot rather than a message.
	KernelArgs []string `yaml:"kernelArgs,omitempty"`
}

// BootloaderNone is the Bootloader.Target value meaning "do not
// install a bootloader".
const BootloaderNone = "none"

// Hooks are commands run at named points. They exist so that
// site-specific work does not require patching the installer, which is
// what happens otherwise.
type Hooks struct {
	PreInstall  []string `yaml:"preInstall,omitempty"`
	PostInstall []string `yaml:"postInstall,omitempty"`
}
