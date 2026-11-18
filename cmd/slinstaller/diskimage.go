package main

import (
	"bufio"
	"fmt"
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
func diskImageCommands(devs []blockdev.Device, image, disk string) [][]string {
	var cmds [][]string
	if d := blockdev.FindByPath(devs, disk); d != nil {
		cmds = append(cmds, blockdev.Teardown(*d)...)
	}
	// A local image is read by its decompressor, or by cat. From a URL it is
	// written as it downloads; it never has to fit on the live system.
	unpack := ""
	switch strings.ToLower(filepath.Ext(image)) {
	case ".zst":
		unpack = "zstd -dc"
	case ".xz":
		unpack = "xz -dc"
	case ".gz":
		unpack = "gzip -dc"
	}
	var reader string
	switch {
	case config.IsRemote(image) && unpack != "":
		reader = `curl -fsSL "$1" | ` + unpack
	case config.IsRemote(image):
		reader = `curl -fsSL "$1"`
	case unpack != "":
		reader = unpack + ` "$1"`
	default:
		reader = `cat "$1"`
	}
	return append(cmds,
		[]string{"sh", "-c", reader + ` | dd of="$2" bs=4M conv=fsync status=none`, "sh", image, disk},
		[]string{"partprobe", disk})
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
	cmds := diskImageCommands(devs, image, disk)
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
	// What the shell command runs: the reader and any decompressor.
	for _, part := range strings.Split(cmds[len(cmds)-2][2], "|") {
		tools = append(tools, strings.Fields(part)[0])
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
