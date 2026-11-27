package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sunlightlinux/slinstaller/pkg/blockdev"
	"github.com/sunlightlinux/slinstaller/pkg/config"
)

func imageConfig(image string) *config.Config {
	return &config.Config{DiskImage: image, TargetMedia: []*config.Media{{Name: testName, Type: "disk"}}}
}

func TestDiskImageCommands(t *testing.T) {
	devs := []blockdev.Device{{Name: "sda", Path: "/dev/sda", Type: "disk", Children: []blockdev.Device{
		{Name: "sda1", Path: "/dev/sda1", Type: "part"}}}}
	var got []string
	for _, c := range diskImageCommands(devs, "/img/My Image.raw.zst", "/dev/sda", false) {
		got = append(got, strings.Join(c, "|"))
	}
	// What is on the disk is released first; the image and the disk reach
	// the shell as arguments, so a name with a space or a quote in it is
	// only ever a file name.
	want := []string{
		"wipefs|-a|/dev/sda1",
		"wipefs|-a|/dev/sda",
		`sh|-c|` + checkedPipeline([]string{`zstd -dc "$1"`, `dd of="$2" bs=4M conv=fsync status=none`}) + `|sh|/img/My Image.raw.zst|/dev/sda`,
		"partprobe|/dev/sda",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("commands:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	for ext, reader := range map[string]string{".raw": `cat "$1"`, ".img": `cat "$1"`, ".xz": `xz -dc "$1"`, ".GZ": `gzip -dc "$1"`, ".bz2": `bzip2 -dc "$1"`} {
		if st := diskImageStages("/img/disk"+ext, false); st[0] != reader {
			t.Errorf("%s: %q", ext, st)
		}
	}
	// From a URL it is written as it downloads, the URL an argument too —
	// and its suffix is read from the path, not a signed URL's query.
	remote := diskImageCommands(nil, "https://img.example/disk.raw.zst?sig=abc", "/dev/sdb", false)[0]
	if st := diskImageStages("https://img.example/disk.raw.zst?sig=abc", false); strings.Join(st, " | ") != `curl -fsSL --speed-limit 1024 --speed-time 600 --proto-redir =https "$1" | zstd -dc | dd of="$2" bs=4M conv=fsync status=none` || remote[4] != "https://img.example/disk.raw.zst?sig=abc" {
		t.Errorf("remote image: %q %v", st, remote)
	}
}

// The write fails when ANY stage of it does: a truncated image or a failed
// download is not reported as written. (dd alone decided before.)
func TestDiskImagePipelineFailsOnAnyStage(t *testing.T) {
	for _, tool := range []string{"sh", "xz", "dd"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("no %s here", tool)
		}
	}
	dir := t.TempDir()
	raw := filepath.Join(dir, "disk.raw")
	os.WriteFile(raw, bytes.Repeat([]byte("sunlight"), 1<<16), 0o644)
	if out, err := exec.Command("xz", "-k", raw).CombinedOutput(); err != nil {
		t.Fatalf("xz: %v %s", err, out)
	}
	whole, _ := os.ReadFile(raw + ".xz")
	cut := filepath.Join(dir, "cut.raw.xz")
	os.WriteFile(cut, whole[:len(whole)/2], 0o644)
	write := func(image string) error {
		target := filepath.Join(dir, "target")
		os.WriteFile(target, nil, 0o644)
		return exec.Command("sh", "-c", checkedPipeline(diskImageStages(image, false)), "sh", image, target).Run()
	}
	if err := write(raw + ".xz"); err != nil {
		t.Fatalf("a good image: %v", err)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "target")); !bytes.Equal(got, bytes.Repeat([]byte("sunlight"), 1<<16)) {
		t.Error("a good image was not written whole")
	}
	if err := write(cut); err == nil {
		t.Error("a truncated image was reported as written")
	}
	if err := write(filepath.Join(dir, "missing.raw")); err == nil {
		t.Error("a missing image was reported as written")
	}
	// gzip's "trailing garbage ignored" (exit 2) is a whole image.
	if _, err := exec.LookPath("gzip"); err == nil {
		exec.Command("gzip", "-k", raw).Run()
		gz, _ := os.ReadFile(raw + ".gz")
		padded := filepath.Join(dir, "padded.raw.gz")
		os.WriteFile(padded, append(gz, "garbage"...), 0o644)
		if err := write(padded); err != nil {
			t.Errorf("a gzip image with trailing garbage: %v", err)
		}
	}
}

func TestDiskImageDryRunAndRefusals(t *testing.T) {
	dir := t.TempDir()
	onlyTools(t, dir)
	image := filepath.Join(dir, "disk.raw.xz")
	os.WriteFile(image, []byte("image"), 0o644)

	out, errOut, err := runWith(t, "", writeConfig(t, dir, imageConfig("disk.raw.xz"))) // beside the config
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if !strings.Contains(out, "xz -dc") || !strings.Contains(out, image+" "+testDisk) || !strings.Contains(errOut, "Dry run. Re-run with --apply to write "+image+" over "+testDisk) {
		t.Errorf("dry run:\n%s\n%s", out, errOut)
	}
	// An https image needs no file to be there for the plan to be shown.
	out2, _, err := runWith(t, "", writeConfig(t, dir, imageConfig("https://img.example/disk.raw.xz")))
	if err != nil || !strings.Contains(out2, "curl -fsSL") || !strings.Contains(out2, "https://img.example/disk.raw.xz "+testDisk) {
		t.Errorf("remote dry run: %v\n%s", err, out2)
	}
	// Nothing of an install is planned: no partitioning, no filesystems.
	if strings.Contains(out, "parted") || strings.Contains(out, "mkfs") {
		t.Errorf("a disk image run planned an install:\n%s", out)
	}
	if os.Geteuid() != 0 {
		_, _, err = runWith(t, "", "--apply", writeConfig(t, dir, imageConfig(image)))
		wantRunErr(t, "apply as a user", err, "--apply must run as root")
	}

	for name, tc := range map[string]struct {
		cfg  *config.Config
		args []string
		want string
	}{
		"absent image": {imageConfig(filepath.Join(dir, "absent.raw")), nil, "is not a file that can be read"},
		"not https":    {imageConfig("ftp://example.org/disk.raw"), nil, "is not an https URL or a file"},
		"plain http":   {imageConfig("http://example.org/disk.raw"), nil, "is plain http"},
		"with a layout": {func() *config.Config {
			c := plainConfig()
			c.DiskImage = image
			return c
		}(), nil, "a layout cannot be given with it"},
		"two disks": {func() *config.Config {
			c := imageConfig(image)
			c.TargetMedia = append(c.TargetMedia, &config.Media{Name: "sdb", Type: "disk"})
			return c
		}(), nil, "is written to one disk; targetMedia names 2 entries"},
		"and a rootfs image": {func() *config.Config {
			c := imageConfig(image)
			c.Image = image
			return c
		}(), nil, "not both"},
		"a path as the name": {&config.Config{DiskImage: image, TargetMedia: []*config.Media{{Name: "/dev/sda", Type: "disk"}}}, nil, "the disk needs a name"},
		"with --through":     {imageConfig(image), []string{"--through", "partition"}, "do not apply"},
	} {
		_, _, err := runWith(t, "", append(tc.args, writeConfig(t, dir, tc.cfg))...)
		wantRunErr(t, name, err, tc.want)
	}
}

// applyDiskImage runs from the namespace child (apply_ns_test.go).
func applyDiskImage(t *testing.T, confirmed string) {
	e := newApplyEnv(t, "sh", "dd", "cat")
	image := filepath.Join(e.base, "disk.raw")
	os.WriteFile(image, []byte("image"), 0o644)
	cfg := imageConfig(image)
	run := func(stdin string, extra ...string) (string, error) {
		_, errOut, err := runWith(t, stdin, append(append([]string{"--apply"}, extra...), writeConfig(t, e.base, cfg))...)
		return errOut, err
	}
	// Declined: nothing is written.
	_, err := run("yes\n")
	e.refused("disk image not confirmed", err, "aborted")
	if got := e.ran("sh"); got != "" {
		t.Errorf("the image was written without confirmation:\n%s", got)
	}
	// Confirmed: the disk is released, then written, then re-read.
	errOut, err := run(confirmed)
	if err != nil {
		t.Fatalf("writing the image: %v\n%s", err, errOut)
	}
	if !strings.Contains(e.ran("wipefs"), "-a "+testDisk) || !strings.Contains(e.ran("sh"), image+" "+testDisk) || !strings.Contains(e.ran("partprobe"), testDisk) {
		t.Errorf("wipefs %q, sh %q, partprobe %q", e.ran("wipefs"), e.ran("sh"), e.ran("partprobe"))
	}
	if !strings.Contains(errOut, "disk.raw is written to "+testDisk) {
		t.Errorf("not reported:\n%s", errOut)
	}
	// The disk is checked as any target is: in use, absent, a missing tool.
	e = newApplyEnv(t, "sh", "dd", "cat")
	setListing(t, e.bin, `{"name":"slitest0","path":"`+testDisk+`","size":1000,"type":"disk","mountpoint":"/data"}`)
	_, err = run(confirmed)
	e.refused("disk in use", err, "is in use")
	e = newApplyEnv(t, "sh", "dd", "cat")
	setListing(t, e.bin, "")
	_, err = run(confirmed)
	e.refused("disk absent", err, "is not among the machine's block devices")
	// An array shared with a disk the image does not replace: refused, as
	// any wiping install is.
	e = newApplyEnv(t, "sh", "dd", "cat")
	setListing(t, e.bin, `{"name":"slitest0","path":"`+testDisk+`","size":64000000000,"type":"disk","children":[
		{"name":"slitest0p1","path":"`+testDisk+`p1","size":1000,"type":"part","children":[
			{"name":"md0","path":"/dev/md0","size":1000,"type":"raid1"}]}]},
		{"name":"sdz","path":"/dev/sdz","size":1000,"type":"disk","children":[
		{"name":"sdz1","path":"/dev/sdz1","size":1000,"type":"part","children":[
			{"name":"md0","path":"/dev/md0","size":1000,"type":"raid1"}]}]}`)
	_, err = run(confirmed)
	e.refused("disk image over a shared array", err, "would break other disks")
	e = newApplyEnv(t, "sh", "cat")
	_, err = run(confirmed)
	e.refused("no dd", err, "missing tools on the live system: dd")
	e = newApplyEnv(t, "sh", "dd", "cat")
	cfg.DiskImage = image + ".zst"
	os.WriteFile(cfg.DiskImage, nil, 0o644)
	_, err = run(confirmed)
	e.refused("no zstd", err, "missing tools on the live system: zstd")
	e = newApplyEnv(t, "sh", "dd", "cat")
	cfg.DiskImage = "https://img.example/disk.raw"
	_, err = run(confirmed)
	e.refused("no curl", err, "missing tools on the live system: curl")
}

func TestSameDisk(t *testing.T) {
	d := func(serial string, size uint64) []blockdev.Device {
		return []blockdev.Device{{Name: "sda", Path: "/dev/sda", Type: "disk", Serial: serial, Size: size}}
	}
	if err := sameDisk(d("A", 1<<30), d("A", 1<<30), "/dev/sda"); err != nil {
		t.Error(err)
	}
	for _, after := range [][]blockdev.Device{d("B", 1<<30), d("A", 2<<30), nil} {
		if err := sameDisk(d("A", 1<<30), after, "/dev/sda"); err == nil || !strings.Contains(err.Error(), "not the disk that was confirmed") {
			t.Errorf("%v: %v", after, err)
		}
	}
}
