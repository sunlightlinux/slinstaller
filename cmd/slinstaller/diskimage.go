package main

import (
	"bufio"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/sunlightlinux/slinstaller/pkg/blockdev"
	"github.com/sunlightlinux/slinstaller/pkg/config"
	"github.com/sunlightlinux/slinstaller/pkg/storage"
)

// diskImageCommands returns the commands that write image onto disk: what
// is on the disk is released first (devs is the machine's listing), the
// image is written through the decompressor its name asks for, and the
// kernel re-reads the new partition table. The image and the disk are
// passed to the shell as arguments, never spliced into its command line.
func diskImageCommands(devs []blockdev.Device, image, disk string, allowInsecureHTTP bool) [][]string {
	var cmds [][]string
	if d := blockdev.FindByPath(devs, disk); d != nil {
		cmds = append(cmds, blockdev.Teardown(*d)...)
	}
	stages := diskImageStages(image, allowInsecureHTTP)
	return append(cmds,
		[]string{"sh", "-c", checkedPipeline(stages), "sh", image, disk},
		[]string{"partprobe", disk})
}

// diskImageStages are the stages of the pipeline that writes image to the
// disk ("$1" the image, "$2" the disk): the reader, any decompressor, dd.
func diskImageStages(image string, allowInsecureHTTP bool) []string {
	// A local image is read by its decompressor, or by cat. From a URL it is
	// written as it downloads; it never has to fit on the live system.
	unpack := ""
	switch imageExt(image) {
	case ".zst":
		unpack = "zstd -dc"
	case ".xz":
		unpack = "xz -dc"
	case ".gz":
		unpack = "gzip -dc"
	case ".bz2":
		unpack = "bzip2 -dc"
	}
	var stages []string
	switch {
	case config.IsRemote(image) && unpack != "":
		stages = []string{config.CurlFetch(image, allowInsecureHTTP), unpack}
	case config.IsRemote(image):
		stages = []string{config.CurlFetch(image, allowInsecureHTTP)}
	case unpack != "":
		stages = []string{unpack + ` "$1"`}
	default:
		stages = []string{`cat "$1"`}
	}
	return append(stages, `dd of="$2" bs=4M conv=fsync status=none`)
}

// imageExt is the image's compression suffix, lower-cased: of a URL's
// path, so a signed URL (img.raw.xz?sig=...) is still read as .xz rather
// than written to the disk compressed.
func imageExt(image string) string {
	if config.IsRemote(image) {
		if u, err := url.Parse(image); err == nil {
			image = u.Path
		}
	}
	return strings.ToLower(filepath.Ext(image))
}

// checkedPipeline is a shell pipeline of stages that fails when ANY stage
// fails, not only the last: sh has no portable pipefail, and the exit
// status of "curl | xz -dc | dd" is dd's alone — a download that failed or
// a truncated image was written in part and reported as written. Each
// stage reports its status on fd 3; all must be 0.
func checkedPipeline(stages []string) string {
	parts := make([]string, len(stages))
	for i, st := range stages {
		parts[i] = "{ " + st + "; echo $? >&3; }"
	}
	return `s=$( { ` + strings.Join(parts, " | ") + `; } 3>&1 ) || exit 1
for c in $s; do [ "$c" = 0 ] || { echo "disk-image: a step of the write failed (exit statuses: $(echo $s))" >&2; exit 1; }; done`
}

// installDiskImage is the whole of a disk-image run: resolve the image,
// show what would be done, and — with apply — check the disk as any target
// disk is checked, have it confirmed, and write.
func installDiskImage(cfg *config.Config, cfgPath string, apply bool, stdin *bufio.Reader) error {
	target, err := cfg.DiskImageTarget()
	if err != nil {
		return err
	}
	image := cfg.DiskImage
	remote := config.IsRemote(image)
	if !remote && !filepath.IsAbs(image) {
		dir := "."
		if cfgPath != "" {
			dir = filepath.Dir(cfgPath)
		}
		if abs, err := filepath.Abs(filepath.Join(dir, image)); err == nil {
			image = abs
		}
	}
	if fi, err := os.Stat(image); !remote && (err != nil || fi.IsDir()) {
		return fmt.Errorf("disk-image %s is not a file that can be read", image)
	}
	disk := "/dev/" + target.Name

	devs, listErr := blockdev.List()
	cmds := diskImageCommands(devs, image, disk, cfg.AllowInsecureHTTP)
	fmt.Print(storage.FormatCommands(cmds))
	if !apply {
		fmt.Fprintf(os.Stderr, "\nDry run. Re-run with --apply to write %s over %s - this destroys its contents.\n", image, disk)
		return nil
	}
	if os.Geteuid() != 0 {
		return fmt.Errorf("--apply must run as root")
	}
	lock, err := lockInstall()
	if err != nil {
		return err
	}
	defer lock.Close()
	if listErr != nil {
		return listErr
	}
	switch d := blockdev.FindByPath(devs, disk); {
	case d == nil:
		return fmt.Errorf("target disk %s is not among the machine's block devices", disk)
	case d.Type != "disk" && d.Type != "loop":
		return fmt.Errorf("target %s is a %s, not a whole disk", disk, d.Type)
	case d.InUse() != "":
		return fmt.Errorf("target disk %s is in use: %s", disk, d.InUse())
	}
	var tools []string
	for _, c := range cmds {
		tools = append(tools, c[0])
	}
	// What the shell command runs: the reader, any decompressor, dd.
	for _, st := range diskImageStages(image, cfg.AllowInsecureHTTP) {
		tools = append(tools, strings.Fields(st)[0])
	}
	if missing := storage.MissingTools(tools); len(missing) > 0 {
		return fmt.Errorf("missing tools on the live system: %s", strings.Join(missing, ", "))
	}
	if !confirm(stdin, &storage.Plan{Disk: disk, Wipe: true}) {
		return fmt.Errorf("aborted")
	}
	if err := storage.RunCommands(cmds, storage.VerboseRunner); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "\n%s is written to %s.\n", filepath.Base(image), disk)
	if action := cfg.PowerAction(); action != "" {
		return powerAfter(action, cfg.PowerStateDelay)
	}
	return nil
}
