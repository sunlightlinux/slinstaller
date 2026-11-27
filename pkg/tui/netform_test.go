package tui

import (
	"strings"
	"testing"
)

// staticEth0 is a model on the interface list with eth0 already static.
func staticEth0() model {
	m := withSize(newModel(twoTestDisks()))
	m.view = viewNetList
	m.netIfaces = []netIfaceEntry{{Name: "eth0", Static: true, Addr: "10.0.0.5/24", Gateway: "10.0.0.1", DNS: "10.0.0.53"}}
	return m
}

// TestNetFormEscKeepsTheInterface: Esc part-way through the static form is
// its cancel; it kept the address and gateway already typed, which the hub
// then showed and the install wrote, while the live system — set only at
// the form's end — kept the old ones.
func TestNetFormEscKeepsTheInterface(t *testing.T) {
	m := send(send(staticEth0(), enter), enter) // Static: the address step
	m = send(typeText(m, "10.0.0.99/24"), enter)
	m = send(typeText(m, "10.0.0.254"), enter) // now on DNS
	if m.formStep != 3 {
		t.Fatalf("not on the DNS step: %d", m.formStep)
	}
	m = send(m, escKey)
	want := netIfaceEntry{Name: "eth0", Static: true, Addr: "10.0.0.5/24", Gateway: "10.0.0.1", DNS: "10.0.0.53"}
	if got := m.netIfaces[0]; got.Addr != want.Addr || got.Gateway != want.Gateway || got.DNS != want.DNS || !got.Static {
		t.Errorf("after Esc: %+v, want %+v", got, want)
	}
	if v := m.sectionValue(secNetwork); v != "eth0: 10.0.0.5/24" {
		t.Errorf("hub shows %q", v)
	}
}

// TestNetFormRefusesAnIPv6Gateway: config.Validate refuses an IPv6
// gateway (dhcpcd's static routers are IPv4 only); the form took it,
// applied it to the live system, and the install was refused at Install.
func TestNetFormRefusesAnIPv6Gateway(t *testing.T) {
	m := send(send(staticEth0(), enter), enter)
	m = send(typeText(m, "10.0.0.5/24"), enter)
	for _, gw := range []string{"fe80::1", "::ffff:10.0.0.1"} {
		got := send(typeText(m, gw), enter)
		if got.formStep != 2 || !strings.Contains(got.formErr, "IPv4") {
			t.Errorf("IPv6 gateway %s went through: step %d err %q", gw, got.formStep, got.formErr)
		}
	}
}
