package main

import (
	"os"
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
	for _, c := range diskImageCommands(devs, "/img/My Image.raw.zst", "/dev/sda") {
		got = append(got, strings.Join(c, "|"))
	}
	// What is on the disk is released first; the image and the disk reach
	// the shell as arguments, so a name with a space or a quote in it is
	// only ever a file name.
	want := []string{
		"wipefs|-a|/dev/sda1",
		"wipefs|-a|/dev/sda",
		`sh|-c|zstd -dc "$1" | dd of="$2" bs=4M conv=fsync status=none|sh|/img/My Image.raw.zst|/dev/sda`,
		"partprobe|/dev/sda",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("commands:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	for ext, reader := range map[string]string{".raw": "cat ", ".img": "cat ", ".xz": "xz -dc ", ".GZ": "gzip -dc "} {
		cmds := diskImageCommands(nil, "/img/disk"+ext, "/dev/sdb")
		if !strings.HasPrefix(cmds[0][2], reader) {
			t.Errorf("%s: %s", ext, cmds[0][2])
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
		"a URL":        {imageConfig("https://example.org/disk.raw"), nil, "is a URL; fetch the image first"},
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
	e := newApplyEnv(t, "sh", "dd")
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
	e = newApplyEnv(t, "sh", "dd")
	setListing(t, e.bin, `{"name":"slitest0","path":"`+testDisk+`","size":1000,"type":"disk","mountpoint":"/data"}`)
	_, err = run(confirmed)
	e.refused("disk in use", err, "is in use")
	e = newApplyEnv(t, "sh", "dd")
	setListing(t, e.bin, "")
	_, err = run(confirmed)
	e.refused("disk absent", err, "is not among the machine's block devices")
	e = newApplyEnv(t, "sh")
	_, err = run(confirmed)
	e.refused("no dd", err, "missing tools on the live system: dd")
	e = newApplyEnv(t, "sh", "dd")
	cfg.DiskImage = image + ".zst"
	os.WriteFile(cfg.DiskImage, nil, 0o644)
	_, err = run(confirmed)
	e.refused("no zstd", err, "missing tools on the live system: zstd")
}
