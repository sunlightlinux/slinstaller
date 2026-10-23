package descriptor

import (
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"
)

// hostnameRe is the conservative intersection of what Linux, DNS and
// the tools in between accept: letters, digits and hyphens, not
// starting or ending with a hyphen.
var hostnameRe = regexp.MustCompile(`^[a-zA-Z0-9]([a-zA-Z0-9-]*[a-zA-Z0-9])?$`)

// sizeRe matches a human size such as 512M, 8G, 1024K.
var sizeRe = regexp.MustCompile(`^[0-9]+[KMGT]?$`)

// Validate reports every problem it can find, as one error.
//
// This runs before anything is partitioned, which makes it the last
// point at which nothing has been destroyed. So it checks everything
// checkable without touching the disk, and it reports all failures
// rather than the first: an operator fixing a descriptor by hand
// should not have to run it five times to find five mistakes.
//
// What it cannot check belongs to the controller — whether the target
// disk exists, whether it has data on it, whether a package name
// resolves. Those need the machine.
func (d *Descriptor) Validate() error {
	var p problems

	if d.Version == 0 {
		p.add("version: missing (this build writes version %d)", CurrentVersion)
	} else if d.Version != CurrentVersion {
		p.add("version: %d is not supported; this build understands %d",
			d.Version, CurrentVersion)
	}

	d.validateTarget(&p)
	d.validateIdentity(&p)
	d.validateAccounts(&p)
	d.validateNetwork(&p)
	d.validateSoftware(&p)
	d.validateBootloader(&p)

	return p.err()
}

func (d *Descriptor) validateTarget(p *problems) {
	if d.TargetDisk == "" {
		p.add("targetDisk: missing")
	} else if !path.IsAbs(d.TargetDisk) {
		p.add("targetDisk: %q is not an absolute path", d.TargetDisk)
	}

	if len(d.Partitions) == 0 {
		p.add("partitions: none declared")
		return
	}

	// Declaring a partition layout on a whole disk repartitions it,
	// which destroys whatever is there. Rather than defer that to the
	// controller — where the disk is already in hand and the operator
	// has already committed — require the authorisation up front.
	if !d.Wipe {
		p.add("wipe: must be true to declare a partition layout, " +
			"because repartitioning destroys the target")
	}

	var rest int
	roots := 0
	seen := map[string]bool{}

	for i, part := range d.Partitions {
		where := fmt.Sprintf("partitions[%d]", i)

		switch {
		case part.Size == "":
			p.add("%s: size missing", where)
		case part.Size == SizeRest:
			rest++
		case !sizeRe.MatchString(part.Size):
			p.add("%s: size %q is not a number optionally followed by K, M, G or T",
				where, part.Size)
		}

		if part.Filesystem == "" {
			p.add("%s: filesystem missing", where)
		}

		if part.Mountpoint == "" {
			continue // created but not mounted; swap is the usual case
		}
		if !path.IsAbs(part.Mountpoint) {
			p.add("%s: mountpoint %q is not absolute", where, part.Mountpoint)
		}
		if part.Mountpoint == "/" {
			roots++
		}
		if seen[part.Mountpoint] {
			p.add("%s: mountpoint %q declared more than once", where, part.Mountpoint)
		}
		seen[part.Mountpoint] = true
	}

	if rest > 1 {
		p.add("partitions: %d declare size %q; at most one can take what remains",
			rest, SizeRest)
	}
	switch roots {
	case 0:
		p.add("partitions: none mounts /")
	case 1:
	default:
		p.add("partitions: %d mount /", roots)
	}
}

func (d *Descriptor) validateIdentity(p *problems) {
	switch {
	case d.Hostname == "":
		p.add("hostname: missing")
	case len(d.Hostname) > 63:
		p.add("hostname: %d characters; the limit is 63", len(d.Hostname))
	case !hostnameRe.MatchString(d.Hostname):
		p.add("hostname: %q must be letters, digits and hyphens, "+
			"not starting or ending with a hyphen", d.Hostname)
	}

	for field, v := range map[string]string{
		"timezone": d.Timezone,
		"locale":   d.Locale,
		"keymap":   d.Keymap,
	} {
		if v == "" {
			p.add("%s: missing", field)
		}
	}
}

// looksHashed reports whether s is a crypt(3) hash rather than a
// cleartext password. Every modern scheme is $id$...; nothing else is
// accepted, so a descriptor cannot carry a password in the clear even
// by accident.
func looksHashed(s string) bool {
	return strings.HasPrefix(s, "$") && strings.Count(s, "$") >= 3
}

func (d *Descriptor) validateAccounts(p *problems) {
	// An install that finishes with no way to log in is a failure that
	// looks like a success until somebody reboots. Check it here,
	// where it is still cheap.
	loginPossible := false

	if d.RootPasswordHash != "" {
		if !looksHashed(d.RootPasswordHash) {
			p.add("rootPasswordHash: does not look like a crypt(3) hash; " +
				"a descriptor must never carry a cleartext password")
		} else {
			loginPossible = true
		}
	}

	names := map[string]bool{}
	for i, u := range d.Users {
		where := fmt.Sprintf("users[%d]", i)

		if u.Name == "" {
			p.add("%s: name missing", where)
		} else if names[u.Name] {
			p.add("%s: name %q declared more than once", where, u.Name)
		}
		names[u.Name] = true

		switch {
		case u.PasswordHash == "":
			p.add("%s (%s): passwordHash missing", where, u.Name)
		case !looksHashed(u.PasswordHash):
			p.add("%s (%s): passwordHash does not look like a crypt(3) hash; "+
				"a descriptor must never carry a cleartext password", where, u.Name)
		default:
			loginPossible = true
		}
	}

	if !loginPossible {
		p.add("accounts: no root password and no user with a password — " +
			"the installed system would have no way to log in")
	}
}

func (d *Descriptor) validateNetwork(p *problems) {
	seen := map[string]bool{}
	for i, iface := range d.Network {
		where := fmt.Sprintf("network[%d]", i)

		if iface.Name == "" {
			p.add("%s: interface missing", where)
		} else if seen[iface.Name] {
			p.add("%s: interface %q declared more than once", where, iface.Name)
		}
		seen[iface.Name] = true

		switch iface.Mode {
		case NetDHCP, NetNone:
			if iface.Address != "" {
				p.add("%s: address is set but mode is %q", where, iface.Mode)
			}
		case NetStatic:
			if iface.Address == "" {
				p.add("%s: mode is static but address is missing", where)
			} else if !strings.Contains(iface.Address, "/") {
				p.add("%s: address %q must be CIDR, e.g. 192.168.1.50/24",
					where, iface.Address)
			}
		case "":
			p.add("%s: mode missing (dhcp, static or none)", where)
		default:
			p.add("%s: mode %q is not dhcp, static or none", where, iface.Mode)
		}

		if iface.WiFi != nil && iface.WiFi.SSID == "" {
			p.add("%s: wifi is set but ssid is missing", where)
		}
	}
}

func (d *Descriptor) validateSoftware(p *problems) {
	switch d.Source {
	case SourceRootfs:
		if d.Mirror != "" {
			p.add("mirror: set, but source is %q, which copies the live image",
				SourceRootfs)
		}
	case SourceNetwork:
		if d.Mirror == "" {
			p.add("mirror: missing, and source is %q", SourceNetwork)
		}
	case "":
		p.add("source: missing (%q or %q)", SourceRootfs, SourceNetwork)
	default:
		p.add("source: %q is not %q or %q", d.Source, SourceRootfs, SourceNetwork)
	}

	for i, name := range d.Packages {
		if strings.TrimSpace(name) == "" {
			p.add("packages[%d]: empty name", i)
		}
	}

	// Enabling and disabling the same service is a contradiction, and
	// which one wins would be an implementation detail nobody should
	// have to know.
	enabled := map[string]bool{}
	for _, s := range d.Services.Enable {
		enabled[s] = true
	}
	for _, s := range d.Services.Disable {
		if enabled[s] {
			p.add("services: %q is in both enable and disable", s)
		}
	}
}

func (d *Descriptor) validateBootloader(p *problems) {
	switch {
	case d.Bootloader.Target == "":
		p.add("bootloader.target: missing (a device, or %q to skip)", BootloaderNone)
	case d.Bootloader.Target == BootloaderNone:
	case !path.IsAbs(d.Bootloader.Target):
		p.add("bootloader.target: %q is not an absolute path or %q",
			d.Bootloader.Target, BootloaderNone)
	}

	for i, a := range d.Bootloader.KernelArgs {
		if strings.ContainsAny(a, " \t") {
			p.add("bootloader.kernelArgs[%d]: %q contains whitespace; "+
				"each argument is one word", i, a)
		}
	}
}

// MountOrder returns the mountable partitions ordered so that a
// parent is always mounted before anything beneath it.
//
// Sorting by path depth, then lexically, is what makes that true: "/"
// comes before "/boot", which comes before "/boot/efi". The declared
// order in the descriptor is the operator's, and is not required to be
// mountable — relying on it would make a correct descriptor fail for a
// reason nobody could see.
func (d *Descriptor) MountOrder() []Partition {
	out := make([]Partition, 0, len(d.Partitions))
	for _, p := range d.Partitions {
		if p.Mountpoint != "" {
			out = append(out, p)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		di := strings.Count(path.Clean(out[i].Mountpoint), "/")
		dj := strings.Count(path.Clean(out[j].Mountpoint), "/")
		if di != dj {
			return di < dj
		}
		return out[i].Mountpoint < out[j].Mountpoint
	})
	return out
}

// problems collects every validation failure so they can be reported
// together.
type problems struct{ list []string }

func (p *problems) add(format string, args ...interface{}) {
	p.list = append(p.list, fmt.Sprintf(format, args...))
}

func (p *problems) err() error {
	switch len(p.list) {
	case 0:
		return nil
	case 1:
		return fmt.Errorf("descriptor: %s", p.list[0])
	default:
		return fmt.Errorf("descriptor: %d problems:\n  - %s",
			len(p.list), strings.Join(p.list, "\n  - "))
	}
}
