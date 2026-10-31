package controller

import (
	"os"
	"path/filepath"
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
	got := r.flat()
	if !strings.Contains(got, "sh -c echo hello") {
		t.Errorf("pre-install hook not run: %q", got)
	}
	// clr-style hook variables are passed as environment.
	if !strings.Contains(got, "chrootDir=") || !strings.Contains(got, "chrooted=0") {
		t.Errorf("hook env vars missing: %q", got)
	}
	if strings.Contains(r.flat(), "parted") {
		t.Error("pre-install stage reached partition")
	}
}

func TestInterfacesFromConfig(t *testing.T) {
	ifaces := interfacesFromConfig([]*config.Interface{
		nil,                          // skipped
		{Name: ""},                   // skipped (no name)
		{Name: "eth0", DHCP: "true"}, // DHCP, not static
		{ // addresses, no dhcp field -> static (clr: static unless dhcp:true)
			Name:    "eth1",
			Addrs:   []*config.Address{{IP: "10.0.0.5", Netmask: "255.255.255.0"}},
			Gateway: "10.0.0.1",
			DNS:     "10.0.0.1",
			Domain:  "lan",
		},
		{ // addresses but dhcp:true -> not static
			Name:  "eth2",
			DHCP:  "true",
			Addrs: []*config.Address{{IP: "10.0.0.6", Netmask: "24"}},
		},
	})

	if len(ifaces) != 3 {
		t.Fatalf("got %d interfaces, want 3 (eth0, eth1, eth2)", len(ifaces))
	}
	byName := map[string]bool{}
	for _, i := range ifaces {
		byName[i.Name] = i.Static
	}
	if byName["eth0"] {
		t.Error("eth0 (dhcp:true, no addrs) should not be static")
	}
	if !byName["eth1"] {
		t.Error("eth1 (addrs, no dhcp) should be static")
	}
	if byName["eth2"] {
		t.Error("eth2 (dhcp:true with addrs) should not be static")
	}

	// eth1 carries its address, gateway, dns and domain through.
	found := false
	for _, i := range ifaces {
		if i.Name != "eth1" {
			continue
		}
		found = true
		if len(i.Addrs) != 1 || i.Addrs[0].IP != "10.0.0.5" {
			t.Errorf("eth1 address not mapped: %+v", i.Addrs)
		}
		if i.Gateway != "10.0.0.1" || i.DNS != "10.0.0.1" || i.Domain != "lan" {
			t.Errorf("eth1 gateway/dns/domain not mapped: %+v", i)
		}
	}
	if !found {
		t.Error("eth1 missing from result")
	}
}

func TestBootsSlinit(t *testing.T) {
	withSlinit := t.TempDir()
	bin := filepath.Join(withSlinit, "usr", "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "slinit"), []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name   string
		root   string
		remove map[string]bool
		want   bool
	}{
		{"slinit installed", withSlinit, nil, true},
		{"slinit not installed (runit-only base)", t.TempDir(), nil, false},
		{"config removes init=", withSlinit, map[string]bool{slinitInitArg: true}, false},
		{"unrelated removal", withSlinit, map[string]bool{"quiet": true}, true},
	}
	for _, c := range cases {
		if got := bootsSlinit(c.root, c.remove); got != c.want {
			t.Errorf("%s: bootsSlinit = %v, want %v", c.name, got, c.want)
		}
	}
}
