// slinstaller installs Sunlight Linux onto a disk from a descriptor.
//
// This is the file front-end: it reads a descriptor from a path,
// validates it, and either prints the partitioning plan (the default,
// which touches nothing) or, with --apply, carries it out. It is the
// thin edge of the installer — it produces a descriptor and hands it to
// the core, and does no installation work of its own.
package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/sunlightlinux/slinstaller/pkg/descriptor"
	"github.com/sunlightlinux/slinstaller/pkg/storage"
	"gopkg.in/yaml.v3"
)

// version is overridden at build time with -ldflags "-X main.version=...".
var version = "0.0.0-dev"

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "slinstaller:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("slinstaller", flag.ContinueOnError)
	var (
		apply       bool
		showVersion bool
	)
	fs.BoolVar(&apply, "apply", false,
		"carry out the plan (destructive); without it, the plan is only printed")
	fs.BoolVar(&showVersion, "version", false, "print version and exit")
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: slinstaller [--apply] <descriptor.yaml>\n\n")
		fmt.Fprintf(os.Stderr, "Prints the partitioning plan for the descriptor. With --apply,\n")
		fmt.Fprintf(os.Stderr, "partitions and formats the target disk, which destroys its contents.\n\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return err
	}

	if showVersion {
		fmt.Println("slinstaller", version)
		return nil
	}

	rest := fs.Args()
	if len(rest) != 1 {
		fs.Usage()
		return fmt.Errorf("expected exactly one descriptor file")
	}

	data, err := os.ReadFile(rest[0])
	if err != nil {
		return err
	}

	var d descriptor.Descriptor
	if err := yaml.Unmarshal(data, &d); err != nil {
		return fmt.Errorf("parsing %s: %w", rest[0], err)
	}

	// Validate before anything else: this is the last point at which
	// nothing has been touched, and it reports every problem at once.
	if err := d.Validate(); err != nil {
		return err
	}

	plan, err := storage.BuildPlan(&d)
	if err != nil {
		return err
	}
	cmds, err := plan.Commands()
	if err != nil {
		return err
	}

	// The plan is shown whether or not it will run, so --apply executes
	// exactly what a dry run printed.
	fmt.Print(storage.FormatCommands(cmds))

	if !apply {
		fmt.Fprintf(os.Stderr,
			"\nDry run. Re-run with --apply to partition %s — this destroys its contents.\n",
			plan.Disk)
		return nil
	}

	if os.Geteuid() != 0 {
		return fmt.Errorf("--apply must run as root")
	}
	if !confirm(plan.Disk) {
		return fmt.Errorf("aborted")
	}
	return storage.RunCommands(cmds, storage.DefaultRunner)
}

// confirm requires the operator to retype the target disk. A yes/no
// prompt is too easy to answer on the wrong machine; retyping the path
// makes the operator look at which disk they are about to erase.
func confirm(disk string) bool {
	fmt.Fprintf(os.Stderr, "\nAbout to PARTITION and ERASE %s.\nType the disk path to confirm: ", disk)
	sc := bufio.NewScanner(os.Stdin)
	if !sc.Scan() {
		return false
	}
	return strings.TrimSpace(sc.Text()) == disk
}
