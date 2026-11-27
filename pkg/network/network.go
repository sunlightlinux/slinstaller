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
	"errors"
	"fmt"
	"net"
	"net/http"
	neturl "net/url"
	"strconv"
	"strings"
	"time"
)

// VerifyConnectivity checks that url is reachable, so an install can fail
// before wiping a disk rather than partway through the base-system
// download. Any HTTP response counts as reachable — connectivity is the
// question, not the exact status; only a transport error (no DNS, no
// route) is a failure. It uses the environment's proxy, if any.
func VerifyConnectivity(url string) error {
	return verify(url, 10*time.Second)
}

// VerifyConnectivityVia is VerifyConnectivity through the given proxy
// URL — one the operator has typed but the environment does not carry
// yet — or, with proxy "", through the environment's.
func VerifyConnectivityVia(url, proxy string) error {
	if strings.TrimSpace(proxy) == "" {
		return VerifyConnectivity(url)
	}
	pu, err := parseProxy(strings.TrimSpace(proxy))
	if err != nil {
		return errors.New("network: the proxy is not a URL") // its text may hold a password
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.Proxy = http.ProxyURL(pu)
	return verifyWith(&http.Client{Timeout: 10 * time.Second, Transport: tr}, url)
}

// parseProxy reads a proxy setting as the install's https_proxy is read
// (Go's ProxyFromEnvironment, and xbps): one without a scheme - host:port,
// an address, user:pw@host - is an http:// one.
func parseProxy(proxy string) (*neturl.URL, error) {
	pu, err := neturl.Parse(proxy)
	if err != nil || pu.Scheme == "" || pu.Host == "" {
		if pu, err := neturl.Parse("http://" + proxy); err == nil {
			return pu, nil
		}
	}
	return pu, err
}

// verify makes one request, a GET whose body is not read: a server that
// refuses HEAD still answers it, and one request instead of a HEAD then a
// GET halves the time an unreachable mirror takes to fail.
func verify(url string, timeout time.Duration) error {
	return verifyWith(&http.Client{Timeout: timeout}, url)
}

func verifyWith(client *http.Client, url string) error {
	resp, err := client.Get(url)
	if err != nil {
		return fmt.Errorf("network: cannot reach %s: %w", url, err)
	}
	resp.Body.Close()
	return nil
}

// WaitForConnectivity retries VerifyConnectivity until url answers or
// wait has passed. Just after boot the network is usually still coming
// up -- dhcpcd backgrounds at once, then spends seconds probing its
// address before applying it -- so one attempt fails installs that a
// short wait would let through. notify, if set, is called once, before
// the first retry, so the operator knows why nothing is happening. Each
// attempt waits at most 5 seconds and no longer than what is left, so the
// whole returns by its deadline (it used to take 66 s for a minute).
func WaitForConnectivity(url string, wait time.Duration, notify func()) error {
	deadline := time.Now().Add(wait)
	return retry(func() error {
		left := time.Until(deadline)
		if left > 5*time.Second {
			left = 5 * time.Second
		}
		if left < time.Second {
			left = time.Second
		}
		return verify(url, left)
	}, wait, 3*time.Second, notify)
}

// retry calls check until it succeeds or wait has passed, sleeping every
// between attempts, and returns the last error.
func retry(check func() error, wait, every time.Duration, notify func()) error {
	deadline := time.Now().Add(wait)
	err := check()
	for err != nil && time.Now().Add(every).Before(deadline) {
		if notify != nil {
			notify()
			notify = nil
		}
		time.Sleep(every)
		err = check()
	}
	return err
}

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
	DNS     string // one or more servers, space-separated
	Domain  string // search domain (clr-installer's interface "domain")

	// A virtual interface made on top of others (see links.go), and the
	// static routes out of this one.
	Bond   *Bond
	VLAN   *VLAN
	Bridge *Bridge
	Routes []Route
}

// ValidAddr reports whether ip and netmask form a usable static address
// (v6 selects IPv6). It is the validation behind DhcpcdConf, exposed so a
// config can be rejected before any disk is touched rather than failing
// partway through an install.
func ValidAddr(ip, netmask string, v6 bool) error {
	parsed := net.ParseIP(ip)
	if parsed == nil {
		return fmt.Errorf("invalid IP %q", ip)
	}
	// An IPv4-mapped IPv6 address ("::ffff:1.2.3.4") is written as IPv6.
	is4 := parsed.To4() != nil && !strings.Contains(ip, ":")
	if v6 && is4 {
		return fmt.Errorf("IP %q is IPv4 but the address is marked IPv6", ip)
	}
	if !v6 && !is4 {
		return fmt.Errorf("IP %q is IPv6 but the address is marked IPv4", ip)
	}
	if _, err := maskToCIDR(netmask, v6); err != nil {
		return err
	}
	return nil
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
		if iface.Domain != "" {
			fmt.Fprintf(&b, "static domain_search=%s\n", iface.Domain)
		}
		// One route per line: dhcpcd reads a routes= value as one
		// destination and one gateway, so two on a line were refused
		// whole ("invalid CIDR") and the interface had none.
		for _, r := range iface.Routes {
			fmt.Fprintf(&b, "static routes=%s %s\n", r.To, r.Via)
		}
	}
	head := DhcpcdGlobal(ifaces)
	if b.Len() == 0 {
		return head, nil
	}
	return head + "\n# static interfaces configured by slinstaller\n" + b.String(), nil
}

// DhcpcdGlobal is the part of DhcpcdConf that is a global option, not an
// interface's stanza: the members of a bond or bridge, which carry no
// address of their own (left alone, dhcpcd would ask for a lease on each).
// dhcpcd reads a global option only before the first interface block, so
// the caller must not put it after one.
func DhcpcdGlobal(ifaces []Interface) string {
	if m := members(ifaces); len(m) > 0 {
		return "\n# members of a bond or bridge, configured by slinstaller\ndenyinterfaces " + strings.Join(m, " ") + "\n"
	}
	return ""
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
		limit := 32
		if v6 {
			limit = 128
		}
		if n < 0 || n > limit {
			return 0, fmt.Errorf("prefix length %d out of range 0..%d", n, limit)
		}
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
