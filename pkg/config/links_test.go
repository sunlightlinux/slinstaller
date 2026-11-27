package config

import (
	"strings"
	"testing"
)

func linkedConfig() *Config {
	c := btrfsConfig()
	c.NetworkInterfaces = []*Interface{
		nil,
		{Name: "bond0", Bond: &Bond{Members: []string{"eth0", "eth1"}, Mode: "802.3ad"}},
		{Name: "bond0.100", VLAN: &VLAN{ID: 100, Link: "bond0"},
			Addrs: []*Address{{IP: "10.0.100.2", Netmask: "24"}}, Gateway: "10.0.100.1",
			Routes: []*Route{nil, {To: "10.1.0.0/16", Via: "10.0.100.254"}}},
		{Name: "br0", Bridge: &Bridge{Members: []string{"eth2"}}},
	}
	return c
}

func TestBondVLANBridgeValidation(t *testing.T) {
	if err := linkedConfig().Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	for name, tc := range map[string]struct {
		change func(c *Config)
		want   string
	}{
		"no members":        {func(c *Config) { c.NetworkInterfaces[1].Bond.Members = nil }, "a bond needs at least one member"},
		"bad mode":          {func(c *Config) { c.NetworkInterfaces[1].Bond.Mode = "lacp" }, `bond mode "lacp" is not one of`},
		"bad member":        {func(c *Config) { c.NetworkInterfaces[1].Bond.Members[0] = "eth 0" }, `bond member "eth 0" is not an interface name`},
		"member twice":      {func(c *Config) { c.NetworkInterfaces[3].Bridge.Members = []string{"eth1"} }, "eth1 is already a member of bond0"},
		"VLAN id":           {func(c *Config) { c.NetworkInterfaces[2].VLAN.ID = 4095 }, "VLAN id 4095 is not between 1 and 4094"},
		"VLAN without link": {func(c *Config) { c.NetworkInterfaces[2].VLAN.Link = "" }, "a VLAN needs the interface it is on"},
		"two kinds":         {func(c *Config) { c.NetworkInterfaces[1].Bridge = &Bridge{} }, "is a bond, a VLAN or a bridge, not several"},
		"long name":         {func(c *Config) { c.NetworkInterfaces[1].Name = "a-very-long-bond-name" }, "not an interface name (1 to 15"},
		"route target":      {func(c *Config) { c.NetworkInterfaces[2].Routes[1].To = "10.1.0.0" }, "is not in CIDR form"},
		"route gateway":     {func(c *Config) { c.NetworkInterfaces[2].Routes[1].Via = "gw" }, `route via "gw" is not a valid IP`},
		"routes on DHCP":    {func(c *Config) { c.NetworkInterfaces[2].Addrs = nil }, "static routes need a static address"},
		// What goes unquoted into the root boot script and dhcpcd.conf.
		"name runs a command": {func(c *Config) { c.NetworkInterfaces[1].Bond.Members[0] = "$(reboot)" }, `bond member "$(reboot)" is not an interface name`},
		"name with a newline": {func(c *Config) { c.NetworkInterfaces[1].Name = "bond0\nx" }, "not an interface name"},
		"dns with commas":     {func(c *Config) { c.NetworkInterfaces[2].DNS = "8.8.8.8,8.8.4.4" }, `dns "8.8.8.8,8.8.4.4" is not an IP address`},
		"dns with a newline":  {func(c *Config) { c.NetworkInterfaces[2].DNS = "1.1.1.1\ninterface wlan0" }, "dns is one line"},
		"bad domain":          {func(c *Config) { c.NetworkInterfaces[2].Domain = "ex ample;" }, `domain "ample;" is not a domain name`},
		"IPv6 gateway":        {func(c *Config) { c.NetworkInterfaces[2].Gateway = "2001:db8::1" }, "gateway 2001:db8::1 is IPv6"},
		"IPv6 route":          {func(c *Config) { c.NetworkInterfaces[2].Routes[1].To = "2001:db8:1::/48" }, "route to 2001:db8:1::/48 is IPv6"},
		// The addresses of a bond go on the bond, not on what it is made of.
		"address on a member": {func(c *Config) {
			c.NetworkInterfaces = append(c.NetworkInterfaces, &Interface{Name: "eth0", Addrs: []*Address{{IP: "10.0.0.2", Netmask: "24"}}})
		}, "eth0: is a member of bond0"},
	} {
		c := linkedConfig()
		tc.change(c)
		if err := c.Validate(); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: error %v, want %q", name, err, tc.want)
		}
	}
	// It survives being saved and read back.
	path := t.TempDir() + "/c.yaml"
	if err := Save(path, linkedConfig()); err != nil {
		t.Fatal(err)
	}
	back, err := Load(path)
	if err != nil || back.NetworkInterfaces[1].Bond.Mode != "802.3ad" || back.NetworkInterfaces[2].VLAN.ID != 100 || back.NetworkInterfaces[2].Routes[1].Via != "10.0.100.254" {
		t.Errorf("reloaded: %v", err)
	}
}
