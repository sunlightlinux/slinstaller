package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/sunlightlinux/slinstaller/pkg/config"
)

// applyAdversarial runs, as root in the namespace (from applyChild), what
// an install must not do to a disk it was not confirmed for.
func applyAdversarial(t *testing.T, confirmed string) {
	swappedUnderPrompt(t, confirmed)
}

// swapAfter makes every listing after the first n show after instead.
func swapAfter(t *testing.T, e *applyEnv, n int, after string) {
	t.Helper()
	os.WriteFile(filepath.Join(e.bin, "lsblk.2.json"), []byte(`{"blockdevices":[`+after+`]}`), 0o644)
	os.WriteFile(filepath.Join(e.bin, "lsblk.switch"), []byte(strconv.Itoa(n)+"\n"), 0o644)
}

// untouched checks that nothing that writes to a disk ran.
func (e *applyEnv) untouched(what string) {
	e.t.Helper()
	for _, tool := range []string{"parted", "wipefs", "mkfs.ext4", "mkfs.vfat", "mkswap", "cryptsetup",
		"sfdisk", "sh", "resize2fs", "e2fsck", "ntfsresize", "mdadm", "blkdiscard", "shred", "dmsetup"} {
		got := e.ran(tool)
		if tool == "parted" { // a print reads the table, and only that
			var writes []string
			for _, l := range strings.Split(strings.TrimSpace(got), "\n") {
				if l != "" && !strings.Contains(l, " print") {
					writes = append(writes, l)
				}
			}
			got = strings.Join(writes, "\n")
		}
		if got != "" {
			e.t.Errorf("%s: %s ran:\n%s", what, tool, got)
		}
	}
}

// swappedUnderPrompt: the disk the operator confirmed is pulled while the
// prompt waits and another takes its name (a USB disk, a hot-swap bay).
// What the partition stage then finds at the path is not what was named,
// and nothing is written to it. The disk-image path refused this already
// (sameDisk); the install itself only re-checked use and sharing.
func swappedUnderPrompt(t *testing.T, confirmed string) {
	const confirmedDisk = `{"name":"slitest0","path":"` + testDisk + `","size":64000000000,"type":"disk","model":"Test","serial":"MINE"}`
	const otherDisk = `{"name":"slitest0","path":"` + testDisk + `","size":64000000000,"type":"disk","model":"Test","serial":"SOMEONE-ELSES"}`

	// The listings before the partition stage's: counted on a run that
	// goes through, so that the swap lands after the prompt.
	e := newApplyEnv(t)
	setListing(t, e.bin, confirmedDisk)
	if _, err := e.run(confirmed, plainConfig(), "--through", "partition"); err != nil {
		t.Fatalf("baseline: %v", err)
	}
	before := strings.Count(e.ran("lsblk"), "\n") - 1

	e = newApplyEnv(t)
	setListing(t, e.bin, confirmedDisk)
	swapAfter(t, e, before, otherDisk)
	_, err := e.run(confirmed, plainConfig(), "--through", "partition")
	wantRunErr(t, "wipe: disk swapped under the prompt", err, "not the disk that was confirmed")
	e.untouched("wipe: disk swapped under the prompt")

	// A Safe install: the confirmed disk's p2 is shrunk and p3 cut in the
	// room. Swapped for another disk laid out alike, nothing is shrunk.
	shrink := &config.Config{ReusePartitions: true, TargetMedia: []*config.Media{{Name: testName, Type: "disk", Children: []*config.Media{
		{Name: testName + "p1", Type: "part", FsType: "vfat", Mountpoint: "/boot/efi", Preserve: true},
		{Name: testName + "p2", Type: "part", FsType: "ext4", Preserve: true, Resize: "40G"},
		{Name: testName + "p3", Type: "part", FsType: "ext4", Mountpoint: "/", Create: true},
	}}}}
	layout := func(serial, uuid string) string {
		return `{"name":"slitest0","path":"` + testDisk + `","size":128000000000,"type":"disk","serial":"` + serial + `","children":[
		{"name":"slitest0p1","path":"` + testDisk + `p1","size":536870912,"type":"part","fstype":"vfat"},
		{"name":"slitest0p2","path":"` + testDisk + `p2","size":107374182400,"type":"part","fstype":"ext4","uuid":"` + uuid + `"}]}`
	}
	tools := []string{"e2fsck", "resize2fs", "sh", "sfdisk", "setsid"}
	gptParted := func(e *applyEnv) {
		os.WriteFile(filepath.Join(e.bin, "parted"), []byte(`#!/bin/sh
printf '%s\n' "$*" >> "`+e.bin+`/parted.args"
case "$*" in *print*) ;; *) exit 0;; esac
echo "BYT;"; echo "`+testDisk+`:122070MiB:scsi:512:512:gpt:DISK:;"
echo "1:1MiB:513MiB:512MiB:fat32::boot, esp;"; echo "2:513MiB:102913MiB:102400MiB:ext4::;"
echo "1:102913MiB:122070MiB:19157MiB:free;"
`), 0o755)
	}
	e = newApplyEnv(t, tools...)
	gptParted(e)
	setListing(t, e.bin, layout("MINE", "u1"))
	if _, err := e.run(confirmed, shrink, "--through", "partition"); err != nil {
		t.Fatalf("safe baseline: %v", err)
	}
	if !strings.Contains(e.ran("resize2fs"), testDisk+"p2") {
		t.Fatalf("safe baseline did not shrink: %q", e.ran("resize2fs"))
	}
	// (Less the partition stage's own.)
	before = strings.Count(e.ran("lsblk"), "\n") - 1
	if before <= 0 {
		t.Fatalf("safe baseline made no listing")
	}
	for what, after := range map[string]string{
		"another disk":              layout("SOMEONE-ELSES", "u2"),
		"same disk, another fs now": layout("MINE", "u2"),
	} {
		e = newApplyEnv(t, tools...)
		gptParted(e)
		setListing(t, e.bin, layout("MINE", "u1"))
		swapAfter(t, e, before, after)
		_, err = e.run(confirmed, shrink, "--through", "partition")
		wantRunErr(t, "safe: "+what, err, "since it was confirmed")
		e.untouched("safe: " + what)
	}
}
