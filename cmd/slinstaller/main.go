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
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"

	"github.com/sunlightlinux/slinstaller/pkg/blockdev"
	"github.com/sunlightlinux/slinstaller/pkg/config"
	"github.com/sunlightlinux/slinstaller/pkg/controller"
	"github.com/sunlightlinux/slinstaller/pkg/lvm"
	"github.com/sunlightlinux/slinstaller/pkg/mdadm"
	"github.com/sunlightlinux/slinstaller/pkg/network"
	"github.com/sunlightlinux/slinstaller/pkg/storage"
	"github.com/sunlightlinux/slinstaller/pkg/tui"
)

// version is overridden at build time with -ldflags "-X main.version=...".
var version = "0.0.0-dev"

// Base-system defaults. Sunlight has no remote package repository of its
// own yet, so the base comes from Void's official repo with the live
// medium's keys, and slinit from a repository the live medium carries —
// swap these for sunlightlinux's when that repo exists.
const (
	defaultRepo    = "https://repo-default.voidlinux.org/current"
	defaultKeysDir = "/var/db/xbps/keys"
	// defaultLocalRepo is where the live ISO keeps the packages no remote
	// repository has yet: slinit and slinit-services.
	defaultLocalRepo = "/usr/share/slinstaller/repo"
	// sunlight-base is Void's base-system with slinit in place of runit:
	// the same packages minus runit-void, plus slinit-services (slinit
	// and its boot services). It brings glibc-locales, so the configured
	// locale can be generated. grub-x86_64-efi is the UEFI bootloader
	// (x86_64 — override --packages for another architecture).
	defaultPackages = "sunlight-base,grub-x86_64-efi"
)

// localOnlyPackages exist only in the live medium's repository; asking
// for one without that repository cannot succeed.
var localOnlyPackages = []string{"slinit", "slinit-services", "sunlight-base"}

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
		localRepo   string
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
	fs.StringVar(&localRepo, "local-repo", defaultLocalRepo,
		"comma-separated xbps repositories on the live medium, searched before --repo (slinit comes from here); paths without a repository index are skipped")
	fs.StringVar(&keys, "keys", defaultKeysDir, "directory of xbps signing keys to seed into the target")
	fs.StringVar(&arch, "arch", "", "XBPS_TARGET_ARCH for the base system (default: host architecture)")
	fs.StringVar(&pkgs, "packages", defaultPackages, "comma-separated base packages to install")
	fs.BoolVar(&showVersion, "version", false, "print version and exit")
	var listDisks bool
	fs.BoolVar(&listDisks, "list-disks", false, "list the machine's disks and exit")
	var textUI bool
	fs.BoolVar(&textUI, "text", false,
		"use the plain text prompt installer instead of the full-screen TUI (for serial consoles)")
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
	var fullscreen bool
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
		// Full-screen wizard on a real terminal; the plain prompt flow
		// over a serial console or a pipe, where it would not render.
		if !textUI && fullScreenCapable() {
			cfg, interactivePass, err = tui.RunFullscreen()
			fullscreen = true
		} else {
			// --text, a pipe/file, a dumb/unset TERM, or a terminal that
			// cannot report its size (typically a serial console): drive the
			// plain prompt flow, which needs none of that.
			cfg, interactivePass, err = tui.Run(stdin, os.Stderr)
		}
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
	// exactly what a dry run printed. The full-screen front-end shows its
	// own progress view instead, so the plain command list is skipped there.
	if !fullscreen {
		fmt.Print(storage.FormatCommands(cmds))
	}

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
	seen := map[string]bool{}
	addPkg := func(p string) {
		if p = strings.TrimSpace(p); p != "" && !seen[p] {
			seen[p] = true
			packages = append(packages, p)
		}
	}
	for _, p := range strings.Split(pkgs, ",") {
		addPkg(p)
	}
	// The installed system needs the tools for the filesystems and
	// features the layout uses, or it cannot fsck/mount/assemble them at
	// boot. (ext4's e2fsprogs and vfat's dosfstools are in base-system.)
	for _, p := range layoutPackages(plan) {
		addPkg(p)
	}
	localRepos := presentRepos(strings.Split(localRepo, ","))

	opts := controller.Options{
		Root:       root,
		Through:    through,
		LocalRepos: localRepos,
		Repo:       repo,
		KeysDir:    keys,
		Arch:       arch,
		Packages:   packages,
		// Stream progress and command output so the operator sees the
		// install work rather than a silent pause.
		Runner: storage.VerboseRunner,
		Log:    os.Stderr,
		Output: os.Stderr,
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
		if lvm.Used(plan) {
			required = append(required, "pvcreate", "vgcreate", "lvcreate")
		}
		if mdadm.Used(plan) {
			required = append(required, "mdadm")
		}
		if missing := storage.MissingTools(required); len(missing) > 0 {
			return fmt.Errorf("missing tools on the live system: %s — install them and retry",
				strings.Join(missing, ", "))
		}

		// slinit is not in any remote repository yet. Without the live
		// medium's repository the install would fail at base-install —
		// after the disk is wiped — so fail here instead.
		if err := checkLocalPackages(packages, localRepos, localRepo); err != nil {
			return err
		}

		// Confirm each target disk exists and is a whole disk before we
		// wipe it — a config written for another machine must not
		// silently repartition the wrong thing (or a partition).
		devs, err := blockdev.List()
		if err != nil {
			return err
		}
		for _, disk := range plan.DiskList() {
			switch d := blockdev.FindByPath(devs, disk); {
			case d == nil:
				return fmt.Errorf("target disk %s is not among the machine's block devices", disk)
			case d.Type != "disk" && d.Type != "loop":
				return fmt.Errorf("target %s is a %s, not a whole disk", disk, d.Type)
			}
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

	// The full-screen front-end confirms and installs inside the interface,
	// with its own progress bar and a live command trace. The file and
	// serial paths confirm by retyping the disk path and install over plain
	// streamed text.
	if fullscreen {
		return tui.RunInstall(cfg, opts, plan)
	}

	if !confirm(stdin, plan.DiskList()) {
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

// layoutPackages is the extra target packages a layout needs so the
// installed system can handle its filesystems and features at boot.
// Names are Void's (the interim base); they move with the repo.
func layoutPackages(plan *storage.Plan) []string {
	var out []string
	add := func(p string) { out = append(out, p) }
	fs := map[string]bool{}
	for _, p := range plan.Parts {
		if p.Filesystem != "" && !fs[p.Filesystem] {
			fs[p.Filesystem] = true
			switch p.Filesystem {
			case "btrfs":
				add("btrfs-progs")
			case "xfs":
				add("xfsprogs")
			case "f2fs":
				add("f2fs-tools")
			}
		}
		if p.Encrypted {
			add("cryptsetup")
		}
		if p.IsPV || p.LV {
			add("lvm2")
		}
		if p.Raid || p.IsRaidMember {
			add("mdadm")
		}
	}
	return out
}

// isTerminal reports whether f is a terminal (not a pipe or file).
func isTerminal(f *os.File) bool {
	fi, err := f.Stat()
	return err == nil && (fi.Mode()&os.ModeCharDevice) != 0
}

// fullScreenCapable reports whether the full-screen TUI should be used.
// It is not enough that stdin/stdout are terminals: a serial console is a
// terminal too, and clr-installer's own TUI crashes on one. The reliable
// signal is whether the terminal can report a window size — a local
// terminal does, a serial console usually reports 0x0 — so here the
// full-screen UI is used only when there is a real size (and TERM is set
// and not "dumb"); everything else falls back to the prompt flow.
func fullScreenCapable() bool {
	if !isTerminal(os.Stdin) || !isTerminal(os.Stdout) {
		return false
	}
	if t := os.Getenv("TERM"); t == "" || t == "dumb" {
		return false
	}
	w, h, ok := terminalSize(os.Stdout)
	return ok && w > 0 && h > 0
}

// terminalSize returns the terminal's columns and rows, and whether they
// could be read. A serial console that never negotiated a size returns
// 0x0, which the caller treats as "no size".
func terminalSize(f *os.File) (cols, rows int, ok bool) {
	ws, err := unix.IoctlGetWinsize(int(f.Fd()), unix.TIOCGWINSZ)
	if err != nil {
		return 0, 0, false
	}
	return int(ws.Col), int(ws.Row), true
}

// confirm requires the operator to retype the target disk. A yes/no
// prompt is too easy to answer on the wrong machine; retyping the path
// makes the operator look at which disk they are about to erase. With
// several disks (a RAID layout) all are listed and the first is retyped.
func confirm(in *bufio.Reader, disks []string) bool {
	if len(disks) == 1 {
		fmt.Fprintf(os.Stderr, "\nAbout to PARTITION and ERASE %s.\nType the disk path to confirm: ", disks[0])
	} else {
		fmt.Fprintln(os.Stderr, "\nAbout to PARTITION and ERASE these disks:")
		for _, d := range disks {
			fmt.Fprintf(os.Stderr, "  %s\n", d)
		}
		fmt.Fprintf(os.Stderr, "Type the first disk path (%s) to confirm: ", disks[0])
	}
	line, err := in.ReadString('\n')
	if err != nil {
		return false
	}
	return strings.TrimSpace(line) == disks[0]
}

// presentRepos keeps the paths that hold an xbps repository index
// (<arch>-repodata), in order; anything else is skipped, so the default
// --local-repo is harmless on a system that does not carry one.
func presentRepos(paths []string) []string {
	var out []string
	for _, p := range paths {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if m, _ := filepath.Glob(filepath.Join(p, "*-repodata")); len(m) > 0 {
			out = append(out, p)
		}
	}
	return out
}

// checkLocalPackages fails when a package that only the live medium's
// repository carries is asked for and no such repository was found.
// tried is what --local-repo named, for the message.
func checkLocalPackages(packages, localRepos []string, tried string) error {
	if len(localRepos) > 0 {
		return nil
	}
	for _, p := range packages {
		for _, l := range localOnlyPackages {
			if p == l {
				return fmt.Errorf("%s is only in the live medium's repository, and none was found at %s — "+
					"pass --local-repo, or leave it out of --packages to install a runit system", p, tried)
			}
		}
	}
	return nil
}
