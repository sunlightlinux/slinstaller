package descriptor

import (
	"strings"
	"testing"
)

// valid returns a descriptor that passes, so each test can break
// exactly one thing. A test that builds its own invalid descriptor from
// scratch tends to fail for a reason other than the one it names.
func valid() *Descriptor {
	return &Descriptor{
		Version:    CurrentVersion,
		TargetDisk: "/dev/sda",
		Wipe:       true,
		Partitions: []Partition{
			{Mountpoint: "/boot/efi", Size: "512M", Filesystem: "vfat"},
			{Mountpoint: "/", Size: SizeRest, Filesystem: "ext4"},
		},
		Hostname:         "sunlight",
		Timezone:         "Europe/Bucharest",
		Locale:           "en_US.UTF-8",
		Keymap:           "us",
		RootPasswordHash: "$6$salt$hashhashhash",
		Source:           SourceRootfs,
		Bootloader:       Bootloader{Target: "/dev/sda"},
	}
}

func TestValidAcceptsAReasonableDescriptor(t *testing.T) {
	if err := valid().Validate(); err != nil {
		t.Fatalf("the baseline descriptor must validate, got: %v", err)
	}
}

// Each case breaks one field and names the substring the error must
// contain, so a passing test means the message points at the right
// thing — not merely that something failed.
func TestValidateRejects(t *testing.T) {
	for _, tc := range []struct {
		name   string
		break_ func(*Descriptor)
		want   string
	}{
		{"no version", func(d *Descriptor) { d.Version = 0 }, "version: missing"},
		{"future version", func(d *Descriptor) { d.Version = 99 }, "not supported"},
		{"no disk", func(d *Descriptor) { d.TargetDisk = "" }, "targetDisk: missing"},
		{"relative disk", func(d *Descriptor) { d.TargetDisk = "sda" }, "not an absolute path"},
		{"no partitions", func(d *Descriptor) { d.Partitions = nil }, "none declared"},

		// The destructive guard: a layout without authorisation.
		{"layout without wipe", func(d *Descriptor) { d.Wipe = false }, "wipe: must be true"},

		{"two rest", func(d *Descriptor) {
			d.Partitions[0].Size = SizeRest
		}, "at most one"},
		{"no root", func(d *Descriptor) {
			d.Partitions[1].Mountpoint = "/home"
		}, "none mounts /"},
		{"two roots", func(d *Descriptor) {
			d.Partitions[0].Mountpoint = "/"
		}, "2 mount /"},
		{"duplicate mountpoint", func(d *Descriptor) {
			d.Partitions = append(d.Partitions,
				Partition{Mountpoint: "/", Size: "1G", Filesystem: "ext4"})
		}, "more than once"},
		{"bad size", func(d *Descriptor) {
			d.Partitions[0].Size = "512 megabytes"
		}, "is not a number"},
		{"no filesystem", func(d *Descriptor) {
			d.Partitions[0].Filesystem = ""
		}, "filesystem missing"},

		{"no hostname", func(d *Descriptor) { d.Hostname = "" }, "hostname: missing"},
		{"hostname with underscore", func(d *Descriptor) {
			d.Hostname = "my_host"
		}, "letters, digits and hyphens"},
		{"hostname trailing hyphen", func(d *Descriptor) {
			d.Hostname = "host-"
		}, "letters, digits and hyphens"},
		{"no timezone", func(d *Descriptor) { d.Timezone = "" }, "timezone: missing"},

		// The two that matter most.
		{"cleartext root password", func(d *Descriptor) {
			d.RootPasswordHash = "hunter2"
		}, "never carry a cleartext password"},
		{"no way to log in", func(d *Descriptor) {
			d.RootPasswordHash = ""
		}, "no way to log in"},

		{"user without hash", func(d *Descriptor) {
			d.Users = []User{{Name: "sunlight"}}
		}, "passwordHash missing"},
		{"user with cleartext", func(d *Descriptor) {
			d.Users = []User{{Name: "sunlight", PasswordHash: "letmein"}}
		}, "never carry a cleartext password"},
		{"duplicate user", func(d *Descriptor) {
			d.Users = []User{
				{Name: "a", PasswordHash: "$6$s$h"},
				{Name: "a", PasswordHash: "$6$s$h"},
			}
		}, "more than once"},

		{"static without address", func(d *Descriptor) {
			d.Network = []Interface{{Name: "eth0", Mode: NetStatic}}
		}, "address is missing"},
		{"address without CIDR", func(d *Descriptor) {
			d.Network = []Interface{{Name: "eth0", Mode: NetStatic, Address: "192.168.1.5"}}
		}, "must be CIDR"},
		{"dhcp with address", func(d *Descriptor) {
			d.Network = []Interface{{Name: "eth0", Mode: NetDHCP, Address: "10.0.0.1/8"}}
		}, "address is set but mode"},
		{"unknown mode", func(d *Descriptor) {
			d.Network = []Interface{{Name: "eth0", Mode: "bridge"}}
		}, "is not dhcp, static or none"},
		{"wifi without ssid", func(d *Descriptor) {
			d.Network = []Interface{{Name: "wlan0", Mode: NetDHCP, WiFi: &WiFi{}}}
		}, "ssid is missing"},

		{"no source", func(d *Descriptor) { d.Source = "" }, "source: missing"},
		{"network without mirror", func(d *Descriptor) {
			d.Source = SourceNetwork
		}, "mirror: missing"},
		{"rootfs with mirror", func(d *Descriptor) {
			d.Mirror = "https://example.invalid"
		}, "source is \"rootfs\""},

		{"service both enabled and disabled", func(d *Descriptor) {
			d.Services = Services{Enable: []string{"sshd"}, Disable: []string{"sshd"}}
		}, "in both enable and disable"},

		{"no bootloader target", func(d *Descriptor) {
			d.Bootloader.Target = ""
		}, "bootloader.target: missing"},
		{"kernel arg with a space", func(d *Descriptor) {
			d.Bootloader.KernelArgs = []string{"init=/usr/bin/slinit --debug"}
		}, "contains whitespace"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := valid()
			tc.break_(d)
			err := d.Validate()
			if err == nil {
				t.Fatalf("expected a failure mentioning %q, got none", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error does not mention %q:\n%v", tc.want, err)
			}
		})
	}
}

// Bootloader "none" is legitimate: an image built for something else to
// boot.
func TestBootloaderNoneIsAllowed(t *testing.T) {
	d := valid()
	d.Bootloader = Bootloader{Target: BootloaderNone}
	if err := d.Validate(); err != nil {
		t.Errorf("target %q must be allowed: %v", BootloaderNone, err)
	}
}

// A partition with no mountpoint is created but not mounted — swap.
func TestUnmountedPartitionIsAllowed(t *testing.T) {
	d := valid()
	d.Partitions = append(d.Partitions,
		Partition{Size: "2G", Filesystem: "swap"})
	if err := d.Validate(); err != nil {
		t.Errorf("an unmounted partition must be allowed: %v", err)
	}
}

// Every problem is reported at once, so fixing a descriptor by hand
// does not take one run per mistake.
func TestAllProblemsReportedTogether(t *testing.T) {
	d := valid()
	d.Hostname = ""
	d.Timezone = ""
	d.TargetDisk = ""
	err := d.Validate()
	if err == nil {
		t.Fatal("expected failures")
	}
	for _, want := range []string{"hostname: missing", "timezone: missing", "targetDisk: missing"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("missing %q from the combined error:\n%v", want, err)
		}
	}
	if !strings.Contains(err.Error(), "3 problems") {
		t.Errorf("the error should count the problems:\n%v", err)
	}
}

// Declared order is the operator's; mount order is derived. A
// descriptor that declares /boot/efi first must still mount / first.
func TestMountOrderPutsParentsFirst(t *testing.T) {
	d := &Descriptor{Partitions: []Partition{
		{Mountpoint: "/boot/efi", Size: "512M", Filesystem: "vfat"},
		{Size: "2G", Filesystem: "swap"}, // unmounted: must be dropped
		{Mountpoint: "/", Size: SizeRest, Filesystem: "ext4"},
		{Mountpoint: "/boot", Size: "1G", Filesystem: "ext4"},
	}}
	got := d.MountOrder()
	want := []string{"/", "/boot", "/boot/efi"}
	if len(got) != len(want) {
		t.Fatalf("got %d mountable partitions, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i].Mountpoint != want[i] {
			t.Errorf("position %d is %q, want %q (full order: %v)",
				i, got[i].Mountpoint, want[i], mountpoints(got))
		}
	}
}

func mountpoints(ps []Partition) []string {
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = p.Mountpoint
	}
	return out
}
