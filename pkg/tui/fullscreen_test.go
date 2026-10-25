package tui

import (
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
	// Hub -> Partitions -> add a plain ext4 root.
	var m tea.Model = newModel(oneDisk())
	m = send(m, down)  // cursor 0 Disk -> 1 Partitions
	m = send(m, enter) // open partition list
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
