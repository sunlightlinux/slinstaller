package controller

import (
	"regexp"
	"strings"
	"testing"

	"github.com/sunlightlinux/slinstaller/pkg/config"
	"github.com/sunlightlinux/slinstaller/pkg/network"
)

// linkWordRe is what a word of the link script may be besides its fixed
// keywords: an interface name, a number, a bond mode.
var linkWordRe = regexp.MustCompile(`^[A-Za-z0-9._:-]+$`)

// FuzzNetworkFiles checks that whatever network interfaces a config that
// passes Validate declares (with skipValidationAll or without), the files
// written from them carry nothing but what they are for: /etc/rc.local,
// run by sh as root at every boot, holds only ip commands on interface
// names; dhcpcd.conf only its own directives, one per line.
func FuzzNetworkFiles(f *testing.F) {
	f.Add(uint8(0), "bond0", "eth0", "802.3ad", "", "10.0.0.2", "24", "10.0.0.1", "1.1.1.1", "lan", "10.1.0.0/16", "10.0.0.254", true)
	f.Add(uint8(1), "br0;reboot", "eth0", "", "", "", "", "", "", "", "", "", true)
	f.Add(uint8(2), "v5", "", "", "eth0$(id)", "", "", "", "1.1.1.1\nscript /x", "", "", "", true)
	f.Add(uint8(3), "eth0", "", "", "", "10.0.0.2", "255.255.255.0", "10.0.0.1", "1.1.1.1 8.8.8.8", "a.example b.example", "", "", false)
	f.Fuzz(func(t *testing.T, kind uint8, name, member, mode, link, ip, mask, gw, dns, domain, to, via string, skip bool) {
		ni := &config.Interface{Name: name, Gateway: gw, DNS: dns, Domain: domain}
		if ip != "" {
			ni.Addrs = []*config.Address{{IP: ip, Netmask: mask}}
		}
		if to != "" || via != "" {
			ni.Routes = []*config.Route{{To: to, Via: via}}
		}
		switch kind % 4 {
		case 0:
			ni.Bond = &config.Bond{Members: []string{member}, Mode: mode}
		case 1:
			ni.Bridge = &config.Bridge{Members: []string{member}}
		case 2:
			ni.VLAN = &config.VLAN{ID: 5, Link: link}
		}
		c := twoPartConfig()
		c.SkipValidationAll = skip
		c.NetworkInterfaces = []*config.Interface{ni}
		if c.Validate() != nil {
			return
		}
		ifaces := interfacesFromConfig(c.NetworkInterfaces)
		for _, line := range strings.Split(network.LinkScript(ifaces), "\n") {
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			for _, w := range strings.Fields(line) {
				switch w {
				case "if", "!", "then", "fi", ">/dev/null", "2>&1;":
				default:
					if !linkWordRe.MatchString(w) {
						t.Errorf("rc.local word %q in line %q", w, line)
					}
				}
			}
		}
		conf, err := network.DhcpcdConf(ifaces)
		if err != nil {
			return
		}
		for _, line := range strings.Split(conf, "\n") {
			switch {
			case line == "", strings.HasPrefix(line, "# "),
				strings.HasPrefix(line, "interface "), strings.HasPrefix(line, "static "),
				strings.HasPrefix(line, "denyinterfaces "):
			default:
				t.Errorf("dhcpcd.conf line %q", line)
			}
		}
	})
}
