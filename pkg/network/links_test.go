package network

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func telcoInterfaces() []Interface {
	return []Interface{
		{Name: "eth0"}, // a member: no address of its own
		{Name: "bond0", Bond: &Bond{Members: []string{"eth0", "eth1"}, Mode: "802.3ad"}},
		{Name: "bond0.100", VLAN: &VLAN{ID: 100, Link: "bond0"}, Static: true, Gateway: "10.0.100.1",
			Addrs:  []Addr{{IP: "10.0.100.2", Netmask: "24"}},
			Routes: []Route{{To: "10.1.0.0/16", Via: "10.0.100.254"}, {To: "10.2.0.0/16", Via: "10.0.100.253"}}},
		{Name: "br0", Bridge: &Bridge{Members: []string{"eth2"}}},
		{Name: "eth3", Static: true, Addrs: []Addr{{IP: "192.168.1.2", Netmask: "24"}}},
	}
}

func TestLinkScript(t *testing.T) {
	script := LinkScript(telcoInterfaces())
	// In the order declared: the VLAN is on the bond, so the bond is first.
	steps := []string{
		"ip link add bond0 type bond mode 802.3ad miimon 100",
		"ip link set eth0 down\nip link set eth0 master bond0\nip link set eth0 up",
		"ip link set eth1 master bond0",
		"ip link set bond0 up",
		"ip link add link bond0 name bond0.100 type vlan id 100",
		"ip link set bond0.100 up",
		"ip link add br0 type bridge",
		"ip link set eth2 master br0",
		"ip link set br0 up",
	}
	last := -1
	for _, s := range steps {
		i := strings.Index(script, s)
		if i < 0 || i < last {
			t.Fatalf("step %q missing or out of order:\n%s", s, script)
		}
		last = i
	}
	// A plain interface is none of the script's business.
	if strings.Contains(script, "eth3") {
		t.Errorf("a physical interface is in the link script:\n%s", script)
	}
	// Running it again must not fail on what is already there.
	if strings.Count(script, "if ! ip link show") != 3 {
		t.Errorf("each link should be made only when absent:\n%s", script)
	}
	// It is a script the shell accepts.
	if sh, err := exec.LookPath("sh"); err == nil {
		path := filepath.Join(t.TempDir(), "links.sh")
		os.WriteFile(path, []byte(script), 0o755)
		if out, err := exec.Command(sh, "-n", path).CombinedOutput(); err != nil {
			t.Errorf("the script does not parse: %v\n%s", err, out)
		}
	}
	// The default bonding mode, and no script when there is nothing to make.
	if s := LinkScript([]Interface{{Name: "bond0", Bond: &Bond{Members: []string{"eth0"}}}}); !strings.Contains(s, "type bond mode active-backup") {
		t.Errorf("default mode:\n%s", s)
	}
	if s := LinkScript([]Interface{{Name: "eth0", Static: true}}); s != "" || HasLinks([]Interface{{Name: "eth0"}}) {
		t.Errorf("a script for nothing: %q", s)
	}
}

func TestDhcpcdConfForLinks(t *testing.T) {
	conf, err := DhcpcdConf(telcoInterfaces())
	if err != nil {
		t.Fatal(err)
	}
	// dhcpcd must not ask for a lease on a member of a bond or bridge.
	if !strings.Contains(conf, "denyinterfaces eth0 eth1 eth2\n") {
		t.Errorf("members are not excluded:\n%s", conf)
	}
	// The VLAN is addressed like any interface, with its routes.
	want := "interface bond0.100\nstatic ip_address=10.0.100.2/24\nstatic routers=10.0.100.1\nstatic routes=10.1.0.0/16 10.0.100.254 10.2.0.0/16 10.0.100.253\n"
	if !strings.Contains(conf, want) {
		t.Errorf("VLAN stanza:\n%s", conf)
	}
	// A bond left on DHCP has no stanza, but its members are still excluded.
	conf, _ = DhcpcdConf([]Interface{{Name: "bond0", Bond: &Bond{Members: []string{"eth0", "eth1"}}}})
	if !strings.Contains(conf, "denyinterfaces eth0 eth1") || strings.Contains(conf, "interface bond0") {
		t.Errorf("DHCP bond:\n%s", conf)
	}
}

func TestLinkNamesAndModes(t *testing.T) {
	for name, ok := range map[string]bool{"bond0": true, "bond0.100": true, "br-lan": true, "": false, "a b": false, "eth/0": false, "sixteen-chars-xx": false, ".": false} {
		if ValidIfName(name) != ok {
			t.Errorf("ValidIfName(%q) = %v", name, !ok)
		}
	}
	for mode, ok := range map[string]bool{"": true, "802.3ad": true, "active-backup": true, "balance-alb": true, "lacp": false, "4": false} {
		if ValidBondMode(mode) != ok {
			t.Errorf("ValidBondMode(%q) = %v", mode, !ok)
		}
	}
}
