package network

import (
	"fmt"
	"os"
	"strings"
)

// LiveCommands returns the commands that put a static interface's settings
// in force on the running system, so an install on a network without DHCP
// can reach its repository: the interface's addresses are replaced by the
// configured ones, the link is brought up, and the default route goes
// through the gateway.
//
// This is for the live system only. The installed system gets the same
// settings through dhcpcd.conf (DhcpcdConf).
func LiveCommands(i Interface) ([][]string, error) {
	if i.Name == "" || len(i.Addrs) == 0 {
		return nil, fmt.Errorf("network: %q has no address to apply", i.Name)
	}
	cmds := [][]string{{"ip", "addr", "flush", "dev", i.Name}}
	for _, a := range i.Addrs {
		prefix, err := maskToCIDR(a.Netmask, a.V6)
		if err != nil {
			return nil, fmt.Errorf("network: %s: %w", i.Name, err)
		}
		cmds = append(cmds, []string{"ip", "addr", "add", fmt.Sprintf("%s/%d", a.IP, prefix), "dev", i.Name})
	}
	cmds = append(cmds, []string{"ip", "link", "set", i.Name, "up"})
	if gw := strings.TrimSpace(i.Gateway); gw != "" {
		cmds = append(cmds, []string{"ip", "route", "replace", "default", "via", gw, "dev", i.Name})
	}
	return cmds, nil
}

// liveResolvConf is the running system's resolver configuration (a
// variable so tests can move it).
var liveResolvConf = "/etc/resolv.conf"

// WriteLiveResolv points the running system's resolver at the interface's
// DNS servers. With none configured the file is left as it is.
func WriteLiveResolv(i Interface) error {
	servers := strings.Fields(i.DNS)
	if len(servers) == 0 {
		return nil
	}
	var b strings.Builder
	b.WriteString("# written by slinstaller for the install\n")
	if d := strings.TrimSpace(i.Domain); d != "" {
		fmt.Fprintf(&b, "search %s\n", d)
	}
	for _, s := range servers {
		fmt.Fprintf(&b, "nameserver %s\n", s)
	}
	if err := os.WriteFile(liveResolvConf, []byte(b.String()), 0o644); err != nil {
		return fmt.Errorf("network: %w", err)
	}
	return nil
}
