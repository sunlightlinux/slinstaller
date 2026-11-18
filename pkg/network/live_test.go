package network

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLiveCommands(t *testing.T) {
	cmds, err := LiveCommands(Interface{Name: "eth0", Static: true, Gateway: " 10.0.0.1 ",
		Addrs: []Addr{{IP: "10.0.0.2", Netmask: "255.255.255.0"}, {IP: "fd00::2", Netmask: "64", V6: true}}})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, c := range cmds {
		got = append(got, strings.Join(c, " "))
	}
	// The old addresses go first, so the interface ends with exactly the
	// configured ones; the route comes last, once there is an address for it.
	want := []string{
		"ip addr flush dev eth0",
		"ip addr add 10.0.0.2/24 dev eth0",
		"ip addr add fd00::2/64 dev eth0",
		"ip link set eth0 up",
		"ip route replace default via 10.0.0.1 dev eth0",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("commands:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	// No gateway: no route is touched.
	cmds, _ = LiveCommands(Interface{Name: "eth0", Addrs: []Addr{{IP: "10.0.0.2", Netmask: "24"}}})
	for _, c := range cmds {
		if c[1] == "route" {
			t.Errorf("a route was set without a gateway: %v", c)
		}
	}
	// Nothing to apply, or a mask that means nothing: no commands at all —
	// flushing an interface and then failing would cut the machine off.
	for _, bad := range []Interface{
		{Name: "eth0"},
		{Addrs: []Addr{{IP: "10.0.0.2", Netmask: "24"}}},
		{Name: "eth0", Addrs: []Addr{{IP: "10.0.0.2", Netmask: "255.0.255.0"}}},
	} {
		if cmds, err := LiveCommands(bad); err == nil || cmds != nil {
			t.Errorf("LiveCommands(%+v) = %v, %v", bad, cmds, err)
		}
	}
}

func TestWriteLiveResolv(t *testing.T) {
	defer func(p string) { liveResolvConf = p }(liveResolvConf)
	liveResolvConf = filepath.Join(t.TempDir(), "resolv.conf")
	os.WriteFile(liveResolvConf, []byte("nameserver 192.0.2.1\n"), 0o644)

	// No DNS configured: what is there stays.
	if err := WriteLiveResolv(Interface{Name: "eth0"}); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(liveResolvConf); string(data) != "nameserver 192.0.2.1\n" {
		t.Errorf("resolv.conf was changed with no DNS given: %q", data)
	}
	if err := WriteLiveResolv(Interface{Name: "eth0", DNS: "10.0.0.53 10.0.0.54", Domain: "example.org"}); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(liveResolvConf)
	if !strings.HasSuffix(string(data), "search example.org\nnameserver 10.0.0.53\nnameserver 10.0.0.54\n") {
		t.Errorf("resolv.conf = %q", data)
	}
	liveResolvConf = filepath.Join(t.TempDir(), "no-such-dir", "resolv.conf")
	if err := WriteLiveResolv(Interface{DNS: "10.0.0.53"}); err == nil {
		t.Error("an unwritable resolv.conf was not reported")
	}
}
