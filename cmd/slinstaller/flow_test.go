package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sunlightlinux/slinstaller/pkg/system"
)

// --system-check of a Safe install whose new partition parted would number
// differently: the plan says "it will refuse", and the check must not then
// say it passed (--apply stops at the partition stage, after the confirm).
func TestSystemCheckRefusesACreateNumber(t *testing.T) {
	if !system.IsUEFI() {
		t.Skip("the system check stops at the firmware on this machine")
	}
	dir := t.TempDir()
	bin := onlyTools(t, dir, baseTools...)
	os.WriteFile(filepath.Join(bin, "parted"), []byte(`#!/bin/sh
echo "BYT;"; echo "`+testDisk+`:100000MiB:scsi:512:512:gpt:DISK:;"
echo "1:1MiB:513MiB:512MiB:fat32::boot, esp;"; echo "2:513MiB:20000MiB:19487MiB:ntfs::;"
echo "3:20000MiB:30000MiB:10000MiB:xfs::;"; echo "4:30000MiB:40000MiB:10000MiB:ext4::;"
echo "1:40000MiB:100000MiB:60000MiB:free;"
`), 0o755)
	setListing(t, bin, `{"name":"`+testName+`","path":"`+testDisk+`","size":104857600000,"type":"disk","children":[
		{"name":"`+testName+`p1","path":"`+testDisk+`p1","size":536870912,"type":"part","fstype":"vfat"}]}`)
	repo := filepath.Join(dir, "repo")
	os.MkdirAll(repo, 0o755)
	os.WriteFile(filepath.Join(repo, "x86_64-repodata"), nil, 0o644)
	path := filepath.Join(dir, "safe.yaml")
	os.WriteFile(path, []byte(`reusePartitions: true
targetMedia:
- name: `+testName+`
  type: disk
  children:
  - {name: `+testName+`p1, type: part, fstype: vfat, mountpoint: /boot/efi, preserve: true}
  - {name: `+testName+`p3, type: part, fstype: ext4, mountpoint: /, create: true, start: 40000M, size: 20000M}
`), 0o644)
	_, errOut, err := runWith(t, "", "--system-check", "--offline", "--local-repo", repo, path)
	if !strings.Contains(errOut, "(it will refuse:") {
		t.Fatalf("the plan did not say it will refuse: %v\n%s", err, errOut)
	}
	if err == nil || strings.Contains(errOut, "System check passed") {
		t.Errorf("--system-check passed what it said --apply refuses: %v\n%s", err, errOut)
	}
}

// interactiveRefusedFirst runs in the user-namespace child (as root): what
// the machine itself refuses - a BIOS boot, another install holding the
// lock - is said before the interactive installer asks anything, not after
// every answer (passwords included) has been typed.
func interactiveRefusedFirst(t *testing.T) {
	newApplyEnv(t) // stand-in tools: the text flow lists testDisk
	asked := func(errOut string) bool { return strings.Contains(errOut, "Install to which disk") }

	saved := isUEFI
	isUEFI = func() bool { return false }
	_, errOut, err := runWith(t, "1\n", "--text")
	isUEFI = saved
	wantRunErr(t, "interactive, BIOS boot", err, "not booted in UEFI mode")
	if asked(errOut) {
		t.Errorf("interactive, BIOS boot: the questions were asked before the refusal:\n%s", errOut)
	}
	// A staged run does not need UEFI: it is not refused for it.
	isUEFI = func() bool { return false }
	_, errOut, _ = runWith(t, "", "--text", "--through", "partition")
	isUEFI = saved
	if !asked(errOut) {
		t.Errorf("interactive --through on a BIOS boot was refused:\n%s", errOut)
	}

	held, lerr := lockInstall()
	if lerr != nil {
		t.Fatalf("taking the lock: %v", lerr)
	}
	_, errOut, err = runWith(t, "1\n", "--text")
	held.Close()
	wantRunErr(t, "interactive, another install running", err, "another slinstaller install is running")
	if asked(errOut) {
		t.Errorf("interactive, another install running: the questions were asked before the refusal:\n%s", errOut)
	}
}
