package controller

import (
	"strings"
	"testing"

	"github.com/sunlightlinux/slinstaller/pkg/config"
)

// twoPartConfig is a minimal valid UEFI config: an ESP and a root.
func twoPartConfig() *config.Config {
	return &config.Config{
		TargetMedia: []*config.Media{{
			Name: "sda",
			Type: "disk",
			Children: []*config.Media{
				{Name: "sda1", Type: "part", FsType: "vfat", Mountpoint: "/boot", Size: "512M"},
				{Name: "sda2", Type: "part", FsType: "ext4", Mountpoint: "/", Size: "20G"},
			},
		}},
	}
}

// recorder is a storage.Runner that records commands instead of running
// them, so tests can watch the controller without touching a disk.
type recorder struct{ cmds [][]string }

func (r *recorder) run(args []string) error {
	r.cmds = append(r.cmds, args)
	return nil
}

func (r *recorder) flat() string {
	var b strings.Builder
	for _, c := range r.cmds {
		b.WriteString(strings.Join(c, " ") + "\n")
	}
	return b.String()
}

func TestInstallUnknownStage(t *testing.T) {
	var r recorder
	err := Install(twoPartConfig(), Options{Through: "nope", Runner: r.run})
	if err == nil || !strings.Contains(err.Error(), "unknown stage") {
		t.Fatalf("unknown stage error = %v, want 'unknown stage'", err)
	}
	if len(r.cmds) != 0 {
		t.Errorf("unknown-stage install ran %d commands", len(r.cmds))
	}
}

func TestInstallThroughPartition(t *testing.T) {
	var r recorder
	// Through "partition" runs pre-install (no hooks here) and partition,
	// then stops before mount, which would make real syscalls.
	if err := Install(twoPartConfig(), Options{Through: "partition", Runner: r.run}); err != nil {
		t.Fatalf("Install through partition: %v", err)
	}
	flat := r.flat()
	for _, want := range []string{
		"parted -s /dev/sda mklabel gpt",
		"mkfs.vfat", "mkfs.ext4", "partprobe /dev/sda",
	} {
		if !strings.Contains(flat, want) {
			t.Errorf("partition commands missing %q\n%s", want, flat)
		}
	}
}

func TestInstallThroughPreInstallRunsHooks(t *testing.T) {
	cfg := twoPartConfig()
	cfg.PreInstall = []*config.Hook{{Cmd: "echo hello"}}

	var r recorder
	// Stopping at pre-install runs the hook and nothing destructive.
	if err := Install(cfg, Options{Through: "pre-install", Runner: r.run}); err != nil {
		t.Fatalf("Install through pre-install: %v", err)
	}
	if got := r.flat(); !strings.Contains(got, "sh -c echo hello") {
		t.Errorf("pre-install hook not run: %q", got)
	}
	if strings.Contains(r.flat(), "parted") {
		t.Error("pre-install stage reached partition")
	}
}
