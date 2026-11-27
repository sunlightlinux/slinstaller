package network

import (
	"fmt"
	"strings"
)

// A Link is a virtual interface made at boot on top of others: a bond of
// several, a VLAN on one, or a bridge over some. At most one of an
// Interface's Bond, VLAN and Bridge is set.
type (
	Bond struct {
		Members []string
		Mode    string // active-backup (the default), 802.3ad, balance-rr, …
	}
	VLAN struct {
		ID   int
		Link string // the interface the VLAN is on
	}
	Bridge struct {
		Members []string
	}
	// Route is a static route out of an interface.
	Route struct {
		To, Via string
	}
)

// BondModes are the kernel's bonding modes.
var BondModes = []string{"active-backup", "802.3ad", "balance-rr", "balance-xor", "broadcast", "balance-tlb", "balance-alb"}

// ValidBondMode reports whether mode is one the kernel knows ("" is the
// default, active-backup).
func ValidBondMode(mode string) bool {
	if mode == "" {
		return true
	}
	for _, m := range BondModes {
		if m == mode {
			return true
		}
	}
	return false
}

// ValidIfName reports whether s can be a network interface name: 1 to 15
// letters, digits and . _ : - (not "." or ".."). Names go unquoted into
// the boot script that creates bonds, VLANs and bridges, which runs as
// root, and into dhcpcd.conf: "a\nb" or "$(cmd)" passed the old check
// (no space, no slash) and would have run at every boot.
func ValidIfName(s string) bool {
	if s == "" || len(s) > 15 || s == "." || s == ".." {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("._:-", r)) {
			return false
		}
	}
	return true
}

// HasLinks reports whether any interface is a bond, a VLAN or a bridge.
func HasLinks(ifaces []Interface) bool {
	for _, i := range ifaces {
		if i.Bond != nil || i.VLAN != nil || i.Bridge != nil {
			return true
		}
	}
	return false
}

// LinkScript returns the shell script that creates the configured bonds,
// VLANs and bridges, in the order they are declared (so a VLAN on a bond
// comes after the bond), or "" when there are none. It is run once at each
// boot of the installed system; addresses are then given to the new
// interfaces by dhcpcd, as to any other.
//
// The script is idempotent: an interface that already exists is left as it
// is, so running it twice is harmless.
func LinkScript(ifaces []Interface) string {
	if !HasLinks(ifaces) {
		return ""
	}
	var b strings.Builder
	b.WriteString("#!/bin/sh\n# Bonds, VLANs and bridges, as configured at install time by slinstaller.\n")
	enslave := func(master string, members []string) {
		for _, m := range members {
			// A member has to be down to be enslaved.
			fmt.Fprintf(&b, "ip link set %s down\nip link set %s master %s\nip link set %s up\n", m, m, master, m)
		}
	}
	for _, i := range ifaces {
		switch {
		case i.Bond != nil:
			mode := i.Bond.Mode
			if mode == "" {
				mode = "active-backup"
			}
			fmt.Fprintf(&b, "\nif ! ip link show %s >/dev/null 2>&1; then\nip link add %s type bond mode %s miimon 100\n", i.Name, i.Name, mode)
			enslave(i.Name, i.Bond.Members)
			b.WriteString("fi\n")
		case i.Bridge != nil:
			fmt.Fprintf(&b, "\nif ! ip link show %s >/dev/null 2>&1; then\nip link add %s type bridge\n", i.Name, i.Name)
			enslave(i.Name, i.Bridge.Members)
			b.WriteString("fi\n")
		case i.VLAN != nil:
			fmt.Fprintf(&b, "\nif ! ip link show %s >/dev/null 2>&1; then\nip link set %s up\nip link add link %s name %s type vlan id %d\nfi\n",
				i.Name, i.VLAN.Link, i.VLAN.Link, i.Name, i.VLAN.ID)
		default:
			continue
		}
		fmt.Fprintf(&b, "ip link set %s up\n", i.Name)
	}
	return b.String()
}

// members returns the interfaces that carry no address of their own because
// they belong to a bond or a bridge.
func members(ifaces []Interface) []string {
	var out []string
	for _, i := range ifaces {
		if i.Bond != nil {
			out = append(out, i.Bond.Members...)
		}
		if i.Bridge != nil {
			out = append(out, i.Bridge.Members...)
		}
	}
	return out
}
