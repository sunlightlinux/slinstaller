// Package network configures the installed system's networking.
//
// clr-installer writes systemd-networkd or NetworkManager files; neither
// fits Sunlight, which has no systemd. The Void-lineage mechanism is
// dhcpcd, and it covers both cases with one tool: left alone it does DHCP
// on every interface, and a static interface is a stanza in
// /etc/dhcpcd.conf. So this package renders those stanzas; the caller
// enables the dhcpcd service and the installed system comes up on boot.
package network

import (
	"fmt"
	"net"
	"strconv"
	"strings"
)

// Addr is one static address on an interface.
type Addr struct {
	IP      string
	Netmask string // dotted (255.255.255.0), a prefix length, or an IPv6 mask
	V6      bool
}

// Interface is one interface to configure. A static interface has
// addresses; otherwise it is left to dhcpcd's DHCP default.
type Interface struct {
	Name    string
	Static  bool
	Addrs   []Addr
	Gateway string
	DNS     string
}

// DhcpcdConf renders the /etc/dhcpcd.conf stanzas for the static
// interfaces among ifaces. DHCP interfaces produce nothing — dhcpcd's
// default already handles them — so the result is empty when nothing is
// static.
func DhcpcdConf(ifaces []Interface) (string, error) {
	var b strings.Builder
	for _, iface := range ifaces {
		if !iface.Static || iface.Name == "" {
			continue
		}

		fmt.Fprintf(&b, "\ninterface %s\n", iface.Name)
		for _, a := range iface.Addrs {
			cidr, err := maskToCIDR(a.Netmask, a.V6)
			if err != nil {
				return "", fmt.Errorf("network: %s: %w", iface.Name, err)
			}
			key := "ip_address"
			if a.V6 {
				key = "ip6_address"
			}
			fmt.Fprintf(&b, "static %s=%s/%d\n", key, a.IP, cidr)
		}
		if iface.Gateway != "" {
			fmt.Fprintf(&b, "static routers=%s\n", iface.Gateway)
		}
		if iface.DNS != "" {
			fmt.Fprintf(&b, "static domain_name_servers=%s\n", iface.DNS)
		}
	}
	if b.Len() == 0 {
		return "", nil
	}
	return "\n# static interfaces configured by slinstaller\n" + b.String(), nil
}

// maskToCIDR turns a netmask into a prefix length. It accepts a dotted
// IPv4 mask, an IPv6 mask, or a plain prefix length already.
func maskToCIDR(mask string, v6 bool) (int, error) {
	mask = strings.TrimSpace(mask)
	if mask == "" {
		if v6 {
			return 64, nil
		}
		return 24, nil
	}
	if n, err := strconv.Atoi(mask); err == nil {
		return n, nil
	}

	ip := net.ParseIP(mask)
	if ip == nil {
		return 0, fmt.Errorf("invalid netmask %q", mask)
	}
	var m net.IPMask
	if v6 {
		m = net.IPMask(ip.To16())
	} else {
		m = net.IPMask(ip.To4())
	}
	ones, bits := m.Size()
	if bits == 0 {
		return 0, fmt.Errorf("non-contiguous netmask %q", mask)
	}
	return ones, nil
}
