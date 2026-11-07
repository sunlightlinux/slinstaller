package tui

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/sunlightlinux/slinstaller/pkg/blockdev"
	"github.com/sunlightlinux/slinstaller/pkg/config"
)

func send(m tea.Model, k tea.KeyMsg) model {
	nm, _ := m.Update(k)
	return nm.(model)
}

var (
	enter = tea.KeyMsg{Type: tea.KeyEnter}
	down  = tea.KeyMsg{Type: tea.KeyDown}
)

func runes(s string) tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)} }

func oneDisk() []blockdev.Device { return []blockdev.Device{{Name: "sda", Path: "/dev/sda"}} }

// planOf builds a config from an editor layout and plans it, as Install
// would.
func planOf(t *testing.T, parts []partEntry) *config.Config {
	t.Helper()
	return &config.Config{
		Hostname: "box",
		Users:    []*config.User{{Login: "me", Password: "$6$a$b", Admin: true}},
		TargetMedia: []*config.Media{{
			Name: "sda", Type: "disk", Children: layoutToMedia("sda", parts),
		}},
	}
}

func TestLayoutToMediaPlain(t *testing.T) {
	cfg := planOf(t, []partEntry{
		{Type: "part", FS: "vfat", Mount: "/boot", Size: "512M"},
		{Type: "part", FS: "swap", Size: "1G"},
		{Type: "part", FS: "ext4", Mount: "/"}, // fill
	})
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	plan, err := cfg.Plan()
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(plan.Parts) != 3 || plan.Parts[0].Device != "/dev/sda1" || plan.Parts[2].Mountpoint != "/" {
		t.Fatalf("parts = %+v", plan.Parts)
	}
}

func TestLayoutToMediaLVM(t *testing.T) {
	cfg := planOf(t, []partEntry{
		{Type: "part", FS: "vfat", Mount: "/boot", Size: "512M"},
		{Type: "lvm", VG: "vg0", Size: "rest", LVs: []lvEntry{
			{Name: "root", FS: "ext4", Mount: "/", Size: "4G"},
			{Name: "home", FS: "ext4", Mount: "/home"}, // fill
		}},
	})
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	plan, err := cfg.Plan()
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	// ESP + PV + 2 LVs.
	if len(plan.Parts) != 4 {
		t.Fatalf("got %d parts, want 4", len(plan.Parts))
	}
	if !plan.Parts[1].IsPV || plan.Parts[1].VGName != "vg0" {
		t.Errorf("PV = %+v", plan.Parts[1])
	}
	if plan.Parts[2].EffectiveDevice() != "/dev/vg0/root" {
		t.Errorf("root LV device = %q", plan.Parts[2].EffectiveDevice())
	}
}

func TestLayoutToMediaEncryptedLVM(t *testing.T) {
	parts := []partEntry{
		{Type: "part", FS: "vfat", Mount: "/boot/efi", Size: "512M"},
		{Type: "lvm", VG: "vg0", Encrypted: true, Size: "rest", LVs: []lvEntry{
			{Name: "root", FS: "ext4", Mount: "/", Size: "4G"},
		}},
	}
	// The editor flags encryption, so the layout needs a passphrase.
	if !hasCrypt(parts) {
		t.Error("encrypted lvm should count as crypt (needs passphrase)")
	}

	cfg := planOf(t, parts)
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	plan, err := cfg.Plan()
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	pv := plan.Parts[1]
	if !pv.IsPV || !pv.Encrypted || pv.EffectiveDevice() != "/dev/mapper/cryptvg0" {
		t.Errorf("PV should be an encrypted container on the mapper: %+v", pv)
	}
}

func TestCanInstall(t *testing.T) {
	m := newModel(oneDisk())
	if m.canInstall() {
		t.Fatal("empty model installable")
	}
	m.diskSet = true
	m.users = []userEntry{{Login: "me", Password: "x", Admin: true}}
	m.parts = []partEntry{
		{Type: "part", FS: "vfat", Mount: "/boot", Size: "512M"},
		{Type: "part", FS: "ext4", Mount: "/"},
	}
	if !m.canInstall() {
		t.Fatal("complete model not installable")
	}
	// A crypt partition needs a passphrase.
	m.parts[1].Type = "crypt"
	if m.canInstall() {
		t.Error("installable with crypt but no passphrase")
	}
	m.passphrase = "x"
	if !m.canInstall() {
		t.Error("not installable with crypt + passphrase")
	}
}

func TestFormAddPartition(t *testing.T) {
	// Hub -> Installation Media (disk + partitions) -> add a plain ext4 root.
	m := openSectionInTab(newModel(oneDisk()), 0, secMedia)
	m = send(m, runes("a"))
	m = send(m, enter)      // type = "part" (first choice)
	m = send(m, enter)      // fs = "ext4" (first choice)
	m = send(m, runes("/")) // mountpoint
	m = send(m, enter)      //
	m = send(m, enter)      // size blank = fill -> append

	got := m
	if len(got.parts) != 1 {
		t.Fatalf("got %d parts, want 1", len(got.parts))
	}
	p := got.parts[0]
	if p.Type != "part" || p.FS != "ext4" || p.Mount != "/" || p.Size != "" {
		t.Fatalf("entry = %+v", p)
	}
}

func TestLineWriter(t *testing.T) {
	var got []string
	w := &lineWriter{sink: func(s string) { got = append(got, s) }}
	// A line split across writes, a trailing \r, and a not-yet-complete line.
	w.Write([]byte("hel"))
	w.Write([]byte("lo\r\nwor"))
	w.Write([]byte("ld\n"))
	w.Write([]byte("partial"))

	if len(got) != 2 || got[0] != "hello" || got[1] != "world" {
		t.Fatalf("got %q, want [hello world] (partial line held back)", got)
	}
}

func TestParseCIDR(t *testing.T) {
	a := parseCIDR("192.168.1.50/24")
	if a == nil || a.IP != "192.168.1.50" || a.Netmask != "255.255.255.0" || a.Version != 0 {
		t.Fatalf("v4 CIDR parsed wrong: %+v", a)
	}
	a6 := parseCIDR("2001:db8::1/64")
	if a6 == nil || a6.Version != 1 || a6.Netmask != "64" {
		t.Fatalf("v6 CIDR parsed wrong: %+v", a6)
	}
	if parseCIDR("not-a-cidr") != nil || parseCIDR("") != nil {
		t.Error("bad CIDR should be nil")
	}
}

func TestStaticInterface(t *testing.T) {
	if staticInterface("", "x", "y", "z") != nil {
		t.Error("blank name must mean DHCP (nil interface)")
	}
	iface := staticInterface("eth0", "10.0.0.5/8", "10.0.0.1", "1.1.1.1")
	if iface == nil || iface.Name != "eth0" || !iface.Static() {
		t.Fatalf("static interface wrong: %+v", iface)
	}
	if len(iface.Addrs) != 1 || iface.Addrs[0].IP != "10.0.0.5" {
		t.Fatalf("address not set: %+v", iface.Addrs)
	}
}

func TestSplitList(t *testing.T) {
	got := splitList("vim, htop  git,,")
	want := []string{"vim", "htop", "git"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
	if splitList("  ") != nil {
		t.Error("blank list should be nil")
	}
}

// withSize returns m with a terminal size set, so View renders at a real
// width/height.
func withSize(m model) model {
	m.width, m.height = 100, 40
	return m
}

// openSectionInTab switches to the given tab, moves the hub cursor to the
// named section, and opens it.
func openSectionInTab(m model, tab int, s section) model {
	if tab == 1 {
		m = send(m, runes("A"))
	}
	vis := visibleSections(tab)
	for i, vs := range vis {
		if vs == s {
			for j := 0; j < i; j++ {
				m = send(m, down)
			}
			break
		}
	}
	return send(m, enter)
}

// TestViewsRenderNoPanic drives the hub, the Installation Media dialog,
// each Advanced section, and both popups, rendering each to confirm the new
// views and the overlay do not panic and are non-empty.
func TestViewsRenderNoPanic(t *testing.T) {
	// Opening the Save Configuration section writes a file; keep it in a temp
	// dir rather than the working tree.
	savedPath := saveConfigPath
	saveConfigPath = filepath.Join(t.TempDir(), "cfg.yaml")
	defer func() { saveConfigPath = savedPath }()

	hub := withSize(newModel(oneDisk()))
	if v := hub.View(); !strings.Contains(v, "[R] Required") {
		t.Errorf("hub render missing the Required tab:\n%s", v)
	}

	// Installation Media dialog (navigate to it, then Enter).
	media := withSize(openSectionInTab(hub, 0, secMedia))
	if v := media.View(); !strings.Contains(v, "Installation Media") {
		t.Errorf("media dialog missing its title:\n%s", v)
	}

	// Advanced tab: open each section and render.
	adv := send(hub, runes("A"))
	for i, s := range visibleSections(1) {
		mm := adv
		for j := 0; j < i; j++ {
			mm = send(mm, down)
		}
		opened := withSize(send(mm, enter))
		if v := opened.View(); v == "" {
			t.Errorf("section %v rendered empty", s)
		}
	}

	// Test Network Settings opens a spinner popup (secondary progress).
	tn := adv
	for _, s := range visibleSections(1) {
		if s == secTestNet {
			break
		}
		tn = send(tn, down)
	}
	tn = withSize(send(tn, enter))
	if v := tn.View(); !strings.Contains(v, "Test Network") {
		t.Errorf("test-network spinner popup not shown:\n%s", v)
	}

	// Adding an encrypted partition in the media dialog pops the passphrase
	// prompt, the way clr collects it.
	mp := openSectionInTab(hub, 0, secMedia)
	mp = send(mp, runes("a")) // add partition
	mp = send(mp, down)       // type cursor -> crypt
	mp = send(mp, enter)      // choose crypt
	mp = send(mp, enter)      // filesystem (first choice)
	mp = send(mp, runes("/data"))
	mp = send(mp, enter) // mountpoint
	mp = send(mp, enter) // size blank -> append -> passphrase popup
	mp = withSize(mp)
	if v := mp.View(); !strings.Contains(v, "Passphrase") {
		t.Errorf("passphrase popup not shown after adding a crypt partition:\n%s", v)
	}

	// The not-ready warning popup (Install pressed on an empty layout).
	warn := hub
	for warn.hubCursor < len(visibleSections(0)) {
		warn = send(warn, down)
	}
	warn = withSize(send(warn, enter)) // Install while incomplete
	if v := warn.View(); !strings.Contains(v, "Not ready") {
		t.Errorf("warning popup not shown:\n%s", v)
	}
}

func TestManageUserTableAddsUser(t *testing.T) {
	// Hub -> Manage User -> Add New User -> fill Login/Name/Password/Admin.
	m := openSectionInTab(newModel(oneDisk()), 0, secUser)
	if v := m.View(); !strings.Contains(v, "No Custom Users Defined") {
		t.Errorf("empty table should say so:\n%s", v)
	}
	m = send(m, runes("a"))     // Add New User
	m = send(m, runes("ionut")) // Login
	m = send(m, enter)
	m = send(m, runes("Ionut N")) // User Name
	m = send(m, enter)
	m = send(m, runes("secret")) // Password
	m = send(m, enter)
	m = send(m, enter) // Admin: Yes (default) -> append

	if len(m.users) != 1 {
		t.Fatalf("got %d users, want 1", len(m.users))
	}
	u := m.users[0]
	if u.Login != "ionut" || u.UserName != "Ionut N" || u.Password != "secret" || !u.Admin {
		t.Fatalf("user = %+v", u)
	}
	// The table now lists the user, masked password, admin yes.
	if v := m.View(); !strings.Contains(v, "ionut") || !strings.Contains(v, "****") {
		t.Errorf("table missing the new user row:\n%s", v)
	}
	// Delete it.
	m.userCursor = 0
	m = send(m, runes("d"))
	if len(m.users) != 0 {
		t.Errorf("delete left %d users", len(m.users))
	}
}

func TestNetworkSubpageStatic(t *testing.T) {
	// Start on the interface list with one interface, configure it static.
	m := newModel(oneDisk())
	m.netIfaces = []netIfaceEntry{{Name: "eth0"}}
	m.view = viewNetList
	m.netCursor = 0

	m = send(m, enter)      // open per-interface form (mode choice)
	m = send(m, runes("l")) // toggle DHCP -> Static
	m = send(m, enter)      // choose Static -> address step
	m = send(m, runes("192.168.1.50/24"))
	m = send(m, enter) // address -> gateway
	m = send(m, runes("192.168.1.1"))
	m = send(m, enter) // gateway -> dns
	m = send(m, runes("1.1.1.1"))
	m = send(m, enter) // dns -> done, back to list

	ni := m.netIfaces[0]
	if !ni.Static || ni.Addr != "192.168.1.50/24" || ni.Gateway != "192.168.1.1" || ni.DNS != "1.1.1.1" {
		t.Fatalf("interface not configured static: %+v", ni)
	}
	if v := m.View(); !strings.Contains(v, "Static 192.168.1.50/24") {
		t.Errorf("list should show the static setting:\n%s", v)
	}

	// DHCP path: reconfigure back to automatic.
	m = send(m, enter)      // open form (edCursor starts at Static=1)
	m = send(m, runes("l")) // toggle -> DHCP (index 0)
	m = send(m, enter)      // choose DHCP
	if m.netIfaces[0].Static {
		t.Error("interface should be back on DHCP")
	}
}

func TestManageUserEditsRow(t *testing.T) {
	// Start with one admin user, then edit it: change the name and make it
	// non-admin, keeping the count at one.
	m := newModel(oneDisk())
	m.users = []userEntry{{Login: "ionut", UserName: "Old", Password: "pw", Admin: true}}
	m.view = viewUserList
	m.userCursor = 0

	m = send(m, enter) // Enter on the row -> edit form (login prefilled)
	m = send(m, enter) // keep login
	// Replace the user name.
	for range "Old" {
		m = send(m, tea.KeyMsg{Type: tea.KeyBackspace})
	}
	m = send(m, runes("New Name"))
	m = send(m, enter)      // user name -> password (prefilled)
	m = send(m, enter)      // keep password
	m = send(m, runes("l")) // Admin toggle Yes -> No
	m = send(m, enter)      // save

	if len(m.users) != 1 {
		t.Fatalf("edit changed the count to %d", len(m.users))
	}
	u := m.users[0]
	if u.Login != "ionut" || u.UserName != "New Name" || u.Password != "pw" || u.Admin {
		t.Fatalf("edited user wrong: %+v", u)
	}
}

func TestSaveConfigWritesFile(t *testing.T) {
	saved := saveConfigPath
	defer func() { saveConfigPath = saved }()
	saveConfigPath = filepath.Join(t.TempDir(), "cfg.yaml")

	m := newModel(oneDisk())
	m.users = []userEntry{{Login: "me", Password: "pw", Admin: true}}
	m.parts = []partEntry{{Type: "part", FS: "ext4", Mount: "/"}}

	m2 := openSectionInTab(m, 1, secSaveConfig)
	if m2.popup != popupMessage {
		t.Fatalf("Save Config should show a result popup")
	}
	data, err := os.ReadFile(saveConfigPath)
	if err != nil {
		t.Fatalf("config not written: %v", err)
	}
	if !strings.Contains(string(data), "login: me") {
		t.Errorf("written config missing the user:\n%s", data)
	}
}

func TestManageUserButtonsNavigate(t *testing.T) {
	m := newModel(oneDisk())
	m.users = []userEntry{{Login: "a", Password: "p", Admin: true}}
	m.view = viewUserList
	m.userCursor = 0 // on the user row

	// Tab moves off the row onto the buttons; two Tabs reach Revert
	// (row=0, Add=1, Revert=2 with one user).
	m = send(m, tea.KeyMsg{Type: tea.KeyTab})
	m = send(m, tea.KeyMsg{Type: tea.KeyTab})
	if m.userCursor != 2 {
		t.Fatalf("after two Tabs cursor=%d, want 2 (Revert)", m.userCursor)
	}
	m = send(m, enter) // Revert clears the table
	if len(m.users) != 0 {
		t.Errorf("Revert did not clear users: %d left", len(m.users))
	}

	// Tab wraps around: from the last button one Tab returns to the top.
	m.userCursor = len(m.users) + len(userButtons) - 1 // last button (Cancel)
	m = send(m, tea.KeyMsg{Type: tea.KeyTab})
	if m.userCursor != 0 {
		t.Errorf("Tab from last did not wrap to 0, got %d", m.userCursor)
	}
}

func TestPanelHeightStable(t *testing.T) {
	// The dialog must not resize moving between the menu and a short section:
	// the title and footer land on the same rows for both.
	titleRow := func(v string) (first, last int) {
		for i, l := range strings.Split(v, "\n") {
			plain := stripANSIForTest(l)
			if strings.Contains(plain, "Sunlight Linux Installer") {
				first = i
			}
			if strings.Contains(plain, "navigate") || strings.Contains(plain, "[Enter] confirm") {
				last = i
			}
		}
		return
	}
	size := func(m model) model { m.width, m.height = 100, 40; return m }

	hub := size(newModel(oneDisk()))
	// Hostname is a single-field text section (footer ends with "[Enter]
	// confirm"), good for checking the box does not resize.
	hn := size(openSectionInTab(newModel(oneDisk()), 1, secHostname))

	hf, hl := titleRow(hub.View())
	tf, tl := titleRow(hn.View())
	if hf != tf || hl != tl {
		t.Errorf("dialog resized between menu and section: hub title@%d footer@%d, section title@%d footer@%d", hf, hl, tf, tl)
	}
	if hl-hf != 33 { // panelHeight(40) = 34 -> span 33 (0-indexed)
		t.Errorf("box height span=%d, want 33", hl-hf)
	}
}

var ansiRe = regexpMustCompile()

func stripANSIForTest(s string) string { return ansiRe.ReplaceAllString(s, "") }

func regexpMustCompile() *regexp.Regexp { return regexp.MustCompile("\x1b\\[[0-9;]*m") }

func TestHubScrollbar(t *testing.T) {
	size := func(m model) model { m.width, m.height = 90, 30; return m }
	plain := func(v string) string { return stripANSIForTest(v) }

	// The Advanced tab has more sections than fit, so it shows a scrollbar
	// and the last section is not visible at the top.
	adv := size(send(newModel(oneDisk()), runes("A")))
	v := plain(adv.View())
	if !strings.Contains(v, "▲") || !strings.Contains(v, "▼") {
		t.Errorf("Advanced tab missing a scrollbar:\n%s", v)
	}
	if strings.Contains(v, "Automatic OS Updates") {
		t.Errorf("last section should be scrolled out of view at the top")
	}

	// Moving the cursor to the bottom scrolls it into view.
	m := adv
	for i := 0; i < len(visibleSections(1))-1; i++ {
		m = send(m, down)
	}
	if !strings.Contains(plain(m.View()), "Automatic OS Updates") {
		t.Errorf("last section not visible after scrolling down:\n%s", plain(m.View()))
	}

	// The Required tab fits, so no scrollbar arrows.
	req := size(newModel(oneDisk()))
	if strings.Contains(plain(req.View()), "▼") {
		t.Errorf("Required tab should not scroll")
	}
}

func TestListSelectAndFilter(t *testing.T) {
	m := newModel(oneDisk())
	m.editing = secTimezone
	m = m.openList([]string{"UTC", "Europe/Berlin", "Europe/Bucharest", "Asia/Tokyo"}, "UTC")
	if m.listCursor != 0 {
		t.Fatalf("cursor should start on the current value, got %d", m.listCursor)
	}
	// Typing filters to the Europe zones.
	for _, r := range "Buch" {
		m = send(m, runes(string(r)))
	}
	got := m.filtered()
	if len(got) != 1 || got[0] != "Europe/Bucharest" {
		t.Fatalf("filter 'Buch' = %v, want [Europe/Bucharest]", got)
	}
	// Enter selects it into the timezone and returns to the hub.
	m = send(m, enter)
	if m.timezone != "Europe/Bucharest" {
		t.Errorf("timezone = %q, want Europe/Bucharest", m.timezone)
	}
	if m.view != viewHub {
		t.Errorf("list did not return to the hub")
	}
}

func TestBundlesChecklist(t *testing.T) {
	m := openSectionInTab(newModel(oneDisk()), 1, secBundles)
	// Toggle "git" (index 2) and "man-pages" (adds two packages).
	m = send(m, down)
	m = send(m, down)
	m = send(m, runes(" ")) // git
	for i := 2; i < 8; i++ {
		m = send(m, down)
	}
	m = send(m, runes(" ")) // man-pages (index 8)
	m = send(m, enter)      // confirm -> hub, derive packages

	if m.view != viewHub {
		t.Fatalf("bundles did not return to the hub")
	}
	// Packages are the ticked bundles' packages, sorted and space-joined.
	if m.packages != "git man-pages mandoc" {
		t.Errorf("packages = %q, want 'git man-pages mandoc'", m.packages)
	}
}
