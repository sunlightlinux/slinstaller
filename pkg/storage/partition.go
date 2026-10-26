package storage

import (
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
)

// Commands returns the full partition-then-format command list — the
// table commands followed by the mkfs commands. It is the dry run for a
// plain install; an encrypted install runs the two halves with
// cryptsetup in between, so the controller uses TableCommands and
// MkfsCommands directly.
func (pl *Plan) Commands() ([][]string, error) {
	mkfs, err := pl.MkfsCommands()
	if err != nil {
		return nil, err
	}
	return append(pl.TableCommands(), mkfs...), nil
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

// diskTableCommands lays down one disk's partition table.
func (pl *Plan) diskTableCommands(disk string) [][]string {
	var cmds [][]string

	// mklabel destroys the existing partition table, so it is gated on
	// Wipe — the config's explicit authorisation to repartition.
	if pl.Wipe {
		cmds = append(cmds, []string{"parted", "-s", disk, "mklabel", "gpt"})
	}

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
		case p.isESP():
			cmds = append(cmds, []string{"parted", "-s", disk, "set",
				fmt.Sprintf("%d", p.Number), "esp", "on"})
		case p.IsRaidMember:
			cmds = append(cmds, []string{"parted", "-s", disk, "set",
				fmt.Sprintf("%d", p.Number), "raid", "on"})
		}
	}

	return append(cmds, []string{"partprobe", disk})
}

// physOn returns the physical partitions on disk (not logical volumes or
// RAID arrays), in on-disk (Number) order.
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
		if p.IsPV || p.IsRaidMember {
			continue
		}
		mkfs, err := mkfsCommand(p)
		if err != nil {
			return nil, err
		}
		cmds = append(cmds, mkfs)
	}
	return cmds, nil
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
	case p.isESP():
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
			return fmt.Errorf("storage: %s: %w", strings.Join(c, " "), err)
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

// VerboseRunner echoes each command and streams its output to stderr, so
// an operator watching an install sees parted, xbps and grub work rather
// than a silent pause — the base-system download in particular takes
// minutes and would otherwise look like a hang.
func VerboseRunner(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("empty command")
	}
	fmt.Fprintf(os.Stderr, "    + %s\n", strings.Join(args, " "))
	c := exec.Command(args[0], args[1:]...)
	c.Stdout = os.Stderr
	c.Stderr = os.Stderr
	return c.Run()
}

// FormatCommands renders a command list one per line, for showing a dry
// run to an operator.
func FormatCommands(cmds [][]string) string {
	var b strings.Builder
	for _, c := range cmds {
		b.WriteString(strings.Join(c, " "))
		b.WriteByte('\n')
	}
	return b.String()
}
