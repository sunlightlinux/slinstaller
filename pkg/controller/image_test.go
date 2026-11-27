package controller

import (
	"bytes"
	"errors"
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
		// From a URL: unpacked as it downloads, tar told the compression.
		"https://img.example/base.tar.zst": `sh -c curl -fsSL --proto-redir =https "$1" | tar -xpf - --zstd -C "$2" --numeric-owner sh https://img.example/base.tar.zst /mnt`,
		"https://img.example/base.tgz":     `sh -c curl -fsSL --proto-redir =https "$1" | tar -xpf - -z -C "$2" --numeric-owner sh https://img.example/base.tgz /mnt`,
		"https://img.example/base.txz":     `sh -c curl -fsSL --proto-redir =https "$1" | tar -xpf - -J -C "$2" --numeric-owner sh https://img.example/base.txz /mnt`,
		"https://img.example/base.tar.bz2": `sh -c curl -fsSL --proto-redir =https "$1" | tar -xpf - -j -C "$2" --numeric-owner sh https://img.example/base.tar.bz2 /mnt`,
		"https://img.example/base.tar":     `sh -c curl -fsSL --proto-redir =https "$1" | tar -xpf - -C "$2" --numeric-owner sh https://img.example/base.tar /mnt`,
	} {
		if got := strings.Join(ImageCommand(image, "/mnt", false), " "); got != want {
			t.Errorf("ImageCommand(%s) = %s, want %s", image, got, want)
		}
	}
	// With plain http allowed, curl may follow a redirect to it; a plain
	// http URL is only reached when it is allowed.
	for image, want := range map[string]string{
		"https://img.example/base.tar": `sh -c curl -fsSL "$1" | tar -xpf - -C "$2" --numeric-owner sh https://img.example/base.tar /mnt`,
		"http://img.example/base.tar":  `sh -c curl -fsSL "$1" | tar -xpf - -C "$2" --numeric-owner sh http://img.example/base.tar /mnt`,
	} {
		if got := strings.Join(ImageCommand(image, "/mnt", true), " "); got != want {
			t.Errorf("ImageCommand(%s, insecure) = %s, want %s", image, got, want)
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

func TestImageTools(t *testing.T) {
	for image, want := range map[string]string{
		"/img/base.tar.zst": "tar", "/img/root.squashfs": "unsquashfs", "https://img.example/base.tar": "sh curl tar",
	} {
		if got := strings.Join(ImageTools(image), " "); got != want {
			t.Errorf("ImageTools(%s) = %s, want %s", image, got, want)
		}
	}
	// A remote image is not looked for beside the config file.
	cfg := twoPartConfig()
	cfg.Image = "https://img.example/base.tar"
	var r recorder
	in := stagedInstaller(t, cfg, &r)
	in.opts.ConfigDir = "/srv/configs"
	_ = in.baseInstall()
	if !strings.Contains(r.flat(), "sh https://img.example/base.tar "+in.opts.Root) || strings.Contains(r.flat(), "/srv/configs") {
		t.Errorf("remote image command:\n%s", r.flat())
	}
}

// With a fallback kernel named, a base install that fails is tried once
// more with it in place of the configured one.
func TestKernelFallback(t *testing.T) {
	cfg := twoPartConfig()
	cfg.Kernel, cfg.KernelFallback = "linux-mainline", "linux-lts"
	kernel := cfg.KernelPackage()
	if kernel == "" {
		t.Fatal("the config names no kernel package")
	}
	var r recorder
	var log bytes.Buffer
	in := stagedInstaller(t, cfg, &r)
	in.opts.Log = &log
	in.opts.Repo, in.opts.Packages = "https://repo.example/current", []string{"sunlight-base", kernel}
	// The first attempt, with the configured kernel, fails.
	in.opts.Runner = func(args []string) error {
		r.cmds = append(r.cmds, args)
		line := strings.Join(args, " ")
		if strings.Contains(line, "xbps-install") && strings.Contains(line, " "+kernel) {
			return errors.New("package not found")
		}
		return nil
	}
	if err := in.baseInstall(); err != nil {
		t.Fatalf("baseInstall with a fallback: %v", err)
	}
	flat := r.flat()
	if strings.Count(flat, "xbps-install") != 2 || !strings.Contains(flat, " linux-lts") {
		t.Errorf("attempts:\n%s", flat)
	}
	if !strings.Contains(log.String(), "trying once more with linux-lts") {
		t.Errorf("the retry is not said: %q", log.String())
	}
	// A failure that is not the kernel's fails the second time too, and is
	// reported; without a fallback there is no second attempt.
	r = recorder{}
	in.opts.Runner = r.failOn("xbps-install")
	wantErr(t, in.baseInstall(), "boom")
	if strings.Count(r.flat(), "xbps-install") != 2 {
		t.Errorf("attempts:\n%s", r.flat())
	}
	cfg.KernelFallback = ""
	r = recorder{}
	in.opts.Runner = r.failOn("xbps-install")
	wantErr(t, in.baseInstall(), "boom")
	if strings.Count(r.flat(), "xbps-install") != 1 {
		t.Errorf("a second attempt with no fallback:\n%s", r.flat())
	}
}
