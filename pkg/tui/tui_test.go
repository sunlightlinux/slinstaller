package tui

import (
	"strings"
	"testing"
)

func TestAssemble(t *testing.T) {
	cfg := assemble("nvme0n1", "box", "ion", "$6$abc$def")

	// Identity and account.
	if cfg.Hostname != "box" || cfg.Keyboard != "us" || cfg.Language != "en_US.UTF-8" {
		t.Errorf("identity = %q/%q/%q", cfg.Hostname, cfg.Keyboard, cfg.Language)
	}
	if len(cfg.Users) != 1 || cfg.Users[0].Login != "ion" || !cfg.Users[0].Admin {
		t.Fatalf("user = %+v", cfg.Users)
	}
	if cfg.Users[0].Password != "$6$abc$def" {
		t.Errorf("password hash not carried through")
	}

	// The layout must be valid and plannable, with nvme partition naming.
	if err := cfg.Validate(); err != nil {
		t.Fatalf("assembled config invalid: %v", err)
	}
	plan, err := cfg.Plan()
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if plan.Disk != "/dev/nvme0n1" {
		t.Errorf("disk = %q", plan.Disk)
	}
	wantDev := []string{"/dev/nvme0n1p1", "/dev/nvme0n1p2", "/dev/nvme0n1p3"}
	for i, p := range plan.Parts {
		if p.Device != wantDev[i] {
			t.Errorf("part %d = %q, want %q", i, p.Device, wantDev[i])
		}
	}

	// Root fills the disk.
	cmds, _ := plan.Commands()
	flat := ""
	for _, c := range cmds {
		flat += strings.Join(c, " ") + "\n"
	}
	if !strings.Contains(flat, "mkpart / ") || !strings.Contains(flat, " 100%") {
		t.Errorf("root does not fill the disk:\n%s", flat)
	}
}
