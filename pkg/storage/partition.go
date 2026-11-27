package storage

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strings"
)

// Commands returns the full partition-then-format command list — a whole-disk
// wipe where one is asked for, the table commands, the mkfs commands and the
// btrfs subvolumes. It is the dry run for a
// plain install; an encrypted install runs the two halves with
// cryptsetup in between, so the controller uses TableCommands and
// MkfsCommands directly.
func (pl *Plan) Commands() ([][]string, error) {
	mkfs, err := pl.MkfsCommands()
	if err != nil {
		return nil, err
	}
	cmds := append(pl.WipeCommands(), pl.ResizeCommands()...)
	cmds = append(cmds, pl.TableCommands()...)
	cmds = append(cmds, pl.ArrayTableCommands()...)
	cmds = append(cmds, mkfs...)
	// Subvolumes are made inside the filesystem, mounted for the purpose;
	// shown here by the device they are made on.
	for _, p := range pl.Parts {
		if p.Preserve {
			continue
		}
		for _, sv := range p.Subvolumes {
			cmds = append(cmds, []string{"btrfs", "subvolume", "create", "[" + p.EffectiveDevice() + "]/" + sv.Name})
		}
	}
	return cmds, nil
}

// TableCommands returns the commands that lay down the partition tables:
// for each disk, label it (only when wiping), cut its partitions, mark the
// ESP and RAID members, and re-read the table. No filesystem is made here
// — on an encrypted disk, cryptsetup runs between this and the mkfs, and
// for RAID mdadm assembles the members.
func (pl *Plan) TableCommands() [][]string {
	var cmds [][]string
	for _, disk := range pl.DiskList() {
		cmds = append(cmds, pl.diskTableCommands(disk)...)
	}
	return cmds
}

// ArrayTableCommands returns the commands that partition the arrays which
// carry a table of their own. They can only run once the arrays exist.
func (pl *Plan) ArrayTableCommands() [][]string {
	var cmds [][]string
	for _, disk := range pl.RaidDisks {
		cmds = append(cmds, pl.diskTableCommands(disk)...)
	}
	return cmds
}

// diskTableCommands lays down one disk's partition table.
func (pl *Plan) diskTableCommands(disk string) [][]string {
	// A non-wiping install leaves the partition table alone, except for any
	// partition marked Create: a Safe/alongside install cuts new partitions
	// in unallocated space (at their StartMiB..EndMiB) while leaving the
	// existing partitions — and any other OS on the disk — intact. A pure
	// reuse install (no Create) emits nothing.
	if !pl.Wipe {
		var cmds [][]string
		for _, p := range pl.physOn(disk) {
			if !p.Create {
				continue
			}
			start, end := fmt.Sprintf("%dMiB", p.StartMiB), fmt.Sprintf("%dMiB", p.EndMiB)
			if p.CreateInFree { // not placed yet: this is the dry run's view
				start, end = "<start of the largest free region>", "<its end>"
			}
			cmds = append(cmds, partedMkpart(disk, p, start, end))
			if p.isESP() || p.SpareESP {
				cmds = append(cmds, []string{"parted", "-s", disk, "set",
					fmt.Sprintf("%d", p.Number), "esp", "on"})
			}
		}
		if len(cmds) > 0 {
			cmds = append(cmds, pl.entryCommands(disk, true)...)
			cmds = append(cmds, []string{"partprobe", disk})
		}
		return cmds
	}

	var cmds [][]string

	// mklabel destroys the existing partition table, so it is gated on
	// Wipe — the config's explicit authorisation to repartition.
	cmds = append(cmds, []string{"parted", "-s", disk, "mklabel", "gpt"})

	parts := pl.physOn(disk)

	// Partitions are cut front to back. The first starts at 0% so parted
	// aligns it; each subsequent one starts where the last ended; the
	// "rest" partition runs to 100%.
	cursorMiB := uint64(0)
	for i, p := range parts {
		start := fmt.Sprintf("%dMiB", cursorMiB)
		if i == 0 {
			start = "0%"
		}

		end := "100%"
		if p.Size != "" && p.Size != SizeRest {
			bytes, err := ParseSize(p.Size)
			if err == nil {
				sizeMiB := bytes >> 20
				if sizeMiB == 0 {
					sizeMiB = 1 // a partition parted will not reject
				}
				cursorMiB += sizeMiB
				end = fmt.Sprintf("%dMiB", cursorMiB)
			}
		}

		cmds = append(cmds, partedMkpart(disk, p, start, end))
	}

	// Flag the EFI System Partition (so firmware recognises it) and RAID
	// members (so the raid type is set). Other partitions are referenced by
	// UUID from fstab, so their GPT type is left at parted's default — which
	// keeps the whole partition step on one tool, no sgdisk needed.
	for _, p := range parts {
		switch {
		case (p.isESP() || p.SpareESP):
			cmds = append(cmds, []string{"parted", "-s", disk, "set",
				fmt.Sprintf("%d", p.Number), "esp", "on"})
		case p.IsRaidMember:
			cmds = append(cmds, []string{"parted", "-s", disk, "set",
				fmt.Sprintf("%d", p.Number), "raid", "on"})
		}
	}

	cmds = append(cmds, pl.entryCommands(disk, false)...)
	return append(cmds, []string{"partprobe", disk})
}

// physOn returns the physical partitions on disk (not logical volumes or
// RAID arrays), in on-disk (Number) order.
// PhysOn is the partitions parted cuts on disk, in on-disk (number) order.
func (pl *Plan) PhysOn(disk string) []Partition { return pl.physOn(disk) }

func (pl *Plan) physOn(disk string) []Partition {
	var out []Partition
	for _, p := range pl.Parts {
		if p.LV || p.Raid {
			continue
		}
		pdisk := p.Disk
		if pdisk == "" {
			pdisk = pl.Disk
		}
		if pdisk == disk {
			out = append(out, p)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Number < out[j].Number })
	return out
}

// MkfsCommands returns the mkfs command for each filesystem-bearing entry,
// on its effective device — the RAID array, the LUKS mapper for an
// encrypted partition, or the raw partition. A physical volume and a RAID
// member carry no filesystem and are skipped.
func (pl *Plan) MkfsCommands() ([][]string, error) {
	var cmds [][]string
	for _, p := range pl.Parts {
		// Nothing is made on a physical volume, a RAID member, or an array
		// that carries a partition table rather than a filesystem.
		if p.IsPV || p.IsRaidMember || (p.Raid && p.Filesystem == "") {
			continue
		}
		// A preserved partition keeps its existing filesystem and data — no
		// mkfs. It is still mounted and listed in fstab.
		if p.Preserve {
			continue
		}
		mkfs, err := mkfsCommand(p)
		if err != nil {
			return nil, err
		}
		if pl.discarded(p) {
			mkfs = noDiscard(mkfs, p)
		}
		cmds = append(cmds, mkfs)
	}
	return cmds, nil
}

// discarded reports whether p is a plain partition of a disk the install
// has just discarded whole (wipe: discard, blkdiscard).
func (pl *Plan) discarded(p Partition) bool {
	if p.Encrypted || p.LV || p.Raid {
		return false
	}
	disk := p.Disk
	if disk == "" {
		disk = pl.Disk
	}
	// Only a discard: zeroing (blkdiscard -z) writes zeros, it does not
	// trim, and an SSD then wants mkfs's own discard.
	return pl.DiskWipe[disk] == "discard"
}

// noDiscard tells mkfs not to discard the device again: mke2fs, mkfs.xfs,
// mkfs.btrfs and mkfs.f2fs discard all of it by default, a second pass
// over a disk blkdiscard has just done (tens of seconds on some USB and
// SATA bridges). An ext filesystem whose options set -E themselves is left
// as it is, rather than guess how two -E would combine.
func noDiscard(cmd []string, p Partition) []string {
	var flag []string
	switch p.Filesystem {
	case "ext2", "ext3", "ext4":
		for _, o := range p.Options {
			if strings.HasPrefix(o, "-E") {
				return cmd
			}
		}
		flag = []string{"-E", "nodiscard"}
	case "xfs", "btrfs":
		flag = []string{"-K"}
	case "f2fs":
		flag = []string{"-t", "0"}
	default:
		return cmd
	}
	dev := cmd[len(cmd)-1]
	out := append(append([]string{}, cmd[:len(cmd)-1]...), flag...)
	return append(out, dev)
}

// SizeRest is the Partition.Size value meaning "all remaining space".
// A caller that knows a partition's exact size gives it as bytes or a
// human size; one that wants a partition to fill the disk uses this.
const SizeRest = "rest"

// partedMkpart builds one mkpart command. The ESP is named EFI with an
// fat32 type, swap is named linux-swap, and anything else carries its
// mountpoint as the partition name (or "primary" when it has none).
func partedMkpart(disk string, p Partition, start, end string) []string {
	cmd := []string{"parted", "-a", "optimal", "--script", disk, "mkpart"}
	switch {
	case (p.isESP() || p.SpareESP):
		cmd = append(cmd, "EFI", "fat32")
	case p.IsRaidMember:
		cmd = append(cmd, "raid")
	case p.Filesystem == "swap":
		cmd = append(cmd, "linux-swap")
	default:
		name := p.Mountpoint
		if name == "" {
			name = "primary"
		}
		cmd = append(cmd, name)
	}
	return append(cmd, start, end)
}

// Runner executes one command, args[0] being the program. DefaultRunner
// runs it for real; tests and dry runs substitute their own.
type Runner func(args []string) error

// RunCommands runs each command in order and stops at the first that
// fails. This is the only function in the package that writes to a
// disk; call it only after the operator has confirmed the target, and
// only with a list that came from Plan.Commands.
func RunCommands(cmds [][]string, run Runner) error {
	for _, c := range cmds {
		if err := run(c); err != nil {
			// Masked: this error reaches the screen, the diagnostics
			// archive and a report-url, and the command can carry a
			// password hash (useradd -p) or proxy credentials.
			return fmt.Errorf("storage: %s: %w", ShowCommand(c), err)
		}
	}
	return nil
}

// DefaultRunner runs a command and, on failure, returns its error with
// whatever it printed, which is usually the part worth reading. It
// captures output, so nothing is shown on success — fine for tests and
// library callers, quiet for an operator. VerboseRunner is the one to
// use for an interactive install.
func DefaultRunner(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("empty command")
	}
	out, err := exec.Command(args[0], args[1:]...).CombinedOutput()
	if err != nil {
		if msg := strings.TrimSpace(string(out)); msg != "" {
			return fmt.Errorf("%w: %s", err, msg)
		}
		return err
	}
	return nil
}

// WithStderr adds what a command printed on stderr to its error, for a
// command run with Output: the exit status alone tells an operator
// nothing, the tool's own message usually does. Other errors are returned
// as they are.
func WithStderr(err error) error {
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		if msg := strings.TrimSpace(string(ee.Stderr)); msg != "" {
			return fmt.Errorf("%w: %s", err, msg)
		}
	}
	return err
}

// VerboseRunner echoes each command and streams its output to stderr, so
// an operator watching an install sees parted, xbps and grub work rather
// than a silent pause — the base-system download in particular takes
// minutes and would otherwise look like a hang.
func VerboseRunner(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("empty command")
	}
	fmt.Fprintf(os.Stderr, "    + %s\n", ShowCommand(args))
	c := exec.Command(args[0], args[1:]...)
	c.Stdout = os.Stderr
	c.Stderr = os.Stderr
	return c.Run()
}

// StreamRunner is VerboseRunner writing to w instead of stderr: each command
// and, live, its output, so a progress bar still moves on the terminal when
// w also goes to one.
func StreamRunner(w io.Writer) Runner {
	return func(args []string) error {
		if len(args) == 0 {
			return fmt.Errorf("empty command")
		}
		fmt.Fprintf(w, "    + %s\n", ShowCommand(args))
		c := exec.Command(args[0], args[1:]...)
		c.Stdout = w
		c.Stderr = w
		return c.Run()
	}
}

// cryptHashRe matches a crypt(3) password hash ($6$salt$..., $y$..., ...).
var cryptHashRe = regexp.MustCompile(`^\$(1|2[abxy]?|5|6|7|y|gy|sha1|md5)\$`)

// ShowCommand renders a command for an operator or a log, with any
// password hash in it (useradd -p) masked: the install log is kept on the
// installed system, and a hash there is one more copy to crack.
func ShowCommand(args []string) string {
	shown := make([]string, len(args))
	for i, a := range args {
		shown[i] = MaskSecret(a)
	}
	return strings.Join(shown, " ")
}

// urlPasswordRe matches the password in a URL's user information
// (https_proxy=http://user:secret@proxy:3128).
// The password runs to the last "@" before the path: a raw @, # or ? in
// it (http://u:p@ss@proxy) would otherwise be shown in part, or not at
// all.
var urlPasswordRe = regexp.MustCompile(`(://[^/?#@\s:]*):[^/\s]*@`)

// MaskSecret returns one argument or value as it may be shown or logged: a
// crypt(3) hash is replaced whole, and the password in a URL's user
// information is replaced by "***", keeping the user and the host.
func MaskSecret(a string) string {
	if cryptHashRe.MatchString(a) {
		return "<password hash>"
	}
	return urlPasswordRe.ReplaceAllString(a, "$1:***@")
}

// FormatCommands renders a command list one per line, for showing a dry
// run to an operator.
func FormatCommands(cmds [][]string) string {
	var b strings.Builder
	for _, c := range cmds {
		b.WriteString(ShowCommand(c))
		b.WriteByte('\n')
	}
	return b.String()
}

// entryCommands returns the commands that set what a config asked of a
// partition's GPT entry — its type, its GUID, its name — on the partitions
// this install makes on disk (onlyCreated: the ones cut into free space,
// when the rest of the table is not ours to touch).
func (pl *Plan) entryCommands(disk string, onlyCreated bool) [][]string {
	var cmds [][]string
	for _, p := range pl.physOn(disk) {
		if onlyCreated && !p.Create {
			continue
		}
		n := fmt.Sprintf("%d", p.Number)
		if p.PartType != "" {
			cmds = append(cmds, []string{"sfdisk", "--part-type", disk, n, p.PartType})
		}
		if p.PartUUID != "" {
			cmds = append(cmds, []string{"sfdisk", "--part-uuid", disk, n, p.PartUUID})
		}
		if p.PartName != "" {
			cmds = append(cmds, []string{"sfdisk", "--part-label", disk, n, p.PartName})
		}
	}
	return cmds
}
