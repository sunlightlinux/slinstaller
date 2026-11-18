package controller

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sunlightlinux/slinstaller/pkg/config"
)

// A stage that failed part of the way through is run again from its start
// when the install is resumed at it. What it wrote the first time must not
// be written twice, and what it did must not be done twice.
func TestStagesCanBeRunAgain(t *testing.T) {
	cfg := twoPartConfig()
	cfg.SwapFileSize, cfg.Keyboard, cfg.Hostname = "1G", "us", "box"
	cfg.KernelArguments = &config.KernelArguments{Add: []string{"quiet"}}
	cfg.Users = []*config.User{{Login: "ion", Password: "$6$x", Admin: true}}
	cfg.NetworkInterfaces = []*config.Interface{{Name: "eth0", Addrs: []*config.Address{{IP: "10.0.0.2", Netmask: "24"}}}}
	var r recorder
	in := stagedInstaller(t, cfg, &r)
	in.opts.ResumeFrom = "swapfile"
	mkTarget(t, in, "etc", "default")

	once := func(rel ...string) string {
		t.Helper()
		return readTarget(t, in, rel...)
	}
	for attempt := 1; attempt <= 2; attempt++ {
		for name, stage := range map[string]func() error{"swapfile": in.swapfile, "bootloader": in.bootloader, "first-boot": in.firstBoot, "network": in.network} {
			if err := stage(); err != nil {
				t.Fatalf("%s, attempt %d: %v", name, attempt, err)
			}
		}
		if attempt == 1 {
			// As the first attempt's useradd would have left it.
			os.WriteFile(filepath.Join(in.opts.Root, "etc", "passwd"), []byte("root:x:0:0::/root:/bin/sh\nion:x:1000:1000::/home/ion:/bin/sh\n"), 0o644)
		}
	}
	for rel, text := range map[string]string{
		"etc/fstab":        "/swapfile",
		"etc/default/grub": "GRUB_CMDLINE_LINUX_DEFAULT=",
		"etc/dhcpcd.conf":  "interface eth0",
		"etc/rc.conf":      "KEYMAP=",
	} {
		if n := strings.Count(once(strings.Split(rel, "/")...), text); n != 1 {
			t.Errorf("%s has %q %d times after two attempts", rel, text, n)
		}
	}
	// The account was made once: the second attempt found it there.
	if n := strings.Count(r.flat(), "useradd"); n != 1 {
		t.Errorf("useradd ran %d times:\n%s", n, r.flat())
	}
	// An account whose name merely starts the same is another account.
	if hasAccount(in.opts.Root, "io") || !hasAccount(in.opts.Root, "ion") || hasAccount(t.TempDir(), "ion") {
		t.Error("hasAccount")
	}
}

// Resuming past the stage that made it, the keyfile from the first attempt
// is the one used: no second key is added to the volumes.
func TestResumeReusesTheKeyfile(t *testing.T) {
	args := fakeCryptsetup(t)
	home := &config.Media{Name: "sda3", Type: "crypt", FsType: "ext4", Mountpoint: "/home"}
	var r recorder
	in, _ := cryptInstaller(t, cryptConfig("/boot", home), &r)
	if err := in.crypttab(); err != nil {
		t.Fatal(err)
	}
	first, _ := os.ReadFile(filepath.Join(in.opts.Root, keyfilePath))
	added := func() int {
		data, _ := os.ReadFile(filepath.Join(args, "args"))
		return strings.Count(string(data), "luksAddKey")
	}
	before := added()

	in.opts.ResumeFrom, in.keyfile = "fstab", ""
	if err := in.crypttab(); err != nil {
		t.Fatal(err)
	}
	again, _ := os.ReadFile(filepath.Join(in.opts.Root, keyfilePath))
	if string(first) != string(again) || added() != before || in.keyfile != keyfilePath {
		t.Errorf("resumed crypttab: key changed %v, luksAddKey %d -> %d, keyfile %q", string(first) != string(again), before, added(), in.keyfile)
	}
	// A fresh install always makes its own.
	in.opts.ResumeFrom = ""
	if err := in.crypttab(); err != nil {
		t.Fatal(err)
	}
	if added() == before {
		t.Error("a fresh install did not add its key")
	}
}

func TestResumeFromValidation(t *testing.T) {
	var r recorder
	err := Install(twoPartConfig(), Options{ResumeFrom: "partition", Runner: r.run, Devices: noDevices})
	if err == nil || !strings.Contains(err.Error(), `cannot be resumed at "partition"`) || len(r.cmds) != 0 {
		t.Errorf("resume at partition: %v", err)
	}
	// From a later stage: mount and meta-fs again, then that stage on.
	in := &installer{opts: Options{ResumeFrom: "bootloader"}}
	var names []string
	for _, s := range in.stages() {
		names = append(names, s.name)
	}
	if got := strings.Join(names, ","); got != "mount,meta-fs,bootloader,first-boot,network,save-config,post-install" {
		t.Errorf("stages from bootloader = %s", got)
	}
	if got := strings.Join(ResumableStages(), ","); !strings.HasPrefix(got, "base-install,fstab") || strings.Contains(got, "mount") {
		t.Errorf("ResumableStages = %s", got)
	}
}
