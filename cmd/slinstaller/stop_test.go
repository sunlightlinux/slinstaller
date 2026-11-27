package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/sunlightlinux/slinstaller/pkg/blockdev"
	"github.com/sunlightlinux/slinstaller/pkg/config"
	"github.com/sunlightlinux/slinstaller/pkg/controller"
	"github.com/sunlightlinux/slinstaller/pkg/storage"
)

// Ctrl+C (SIGINT) during an install stops it after the stage running,
// through the controller's cleanup, instead of killing the process.
func TestInstallStopsOnSIGINT(t *testing.T) {
	var ran []string
	signalled := false
	opts := controller.Options{
		Root: t.TempDir(), Through: "mount",
		Devices: func() ([]blockdev.Device, error) { return nil, nil },
		Runner: func(args []string) error {
			ran = append(ran, strings.Join(args, " "))
			if !signalled {
				signalled = true
				syscall.Kill(syscall.Getpid(), syscall.SIGINT)
				time.Sleep(200 * time.Millisecond) // let the handler see it
			}
			return nil
		},
	}
	err := installStoppable(plainConfig(), opts)
	if !errors.Is(err, controller.ErrStopped) || !strings.Contains(err.Error(), "before mount") {
		t.Fatalf("err = %v\nran:\n%s", err, strings.Join(ran, "\n"))
	}
}

// --help prints the usage and succeeds: a script asking for it is not told
// it failed.
func TestHelpIsNotAnError(t *testing.T) {
	for _, a := range []string{"-h", "--help"} {
		if err := run([]string{a}); err != nil {
			t.Errorf("%s: %v", a, err)
		}
	}
	if err := run([]string{"--no-such-flag"}); err == nil {
		t.Error("an unknown flag was not an error")
	}
}

// A misspelt key is named even when the config it leaves short fails to
// validate: "no partition mounts /" alone did not say why.
func TestUnknownKeyIsNamedBeforeValidation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "typo.yaml")
	os.WriteFile(path, []byte(`targetMedia:
- name: sda
  type: disk
  children:
  - {name: sda1, type: part, fstype: vfat, mountpoint: /boot, size: 512M}
  - {name: sda2, type: part, fstype: ext4, mountpiont: /}
`), 0o644)
	_, errOut, err := runWith(t, "", path)
	if err == nil || !strings.Contains(errOut, "config key targetMedia[0].children[1].mountpiont is not one slinstaller knows") {
		t.Errorf("err %v, stderr:\n%s", err, errOut)
	}
}

// The dry run of a Safe install says when a new partition is named for one
// the disk already has, instead of only listing a mkfs of it.
func TestDryRunSaysAboutACreateNumber(t *testing.T) {
	bin := t.TempDir()
	os.WriteFile(filepath.Join(bin, "parted"), []byte(`#!/bin/sh
echo "BYT;"; echo "/dev/sda:100000MiB:scsi:512:512:gpt:DISK:;"
echo "1:1MiB:513MiB:512MiB:fat32::boot, esp;"; echo "2:513MiB:20000MiB:19487MiB:ntfs::;"
echo "3:20000MiB:30000MiB:10000MiB:xfs::;"; echo "4:30000MiB:40000MiB:10000MiB:ext4::;"
echo "1:40000MiB:100000MiB:60000MiB:free;"
`), 0o755)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	dir := t.TempDir()
	path := filepath.Join(dir, "safe.yaml")
	os.WriteFile(path, []byte(`reusePartitions: true
targetMedia:
- name: sda
  type: disk
  children:
  - {name: sda1, type: part, fstype: vfat, mountpoint: /boot/efi, preserve: true}
  - {name: sda3, type: part, fstype: ext4, mountpoint: /, create: true, start: 40000M, size: 20000M}
`), 0o644)
	_, errOut, err := runWith(t, "", path)
	if err != nil || !strings.Contains(errOut, "(it will refuse:") || !strings.Contains(errOut, "parted will number it 5") {
		t.Errorf("err %v, stderr:\n%s", err, errOut)
	}
}

// --root is never the live root nor relative, and stage names are checked
// before anything else is.
func TestRootAndStageFlags(t *testing.T) {
	for _, root := range []string{"/", "mnt", "/mnt/..", "//", "/dev", "/run/x", "/usr/local/target"} {
		if err := run([]string{"--root", root, "--unmount"}); err == nil || !strings.Contains(err.Error(), "--root") {
			t.Errorf("--root %q: %v", root, err)
		}
	}
	if err := run([]string{"--through", "bootlaoder", "x.yaml"}); err == nil || !strings.Contains(err.Error(), `--through "bootlaoder" is not a stage`) {
		t.Errorf("--through typo: %v", err)
	}
	if _, err := storage.UnmountTree("/"); err == nil {
		t.Error("UnmountTree(/) was not refused")
	}
}

// A flag after the config file is said to be one, not a second file.
func TestFlagAfterTheConfig(t *testing.T) {
	if err := run([]string{"a.yaml", "--text"}); err == nil || !strings.Contains(err.Error(), "--text comes after the config file; flags go before it") {
		t.Errorf("err = %v", err)
	}
}

// A foreign --arch is refused for a full install (its packages cannot be
// configured here), allowed for a staged one.
func TestForeignArch(t *testing.T) {
	if err := run([]string{"--apply", "--arch", "riscv64", "x.yaml"}); err == nil || !strings.Contains(err.Error(), "--arch riscv64 is not this machine's") {
		t.Errorf("foreign arch: %v", err)
	}
	if err := run([]string{"--arch", hostXBPSArch() + "-musl", "--version"}); err != nil {
		t.Errorf("this machine's arch (musl): %v", err)
	}
}

// A run that installs nothing is not refused for a foreign --arch.
func TestForeignArchWithoutApply(t *testing.T) {
	if err := run([]string{"--arch", "riscv64", "--version"}); err != nil {
		t.Errorf("--version with a foreign arch: %v", err)
	}
}

// The diagnostics config leaves out secret env values and write-files contents.
func TestDiagRedactsEnvAndFiles(t *testing.T) {
	cfg := plainConfig()
	cfg.Environment = map[string]string{"API_TOKEN": "s3cr3t", "API_URL": "https://api.example"}
	cfg.WriteFiles = []*config.WriteFile{{Path: "/etc/wpa.conf", Content: `psk="wifi-secret"`}}
	got := string(redactedConfig(cfg))
	if strings.Contains(got, "s3cr3t") || strings.Contains(got, "wifi-secret") || !strings.Contains(got, "https://api.example") {
		t.Errorf("redacted config:\n%s", got)
	}
}

func TestLogLevelRange(t *testing.T) {
	for _, l := range []string{"0", "5", "-1"} {
		if err := run([]string{"--log-level", l, "x.yaml"}); err == nil || !strings.Contains(err.Error(), "the levels are 1") {
			t.Errorf("--log-level %s: %v", l, err)
		}
	}
}
