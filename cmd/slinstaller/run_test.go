package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sunlightlinux/slinstaller/pkg/config"
	"github.com/sunlightlinux/slinstaller/pkg/system"
	"golang.org/x/sys/unix"
)

// testDisk is the install target in these tests: a device that does not
// exist, so nothing here could reach a real disk even by mistake.
const (
	testName = "slitest0"
	testDisk = "/dev/" + testName
)

// runWith calls run with stdin, stdout and stderr replaced by files.
func runWith(t *testing.T, stdin string, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	dir := t.TempDir()
	open := func(name, content string) *os.File {
		p := filepath.Join(dir, name)
		if werr := os.WriteFile(p, []byte(content), 0o600); werr != nil {
			t.Fatal(werr)
		}
		f, oerr := os.OpenFile(p, os.O_RDWR, 0)
		if oerr != nil {
			t.Fatal(oerr)
		}
		return f
	}
	in, out, errf := open("in", stdin), open("out", ""), open("err", "")
	savedIn, savedOut, savedErr := os.Stdin, os.Stdout, os.Stderr
	os.Stdin, os.Stdout, os.Stderr = in, out, errf
	err = run(args)
	os.Stdin, os.Stdout, os.Stderr = savedIn, savedOut, savedErr
	in.Close()
	out.Close()
	errf.Close()
	o, _ := os.ReadFile(filepath.Join(dir, "out"))
	e, _ := os.ReadFile(filepath.Join(dir, "err"))
	return string(o), string(e), err
}

// plainConfig is the template's layout on testDisk: ESP, swap, root.
func plainConfig() *config.Config {
	return &config.Config{TargetMedia: []*config.Media{{Name: testName, Type: "disk", Children: []*config.Media{
		{Name: testName + "p1", Type: "part", FsType: "vfat", Mountpoint: "/boot", Size: "512M"},
		{Name: testName + "p2", Type: "part", FsType: "swap", Size: "2G"},
		{Name: testName + "p3", Type: "part", FsType: "ext4", Mountpoint: "/", Size: "0"},
	}}}}
}

func writeConfig(t *testing.T, dir string, cfg *config.Config) string {
	t.Helper()
	f, err := os.CreateTemp(dir, "cfg-*.yaml")
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	if err := config.Save(f.Name(), cfg); err != nil {
		t.Fatal(err)
	}
	return f.Name()
}

var catPath string

// (The stand-ins record with printf, not echo: an argument such as -e has to
// be recorded, not taken as echo's own option.)
//
// onlyTools makes PATH a directory holding nothing but stand-ins: each named
// tool records its arguments in <dir>/<name>.args and succeeds. lsblk and
// pvs print <dir>/lsblk.json and <dir>/pvs.out. No real tool is reachable.
func onlyTools(t *testing.T, dir string, names ...string) string {
	t.Helper()
	// Looked up once: after the first call PATH holds only the stand-ins.
	if catPath == "" {
		p, err := exec.LookPath("cat")
		if err != nil {
			t.Skip("no cat to build stand-in tools with")
		}
		catPath = p
	}
	cat := catPath
	bin, err := os.MkdirTemp(dir, "bin-")
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range append(names, "lsblk", "pvs") {
		body := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"" + bin + "/" + n + ".args\"\n"
		switch n {
		case "lsblk":
			body += "exec " + cat + " \"" + bin + "/lsblk.json\"\n"
		case "pvs":
			body += "[ -f \"" + bin + "/pvs.out\" ] && exec " + cat + " \"" + bin + "/pvs.out\"\nexit 0\n"
		case "cryptsetup":
			body += cat + " > /dev/null\n"
		}
		if err := os.WriteFile(filepath.Join(bin, n), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	setListing(t, bin, `{"name":"slitest0","path":"`+testDisk+`","size":64000000000,"type":"disk","model":"Test"}`)
	t.Setenv("PATH", bin)
	return bin
}

func setListing(t *testing.T, bin string, devices string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(bin, "lsblk.json"), []byte(`{"blockdevices":[`+devices+`]}`), 0o644); err != nil {
		t.Fatal(err)
	}
}

func wantRunErr(t *testing.T, what string, err error, part string) {
	t.Helper()
	if err == nil {
		t.Errorf("%s: no error, want %q", what, part)
	} else if !strings.Contains(err.Error(), part) {
		t.Errorf("%s: error %q does not say %q", what, err, part)
	}
}

func TestRunInformationalFlags(t *testing.T) {
	out, _, err := runWith(t, "", "--version")
	if err != nil || out != "slinstaller "+version+"\n" {
		t.Errorf("--version: %q, %v", out, err)
	}

	// --genpass prints a crypt hash a config's user can carry; "-" reads the
	// password from the input instead of the command line.
	for _, tc := range [][2]string{{"correct horse", ""}, {"-", "correct horse\n"}} {
		out, _, err = runWith(t, tc[1], "--genpass", tc[0])
		if err != nil || !strings.HasPrefix(out, "$6$") || strings.Contains(out, "correct horse") {
			t.Errorf("--genpass %s: %q, %v", tc[0], out, err)
		}
	}

	_, errOut, err := runWith(t, "", "--no-such-flag")
	if err == nil || !strings.Contains(errOut, "usage: slinstaller") {
		t.Errorf("unknown flag: %v\n%s", err, errOut)
	}
	_, errOut, err = runWith(t, "", "a.yaml", "b.yaml")
	wantRunErr(t, "two configs", err, "at most one config file")
	if !strings.Contains(errOut, "usage: slinstaller") {
		t.Errorf("two configs: usage not shown:\n%s", errOut)
	}
	_, _, err = runWith(t, "", "-b", "nocolon", "x.yaml")
	wantRunErr(t, "bad --block-device", err, "want name:file")
	_, _, err = runWith(t, "", filepath.Join(t.TempDir(), "absent.yaml"))
	if err == nil {
		t.Error("a missing config file was accepted")
	}

	a := aliasFlag{{Name: "disk", File: "/dev/sda"}, {Name: "data", File: "/dev/sdb"}}
	if a.String() != "disk:/dev/sda,data:/dev/sdb" {
		t.Errorf("aliasFlag.String = %q", a.String())
	}
	_, _, err = runWith(t, "", "--template", filepath.Join(t.TempDir(), "no-such-dir", "t.yaml"))
	if err == nil {
		t.Error("a template in a missing directory was reported as written")
	}
}

func TestListDisks(t *testing.T) {
	dir := t.TempDir()
	bin := onlyTools(t, dir)
	setListing(t, bin, `{"name":"sda","path":"/dev/sda","size":64000000000,"type":"disk","model":"Fast SSD","children":[
		{"name":"sda1","path":"/dev/sda1","size":1000000000,"type":"part","mountpoint":"/boot"}]},
		{"name":"sr0","path":"/dev/sr0","size":1000,"type":"rom"}`)
	out, _, err := runWith(t, "", "--list-disks")
	if err != nil {
		t.Fatal(err)
	}
	// The disk with its model and partitions, and why it cannot be a target;
	// the optical drive is not a disk.
	for _, want := range []string{"/dev/sda", "Fast SSD", "/dev/sda1", "(not installable: /dev/sda1 is mounted at /boot)"} {
		if !strings.Contains(out, want) {
			t.Errorf("--list-disks missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "sr0") {
		t.Errorf("--list-disks shows a non-disk:\n%s", out)
	}

	setListing(t, bin, "")
	if out, _, err = runWith(t, "", "--list-disks"); err != nil || out != "no disks found\n" {
		t.Errorf("no disks: %q, %v", out, err)
	}
	os.Remove(filepath.Join(bin, "lsblk.json"))
	_, _, err = runWith(t, "", "--list-disks")
	wantRunErr(t, "lsblk failing", err, "lsblk")
}

func TestDryRun(t *testing.T) {
	dir := t.TempDir()
	bin := onlyTools(t, dir)
	path := writeConfig(t, dir, plainConfig())

	// A dry run prints the plan and what would be released first, and says
	// what --apply would destroy. Nothing runs.
	setListing(t, bin, `{"name":"slitest0","path":"`+testDisk+`","size":64000000000,"type":"disk","children":[
		{"name":"slitest0p1","path":"`+testDisk+`p1","size":1000,"type":"part","mountpoint":"/mnt/data"}]}`)
	out, errOut, err := runWith(t, "", path)
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if !strings.Contains(out, "parted") || !strings.Contains(out, testDisk) {
		t.Errorf("the plan was not printed:\n%s", out)
	}
	for _, want := range []string{
		"Before partitioning " + testDisk, "wipefs -a " + testDisk + "p1",
		"(it will refuse: " + testDisk + "p1 is mounted at /mnt/data)",
		// Not "Re-run with --apply" after saying --apply would refuse.
		"As it stands, --apply would refuse this",
	} {
		if !strings.Contains(errOut, want) {
			t.Errorf("dry run missing %q:\n%s", want, errOut)
		}
	}
	if strings.Contains(errOut, "Re-run with --apply") {
		t.Errorf("invited to --apply what it would refuse:\n%s", errOut)
	}
	if _, err := os.Stat(filepath.Join(bin, "parted.args")); err == nil {
		t.Error("a dry run ran parted")
	}

	// A target the machine does not have, or a listing that fails: the plan
	// is still shown.
	setListing(t, bin, "")
	if _, _, err = runWith(t, "", path); err != nil {
		t.Errorf("dry run for an absent disk: %v", err)
	}
	os.Remove(filepath.Join(bin, "lsblk.json"))
	if _, _, err = runWith(t, "", path); err != nil {
		t.Errorf("dry run without a listing: %v", err)
	}

	// Reusing partitions keeps the table, and the message says so.
	reuse := plainConfig()
	reuse.ReusePartitions = true
	_, errOut, err = runWith(t, "", writeConfig(t, dir, reuse))
	if err != nil || !strings.Contains(errOut, "Dry run (reusePartitions)") || !strings.Contains(errOut, "the table is kept") {
		t.Errorf("reuse dry run: %v\n%s", err, errOut)
	}
	// On a disk in use it says --apply would refuse, not "Re-run with --apply".
	setListing(t, bin, `{"name":"slitest0","path":"`+testDisk+`","size":64000000000,"type":"disk","children":[
		{"name":"slitest0p1","path":"`+testDisk+`p1","size":1000,"type":"part","mountpoint":"/boot/efi"}]}`)
	_, errOut, _ = runWith(t, "", writeConfig(t, dir, reuse))
	if !strings.Contains(errOut, "it will refuse: "+testDisk+" is in use") || strings.Contains(errOut, "Re-run with --apply") {
		t.Errorf("reuse dry run on a disk in use:\n%s", errOut)
	}
	os.Remove(filepath.Join(bin, "lsblk.json"))

	// The flags that stand for config fields are accepted together.
	if _, _, err = runWith(t, "", "--offline", "--skip-validation-size", "--skip-validation-all", "--reboot",
		"--copy-network", "--allow-insecure-http", "--swap-file-size", "1G", path); err != nil {
		t.Errorf("dry run with the config flags: %v", err)
	}
	// --swap-file-size is validated like the config's own field.
	_, _, err = runWith(t, "", "--swap-file-size", "lots", path)
	if err == nil {
		t.Error("an unparseable --swap-file-size was accepted")
	}

	// A key nothing reads is warned about, and the dry run goes on.
	typo := filepath.Join(dir, "typo.yaml")
	data, _ := os.ReadFile(path)
	if err := os.WriteFile(typo, append(data, "encryptd: true\n"...), 0o600); err != nil {
		t.Fatal(err)
	}
	_, errOut, err = runWith(t, "", typo)
	if err != nil || !strings.Contains(errOut, "warning: config key encryptd is not one slinstaller knows") {
		t.Errorf("unknown key: %v\n%s", err, errOut)
	}

	// A config that cannot be planned is refused.
	bad := plainConfig()
	bad.TargetMedia[0].Children[2].FsType = "ntfs"
	if _, _, err = runWith(t, "", writeConfig(t, dir, bad)); err == nil {
		t.Error("a layout with an unsupported filesystem was planned")
	}

	// A mistyped timezone or keymap is caught here when the live system can
	// tell — and not at all with --skip-validation-all.
	if ok, checked := system.ValidTimezone("Mars/Olympus"); checked && !ok {
		tz := plainConfig()
		tz.Timezone = "Mars/Olympus"
		p := writeConfig(t, dir, tz)
		_, _, err = runWith(t, "", p)
		wantRunErr(t, "bad timezone", err, "not a known zoneinfo zone")
		if _, _, err = runWith(t, "", "--skip-validation-all", p); err != nil {
			t.Errorf("bad timezone with validation off: %v", err)
		}
	}
	if ok, checked := system.ValidKeymap("zz-nokeys"); checked && !ok {
		km := plainConfig()
		km.Keyboard = "zz-nokeys"
		_, _, err = runWith(t, "", writeConfig(t, dir, km))
		wantRunErr(t, "bad keymap", err, "not among the system's console keymaps")
	}
}

func TestApplyNeedsRoot(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root")
	}
	dir := t.TempDir()
	onlyTools(t, dir)
	path := writeConfig(t, dir, plainConfig())
	log := filepath.Join(dir, "install.log")

	os.WriteFile(log, []byte("a failed install's log\n"), 0o600)
	_, _, err := runWith(t, "", "--apply", "--log", log, path)
	wantRunErr(t, "--apply as a user", err, "--apply must run as root")
	if got, _ := os.ReadFile(log); string(got) != "a failed install's log\n" {
		t.Errorf("the log was truncated before the refusal: %q", got)
	}
	_, _, err = runWith(t, "")
	wantRunErr(t, "interactive as a user", err, "interactive install must run as root")

	// A --log that cannot be written is an error: it was asked for. (As
	// root: a user is refused before the log is opened, which truncates it.)
	asRoot := func() { geteuid = func() int { return 0 } }
	defer func() { geteuid = os.Geteuid }()
	asRoot()
	_, _, err = runWith(t, "", "--apply", "--log", filepath.Join(dir, "no-such-dir", "x.log"), path)
	wantRunErr(t, "unwritable --log", err, "--log:")
	geteuid = os.Geteuid

	// A local repository under the target root would vanish when the target
	// is mounted over it.
	root := filepath.Join(dir, "mnt")
	repo := filepath.Join(root, "repo")
	os.MkdirAll(repo, 0o755)
	os.WriteFile(filepath.Join(repo, "x86_64-repodata"), nil, 0o644)
	_, _, err = runWith(t, "", "--apply", "--log", log, "--root", root, "--local-repo", repo, path)
	wantRunErr(t, "repo under the root", err, "is under the target root")

	// The config's mirror, and offline, pass the same gate.
	mirror := plainConfig()
	mirror.SwupdMirror = "https://mirror.example/current"
	_, _, err = runWith(t, "", "--apply", "--log", log, writeConfig(t, dir, mirror))
	wantRunErr(t, "config mirror", err, "--apply must run as root")
	_, _, err = runWith(t, "", "--apply", "--offline", "--repo", "https://repo.example", "--log", log, path)
	wantRunErr(t, "offline", err, "--apply must run as root")

	// Without a place to seed the mirror's signing key the install stops:
	// it would otherwise fetch packages it cannot verify.
	extra := plainConfig()
	extra.Packages = []string{"vim", "vim"}
	t.Setenv("TMPDIR", filepath.Join(dir, "no-such-dir"))
	asRoot()
	_, _, err = runWith(t, "", "--apply", "--log", log, writeConfig(t, dir, extra))
	wantRunErr(t, "no temp directory for the keys", err, "seed the Sunlight repo key")
}

func TestSeedSunlightKeyCopiesOnlyKeyFiles(t *testing.T) {
	live := t.TempDir()
	os.WriteFile(filepath.Join(live, "void.plist"), []byte("void"), 0o644)
	os.Mkdir(filepath.Join(live, "subdir"), 0o755)
	dir, err := seedSunlightKey(live)
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	if _, err := os.Stat(filepath.Join(dir, "subdir")); err == nil {
		t.Error("a directory was copied into the keys")
	}
	if _, err := os.Stat(filepath.Join(dir, "void.plist")); err != nil {
		t.Errorf("the live key was not copied: %v", err)
	}

	// A key that cannot be read fails the seeding and leaves no directory
	// behind. (A dangling link is unreadable for root too.)
	os.Symlink(filepath.Join(live, "gone"), filepath.Join(live, "broken.plist"))
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	if _, err := seedSunlightKey(live); err == nil {
		t.Error("an unreadable key was not reported")
	}
	if left, _ := os.ReadDir(tmp); len(left) != 0 {
		t.Errorf("a failed seeding left %d entries behind", len(left))
	}
	// Nowhere to make the directory.
	t.Setenv("TMPDIR", filepath.Join(tmp, "no-such-dir"))
	if _, err := seedSunlightKey(live); err == nil {
		t.Error("seeding without a temp directory succeeded")
	}
}

func TestFullScreenOnlyOnATerminalWithASize(t *testing.T) {
	savedIn, savedOut := os.Stdin, os.Stdout
	defer func() { os.Stdin, os.Stdout = savedIn, savedOut }()
	t.Setenv("TERM", "xterm")

	// A file or pipe is not a terminal.
	f, err := os.Create(filepath.Join(t.TempDir(), "f"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	os.Stdin, os.Stdout = f, f
	if isTerminal(f) || fullScreenCapable() {
		t.Error("a regular file was taken for a terminal")
	}
	if _, _, ok := terminalSize(f); ok {
		t.Error("a regular file reported a window size")
	}

	// A pseudo-terminal that has not been given a size is what a serial
	// console looks like: the full-screen interface is not used there.
	pty, err := os.OpenFile("/dev/ptmx", os.O_RDWR, 0)
	if err != nil {
		t.Skipf("no pseudo-terminal: %v", err)
	}
	defer pty.Close()
	os.Stdin, os.Stdout = pty, pty
	if !isTerminal(pty) {
		t.Fatal("a pseudo-terminal is a terminal")
	}
	if fullScreenCapable() {
		t.Error("full-screen chosen on a terminal with no size")
	}
	// With a size it is used …
	if err := unix.IoctlSetWinsize(int(pty.Fd()), unix.TIOCSWINSZ, &unix.Winsize{Col: 80, Row: 24}); err != nil {
		t.Skipf("cannot size the pseudo-terminal: %v", err)
	}
	if c, r, ok := terminalSize(pty); !ok || c != 80 || r != 24 {
		t.Errorf("terminalSize = %dx%d, %v", c, r, ok)
	}
	if !fullScreenCapable() {
		t.Error("full-screen not chosen on a sized terminal")
	}
	// … unless TERM says the terminal cannot draw it.
	for _, term := range []string{"dumb", ""} {
		t.Setenv("TERM", term)
		if fullScreenCapable() {
			t.Errorf("full-screen chosen with TERM=%q", term)
		}
	}
}

// A config that names its disk by serial is planned for whatever that disk
// is called on this machine, and refused when the disk is not here.
func TestDryRunResolvesADiskBySerial(t *testing.T) {
	dir := t.TempDir()
	bin := onlyTools(t, dir)
	setListing(t, bin, `{"name":"sda","path":"/dev/sda","size":64000000000,"type":"disk","serial":"OTHER"},
		{"name":"nvme0n1","path":"/dev/nvme0n1","size":64000000000,"type":"disk","serial":"NV42","wwn":"eui.0025"}`)
	cfg := plainConfig()
	cfg.TargetMedia[0].Match = &config.DiskMatch{Serial: "NV42"}
	path := writeConfig(t, dir, cfg)

	out, errOut, err := runWith(t, "", path)
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if !strings.Contains(errOut, "disk with serial NV42 is /dev/nvme0n1") {
		t.Errorf("the resolved disk is not shown:\n%s", errOut)
	}
	if !strings.Contains(out, "/dev/nvme0n1 mklabel gpt") || !strings.Contains(out, "/dev/nvme0n1p3") || strings.Contains(out, testDisk) {
		t.Errorf("the plan is not for the matched disk:\n%s", out)
	}

	cfg.TargetMedia[0].Match.Serial = "NOT-HERE"
	_, _, err = runWith(t, "", writeConfig(t, dir, cfg))
	wantRunErr(t, "absent serial", err, "no disk on this machine matches serial NOT-HERE")

	os.Remove(filepath.Join(bin, "lsblk.json"))
	_, _, err = runWith(t, "", path)
	wantRunErr(t, "no listing", err, "cannot list the disks")
}

// A swap sized "auto" is given a size for this machine before the plan is
// shown, so what a dry run prints is what --apply would do.
func TestDryRunSizesAutoSwap(t *testing.T) {
	dir := t.TempDir()
	onlyTools(t, dir)
	cfg := plainConfig()
	cfg.TargetMedia[0].Children[1].Size = "auto"
	out, _, err := runWith(t, "", writeConfig(t, dir, cfg))
	if err != nil {
		t.Fatalf("dry run with an auto swap: %v", err)
	}
	want := config.SuggestedSwap(system.MemTotalBytes()) >> 20
	if !strings.Contains(out, fmt.Sprintf("mkpart linux-swap 512MiB %dMiB", 512+want)) {
		t.Errorf("swap partition not sized for this machine (%d MiB):\n%s", want, out)
	}
}
