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

	"github.com/sunlightlinux/slinstaller/pkg/config"
	"github.com/sunlightlinux/slinstaller/pkg/controller"
	"github.com/sunlightlinux/slinstaller/pkg/storage"
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
	// bootloader (x86_64 — override --packages for another architecture).
	defaultPackages = "base-system,grub-x86_64-efi"
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
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: slinstaller [--apply] <config.yaml>\n\n")
		fmt.Fprintf(os.Stderr, "Reads a clr-installer-format config and prints its partitioning plan.\n")
		fmt.Fprintf(os.Stderr, "With --apply, partitions and formats the target disk, destroying its contents.\n\n")
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
		return fmt.Errorf("expected exactly one config file")
	}

	cfg, err := config.Load(rest[0])
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
	if through == "" {
		required := append(plan.RequiredTools(), "xbps-install", "chroot")
		if missing := storage.MissingTools(required); len(missing) > 0 {
			return fmt.Errorf("missing tools on the live system: %s — install them and retry",
				strings.Join(missing, ", "))
		}
	}

	if !confirm(plan.Disk) {
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
