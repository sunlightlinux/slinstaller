package network

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDhcpcdConfStatic(t *testing.T) {
	conf, err := DhcpcdConf([]Interface{
		{
			Name:    "eth0",
			Static:  true,
			Addrs:   []Addr{{IP: "192.168.1.50", Netmask: "255.255.255.0"}},
			Gateway: "192.168.1.1",
			DNS:     "10.0.0.1",
		},
	})
	if err != nil {
		t.Fatalf("DhcpcdConf: %v", err)
	}
	for _, want := range []string{
		"interface eth0",
		"static ip_address=192.168.1.50/24",
		"static routers=192.168.1.1",
		"static domain_name_servers=10.0.0.1",
	} {
		if !strings.Contains(conf, want) {
			t.Errorf("conf missing %q:\n%s", want, conf)
		}
	}
}

func TestDhcpcdConfDHCPIsEmpty(t *testing.T) {
	// A DHCP interface (not static) produces no stanza — dhcpcd's default
	// already does DHCP.
	conf, err := DhcpcdConf([]Interface{{Name: "eth0", Static: false}})
	if err != nil {
		t.Fatalf("DhcpcdConf: %v", err)
	}
	if conf != "" {
		t.Errorf("DHCP interface should render nothing, got:\n%s", conf)
	}
}

func TestDhcpcdConfIPv6(t *testing.T) {
	conf, err := DhcpcdConf([]Interface{{
		Name:   "eth0",
		Static: true,
		Addrs:  []Addr{{IP: "fe80::1", Netmask: "64", V6: true}},
	}})
	if err != nil {
		t.Fatalf("DhcpcdConf: %v", err)
	}
	if !strings.Contains(conf, "static ip6_address=fe80::1/64") {
		t.Errorf("IPv6 stanza wrong:\n%s", conf)
	}
}

func TestMaskToCIDR(t *testing.T) {
	cases := []struct {
		mask string
		v6   bool
		want int
	}{
		{"255.255.255.0", false, 24},
		{"255.255.0.0", false, 16},
		{"255.255.255.128", false, 25},
		{"24", false, 24}, // already a prefix length
		{"", false, 24},   // default v4
		{"", true, 64},    // default v6
	}
	for _, c := range cases {
		got, err := maskToCIDR(c.mask, c.v6)
		if err != nil {
			t.Errorf("maskToCIDR(%q): %v", c.mask, err)
			continue
		}
		if got != c.want {
			t.Errorf("maskToCIDR(%q) = %d, want %d", c.mask, got, c.want)
		}
	}
	if _, err := maskToCIDR("not-a-mask", false); err == nil {
		t.Error("maskToCIDR should reject garbage")
	}
}

func TestVerifyConnectivity(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound) // any response counts as reachable
	}))
	defer srv.Close()

	if err := VerifyConnectivity(srv.URL); err != nil {
		t.Errorf("reachable server reported unreachable: %v", err)
	}

	// A port nothing listens on is unreachable.
	if err := VerifyConnectivity("http://127.0.0.1:1"); err == nil {
		t.Error("unreachable URL reported reachable")
	}
}
