package tui

import (
	"strings"
	"testing"
)

func TestAssemblePlain(t *testing.T) {
	cfg := assemble(assembly{
		Disk: "nvme0n1", RootFS: "ext4", Swap: "partition", SwapSize: "2G",
		Hostname: "box", Login: "ion", PassHash: "$6$abc$def",
	})

	if cfg.Hostname != "box" || cfg.Keyboard != "us" || cfg.Language != "en_US.UTF-8" {
		t.Errorf("identity = %q/%q/%q", cfg.Hostname, cfg.Keyboard, cfg.Language)
	}
	if len(cfg.Users) != 1 || cfg.Users[0].Login != "ion" || !cfg.Users[0].Admin ||
		cfg.Users[0].Password != "$6$abc$def" {
		t.Fatalf("user = %+v", cfg.Users)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("assembled config invalid: %v", err)
	}

	plan, err := cfg.Plan()
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	// ESP, swap partition, root — with nvme partition naming.
	wantDev := []string{"/dev/nvme0n1p1", "/dev/nvme0n1p2", "/dev/nvme0n1p3"}
	if len(plan.Parts) != 3 {
		t.Fatalf("got %d partitions, want 3", len(plan.Parts))
	}
	for i, p := range plan.Parts {
		if p.Device != wantDev[i] {
			t.Errorf("part %d = %q, want %q", i, p.Device, wantDev[i])
		}
	}
	if plan.Parts[2].Encrypted {
		t.Error("root should not be encrypted")
	}
}

func TestAssembleEncryptedSwapFile(t *testing.T) {
	cfg := assemble(assembly{
		Disk: "sda", RootFS: "btrfs", Encrypt: true, Swap: "file", SwapSize: "4G",
		Hostname: "box", Login: "ion", PassHash: "$6$abc$def",
	})

	if cfg.SwapFileSize != "4G" {
		t.Errorf("SwapFileSize = %q, want 4G", cfg.SwapFileSize)
	}
	plan, err := cfg.Plan()
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	// No swap partition: just ESP + root.
	if len(plan.Parts) != 2 {
		t.Fatalf("got %d partitions, want 2 (ESP + root, no swap)", len(plan.Parts))
	}
	root := plan.Parts[1]
	if !root.Encrypted || root.MapName != "cryptroot" || root.Filesystem != "btrfs" {
		t.Errorf("root = %+v, want encrypted btrfs cryptroot", root)
	}
	if root.EffectiveDevice() != "/dev/mapper/cryptroot" {
		t.Errorf("EffectiveDevice = %q", root.EffectiveDevice())
	}
}

func TestAssembleNoSwap(t *testing.T) {
	cfg := assemble(assembly{
		Disk: "sda", RootFS: "ext4", Swap: "none",
		Hostname: "box", Login: "ion", PassHash: "$6$abc$def",
	})
	plan, _ := cfg.Plan()
	if len(plan.Parts) != 2 {
		t.Fatalf("got %d partitions, want 2", len(plan.Parts))
	}
	for _, p := range plan.Parts {
		if p.Filesystem == "swap" {
			t.Error("no-swap layout has a swap partition")
		}
	}
	// Root fills the disk.
	cmds, _ := plan.Commands()
	flat := ""
	for _, c := range cmds {
		flat += strings.Join(c, " ") + "\n"
	}
	if !strings.Contains(flat, " 100%") {
		t.Errorf("root does not fill the disk:\n%s", flat)
	}
}
