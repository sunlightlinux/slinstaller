package tui

import (
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/sunlightlinux/slinstaller/pkg/config"
	"github.com/sunlightlinux/slinstaller/pkg/network"
)

// --- Test Network Settings: a secondary-progress popup ---

// netTestTarget is the endpoint the connectivity check probes. A plain,
// widely-reachable host so the test reflects general network health, not a
// specific mirror.
const netTestTarget = "https://repo-default.voidlinux.org/current"

type spinnerTickMsg struct{}

type netTestDoneMsg struct {
	result string
	ok     bool
}

// spinnerTickCmd schedules the next spinner frame.
func spinnerTickCmd() tea.Cmd {
	return tea.Tick(120*time.Millisecond, func(time.Time) tea.Msg { return spinnerTickMsg{} })
}

// testNetCmd runs the connectivity check off the UI goroutine and reports
// the outcome as a message.
func testNetCmd() tea.Cmd {
	return func() tea.Msg {
		if err := network.VerifyConnectivity(netTestTarget); err != nil {
			return netTestDoneMsg{result: "[FAIL] No connectivity: " + err.Error()}
		}
		return netTestDoneMsg{result: "[ OK ] Network is reachable.", ok: true}
	}
}

// staticInterface builds a config.Interface for a single static interface
// from the network form's fields, parsing the CIDR address into an IP and a
// dotted netmask. A blank name (DHCP) or an unparseable address yields nil.
func staticInterface(name, cidr, gateway, dns string) *config.Interface {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil
	}
	iface := &config.Interface{Name: name, DHCP: "false", Gateway: strings.TrimSpace(gateway), DNS: strings.TrimSpace(dns)}
	if a := parseCIDR(strings.TrimSpace(cidr)); a != nil {
		iface.Addrs = []*config.Address{a}
	}
	return iface
}

// parseCIDR turns "192.168.1.50/24" into a config.Address (dotted netmask
// for IPv4, prefix length kept for IPv6), or nil if it does not parse.
func parseCIDR(s string) *config.Address {
	if s == "" {
		return nil
	}
	ip, ipnet, err := net.ParseCIDR(s)
	if err != nil {
		return nil
	}
	if v4 := ip.To4(); v4 != nil {
		mask := ipnet.Mask
		return &config.Address{IP: v4.String(), Netmask: fmt.Sprintf("%d.%d.%d.%d", mask[0], mask[1], mask[2], mask[3]), Version: 0}
	}
	ones, _ := ipnet.Mask.Size()
	return &config.Address{IP: ip.String(), Netmask: strconv.Itoa(ones), Version: 1}
}

// netIfaceEntry is one network interface in the Configure Network Interfaces
// sub-page. Details are the interface's current live addresses (read-only,
// as clr-installer shows them); Static carries a CIDR address, gateway and
// DNS to configure, otherwise the interface is left on DHCP.
type netIfaceEntry struct {
	Name    string
	Details []string // live "ipv4: …" / "netmask: …" lines
	Static  bool
	Addr    string // CIDR, e.g. 192.168.1.50/24
	Gateway string
	DNS     string
}

// liveInterfaces lists the machine's non-loopback interfaces with their
// current addresses, each starting on DHCP, for the network sub-page.
func liveInterfaces() []netIfaceEntry {
	ifs, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var out []netIfaceEntry
	for _, i := range ifs {
		if i.Flags&net.FlagLoopback != 0 {
			continue
		}
		e := netIfaceEntry{Name: i.Name}
		addrs, _ := i.Addrs()
		for _, a := range addrs {
			ipnet, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			kind := "ipv6"
			ip := ipnet.IP
			if v4 := ip.To4(); v4 != nil {
				kind, ip = "ipv4", v4
			}
			// net.IP.String() on the mask gives the dotted form for IPv4 and
			// the ffff:… form for IPv6, matching clr's display.
			e.Details = append(e.Details,
				fmt.Sprintf("%-8s %s", kind+":", ip.String()),
				fmt.Sprintf("%-8s %s", "netmask:", net.IP(ipnet.Mask).String()))
		}
		if len(e.Details) == 0 {
			e.Details = []string{"(no address - link down or DHCP pending)"}
		}
		out = append(out, e)
	}
	return out
}

// updateNetList drives the interface list: pick one to configure, or leave.
func (m model) updateNetList(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch key.String() {
	case "esc":
		m.view = viewHub
	case "up", "k":
		if m.netCursor > 0 {
			m.netCursor--
		}
	case "down", "j":
		if m.netCursor < len(m.netIfaces)-1 {
			m.netCursor++
		}
	case "enter":
		if m.netCursor < len(m.netIfaces) {
			return m.startNetForm(m.netCursor)
		}
	}
	return m, nil
}

// startNetForm opens the per-interface form, starting at the DHCP/Static
// mode choice.
func (m model) startNetForm(idx int) (tea.Model, tea.Cmd) {
	m.view = viewNetForm
	m.netEdit = idx
	m.formStep = 0
	if m.netIfaces[idx].Static {
		m.edCursor = 1
	} else {
		m.edCursor = 0
	}
	return m, nil
}

// updateNetForm configures one interface: first DHCP or Static, then (for
// static) the CIDR address, gateway and DNS.
func (m model) updateNetForm(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	ni := &m.netIfaces[m.netEdit]
	if key.String() == "esc" {
		m.view = viewNetList
		return m, nil
	}
	if m.formStep == 0 { // mode choice: DHCP / Static
		switch key.String() {
		case "up", "k", "down", "j", "left", "h", "right", "l", "tab":
			m.edCursor = 1 - m.edCursor
		case "enter":
			if m.edCursor == 0 { // DHCP
				ni.Static, ni.Addr, ni.Gateway, ni.DNS = false, "", "", ""
				m.view = viewNetList
				return m, nil
			}
			m.formStep = 1 // static: collect fields
			ti := textinput.New()
			ti.Focus()
			ti.SetValue(ni.Addr)
			m.input = ti
			return m, textinput.Blink
		}
		return m, nil
	}
	if key.String() == "enter" {
		v := strings.TrimSpace(m.input.Value())
		switch m.formStep {
		case 1:
			ni.Addr = v
		case 2:
			ni.Gateway = v
		case 3:
			ni.DNS = v
			ni.Static = true
			m.view = viewNetList
			return m, nil
		}
		m.formStep++
		ti := textinput.New()
		ti.Focus()
		switch m.formStep {
		case 2:
			ti.SetValue(ni.Gateway)
		case 3:
			ti.SetValue(ni.DNS)
		}
		m.input = ti
		return m, textinput.Blink
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(key)
	return m, cmd
}

// ifaceLiveV4 returns the interface's current IPv4 address from its live
// details, or "" if it has none.
func ifaceLiveV4(ni netIfaceEntry) string {
	for _, d := range ni.Details {
		if strings.HasPrefix(d, "ipv4:") {
			return strings.TrimSpace(strings.TrimPrefix(d, "ipv4:"))
		}
	}
	return ""
}

// netListView renders the list of interfaces and their current setting.
func (m model) netListView() string {
	w := m.panelWidth()
	body := []string{
		itemLabel.Width(w).Render("  " + sectionTitles[secNetwork]),
		rule(w),
	}
	if len(m.netIfaces) == 0 {
		body = append(body, footer.Width(w).Render("  No network interfaces found"))
	}
	for i, ni := range m.netIfaces {
		setting := "Automatic (DHCP)"
		if ni.Static {
			setting = "Static " + ni.Addr
		} else if v4 := ifaceLiveV4(ni); v4 != "" {
			setting = "DHCP - " + v4
		}
		body = append(body, row(w, i == m.netCursor, fmt.Sprintf("%-14s %s", ni.Name, setting)))
	}
	return m.screen(body, "[Up/Down] move · [Enter] configure · [Esc] back")
}

// netFormView renders the per-interface form: the DHCP/Static mode choice,
// then the static address, gateway and DNS fields.
func (m model) netFormView() string {
	w := m.panelWidth()
	name := ""
	if m.netEdit < len(m.netIfaces) {
		name = m.netIfaces[m.netEdit].Name
	}
	if m.formStep == 0 {
		// clr-style read-only detail header: the interface's current
		// addresses, then the Addressing choice.
		body := []string{
			itemLabel.Width(w).Render("  interface: " + name),
			lineBG.Width(w).Render(""),
		}
		if m.netEdit < len(m.netIfaces) {
			for _, d := range m.netIfaces[m.netEdit].Details {
				body = append(body, itemValue.Width(w).Render("    "+d))
			}
		}
		body = append(body, rule(w), itemLabel.Width(w).Render("  Addressing"), lineBG.Width(w).Render(""))
		for i, opt := range []string{"Automatic (DHCP)", "Static IP"} {
			body = append(body, row(w, i == m.edCursor, opt))
		}
		return m.screen(body, "[Up/Down] choose · [Enter] select · [Esc] back")
	}
	var title, hint string
	switch m.formStep {
	case 1:
		title, hint = "Address in CIDR form", "e.g. 192.168.1.50/24"
	case 2:
		title, hint = "Gateway (blank for none)", "e.g. 192.168.1.1"
	case 3:
		title, hint = "DNS server (blank for none)", "e.g. 192.168.1.1"
	}
	body := []string{
		itemLabel.Width(w).Render("  Configure " + name + " (static)"),
		rule(w),
		itemLabel.Width(w).Render("  " + title),
		footer.Width(w).Render("  " + hint),
		lineBG.Width(w).Render(""),
		lineBG.Width(w).Render("  " + m.input.View()),
	}
	return m.screen(body, "type · [Enter] next · [Esc] cancel")
}
