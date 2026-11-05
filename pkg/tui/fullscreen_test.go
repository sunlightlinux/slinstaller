package tui

import (
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
	m.userSet = true
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
	var m tea.Model = newModel(oneDisk())
	m = send(m, enter) // cursor 0 = Installation Media -> open the media dialog
	m = send(m, runes("a"))
	m = send(m, enter)      // type = "part" (first choice)
	m = send(m, enter)      // fs = "ext4" (first choice)
	m = send(m, runes("/")) // mountpoint
	m = send(m, enter)      //
	m = send(m, enter)      // size blank = fill -> append

	got := m.(model)
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

// TestViewsRenderNoPanic drives the hub, the Installation Media dialog,
// each Advanced section, and both popups, rendering each to confirm the new
// views and the overlay do not panic and are non-empty.
func TestViewsRenderNoPanic(t *testing.T) {
	hub := withSize(newModel(oneDisk()))
	if v := hub.View(); !strings.Contains(v, "[R] Required") {
		t.Errorf("hub render missing the Required tab:\n%s", v)
	}

	// Installation Media dialog (cursor 0, Enter).
	media := withSize(send(hub, enter))
	if v := media.View(); !strings.Contains(v, "Installation Media") {
		t.Errorf("media dialog missing its title")
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

	// The passphrase popup overlays the hub.
	pm := adv
	for _, s := range visibleSections(1) {
		if s == secPassphrase {
			break
		}
		pm = send(pm, down)
	}
	pass := withSize(send(pm, enter))
	if v := pass.View(); !strings.Contains(v, "Passphrase") {
		t.Errorf("passphrase popup not shown:\n%s", v)
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
