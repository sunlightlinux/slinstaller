package controller

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sunlightlinux/slinstaller/pkg/storage"
)

func TestImageCommand(t *testing.T) {
	for image, want := range map[string]string{
		"/run/img/base.tar.zst":    "tar -xpf /run/img/base.tar.zst -C /mnt --numeric-owner",
		"/run/img/base.tar":        "tar -xpf /run/img/base.tar -C /mnt --numeric-owner",
		"/run/img/base.txz":        "tar -xpf /run/img/base.txz -C /mnt --numeric-owner",
		"/run/img/root.squashfs":   "unsquashfs -f -d /mnt /run/img/root.squashfs",
		"/run/img/ROOT.SQFS":       "unsquashfs -f -d /mnt /run/img/ROOT.SQFS",
		"/run/img/live-rootfs.sfs": "unsquashfs -f -d /mnt /run/img/live-rootfs.sfs",
	} {
		if got := strings.Join(ImageCommand(image, "/mnt"), " "); got != want {
			t.Errorf("ImageCommand(%s) = %s, want %s", image, got, want)
		}
	}
}

// With an image, the base system is unpacked rather than installed from
// packages: no xbps runs, and the mirror is still recorded for updates.
func TestBaseInstallFromAnImage(t *testing.T) {
	cfg := twoPartConfig()
	cfg.Image = "images/base.tar.gz"
	var r recorder
	in := stagedInstaller(t, cfg, &r)
	in.opts.ConfigDir = "/srv/configs"
	in.opts.Repo, in.opts.Packages = "https://repo.example/current", []string{"sunlight-base"}
	in.opts.ExtraRepos = []string{"https://mirror.example/sunlight"}
	bin := mkTarget(t, in, "usr", "bin")
	os.WriteFile(filepath.Join(bin, "grub-install"), nil, 0o755) // as the image would leave it

	if err := in.baseInstall(); err != nil {
		t.Fatal(err)
	}
	// A relative image is found beside the config file.
	if got := r.flat(); got != "tar -xpf /srv/configs/images/base.tar.gz -C "+in.opts.Root+" --numeric-owner\n" {
		t.Errorf("commands:\n%s", got)
	}
	if got := readTarget(t, in, "etc", "xbps.d", "10-sunlight.conf"); !strings.Contains(got, "mirror.example") {
		t.Errorf("mirror config:\n%s", got)
	}

	// An image without GRUB cannot be made bootable: said here, not three
	// stages later.
	in = stagedInstaller(t, cfg, &r)
	wantErr(t, in.baseInstall(), "holds no grub-install")
	// Unpacking failing stops the stage.
	in = stagedInstaller(t, cfg, &r)
	in.opts.Runner = r.failOn("tar")
	wantErr(t, in.baseInstall(), "boom")
}

// The real thing, with a small tar: what is in the image is what ends up in
// the target, modes included.
func TestUnpackARealImage(t *testing.T) {
	if _, err := exec.LookPath("tar"); err != nil {
		t.Skip("no tar")
	}
	src := t.TempDir()
	os.MkdirAll(filepath.Join(src, "usr", "bin"), 0o755)
	os.MkdirAll(filepath.Join(src, "etc"), 0o755)
	os.WriteFile(filepath.Join(src, "usr", "bin", "grub-install"), []byte("#!/bin/sh\n"), 0o755)
	os.WriteFile(filepath.Join(src, "etc", "shadow"), []byte("root:!::\n"), 0o600)
	os.WriteFile(filepath.Join(src, "etc", "os-release"), []byte("NAME=Sunlight\n"), 0o644)
	image := filepath.Join(t.TempDir(), "base.tar.gz")
	if out, err := exec.Command("tar", "-czf", image, "-C", src, ".").CombinedOutput(); err != nil {
		t.Skipf("cannot make a test image: %v\n%s", err, out)
	}

	cfg := twoPartConfig()
	cfg.Image = image
	var r recorder
	in := stagedInstaller(t, cfg, &r)
	in.opts.Runner = storage.SinkRunner(func(string) {})
	if err := in.baseInstall(); err != nil {
		t.Fatalf("unpacking the image: %v", err)
	}
	if got := readTarget(t, in, "etc", "os-release"); got != "NAME=Sunlight\n" {
		t.Errorf("os-release = %q", got)
	}
	if st, err := os.Stat(filepath.Join(in.opts.Root, "etc", "shadow")); err != nil || st.Mode().Perm() != 0o600 {
		t.Errorf("shadow: %v, mode %v", err, st.Mode().Perm())
	}
}
