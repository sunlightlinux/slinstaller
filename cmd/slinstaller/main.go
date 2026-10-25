// slinstaller installs Sunlight Linux onto a disk from a config file.
//
// This is the file front-end: it reads a config in clr-installer's
// format, validates it, and either prints the partitioning plan (the
// default, which touches nothing) or, with --apply, carries it out. It
// produces a config and hands it to the core, and does no installation
// work of its own.
package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/sunlightlinux/slinstaller/pkg/blockdev"
	"github.com/sunlightlinux/slinstaller/pkg/config"
	"github.com/sunlightlinux/slinstaller/pkg/controller"
	"github.com/sunlightlinux/slinstaller/pkg/network"
	"github.com/sunlightlinux/slinstaller/pkg/storage"
	"github.com/sunlightlinux/slinstaller/pkg/tui"
)

// version is overridden at build time with -ldflags "-X main.version=...".
var version = "0.0.0-dev"

// Base-system defaults. Sunlight has no package repository of its own
// yet, so until it does slinstaller installs from Void's official repo
// with the live medium's keys — swap these for sunlightlinux's when the
// repo exists. base-system is what void-installer lays down (it still
// pulls runit; slinit replaces it once it is in a repo we control).
const (
	defaultRepo    = "https://repo-default.voidlinux.org/current"
	defaultKeysDir = "/var/db/xbps/keys"
	// base-system is the Void base; grub-x86_64-efi is the UEFI
	// bootloader (x86_64 — override --packages for another architecture);
	// glibc-locales lets the configured locale be generated.
	defaultPackages = "base-system,grub-x86_64-efi,glibc-locales"
)

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
		through     string
		root        string
		repo        string
		keys        string
		arch        string
		pkgs        string
		showVersion bool
	)
	fs.BoolVar(&apply, "apply", false,
		"carry out the plan (destructive); without it, the plan is only printed")
	fs.StringVar(&through, "through", "",
		"with --apply, run only up to this install stage (partition, mount, meta-fs, base-install, fstab) — for VM testing")
	fs.StringVar(&root, "root", "/mnt", "with --apply, the target mount tree")
	fs.StringVar(&repo, "repo", "",
		"xbps repository for the base system (default: the config's swupdMirror, else Void's official repo)")
	fs.StringVar(&keys, "keys", defaultKeysDir, "directory of xbps signing keys to seed into the target")
	fs.StringVar(&arch, "arch", "", "XBPS_TARGET_ARCH for the base system (default: host architecture)")
	fs.StringVar(&pkgs, "packages", defaultPackages, "comma-separated base packages to install")
	fs.BoolVar(&showVersion, "version", false, "print version and exit")
	var listDisks bool
	fs.BoolVar(&listDisks, "list-disks", false, "list the machine's disks and exit")
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: slinstaller [--apply] [config.yaml]\n\n")
		fmt.Fprintf(os.Stderr, "With a config file, reads a clr-installer-format config and prints its plan;\n")
		fmt.Fprintf(os.Stderr, "--apply carries it out. With no file, runs the interactive installer.\n\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return err
	}

	if showVersion {
		fmt.Println("slinstaller", version)
		return nil
	}

	if listDisks {
		return printDisks()
	}

	// One reader for all terminal input, so the interactive front-end and
	// the later prompts don't fight over buffered stdin.
	stdin := bufio.NewReader(os.Stdin)

	rest := fs.Args()
	var cfg *config.Config
	var interactivePass string
	var err error
	switch len(rest) {
	case 1:
		cfg, err = config.Load(rest[0])
	case 0:
		// No config file: drive the interactive front-end, which builds
		// one. It installs, so it needs root.
		if os.Geteuid() != 0 {
			return fmt.Errorf("interactive install must run as root (or pass a config file for a dry run)")
		}
		cfg, interactivePass, err = tui.Run(stdin, os.Stderr)
		apply = true
	default:
		fs.Usage()
		return fmt.Errorf("expected at most one config file")
	}
	if err != nil {
		return err
	}

	// Validate before anything else: this is the last point at which
	// nothing has been touched.
	if err := cfg.Validate(); err != nil {
		return err
	}

	plan, err := cfg.Plan()
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

	// Resolve the base-system install: an explicit --repo wins, then the
	// config's swupdMirror, then Void's default repo.
	if repo == "" {
		if cfg.SwupdMirror != "" {
			repo = cfg.SwupdMirror
		} else {
			repo = defaultRepo
		}
	}
	var packages []string
	for _, p := range strings.Split(pkgs, ",") {
		if p = strings.TrimSpace(p); p != "" {
			packages = append(packages, p)
		}
	}

	opts := controller.Options{
		Root:     root,
		Through:  through,
		Repo:     repo,
		KeysDir:  keys,
		Arch:     arch,
		Packages: packages,
		// Stream progress and command output so the operator sees the
		// install work rather than a silent pause.
		Runner: storage.VerboseRunner,
		Log:    os.Stderr,
	}

	if os.Geteuid() != 0 {
		return fmt.Errorf("--apply must run as root")
	}

	// Preflight: a full install needs these on the live system. Check
	// before confirming, so a missing tool fails here rather than partway
	// through partitioning. (Staged --through runs skip this — the
	// operator steering them knows the environment.)
	encrypted := false
	for _, p := range plan.Parts {
		if p.Encrypted {
			encrypted = true
			break
		}
	}

	if through == "" {
		required := append(plan.RequiredTools(), "xbps-install", "chroot")
		if encrypted {
			required = append(required, "cryptsetup")
		}
		if missing := storage.MissingTools(required); len(missing) > 0 {
			return fmt.Errorf("missing tools on the live system: %s — install them and retry",
				strings.Join(missing, ", "))
		}

		// Confirm the target disk exists and is a whole disk before we
		// wipe it — a config written for another machine must not
		// silently repartition the wrong thing (or a partition).
		devs, err := blockdev.List()
		if err != nil {
			return err
		}
		switch d := blockdev.FindByPath(devs, plan.Disk); {
		case d == nil:
			return fmt.Errorf("target disk %s is not among the machine's block devices", plan.Disk)
		case d.Type != "disk" && d.Type != "loop":
			return fmt.Errorf("target %s is a %s, not a whole disk", plan.Disk, d.Type)
		}

		// Fetching the base system needs the repository; check it is
		// reachable before wiping, not after. Local (non-http) repos are
		// skipped.
		if strings.HasPrefix(repo, "http") {
			if err := network.VerifyConnectivity(repo); err != nil {
				return fmt.Errorf("%w — check the network/proxy, or pass --repo", err)
			}
		}
	}

	// LUKS passphrase, when the layout is encrypted. The interactive
	// front-end already collected it; otherwise take it from the
	// environment for an unattended install, or prompt.
	if encrypted {
		opts.Passphrase = interactivePass
		if opts.Passphrase == "" {
			opts.Passphrase = os.Getenv("SLINSTALLER_LUKS_PASSPHRASE")
		}
		if opts.Passphrase == "" {
			fmt.Fprint(os.Stderr, "LUKS passphrase: ")
			line, _ := stdin.ReadString('\n')
			opts.Passphrase = strings.TrimSpace(line)
		}
		if opts.Passphrase == "" {
			return fmt.Errorf("the layout is encrypted but no passphrase was given")
		}
	}

	if !confirm(stdin, plan.Disk) {
		return fmt.Errorf("aborted")
	}
	if err := controller.Install(cfg, opts); err != nil {
		return err
	}
	fmt.Fprintln(os.Stderr, "\nInstall complete.")
	if through != "" {
		fmt.Fprintf(os.Stderr, "(stopped after stage %q; target left mounted at %s)\n", through, root)
	}
	return nil
}

// printDisks lists the machine's whole disks and their partitions.
func printDisks() error {
	devs, err := blockdev.List()
	if err != nil {
		return err
	}
	disks := blockdev.Disks(devs)
	if len(disks) == 0 {
		fmt.Println("no disks found")
		return nil
	}
	for _, d := range disks {
		fmt.Printf("%-14s %9s  %s\n", d.Path, humanSize(d.Size), d.Model)
		for _, p := range d.Children {
			fmt.Printf("  %-12s %9s  %s\n", p.Path, humanSize(p.Size), p.Type)
		}
	}
	return nil
}

// humanSize formats a byte count in binary units.
func humanSize(b uint64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%dB", b)
	}
	units := []string{"K", "M", "G", "T", "P"}
	v, i := float64(b), -1
	for v >= unit && i < len(units)-1 {
		v /= unit
		i++
	}
	return fmt.Sprintf("%.1f%s", v, units[i])
}

// confirm requires the operator to retype the target disk. A yes/no
// prompt is too easy to answer on the wrong machine; retyping the path
// makes the operator look at which disk they are about to erase.
func confirm(in *bufio.Reader, disk string) bool {
	fmt.Fprintf(os.Stderr, "\nAbout to PARTITION and ERASE %s.\nType the disk path to confirm: ", disk)
	line, err := in.ReadString('\n')
	if err != nil {
		return false
	}
	return strings.TrimSpace(line) == disk
}
