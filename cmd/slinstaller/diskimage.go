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
		// gzip's 2 is a warning ("decompression OK, trailing garbage
		// ignored"): the image came out whole.
		ok := ""
		if strings.HasPrefix(st, "gzip ") {
			ok = `[ $r = 2 ] && r=0; `
		}
		parts[i] = "{ " + st + "; r=$?; " + ok + "echo $r >&3; }"
	}
	// One status per stage must come back, all 0: a stage gone without
	// reporting counts as failed.
	return `s=$( { ` + strings.Join(parts, " | ") + `; } 3>&1 ) || exit 1
n=0; for c in $s; do n=$((n+1)); [ "$c" = 0 ] || { echo "disk-image: a step of the write failed (exit statuses: $(echo $s))" >&2; exit 1; }; done
[ "$n" = ` + fmt.Sprint(len(stages)) + ` ] || { echo "disk-image: a step of the write did not finish" >&2; exit 1; }`
}

// installDiskImage is the whole of a disk-image run: resolve the image,
// show what would be done, and — with apply — check the disk as any target
// disk is checked, have it confirmed, and write.
func installDiskImage(cfg *config.Config, cfgPath string, apply, forceDestructive bool, stdin *bufio.Reader) error {
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
		// What --apply refuses before the confirmation, said here, as the
		// install's dry run says it: not an invitation to --apply it.
		// (Still exit 0, as there: a fact of this machine, not the config.)
		if listErr == nil {
			refuses := false
			// The checks --apply makes of the target, all of them: a disk
			// missing, a partition, size 0, zram, in use.
			if err := checkTargetDisks([]string{disk}, devs); err != nil {
				fmt.Fprintf(os.Stderr, "\n(it will refuse: %v)\n", err)
				refuses = true
			}
			if shared := wouldBreak(devs, disk); len(shared) > 0 && !forceDestructive {
				fmt.Fprintf(os.Stderr, "\n(it will refuse, unless --force-destructive: writing over %s would break other disks: %s)\n", disk, strings.Join(shared, "; "))
				refuses = true
			}
			if refuses {
				fmt.Fprintln(os.Stderr, "\nDry run. As it stands, --apply would refuse this - see \"it will refuse\" above.")
				return nil
			}
		}
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
	// The same checks as any target disk: it exists, is whole, has a size
	// and is not in use — and wiping it must not break an array or volume
	// group it shares with another disk, unless forced.
	if err := checkTargetDisks([]string{disk}, devs); err != nil {
		return err
	}
	if shared := wouldBreak(devs, disk); len(shared) > 0 && !forceDestructive {
		return fmt.Errorf("writing over %s would break other disks:\n  %s\npass --force-destructive to write it anyway",
			disk, strings.Join(shared, "\n  "))
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
	// Checked again on a fresh listing, and the teardown made from it (as
	// the controller's own release does): a disk swapped, or an array
	// assembled, while the prompt waited was otherwise written over by
	// the listing from before it.
	now, err := blockdev.List()
	if err != nil {
		return err
	}
	if err := sameDisk(devs, now, disk); err != nil {
		return err
	}
	if err := checkTargetDisks([]string{disk}, now); err != nil {
		return fmt.Errorf("since the confirmation: %w; nothing was written", err)
	}
	if shared := wouldBreak(now, disk); len(shared) > 0 && !forceDestructive {
		return fmt.Errorf("since the confirmation, writing over %s would break other disks:\n  %s\nnothing was written", disk, strings.Join(shared, "\n  "))
	}
	cmds = diskImageCommands(now, image, disk, cfg.AllowInsecureHTTP)
	if err := storage.RunCommands(cmds, storage.VerboseRunner); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "\n%s is written to %s.\n", filepath.Base(image), disk)
	if action := cfg.PowerAction(); action != "" {
		return powerAfter(action, cfg.PowerStateDelay)
	}
	return nil
}

// wouldBreak is what writing over disk breaks on other disks: an array or
// volume group it shares with one (Shared, SharedMembers), or a physical
// volume whose group pvs could not be asked about (UnknownGroups).
func wouldBreak(devs []blockdev.Device, disk string) []string {
	vgs, known := blockdev.VolumeGroupsKnown()
	out := append(blockdev.Shared(devs, disk, []string{disk}, vgs), blockdev.SharedMembers(devs, []string{disk})...)
	if !known {
		out = append(out, blockdev.UnknownGroups(devs, []string{disk})...)
	}
	return out
}

// sameDisk refuses when disk, between two listings, is no longer the same
// device (its serial, WWN or size changed: swapped while a prompt waited).
func sameDisk(before, after []blockdev.Device, disk string) error {
	a, b := blockdev.FindByPath(before, disk), blockdev.FindByPath(after, disk)
	if a == nil || b == nil || a.Serial != b.Serial || a.WWN != b.WWN || a.Size != b.Size {
		return fmt.Errorf("%s is not the disk that was confirmed any more (swapped?); nothing was written", disk)
	}
	return nil
}
