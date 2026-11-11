package tui

import (
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/sunlightlinux/slinstaller/pkg/blockdev"
	"github.com/sunlightlinux/slinstaller/pkg/config"
	"github.com/sunlightlinux/slinstaller/pkg/system"
)

func send(m tea.Model, k tea.KeyMsg) model {
	nm, _ := m.Update(k)
	return nm.(model)
}

var (
	enter  = tea.KeyMsg{Type: tea.KeyEnter}
	down   = tea.KeyMsg{Type: tea.KeyDown}
	up     = tea.KeyMsg{Type: tea.KeyUp}
	tabKey = tea.KeyMsg{Type: tea.KeyTab}
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

// openMediaEditor opens Installation Media and activates the Partition
// button, landing in the manual partition editor.
func openMediaEditor(start model) model {
	m := openSectionInTab(start, 0, secMedia) // -> the media front dialog
	m.mediaCursor = m.mediaEncIndex() + 1 + 1 // the "Partition" button
	return send(m, enter)                     // -> the partition list
}

func TestFormAddPartition(t *testing.T) {
	// Hub -> Installation Media -> Partition -> add a plain ext4 root.
	m := openMediaEditor(newModel(oneDisk()))
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
	vis := m.visibleSections(tab)
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

// kernelTop resets the profile-list cursor to the first entry. The list opens
// on the current selection, which the Server default makes non-zero, so tests
// that count `down` presses from the top reset first.
func kernelTop(m model) model {
	for i := 0; i < len(kernelProfiles); i++ {
		m = send(m, up)
	}
	return m
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
	for i, s := range adv.visibleSections(1) {
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
	for _, s := range adv.visibleSections(1) {
		if s == secTestNet {
			break
		}
		tn = send(tn, down)
	}
	tn = withSize(send(tn, enter))
	if v := tn.View(); !strings.Contains(v, "Test Network") {
		t.Errorf("test-network spinner popup not shown:\n%s", v)
	}

	// Adding an encrypted partition in the partition editor pops the
	// passphrase prompt, the way clr collects it.
	mp := openMediaEditor(hub)
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
	for warn.hubCursor < len(warn.visibleSections(0)) {
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
	m = send(m, runes("secret123")) // Password
	m = send(m, enter)
	m = send(m, runes("secret123")) // Confirm
	m = send(m, enter)
	m = send(m, enter) // Admin: Yes (default) -> append

	if len(m.users) != 1 {
		t.Fatalf("got %d users, want 1", len(m.users))
	}
	u := m.users[0]
	if u.Login != "ionut" || u.UserName != "Ionut N" || u.Password != "secret123" || !u.Admin {
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
	m.users = []userEntry{{Login: "ionut", UserName: "Old", Password: "password1", Admin: true}}
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
	m = send(m, enter)      // its confirmation is prefilled too
	m = send(m, runes("l")) // Admin toggle Yes -> No
	m = send(m, enter)      // save

	if len(m.users) != 1 {
		t.Fatalf("edit changed the count to %d", len(m.users))
	}
	u := m.users[0]
	if u.Login != "ionut" || u.UserName != "New Name" || u.Password != "password1" || u.Admin {
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
	if hl-hf != panelHeight(40)-1 { // span of the panel rows, 0-indexed
		t.Errorf("box height span=%d, want %d", hl-hf, panelHeight(40)-1)
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
	for i := 0; i < len(m.visibleSections(1))-1; i++ {
		m = send(m, down)
	}
	if !strings.Contains(plain(m.View()), "Automatic OS Updates") {
		t.Errorf("last section not visible after scrolling down:\n%s", plain(m.View()))
	}

	// The Required tab now also holds Kernel Command Line, so its six sections
	// no longer all fit the 80x24 dialog: the first five show at the top and
	// Kernel Command Line scrolls into view at the bottom. Telemetry, clr's own
	// sixth, is still left out: Sunlight collects none.
	req := size(newModel(oneDisk()))
	reqV := plain(req.View())
	for _, sec := range []section{secTimezone, secLanguage, secKeyboard, secMedia, secUser} {
		if !strings.Contains(reqV, sectionLabels[sec]) {
			t.Errorf("%q not visible at the top of the Required tab:\n%s", sectionLabels[sec], reqV)
		}
	}
	rm := req
	for i := 0; i < len(rm.visibleSections(0))-1; i++ {
		rm = send(rm, down)
	}
	if !strings.Contains(plain(rm.View()), sectionLabels[secKernelArgs]) {
		t.Errorf("Kernel Command Line not visible after scrolling the Required tab:\n%s", plain(rm.View()))
	}
	if strings.Contains(reqV, "Telemetry") {
		t.Errorf("Telemetry is still offered:\n%s", reqV)
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
	if m.packages != "git man-pages mdocml" {
		t.Errorf("packages = %q, want 'git man-pages mdocml'", m.packages)
	}
}

func TestKernelArgsAddRemove(t *testing.T) {
	m := openSectionInTab(newModel(oneDisk()), 0, secKernelArgs) // profile list
	// Move to the "Custom" profile (last) and open the Add/Remove form.
	for i := 0; i < kernelProfileIndex("Custom"); i++ {
		m = send(m, down)
	}
	m = send(m, enter)
	m = send(m, runes("quiet loglevel=3"))
	m = send(m, enter) // -> Remove step
	m = send(m, runes("console=ttyS0"))
	m = send(m, enter) // confirm -> hub
	if m.kernelArgs != "quiet loglevel=3" || m.kernelArgsRemove != "console=ttyS0" {
		t.Fatalf("add=%q remove=%q", m.kernelArgs, m.kernelArgsRemove)
	}
	cfg, err := m.buildConfig()
	if err != nil {
		t.Fatal(err)
	}
	ka := cfg.KernelArguments
	if ka == nil || len(ka.Add) != 2 || ka.Add[0] != "quiet" || len(ka.Remove) != 1 || ka.Remove[0] != "console=ttyS0" {
		t.Fatalf("kernel-arguments wrong: %+v", ka)
	}
}

func TestMirrorInsecureCheckbox(t *testing.T) {
	m := openSectionInTab(newModel(oneDisk()), 1, secMirror)
	m = send(m, runes("http://mirror.example/repo"))
	m = send(m, enter)      // URL -> checkbox step
	m = send(m, runes(" ")) // toggle Allow insecure
	m = send(m, enter)      // confirm
	if m.mirror != "http://mirror.example/repo" || !m.allowInsecure {
		t.Fatalf("mirror=%q insecure=%v", m.mirror, m.allowInsecure)
	}
	cfg, _ := m.buildConfig()
	if cfg.SwupdMirror != "http://mirror.example/repo" || !cfg.AllowInsecureHTTP {
		t.Fatalf("config mirror=%q insecure=%v", cfg.SwupdMirror, cfg.AllowInsecureHTTP)
	}
}

func TestMediaDestructiveAutoLayout(t *testing.T) {
	// Destructive (default mode) + Confirm builds the standard layout.
	m := openSectionInTab(newModel(oneDisk()), 0, secMedia)
	m.mediaCursor = m.mediaEncIndex() + 1 // Confirm button
	m = send(m, enter)
	if !m.diskSet || len(m.parts) != 3 {
		t.Fatalf("auto layout: diskSet=%v parts=%d", m.diskSet, len(m.parts))
	}
	if !hasESP(m.parts) || !hasRoot(m.parts) || hasCrypt(m.parts) {
		t.Errorf("plain auto layout wrong: %+v", m.parts)
	}

	// With encryption ticked, root is crypt and the passphrase popup opens.
	m2 := openSectionInTab(newModel(oneDisk()), 0, secMedia)
	m2.encrypt = true
	m2.mediaCursor = m2.mediaEncIndex() + 1 // Confirm
	m2 = send(m2, enter)
	if !hasCrypt(m2.parts) {
		t.Errorf("encrypted auto layout should have a crypt partition: %+v", m2.parts)
	}
	if m2.popup != popupPassphrase {
		t.Errorf("encrypted install should prompt for a passphrase")
	}
}

// The dialog is clr-installer's window: 80x24 whatever the terminal, and
// smaller only when the terminal is.
func TestDialogIsClrSized(t *testing.T) {
	box := func(tw, th int) (int, int) {
		lines := make([]string, panelHeight(th))
		for i := range lines {
			lines[i] = lineBG.Width(contentWidth(tw)).Render("")
		}
		b := boxStyle.Render(strings.Join(lines, "\n"))
		return lipgloss.Width(b), lipgloss.Height(b)
	}
	for _, sz := range [][2]int{{80, 24}, {120, 40}, {240, 70}} {
		if w, h := box(sz[0], sz[1]); w != 80 || h != 24 {
			t.Errorf("terminal %dx%d: dialog %dx%d, want 80x24", sz[0], sz[1], w, h)
		}
	}
}

// Buttons sit at the bottom of the dialog on every page that has them,
// just above the rule and footer, as clr-installer's control frame does —
// not right after the content, where a short page left them halfway up.
func TestButtonsSitAtTheBottom(t *testing.T) {
	sized := func(m model) model { m.width, m.height = 100, 40; return m }
	// lineAboveRule returns the n-th line above the dialog's bottom rule.
	lineAboveRule := func(v string, n int) string {
		lines := strings.Split(stripANSIForTest(v), "\n")
		for i := len(lines) - 1; i >= 0; i-- {
			if strings.Contains(lines[i], "────") {
				if i-n >= 0 {
					return lines[i-n]
				}
				break
			}
		}
		return ""
	}
	cases := []struct {
		name  string
		view  string
		above int // the hub has its "Need: …" line between buttons and rule
		want  string
	}{
		{"hub", sized(newModel(oneDisk())).View(), 2, "Install"},
		{"manage user", sized(openSectionInTab(newModel(oneDisk()), 0, secUser)).View(), 1, "Add New User"},
		{"install confirm", installModel{width: 100, height: 40, disk: "/dev/sda", phase: phaseConfirm}.confirmView(), 1, "[Enter] Install"},
		{"install done", installModel{width: 100, height: 40, disk: "/dev/sda", phase: phaseDone}.doneView(), 1, "Reboot"},
	}
	for _, c := range cases {
		if got := lineAboveRule(c.view, c.above); !strings.Contains(got, c.want) {
			t.Errorf("%s: %q is not just above the bottom rule; that line is %q", c.name, c.want, got)
		}
	}
}

// The hub's section list reaches down to the buttons: in the 80x24 dialog
// five sections fit exactly, and the row above Cancel/Install is the last
// section's separator, not a gap. It used to show four with three empty
// rows below them.
func TestHubListReachesTheButtons(t *testing.T) {
	m := newModel(oneDisk())
	m.width, m.height = 80, 24
	lines := strings.Split(stripANSIForTest(m.View()), "\n")
	values, btn := 0, -1
	for i, l := range lines {
		if strings.Contains(l, "» ") {
			values++
		}
		if strings.Contains(l, "Cancel") && strings.Contains(l, "Install") {
			btn = i
		}
	}
	if values != 5 {
		t.Errorf("hub shows %d sections at 80x24, want 5", values)
	}
	if btn < 1 || strings.TrimSpace(strings.Trim(lines[btn-1], "▲▼█░│ ")) == "" {
		t.Errorf("the row above the buttons is empty; the list should reach them:\n%s", strings.Join(lines, "\n"))
	}
}

// Tab reaches the Cancel / Confirm buttons on a selection list, as in
// clr-installer: they used to be drawn only, with Confirm always lit and
// no key that could reach either. Enter on Cancel leaves the value as it
// was; Enter on Confirm takes the highlighted item.
func TestListButtonsAreReachable(t *testing.T) {
	tab := tea.KeyMsg{Type: tea.KeyTab}
	open := func() model {
		m := newModel(oneDisk())
		m.width, m.height = 80, 24
		m.editing = secTimezone
		m.timezone = "UTC"
		return m.openList([]string{"UTC", "Europe/Bucharest"}, "UTC")
	}

	m := send(open(), down) // highlight Europe/Bucharest
	m = send(m, tab)        // Cancel
	if m.btnFocus != focusCancel {
		t.Fatalf("Tab from the list: focus %d, want Cancel", m.btnFocus)
	}
	if l := lineWith(m.View(), "Cancel"); !strings.Contains(l, "Confirm") {
		t.Fatalf("buttons row not found: %q", l)
	}
	m = send(m, enter)
	if m.view != viewHub || m.timezone != "UTC" {
		t.Errorf("Enter on Cancel: view %v timezone %q, want the hub and UTC unchanged", m.view, m.timezone)
	}

	m = send(send(send(open(), down), tab), tab) // Confirm
	if m.btnFocus != focusConfirm {
		t.Fatalf("second Tab: focus %d, want Confirm", m.btnFocus)
	}
	m = send(m, enter)
	if m.timezone != "Europe/Bucharest" {
		t.Errorf("Enter on Confirm: timezone %q, want the highlighted Europe/Bucharest", m.timezone)
	}

	// A third Tab returns to the list, and the dialog keeps its 80x24 size
	// (a footer too long for it would wrap and add a row).
	m = send(send(send(open(), tab), tab), tab)
	if m.btnFocus != focusList {
		t.Errorf("third Tab: focus %d, want the list", m.btnFocus)
	}
	if h := len(strings.Split(m.View(), "\n")); h != 24 {
		t.Errorf("list page is %d rows tall, want 24", h)
	}
}

func lineWith(v, s string) string {
	for _, l := range strings.Split(stripANSIForTest(v), "\n") {
		if strings.Contains(l, s) {
			return l
		}
	}
	return ""
}

// The bundles page's Cancel is reachable too, and leaves the selection.
func TestBundlesCancelIsReachable(t *testing.T) {
	m := openSectionInTab(newModel(oneDisk()), 1, secBundles)
	m.width, m.height = 80, 24
	m = send(m, runes(" ")) // tick the first bundle
	m = send(m, tea.KeyMsg{Type: tea.KeyTab})
	if m.btnFocus != focusCancel {
		t.Fatalf("Tab: focus %d, want Cancel", m.btnFocus)
	}
	m = send(m, enter)
	if m.view != viewHub || m.packages != "" {
		t.Errorf("Enter on Cancel: view %v packages %q, want the hub and no packages", m.view, m.packages)
	}
}

// The Linux console has 8 background colours; a bright one (SGR 100-107)
// shows as black there, which hid the active tab, the selection bar and
// the disabled Install button. No style may ask the console for one.
func TestNoBrightBackgroundsOnTheConsole(t *testing.T) {
	r := lipgloss.NewRenderer(io.Discard)
	r.SetColorProfile(termenv.ANSI)
	styles := map[string]lipgloss.Style{
		"boxStyle": boxStyle, "lineBG": lineBG, "selBG": selBG, "titleBar": titleBar,
		"footer": footer, "ruleBG": ruleBG, "tabStrip": tabStrip, "tabActive": tabActive,
		"tabInactive": tabInactive, "itemLabel": itemLabel, "itemValue": itemValue,
		"itemSelLabel": itemSelLabel, "itemSelValue": itemSelValue, "btnSel": btnSel,
		"btnIdle": btnIdle, "btnOff": btnOff, "warnText": warnText, "popupBoxStyle": popupBoxStyle,
		"popupTitle": popupTitle, "popupHint": popupHint, "popupInput": popupInput,
		"scrollStyle": scrollStyle, "stepOKStyle": stepOKStyle, "stepFailStyle": stepFailStyle,
		"stepRun": stepRun, "stepWait": stepWait,
	}
	bright := regexp.MustCompile(`(^|;)10[0-7](;|$)`)
	for name, st := range styles {
		out := st.Renderer(r).Render("x")
		for _, m := range regexp.MustCompile(`\x1b\[([0-9;]+)m`).FindAllStringSubmatch(out, -1) {
			if bright.MatchString(m[1]) {
				t.Errorf("%s asks the console for a bright background: SGR %s", name, m[1])
			}
		}
		if !strings.Contains(out, "\x1b[") && name != "tabStrip" {
			t.Errorf("%s rendered no colour at all; the test is not seeing the console profile", name)
		}
	}
}

// Space picks the disk under the cursor on the media page, as Enter does.
func TestMediaSpaceChoosesDisk(t *testing.T) {
	m := newModel([]blockdev.Device{{Name: "vda", Path: "/dev/vda"}, {Name: "vdb", Path: "/dev/vdb"}})
	m.view, m.width, m.height = viewMedia, 80, 24
	m.mediaCursor = len(mediaModes) + 1 // the second disk
	nm, _ := m.Update(tea.KeyMsg{Type: tea.KeySpace})
	got := nm.(model)
	if !got.diskSet || got.diskIdx != 1 {
		t.Errorf("Space on /dev/vdb: diskSet=%v diskIdx=%d, want the second disk chosen", got.diskSet, got.diskIdx)
	}
}

// The space between buttons keeps the dialog's colour. Plain spaces after
// a button's reset fell back to the terminal's background: black blocks
// between Confirm, Partition, Rescan Media and Cancel.
func TestButtonGapsKeepTheDialogColour(t *testing.T) {
	prev := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.ANSI)
	t.Cleanup(func() { lipgloss.SetColorProfile(prev) })

	bare := regexp.MustCompile(`\x1b\[0m +\x1b\[`) // a reset, raw spaces, the next style
	m := newModel(oneDisk())
	m.width, m.height = 80, 24
	views := map[string]func() string{
		"hub":   m.View,
		"media": func() string { m2 := m; m2.view = viewMedia; return m2.View() },
	}
	im := installModel{width: 80, height: 24, phase: phaseDone, disk: "/dev/sda"}
	views["install-done"] = im.View
	im.phase = phaseConfirm
	views["install-confirm"] = im.View
	for name, v := range views {
		for _, l := range strings.Split(v(), "\n") {
			if bare.MatchString(l) {
				t.Errorf("%s: unstyled gap in %q", name, l)
			}
		}
	}
}

// The buttons in a row are all as wide as the widest, and the widest row,
// the final screen's four, still fits the dialog on one line.
func TestButtonsAreUniform(t *testing.T) {
	row := buttonRow(button{"OK", btnIdle}, button{"Back to menu", btnSel}, button{"Cancel", btnOff})
	if got, want := lipgloss.Width(row), 3*(len("Back to menu")+4)+2*2; got != want {
		t.Errorf("row is %d cells, want %d: three buttons of %d and two gaps", got, want, len("Back to menu")+4)
	}

	im := installModel{width: 80, height: 24, phase: phaseDone, disk: "/dev/vda"}
	v := stripANSIForTest(im.View())
	l := lineWith(v, "Reboot")
	for _, b := range doneButtons {
		if !strings.Contains(l, b.label) {
			t.Errorf("final screen's buttons are not on one line: %q lacks %q", l, b.label)
		}
	}
	for _, line := range strings.Split(im.View(), "\n") {
		if w := lipgloss.Width(line); w > 80 {
			t.Errorf("final screen line is %d cells wide: %q", w, stripANSIForTest(line))
		}
	}
}

// The account form refuses what clr-installer's refuses, on the step
// itself, with the reason: a reserved or repeated login, a short password,
// and a confirmation that does not match (which goes back to the password).
func TestUserFormChecksEntries(t *testing.T) {
	m := newModel(oneDisk())
	m.users = []userEntry{{Login: "taken", Password: "password1", Admin: true}}
	m.view, m.width, m.height = viewUserList, 80, 24
	m = send(m, runes("a"))

	for _, bad := range []struct{ login, why string }{{"root", "system account"}, {"taken", "already in the list"}, {"Bad", "valid Unix name"}} {
		m.input.SetValue(bad.login)
		m = send(m, enter)
		if m.formStep != userStepLogin || !strings.Contains(m.View(), bad.why) {
			t.Errorf("login %q: step %d, view lacks %q:\n%s", bad.login, m.formStep, bad.why, m.View())
		}
	}
	m.input.SetValue("ionut")
	m = send(m, enter)
	m = send(m, enter) // no user name
	m = send(m, runes("short"))
	m = send(m, enter)
	if m.formStep != userStepPassword || !strings.Contains(m.View(), "at least 8 characters") {
		t.Errorf("short password accepted: step %d", m.formStep)
	}
	m.input.SetValue("longenough")
	m = send(m, enter)
	m = send(m, runes("different1"))
	m = send(m, enter)
	if m.formStep != userStepPassword || !strings.Contains(m.View(), "do not match") || m.input.Value() != "" {
		t.Errorf("mismatch: step %d, input %q", m.formStep, m.input.Value())
	}
	m = send(m, runes("longenough"))
	m = send(m, enter)
	m = send(m, runes("longenough"))
	m = send(m, enter)
	m = send(m, enter) // admin
	if len(m.users) != 2 || m.users[1].Login != "ionut" || m.users[1].Password != "longenough" {
		t.Errorf("users = %+v", m.users)
	}
}

// A passing network test carries the live network into the install, as
// clr-installer's does; a failing one does not.
func TestNetworkTestSetsCopyNetwork(t *testing.T) {
	m := newModel(oneDisk())
	m.users = []userEntry{{Login: "me", Password: "password1", Admin: true}}
	nm, _ := m.Update(netTestDoneMsg{result: "[FAIL] no", ok: false})
	if nm.(model).copyNetwork {
		t.Error("a failed test set copyNetwork")
	}
	nm, _ = m.Update(netTestDoneMsg{result: "[ OK ] yes", ok: true})
	m = nm.(model)
	if !m.copyNetwork {
		t.Fatal("a passed test did not set copyNetwork")
	}
	cfg, err := m.buildConfig()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.CopyNetwork {
		t.Error("buildConfig dropped copyNetwork")
	}
}

// A row of buttons sits in the middle of the dialog, with a margin on both
// sides, however many it has. The final screen's four used to run from the
// left indent to the right edge.
func TestButtonRowsAreCentred(t *testing.T) {
	const w = 76
	for name, line := range map[string]string{
		"one": buttonLine(w, button{"Details", btnSel}),
		"two": buttonLine(w, button{"Cancel", btnIdle}, button{"Install", btnOff}),
		// Equal labels: without colour a button's inner padding reads as
		// margin too, so uneven labels would not measure the row itself.
		"four": buttonLine(w, button{"Exit to shell", btnSel}, button{"Exit to shell", btnIdle}, button{"Exit to shell", btnIdle}, button{"Exit to shell", btnIdle}),
	} {
		if got := lipgloss.Width(line); got != w {
			t.Errorf("%s: line is %d cells, want %d", name, got, w)
		}
		text := stripANSIForTest(line)
		left := len(text) - len(strings.TrimLeft(text, " "))
		right := len(text) - len(strings.TrimRight(text, " "))
		if left < 2 || right < 2 || left-right > 1 || right-left > 1 {
			t.Errorf("%s: margins %d and %d in %q", name, left, right, text)
		}
	}
}

// A popup replaces only the cells it covers: left and right of it the row
// keeps its text and its colours, instead of a black band across the dialog.
func TestPopupKeepsTheRowAroundIt(t *testing.T) {
	prev := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.ANSI)
	t.Cleanup(func() { lipgloss.SetColorProfile(prev) })

	base := lineBG.Render(strings.Repeat("a", 20)) + footer.Render(strings.Repeat("b", 20))
	got := overlayLine(base, popupTitle.Render("POPUP"), 40)
	if w := lipgloss.Width(got); w != 40 {
		t.Errorf("line is %d cells: %q", w, got)
	}
	if text := stripANSIForTest(got); text != strings.Repeat("a", 17)+"POPUP"+strings.Repeat("b", 18) {
		t.Errorf("text = %q", text)
	}
	if !strings.HasSuffix(got, footer.Render(strings.Repeat("b", 18))) {
		t.Errorf("the right part lost its colours: %q", got)
	}
	if !strings.HasPrefix(got, lineBG.Render(strings.Repeat("a", 17))[:6]) {
		t.Errorf("the left part lost its colours: %q", got)
	}

	// A real popup: every row of the screen keeps the width, and the rows it
	// covers still show the dialog on both sides.
	m := newModel(oneDisk())
	m.width, m.height = 80, 24
	m.popup, m.popupMsg = popupMessage, []string{"Test Network Settings", "", "[ OK ] Network is reachable."}
	lines := strings.Split(m.View(), "\n")
	black := regexp.MustCompile(`^\x1b\[40m +\x1b\[0m\S`)
	for i, l := range lines {
		if w := lipgloss.Width(l); w > 80 {
			t.Errorf("row %d is %d cells", i, w)
		}
		if strings.Contains(l, "Test Network") && black.MatchString(l) {
			t.Errorf("row %d has a black band beside the popup: %q", i, l)
		}
	}
}

func TestCellsFromKeepsActiveStyles(t *testing.T) {
	s := "\x1b[31mab\x1b[0m\x1b[32mc»d\x1b[0m"
	if got, want := cellsFrom(s, 3), "\x1b[32m»d\x1b[0m"; got != want {
		t.Errorf("cellsFrom = %q, want %q", got, want)
	}
	if got := cellsFrom(s, 99); got != "" {
		t.Errorf("past the end: %q", got)
	}
}

// Bundle packages are Void's names, whatever the upstream project is called:
// `mandoc` is packaged as mdocml, and asking for it failed the install after
// the disk was wiped.
func TestManPagesBundleUsesVoidNames(t *testing.T) {
	for _, b := range bundleOptions {
		for _, p := range b.packs {
			if p == "mandoc" {
				t.Errorf("bundle %q asks for mandoc, which Void packages as mdocml", b.name)
			}
		}
	}
}

// A new LUKS passphrase is typed twice and must be long enough: a typo here
// locks the disk. A mismatch goes back to the first entry.
func TestPassphrasePopupAsksTwice(t *testing.T) {
	m := newModel(oneDisk())
	m.width, m.height = 80, 24
	nm, _ := m.openPassphrasePopup()
	m = nm.(model)

	m = send(m, runes("short"))
	m = send(m, enter)
	if m.popup != popupPassphrase || m.passFirst != "" || !strings.Contains(m.View(), "at least 8") {
		t.Fatalf("short passphrase accepted: first %q\n%s", m.passFirst, m.View())
	}
	m.input.SetValue("correct horse")
	m = send(m, enter)
	if !strings.Contains(m.View(), "Type the passphrase again") || m.input.Value() != "" {
		t.Fatalf("no second entry, input %q\n%s", m.input.Value(), m.View())
	}
	m = send(m, runes("correct hrose"))
	m = send(m, enter)
	if m.passphrase != "" || m.passFirst != "" || !strings.Contains(m.View(), "do not match") {
		t.Fatalf("mismatch: passphrase %q first %q\n%s", m.passphrase, m.passFirst, m.View())
	}
	m = send(m, runes("correct horse"))
	m = send(m, enter)
	m = send(m, runes("correct horse"))
	m = send(m, enter)
	if m.popup != popupNone || m.passphrase != "correct horse" {
		t.Errorf("popup %v, passphrase %q", m.popup, m.passphrase)
	}
}

func TestIfaceLiveV4(t *testing.T) {
	ni := netIfaceEntry{Details: []string{"ipv4:    10.0.2.15", "netmask: 255.255.255.0", "ipv6:    fe80::1"}}
	if got := ifaceLiveV4(ni); got != "10.0.2.15" {
		t.Errorf("ifaceLiveV4 = %q, want 10.0.2.15", got)
	}
	if got := ifaceLiveV4(netIfaceEntry{Details: []string{"ipv6:    fe80::1"}}); got != "" {
		t.Errorf("no v4 should give empty, got %q", got)
	}
}

func TestNetInterfaceDetailView(t *testing.T) {
	m := newModel(oneDisk())
	m.netIfaces = []netIfaceEntry{{Name: "enp0s3", Details: []string{"ipv4:    10.0.2.15", "netmask: 255.255.255.0"}}}
	m.width, m.height = 90, 28
	nm, _ := m.startNetForm(0)
	v := stripANSIForTest(nm.(model).View())
	// The detail header, the live address, and the still-present choice.
	for _, want := range []string{"interface: enp0s3", "ipv4:    10.0.2.15", "Automatic (DHCP)", "Static IP"} {
		if !strings.Contains(v, want) {
			t.Errorf("interface detail missing %q:\n%s", want, v)
		}
	}
}

func TestTestNetProgressBar(t *testing.T) {
	// Opening Test Network shows the progress popup (not a spinner).
	m := openSectionInTab(newModel(oneDisk()), 1, secTestNet)
	if m.popup != popupSpinner || !m.testingNet {
		t.Fatalf("Test Network did not start the progress popup")
	}
	m.width, m.height = 90, 28
	if v := stripANSIForTest(m.View()); !strings.Contains(v, "Testing connectivity") {
		t.Errorf("popup missing the progress text:\n%s", v)
	}

	// A tick advances the bar and wraps at the end.
	nm, _ := m.Update(spinnerTickMsg{})
	if got := nm.(model).netProgress; got <= 0 {
		t.Errorf("tick did not advance the bar: %v", got)
	}
	m.netProgress = 0.99
	nm, _ = m.Update(spinnerTickMsg{})
	if got := nm.(model).netProgress; got != 0 {
		t.Errorf("bar did not wrap at the end: %v", got)
	}

	// The result arrives -> a message popup, done testing.
	nm, _ = m.Update(netTestDoneMsg{result: "[ OK ] Network is reachable.", ok: true})
	done := nm.(model)
	if done.testingNet || done.popup != popupMessage {
		t.Errorf("result did not switch to a message popup: testing=%v popup=%v", done.testingNet, done.popup)
	}
}

func TestSafeLayoutBuildsAlongsidePlan(t *testing.T) {
	// The layout confirmSafe produces: reuse the ESP, create root in free space.
	m := newModel(oneDisk())
	m.users = []userEntry{{Login: "me", Password: "pw", Admin: true}}
	m.safeInstall = true
	m.parts = []partEntry{
		{Name: "sda1", Type: "part", FS: "vfat", Mount: "/boot/efi", Preserve: true},
		{Name: "sda3", Type: "part", FS: "ext4", Mount: "/", Create: true, Start: "20480M", Size: "25000M"},
	}
	cfg, err := m.buildConfig()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.ReusePartitions {
		t.Error("Safe config should set reusePartitions")
	}
	plan, err := cfg.Plan()
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if plan.Wipe {
		t.Error("Safe plan must not wipe")
	}
	var sawCreate, sawPreserve bool
	for _, p := range plan.Parts {
		if p.Device == "/dev/sda3" && p.Create && p.StartMiB == 20480 {
			sawCreate = true
		}
		if p.Device == "/dev/sda1" && p.Preserve {
			sawPreserve = true
		}
	}
	if !sawCreate || !sawPreserve {
		t.Errorf("Safe plan wrong: create=%v preserve=%v", sawCreate, sawPreserve)
	}
}

func TestKernelProfileSelect(t *testing.T) {
	m := openSectionInTab(newModel(oneDisk()), 0, secKernelArgs) // profile list
	m = kernelTop(m)
	// Select "Realtime".
	idx := 0
	for i, p := range kernelProfiles {
		if p.name == "Realtime" {
			idx = i
		}
	}
	for i := 0; i < idx; i++ {
		m = send(m, down)
	}
	m = send(m, enter)
	if m.kernelProfile != "Realtime" || !strings.Contains(m.kernelArgs, "preempt=full") {
		t.Fatalf("profile=%q args=%q", m.kernelProfile, m.kernelArgs)
	}
	if m.view != viewHub {
		t.Error("profile select should return to the hub")
	}
	// The hub value names the profile; the config carries the args.
	if got := m.sectionValue(secKernelArgs); got != "Realtime profile" {
		t.Errorf("hub value = %q, want 'Realtime profile'", got)
	}
	cfg, _ := m.buildConfig()
	if cfg.KernelArguments == nil || len(cfg.KernelArguments.Add) == 0 {
		t.Errorf("config kernel-arguments empty")
	}
}

func TestKernelProfileFilterAndNotes(t *testing.T) {
	// The expanded set has the industry profiles.
	for _, name := range []string{"HPC / Compute", "Virtualization Host (KVM)", "GPU Passthrough (VFIO)", "Hardened (AppArmor)"} {
		if kernelProfileByName(name).name != name {
			t.Errorf("missing profile %q", name)
		}
	}
	// Trade-off profiles carry a note.
	if kernelProfileByName("Telco/RAN").note == "" || kernelProfileByName("HPC / Compute").note == "" {
		t.Error("aggressive profiles should carry a trade-off note")
	}

	// Type-to-filter narrows the list, and Enter selects the match.
	m := openSectionInTab(newModel(oneDisk()), 0, secKernelArgs)
	for _, r := range "Hardened" {
		m = send(m, runes(string(r)))
	}
	if got := m.filtered(); len(got) != 1 || got[0] != "Hardened (AppArmor)" {
		t.Fatalf("filter 'Hardened' = %v", got)
	}
	m = send(m, enter)
	if m.kernelProfile != "Hardened (AppArmor)" || !strings.Contains(m.kernelArgs, "apparmor=1") {
		t.Fatalf("profile=%q args=%q", m.kernelProfile, m.kernelArgs)
	}
}

func TestKernelProfileAutoPinning(t *testing.T) {
	m := openSectionInTab(newModel(oneDisk()), 0, secKernelArgs)
	// Pretend this machine was detected with 16 CPUs (housekeeping 0-1).
	m.cpuIso = system.CPUIsolation{
		Total: 16, Housekeeping: "0-1", Isolated: "2-15", OK: true,
		Args: "isolcpus=domain,nohz,managed_irq,2-15 nohz_full=2-15 rcu_nocbs=2-15 irqaffinity=0-1",
	}
	m.hugeGiB = 7
	m = kernelTop(m)
	// Select Telco/RAN.
	for i := 0; i < kernelProfileIndex("Telco/RAN"); i++ {
		m = send(m, down)
	}
	m = send(m, enter)
	for _, want := range []string{
		"isolcpus=domain,nohz,managed_irq,2-15", "nohz_full=2-15", "rcu_nocbs=2-15",
		"irqaffinity=0-1", "hugepages=7", "mitigations=off",
	} {
		if !strings.Contains(m.kernelArgs, want) {
			t.Errorf("Telco/RAN args missing %q:\n%s", want, m.kernelArgs)
		}
	}
}

func TestConsolesAppliedToKernelArgs(t *testing.T) {
	m := newModel(oneDisk())
	m.users = []userEntry{{Login: "me", Password: "pw", Admin: true}}
	m.consoles = []string{"console=tty0", "console=ttyS0,115200n8"}
	m.kernelArgs = "quiet"
	cfg, err := m.buildConfig()
	if err != nil {
		t.Fatal(err)
	}
	ka := cfg.KernelArguments
	if ka == nil {
		t.Fatal("no kernel arguments")
	}
	for _, want := range []string{"quiet", "console=tty0", "console=ttyS0,115200n8"} {
		if !containsStr(ka.Add, want) {
			t.Errorf("kernel args missing %q: %v", want, ka.Add)
		}
	}
	// A console already present is not duplicated.
	m.kernelArgs = "console=tty0 quiet"
	cfg, _ = m.buildConfig()
	n := 0
	for _, a := range cfg.KernelArguments.Add {
		if a == "console=tty0" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("console=tty0 duplicated %d times", n)
	}
}

func TestTelcoAMDPstate(t *testing.T) {
	// AMD: the isolation profile gets amd_pstate=active.
	m := openSectionInTab(newModel(oneDisk()), 0, secKernelArgs)
	m.cpuVendor = "amd"
	p := kernelProfileByName("Telco/RAN")
	if a := m.profileArgs(p); !strings.Contains(a, "amd_pstate=active") {
		t.Errorf("AMD Telco missing amd_pstate=active: %s", a)
	}
	// Intel: no amd_pstate, no intel_pstate (HWP kept by default).
	m.cpuVendor = "intel"
	a := m.profileArgs(p)
	if strings.Contains(a, "amd_pstate") || strings.Contains(a, "intel_pstate") {
		t.Errorf("Intel Telco should not force a pstate driver: %s", a)
	}
}

func TestTelcoMaxPerfProfile(t *testing.T) {
	m := openSectionInTab(newModel(oneDisk()), 0, secKernelArgs)
	m.cpuVendor = "amd"
	p := kernelProfileByName("Telco/RAN Max Perf")
	a := m.profileArgs(p)
	for _, want := range []string{
		"idle=poll", "cpuidle.off=1", "processor.max_cstate=0", "pcie_aspm=off",
		"audit=0", "amd_pstate=active", "amd_iommu=on",
	} {
		if !strings.Contains(a, want) {
			t.Errorf("Telco Max Perf missing %q:\n%s", want, a)
		}
	}
}

func TestGPUPassthroughConsumerProfile(t *testing.T) {
	m := openSectionInTab(newModel(oneDisk()), 0, secKernelArgs)
	p := kernelProfileByName("GPU Passthrough (consumer/ACS)")
	// Intel: igfx_off and ACS override are kept.
	m.cpuVendor = "intel"
	for _, want := range []string{"intel_iommu=on,igfx_off", "pcie_acs_override=downstream", "vfio-pci.disable_vga=1", "kvm-intel.nested=1"} {
		if !strings.Contains(m.profileArgs(p), want) {
			t.Errorf("Intel consumer passthrough missing %q: %s", want, m.profileArgs(p))
		}
	}
	// AMD: iommu normalizes (igfx_off dropped), kvm and ACS adapt.
	m.cpuVendor = "amd"
	a := m.profileArgs(p)
	for _, want := range []string{"amd_iommu=on", "kvm-amd.nested=1", "pcie_acs_override=downstream"} {
		if !strings.Contains(a, want) {
			t.Errorf("AMD consumer passthrough missing %q: %s", want, a)
		}
	}
	if strings.Contains(a, "igfx_off") || strings.Contains(a, "intel_iommu") {
		t.Errorf("AMD should not carry Intel iommu/igfx_off: %s", a)
	}
}

func TestAMDInstinctProfile(t *testing.T) {
	m := openSectionInTab(newModel(oneDisk()), 0, secKernelArgs)
	p := kernelProfileByName("AMD Instinct (GPU compute)")
	// AMD host: intel_iommu=on becomes amd_iommu=on; required Instinct args present.
	m.cpuVendor = "amd"
	a := m.profileArgs(p)
	for _, want := range []string{"amd_iommu=on", "iommu=pt", "pci=realloc=off", "pci=bfsort", "tsc=nowatchdog", "numa_balancing=disable"} {
		if !strings.Contains(a, want) {
			t.Errorf("AMD Instinct missing %q: %s", want, a)
		}
	}
	// Intel host: keeps intel_iommu=on (Instinct on Xeon).
	m.cpuVendor = "intel"
	if !strings.Contains(m.profileArgs(p), "intel_iommu=on") {
		t.Errorf("Intel host Instinct should keep intel_iommu=on: %s", m.profileArgs(p))
	}
}

func TestNvidiaProfile(t *testing.T) {
	m := openSectionInTab(newModel(oneDisk()), 0, secKernelArgs)
	m.cpuVendor = "amd" // vendor-neutral args: unchanged by the rewriter
	a := m.profileArgs(kernelProfileByName("NVIDIA GPU (proprietary)"))
	for _, want := range []string{"module_blacklist=nouveau", "nouveau.modeset=0", "nvidia-drm.modeset=1"} {
		if !strings.Contains(a, want) {
			t.Errorf("NVIDIA profile missing %q: %s", want, a)
		}
	}
	if strings.Contains(a, "iommu") {
		t.Errorf("NVIDIA profile should not carry iommu args: %s", a)
	}
}

// In a VM, a profile that keeps idle CPUs spinning says so: a Telco/RAN profile
// held a 12-CPU guest at 95% doing nothing. On bare metal, or for a profile
// without such arguments, nothing is said. The choice stands either way.
func TestBusyIdleProfileWarnsInAVM(t *testing.T) {
	pick := func(vm bool, name string) model {
		t.Helper()
		m := openSectionInTab(newModel(oneDisk()), 0, secKernelArgs)
		m = kernelTop(m)
		m.vm, m.vmName = vm, "QEMU"
		idx := -1
		for i, p := range kernelProfiles {
			if p.name == name {
				idx = i
			}
		}
		if idx < 0 {
			t.Fatalf("no profile %q", name)
		}
		for i := 0; i < idx; i++ {
			m = send(m, down)
		}
		return send(m, enter)
	}

	for _, name := range []string{"Telco/RAN", "Telco/RAN Max Perf"} {
		m := pick(true, name)
		if m.popup != popupMessage || !strings.Contains(strings.Join(m.popupMsg, " "), "QEMU") {
			t.Errorf("%s in a VM: popup %v %q", name, m.popup, m.popupMsg)
		}
		if m.kernelProfile != name || !strings.Contains(m.kernelArgs, "idle=poll") {
			t.Errorf("%s in a VM: the choice did not stand: %q", name, m.kernelArgs)
		}
		if !strings.Contains(m.profileNote(kernelProfileByName(name)), "host CPU busy") {
			t.Errorf("%s in a VM: no note", name)
		}
		if m = pick(false, name); m.popup == popupMessage {
			t.Errorf("%s on bare metal: warned %q", name, m.popupMsg)
		}
	}
	if m := pick(true, "VM Guest"); m.popup == popupMessage {
		t.Errorf("VM Guest in a VM: warned %q", m.popupMsg)
	}
}

func TestLogLevelSection(t *testing.T) {
	// The section lives on the Advanced tab.
	found := false
	for _, s := range newModel(oneDisk()).visibleSections(1) {
		if s == secLogLevel {
			found = true
		}
	}
	if !found {
		t.Fatal("Kernel Log Level not on the Advanced tab")
	}
	// Default is unset.
	m := newModel(oneDisk())
	if m.logLevel != -1 {
		t.Fatalf("default logLevel = %d, want -1", m.logLevel)
	}
	if got := m.sectionValue(secLogLevel); got != "Kernel default (not set)" {
		t.Errorf("default value = %q", got)
	}
	// Open and select "3 - error" (index 4: unset,0,1,2,3 -> 3 at index 4).
	m = openSectionInTab(newModel(oneDisk()), 1, secLogLevel)
	for i := 0; i < 4; i++ {
		m = send(m, down)
	}
	m = send(m, enter)
	if m.logLevel != 3 {
		t.Fatalf("logLevel = %d, want 3", m.logLevel)
	}
	if got := m.sectionValue(secLogLevel); got != "loglevel=3, kernel.printk = 3 4 1 7" {
		t.Errorf("value = %q", got)
	}
	// buildConfig carries it.
	m.users = []userEntry{{Login: "me", Password: "pw", Admin: true}}
	cfg, err := m.buildConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ConsoleLogLevel == nil || *cfg.ConsoleLogLevel != 3 {
		t.Fatalf("ConsoleLogLevel = %v", cfg.ConsoleLogLevel)
	}
}

func TestAmdPstateVisibleOnlyOnAMD(t *testing.T) {
	has := func(cpuVendor string) bool {
		m := newModel(oneDisk())
		m.cpuVendor = cpuVendor
		for _, s := range m.visibleSections(1) {
			if s == secAmdPstate {
				return true
			}
		}
		return false
	}
	if !has("amd") {
		t.Error("AMD P-State section missing on an AMD host")
	}
	if has("intel") || has("") {
		t.Error("AMD P-State section should be hidden on non-AMD hosts")
	}
}

func TestAmdPstateSelectable(t *testing.T) {
	// profileArgs honours the chosen mode.
	m := newModel(oneDisk())
	m.cpuVendor = "amd"
	m.amdPstate = "passive"
	if a := m.profileArgs(kernelProfileByName("Telco/RAN")); !strings.Contains(a, "amd_pstate=passive") {
		t.Errorf("passive not applied: %s", a)
	}
	// "Not set" adds no amd_pstate.
	m.amdPstate = ""
	if a := m.profileArgs(kernelProfileByName("Telco/RAN")); strings.Contains(a, "amd_pstate") {
		t.Errorf("unset should add no amd_pstate: %s", a)
	}

	// The section updates the mode and re-applies the current profile so its
	// args stay in sync. Start on an AMD host with Telco/RAN selected.
	m = newModel(oneDisk())
	m.cpuVendor = "amd"
	m.kernelProfile = "Telco/RAN"
	m.cpuIso = system.CPUIsolation{OK: false} // isolation not relevant here
	m.kernelArgs = m.profileArgs(kernelProfileByName("Telco/RAN"))
	if !strings.Contains(m.kernelArgs, "amd_pstate=active") {
		t.Fatalf("precondition: %s", m.kernelArgs)
	}
	// Open AMD P-State section, move to "guided" (index 2), select.
	m = openSectionInTab(m, 1, secAmdPstate)
	for i := 0; i < 2; i++ {
		m = send(m, down)
	}
	m = send(m, enter)
	if m.amdPstate != "guided" {
		t.Fatalf("mode = %q, want guided", m.amdPstate)
	}
	if !strings.Contains(m.kernelArgs, "amd_pstate=guided") || strings.Contains(m.kernelArgs, "amd_pstate=active") {
		t.Errorf("profile args not re-synced: %s", m.kernelArgs)
	}
}

func TestKdumpSection(t *testing.T) {
	// Default: off.
	m := newModel(oneDisk())
	if m.crashkernel != "" {
		t.Fatalf("default crashkernel = %q, want empty", m.crashkernel)
	}
	if got := m.sectionValue(secKdump); got != "Off (no crash dump)" {
		t.Errorf("default value = %q", got)
	}
	// Open: step 0 prefilled with the suggested default; accept it, set panic.
	m = openSectionInTab(newModel(oneDisk()), 1, secKdump)
	if m.input.Value() != defaultCrashkernel {
		t.Errorf("crashkernel field not prefilled: %q", m.input.Value())
	}
	m = send(m, enter) // accept crashkernel -> panic step (prefilled "5")
	if m.input.Value() != defaultPanicSecs {
		t.Errorf("panic field not prefilled: %q", m.input.Value())
	}
	m = send(m, enter) // accept panic
	if m.crashkernel != defaultCrashkernel || m.panicSecs != "5" {
		t.Fatalf("crashkernel=%q panic=%q", m.crashkernel, m.panicSecs)
	}
	// buildConfig puts both on the kernel command line.
	m.users = []userEntry{{Login: "me", Password: "pw", Admin: true}}
	cfg, err := m.buildConfig()
	if err != nil {
		t.Fatal(err)
	}
	if !containsStr(cfg.KernelArguments.Add, "crashkernel="+defaultCrashkernel) {
		t.Errorf("crashkernel not in args: %v", cfg.KernelArguments.Add)
	}
	if !containsStr(cfg.KernelArguments.Add, "panic=5") {
		t.Errorf("panic not in args: %v", cfg.KernelArguments.Add)
	}

	// Clearing the crashkernel field disables kdump (no crashkernel=/panic=).
	m2 := openSectionInTab(newModel(oneDisk()), 1, secKdump)
	m2.input.SetValue("")
	m2 = send(m2, enter) // empty crashkernel -> panic step
	m2 = send(m2, enter)
	m2.users = []userEntry{{Login: "me", Password: "pw", Admin: true}}
	cfg2, _ := m2.buildConfig()
	for _, a := range cfg2.KernelArguments.Add {
		if strings.HasPrefix(a, "crashkernel=") || strings.HasPrefix(a, "panic=") {
			t.Errorf("kdump off but got %q", a)
		}
	}
}

func TestKernelDetailsButton(t *testing.T) {
	m := openSectionInTab(newModel(oneDisk()), 0, secKernelArgs)
	m = kernelTop(m)
	// Move to Telco/RAN.
	for i := 0; i < kernelProfileIndex("Telco/RAN"); i++ {
		m = send(m, down)
	}
	// Tab once -> Details is the first stop after the list.
	m = send(m, tabKey)
	if m.btnFocus != focusDetails {
		t.Fatalf("first Tab stop = %d, want focusDetails", m.btnFocus)
	}
	m = send(m, enter) // open Details
	if m.popup != popupMessage {
		t.Fatalf("Details did not open a window: popup=%v", m.popup)
	}
	joined := strings.Join(m.popupMsg, " ")
	for _, want := range []string{"Telco/RAN", "Parameters:", "idle=poll", "real-time"} {
		if !strings.Contains(joined, want) {
			t.Errorf("Details window missing %q:\n%s", want, joined)
		}
	}
	// Any key closes it, back to the list (not the hub).
	m = send(m, enter)
	if m.popup != popupNone || m.view != viewList {
		t.Errorf("closing Details should return to the list: popup=%v view=%v", m.popup, m.view)
	}
}

func TestKernelListNoInlinePreview(t *testing.T) {
	// The profile picker no longer shows the args inline (that moved to the
	// Details window), so the list is not narrowed by a preview block.
	m := openSectionInTab(newModel(oneDisk()), 0, secKernelArgs)
	m.width, m.height = 90, 30
	v := stripANSIForTest(m.View())
	if strings.Contains(v, "mitigations=auto transparent_hugepage") {
		t.Errorf("list still shows an inline args preview:\n%s", v)
	}
	if !strings.Contains(v, "Details") {
		t.Errorf("Details button missing from the list:\n%s", v)
	}
}

func TestIntelPstateVisibleOnlyOnIntel(t *testing.T) {
	has := func(cpuVendor string) bool {
		m := newModel(oneDisk())
		m.cpuVendor = cpuVendor
		for _, s := range m.visibleSections(1) {
			if s == secIntelPstate {
				return true
			}
		}
		return false
	}
	if !has("intel") {
		t.Error("Intel P-State section missing on an Intel host")
	}
	if has("amd") || has("") {
		t.Error("Intel P-State section should be hidden on non-Intel hosts")
	}
}

func TestIntelPstateSelectable(t *testing.T) {
	// Default: nothing added (HWP kept).
	m := newModel(oneDisk())
	m.cpuVendor = "intel"
	if a := m.profileArgs(kernelProfileByName("Telco/RAN")); strings.Contains(a, "intel_pstate") {
		t.Errorf("default should force no intel_pstate: %s", a)
	}
	if got := m.sectionValue(secIntelPstate); got != "Not set (keep HWP; Intel RT/telco profiles)" {
		t.Errorf("default value = %q", got)
	}
	// Choose passive via the section; Telco/RAN picks it up.
	m.kernelProfile = "Telco/RAN"
	m.kernelArgs = m.profileArgs(kernelProfileByName("Telco/RAN"))
	m = openSectionInTab(m, 1, secIntelPstate)
	for i := 0; i < 2; i++ { // Not set(0) -> active(1) -> passive(2)
		m = send(m, down)
	}
	m = send(m, enter)
	if m.intelPstate != "passive" {
		t.Fatalf("mode = %q, want passive", m.intelPstate)
	}
	if !strings.Contains(m.kernelArgs, "intel_pstate=passive") {
		t.Errorf("profile args not re-synced: %s", m.kernelArgs)
	}
	// AMD host leaves intel_pstate untouched even if set.
	m.cpuVendor = "amd"
	if a := m.profileArgs(kernelProfileByName("Telco/RAN")); strings.Contains(a, "intel_pstate") {
		t.Errorf("intel_pstate should not apply on AMD: %s", a)
	}
}

func TestGovernorSection(t *testing.T) {
	// On the Advanced tab (any vendor), default = profile default.
	m := newModel(oneDisk())
	if m.governor != "" {
		t.Fatalf("default governor = %q, want empty", m.governor)
	}
	if got := m.sectionValue(secGovernor); got != "Profile default" {
		t.Errorf("default value = %q", got)
	}
	// setGovernor replaces an existing cpufreq.default_governor=.
	got := setGovernor("quiet cpufreq.default_governor=schedutil ro", "performance")
	if !strings.Contains(got, "cpufreq.default_governor=performance") ||
		strings.Contains(got, "schedutil") {
		t.Errorf("setGovernor = %q", got)
	}
	// Choosing a governor overrides the Server profile's schedutil.
	m.kernelProfile = "Server"
	m.kernelArgs = m.profileArgs(kernelProfileByName("Server"))
	m = openSectionInTab(m, 1, secGovernor)
	for i := 0; i < 1; i++ { // Not set(0) -> performance(1)
		m = send(m, down)
	}
	m = send(m, enter)
	if m.governor != "performance" {
		t.Fatalf("governor = %q, want performance", m.governor)
	}
	if strings.Contains(m.kernelArgs, "schedutil") || !strings.Contains(m.kernelArgs, "cpufreq.default_governor=performance") {
		t.Errorf("Server args not re-synced to performance: %s", m.kernelArgs)
	}
}
