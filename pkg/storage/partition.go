package storage

import (
	"fmt"
	"os"
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
	// Wipe — the config's explicit authorisation to repartition.
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
		if p.Size != "" && p.Size != SizeRest {
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

	// Mark the EFI System Partition so firmware recognises it. parted's
	// esp flag sets the right GPT type; the other partitions are mounted
	// by UUID from fstab, so their GPT type is left at parted's default.
	// That keeps the whole partition step on one tool — no sgdisk to
	// install on the live medium.
	for _, p := range pl.Parts {
		if p.isESP() {
			cmds = append(cmds, []string{"parted", "-s", pl.Disk, "set",
				fmt.Sprintf("%d", p.Number), "esp", "on"})
			break
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
