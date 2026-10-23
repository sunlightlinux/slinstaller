package storage

import (
	"fmt"
	"os/exec"
	"strings"
)

// Commands returns the ordered list of commands that partition the disk
// and create its filesystems, and runs nothing: this is the dry run.
//
// The order is the order of destruction: label the disk (only when
// wiping), cut the partitions, set their GPT type codes, re-read the
// table, then make each filesystem. RunCommands runs exactly this list,
// so what an operator is shown is what executes.
func (pl *Plan) Commands() ([][]string, error) {
	var cmds [][]string

	// mklabel destroys the existing partition table, so it is gated on
	// Wipe — the descriptor's explicit authorisation to repartition.
	if pl.Wipe {
		cmds = append(cmds, []string{"parted", "-s", pl.Disk, "mklabel", "gpt"})
	}

	// Partitions are cut front to back. The first starts at 0% so parted
	// aligns it; each subsequent one starts where the last ended; the
	// "rest" partition runs to 100%.
	cursorMiB := uint64(0)
	for i, p := range pl.Parts {
		start := fmt.Sprintf("%dMiB", cursorMiB)
		if i == 0 {
			start = "0%"
		}

		end := "100%"
		if p.Size != "" && p.Size != restSize {
			bytes, err := ParseSize(p.Size)
			if err != nil {
				return nil, fmt.Errorf("storage: partition %d: %w", i, err)
			}
			sizeMiB := bytes >> 20
			if sizeMiB == 0 {
				sizeMiB = 1 // a partition parted will not reject
			}
			cursorMiB += sizeMiB
			end = fmt.Sprintf("%dMiB", cursorMiB)
		}

		cmds = append(cmds, partedMkpart(pl.Disk, p, start, end))
	}

	// GPT type codes, by partition number, for the types we recognise.
	for _, p := range pl.Parts {
		if g := p.guid(); g != "" {
			cmds = append(cmds, []string{"sgdisk", pl.Disk,
				fmt.Sprintf("--typecode=%d:%s", p.Number, g)})
		}
	}

	cmds = append(cmds, []string{"partprobe", pl.Disk})

	for _, p := range pl.Parts {
		mkfs, err := mkfsCommand(p)
		if err != nil {
			return nil, err
		}
		cmds = append(cmds, mkfs)
	}

	return cmds, nil
}

// restSize mirrors descriptor.SizeRest without importing it into every
// file; BuildPlan copies the value straight from the descriptor, so the
// literal match holds.
const restSize = "rest"

// partedMkpart builds one mkpart command. The ESP is named EFI with an
// fat32 type, swap is named linux-swap, and anything else carries its
// mountpoint as the partition name (or "primary" when it has none).
func partedMkpart(disk string, p Partition, start, end string) []string {
	cmd := []string{"parted", "-a", "optimal", "--script", disk, "mkpart"}
	switch {
	case p.isESP():
		cmd = append(cmd, "EFI", "fat32")
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
// whatever it printed, which is usually the part worth reading.
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
