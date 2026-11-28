package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sunlightlinux/slinstaller/pkg/config"
)

// --system-check of a Safe install whose new partition parted would number
// differently: the plan says "it will refuse", and the check must not then
// say it passed (--apply stops at the partition stage, after the confirm).
func TestSystemCheckRefusesACreateNumber(t *testing.T) {
	// The firmware is stood in for: skipped on a machine not booted under
	// UEFI (CI's), the test checked nothing there.
	savedUEFI := isUEFI
	isUEFI = func() bool { return true }
	defer func() { isUEFI = savedUEFI }()
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
	// With --apply beside it too: a system check still changes nothing,
	// and its verdict is the same.
	_, errOut, err = runWith(t, "", "--system-check", "--apply", "--offline", "--local-repo", repo, path)
	if err == nil || strings.Contains(errOut, "System check passed") {
		t.Errorf("--system-check --apply passed what --apply refuses: %v\n%s", err, errOut)
	}
	// But not for a resume, which partitions nothing: its new partition is
	// on the disk, numbered as the config names it.
	_, errOut, _ = runWith(t, "", "--system-check", "--apply", "--resume", "--offline", "--local-repo", repo, path)
	if strings.Contains(errOut, "is to be created") {
		t.Errorf("--system-check of a resume refused its partitions as to be created:\n%s", errOut)
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

	// On UEFI firmware, stood in for: on a BIOS host the firmware's refusal
	// came first, and this case failed there.
	isUEFI = func() bool { return true }
	held, lerr := lockInstall()
	if lerr != nil {
		t.Fatalf("taking the lock: %v", lerr)
	}
	_, errOut, err = runWith(t, "1\n", "--text")
	held.Close()
	isUEFI = saved
	wantRunErr(t, "interactive, another install running", err, "another slinstaller install is running")
	if asked(errOut) {
		t.Errorf("interactive, another install running: the questions were asked before the refusal:\n%s", errOut)
	}
}

// The wizard's "Back to menu" re-runs everything with a fresh config: a
// proxy the first pass set stayed in the environment, and the second pass,
// its proxy cleared on screen, still fetched through it.
func TestProxyEnvIsPerPass(t *testing.T) {
	for _, k := range proxyVars {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}
	t.Setenv("no_proxy", "localhost") // the operator's own, kept
	base := saveProxyEnv()

	first := &config.Config{HTTPSProxy: "http://bad:3128"}
	applyProxyEnv(base, first.ProxyEnv())
	if os.Getenv("HTTPS_PROXY") != "http://bad:3128" || os.Getenv("http_proxy") != "http://bad:3128" {
		t.Fatalf("the config's proxy was not applied: %q", os.Environ())
	}
	applyProxyEnv(base, (&config.Config{}).ProxyEnv())
	for _, k := range proxyVars {
		v, set := os.LookupEnv(k)
		if k == "no_proxy" {
			if v != "localhost" {
				t.Errorf("no_proxy = %q, want the operator's localhost back", v)
			}
			continue
		}
		if set {
			t.Errorf("%s=%q left over from the previous pass", k, v)
		}
	}
}

// The disk-image dry run invited "Re-run with --apply" over a disk --apply
// then refuses - in use, or sharing a volume group with another disk -
// where the install's dry run says "it will refuse".
func TestDiskImageDryRunSaysItWillRefuse(t *testing.T) {
	dir := t.TempDir()
	bin := onlyTools(t, dir)
	image := filepath.Join(dir, "disk.raw")
	os.WriteFile(image, []byte("image"), 0o644)
	path := writeConfig(t, dir, imageConfig(image))

	setListing(t, bin, `{"name":"`+testName+`","path":"`+testDisk+`","size":64000000000,"type":"disk","children":[
		{"name":"`+testName+`p1","path":"`+testDisk+`p1","size":1000,"type":"part","mountpoint":"/mnt/data"}]}`)
	_, errOut, err := runWith(t, "", path)
	if err != nil || !strings.Contains(errOut, "it will refuse: target disk "+testDisk+" is in use: "+testDisk+"p1 is mounted at /mnt/data") || strings.Contains(errOut, "Re-run with --apply") {
		t.Errorf("disk in use: %v\n%s", err, errOut)
	}
	// The other checks --apply makes of the target too: a disk that is not
	// there was invited to an --apply that refused it.
	setListing(t, bin, `{"name":"sdz","path":"/dev/sdz","size":64000000000,"type":"disk"}`)
	_, errOut, err = runWith(t, "", path)
	if err != nil || !strings.Contains(errOut, "it will refuse: target disk "+testDisk+" is not among the machine's block devices") || strings.Contains(errOut, "Re-run with --apply") {
		t.Errorf("disk missing: %v\n%s", err, errOut)
	}

	setListing(t, bin, `{"name":"`+testName+`","path":"`+testDisk+`","size":64000000000,"type":"disk","children":[
		{"name":"`+testName+`p1","path":"`+testDisk+`p1","size":1000,"type":"part","fstype":"LVM2_member"}]},
		{"name":"sdz","path":"/dev/sdz","size":64000000000,"type":"disk","children":[
		{"name":"sdz1","path":"/dev/sdz1","size":1000,"type":"part","fstype":"LVM2_member"}]}`)
	os.WriteFile(filepath.Join(bin, "pvs.out"), []byte("  "+testDisk+"p1:data\n  /dev/sdz1:data\n"), 0o644)
	_, errOut, err = runWith(t, "", path)
	if err != nil || !strings.Contains(errOut, "it will refuse, unless --force-destructive") || strings.Contains(errOut, "Re-run with --apply") {
		t.Errorf("disk sharing a group: %v\n%s", err, errOut)
	}
	_, errOut, _ = runWith(t, "", "--force-destructive", path)
	if !strings.Contains(errOut, "Re-run with --apply") {
		t.Errorf("--force-destructive:\n%s", errOut)
	}
}

// A config given as a URL is not fetched for a run the flags alone refuse.
func TestURLConfigNotFetchedForARefusedRun(t *testing.T) {
	fetched := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fetched++
		w.Write([]byte("targetMedia: []\n"))
	}))
	defer srv.Close()
	err := run([]string{"--allow-insecure-http", "--resume", srv.URL + "/c.yaml"})
	wantRunErr(t, "--resume without --apply", err, "--resume continues a full install")
	if fetched != 0 {
		t.Errorf("the config was fetched %d times for a run refused by its flags", fetched)
	}
}

// A two-disk plan with one disk missing: the dry run says so, and still
// says what it says of the other disk - in use, and what the teardown would
// release from it. (It returned at the missing disk and hid them.)
func TestDryRunOneOfTwoDisksMissing(t *testing.T) {
	dir := t.TempDir()
	bin := onlyTools(t, dir)
	cfg := &config.Config{
		TargetMedia: []*config.Media{
			{Name: testName, Type: "disk", Children: []*config.Media{
				{Name: testName + "p1", Type: "part", FsType: "vfat", Mountpoint: "/boot", Size: "512M"},
				{Name: testName + "p2", Type: "raid", Raid: "md0"}}},
			{Name: "slitest1", Type: "disk", Children: []*config.Media{
				{Name: "slitest1p1", Type: "raid", Raid: "md0"}}},
		},
		Raid: []*config.RaidArray{{Name: "md0", Level: "1", FsType: "ext4", Mountpoint: "/"}},
	}
	path := writeConfig(t, dir, cfg)
	setListing(t, bin, `{"name":"`+testName+`","path":"`+testDisk+`","size":64000000000,"type":"disk","children":[
		{"name":"`+testName+`p1","path":"`+testDisk+`p1","size":1000,"type":"part","mountpoint":"/mnt/data"}]}`)
	_, errOut, err := runWith(t, "", path)
	if err != nil {
		t.Fatalf("dry run: %v\n%s", err, errOut)
	}
	for _, want := range []string{
		"it will refuse: target disk /dev/slitest1 is not among",
		"Before partitioning " + testDisk,
		"(it will refuse: " + testDisk + "p1 is mounted at /mnt/data)",
		"As it stands, --apply would refuse this",
	} {
		if !strings.Contains(errOut, want) {
			t.Errorf("dry run missing %q:\n%s", want, errOut)
		}
	}
}
