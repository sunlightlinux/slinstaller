package network

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
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

func TestDhcpcdConfDomainAndMultiDNS(t *testing.T) {
	conf, err := DhcpcdConf([]Interface{{
		Name:   "eth0",
		Static: true,
		Addrs:  []Addr{{IP: "192.168.1.50", Netmask: "255.255.255.0"}},
		DNS:    "10.0.0.1 10.0.0.2",
		Domain: "example.com",
	}})
	if err != nil {
		t.Fatalf("DhcpcdConf: %v", err)
	}
	for _, want := range []string{
		"static domain_name_servers=10.0.0.1 10.0.0.2",
		"static domain_search=example.com",
	} {
		if !strings.Contains(conf, want) {
			t.Errorf("conf missing %q:\n%s", want, conf)
		}
	}
}

func TestValidAddr(t *testing.T) {
	if err := ValidAddr("192.168.1.10", "255.255.255.0", false); err != nil {
		t.Errorf("good v4 address rejected: %v", err)
	}
	if err := ValidAddr("fe80::1", "64", true); err != nil {
		t.Errorf("good v6 address rejected: %v", err)
	}
	if err := ValidAddr("", "255.255.255.0", false); err == nil {
		t.Error("empty IP accepted")
	}
	if err := ValidAddr("not-an-ip", "255.255.255.0", false); err == nil {
		t.Error("bad IP accepted")
	}
	if err := ValidAddr("192.168.1.10", "not-a-mask", false); err == nil {
		t.Error("bad netmask accepted")
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

func TestRetry(t *testing.T) {
	// Network comes up on the third attempt: retry gets there, and
	// says once that it is waiting.
	calls, notified := 0, 0
	err := retry(func() error {
		calls++
		if calls < 3 {
			return errors.New("no route")
		}
		return nil
	}, time.Second, time.Millisecond, func() { notified++ })
	if err != nil {
		t.Fatalf("retry = %v, want success on the third attempt", err)
	}
	if calls != 3 || notified != 1 {
		t.Errorf("calls = %d, notified = %d; want 3 and 1", calls, notified)
	}

	// Never comes up: the last error comes back once the wait is spent.
	start := time.Now()
	err = retry(func() error { return errors.New("no route") }, 30*time.Millisecond, 5*time.Millisecond, nil)
	if err == nil || err.Error() != "no route" {
		t.Errorf("retry = %v, want the last error", err)
	}
	if time.Since(start) > time.Second {
		t.Errorf("retry ran %v, far past its 30ms wait", time.Since(start))
	}

	// Up at once: no wait, no notice.
	notified = 0
	if err := retry(func() error { return nil }, time.Second, time.Millisecond, func() { notified++ }); err != nil || notified != 0 {
		t.Errorf("immediate success: err = %v, notified = %d", err, notified)
	}
}
