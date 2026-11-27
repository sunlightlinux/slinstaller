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
	"bytes"
	"context"
	"embed"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"github.com/sunlightlinux/slinstaller/pkg/blockdev"
	"github.com/sunlightlinux/slinstaller/pkg/config"
	"github.com/sunlightlinux/slinstaller/pkg/controller"
	"github.com/sunlightlinux/slinstaller/pkg/lvm"
	"github.com/sunlightlinux/slinstaller/pkg/mdadm"
	"github.com/sunlightlinux/slinstaller/pkg/network"
	"github.com/sunlightlinux/slinstaller/pkg/storage"
	"github.com/sunlightlinux/slinstaller/pkg/system"
	"github.com/sunlightlinux/slinstaller/pkg/tui"
	"github.com/sunlightlinux/slinstaller/pkg/xbps"
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
	// repository has yet: slinit, slinit-services and slpam.
	defaultLocalRepo = "/usr/share/slinstaller/repo"
	// sunlight-base is Void's base-system with slinit in place of runit:
	// the same packages minus runit-void, plus slinit-services (slinit
	// and its boot services). It brings glibc-locales, so the configured
	// locale can be generated. grub-x86_64-efi is the UEFI bootloader
	// (x86_64 — override --packages for another architecture). slpam is
	// pam_slinit.so, which registers logins with slinit-logind; the
	// live medium's pam-base names it in system-login.
	defaultPackages = "sunlight-base,grub-x86_64-efi,slpam"

	// defaultExtraRepo is the Sunlight package mirror, layered ahead of Void
	// (see --extra-repo). Signed (its key is embedded below) and fetched over
	// https: the signature guards what a package is, https also which
	// version is current — over http a stale but correctly signed index
	// could be served.
	defaultExtraRepo = "https://repo-default.ionutnechita.ro/current"

	// sunlightKeyFile is the Sunlight repo key's xbps filename (its RSA
	// fingerprint). Seeded into the target so the mirror's signatures verify.
	sunlightKeyFile = "d8:42:05:80:77:16:f2:b7:20:a0:d9:60:35:00:6d:5a.plist"
)

// sunlightKeyFS carries the Sunlight mirror's public signing key, so an
// install verifies what it pulls from the mirror without relying on the
// live medium already trusting it.
//
//go:embed keys/sunlight-repo.plist
var sunlightKeyFS embed.FS

// localOnlyPackages exist only in the live medium's repository; asking
// for one without that repository cannot succeed.
var localOnlyPackages = []string{"slinit", "slinit-services", "sunlight-base", "slpam"}

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
		extraRepo   string
		keys        string
		arch        string
		pkgs        string
		showVersion bool
	)
	fs.BoolVar(&apply, "apply", false,
		"carry out the plan (destructive); without it, the plan is only printed")
	fs.StringVar(&through, "through", "",
		"with --apply, run only up to this install stage: "+strings.Join(controller.StageNames(), ", ")+" - for VM testing")
	fs.StringVar(&root, "root", "/mnt", "with --apply, the target mount tree")
	fs.StringVar(&repo, "repo", "",
		"xbps repository for the base system (default: the config's swupdMirror, else Void's official repo)")
	fs.StringVar(&localRepo, "local-repo", defaultLocalRepo,
		"comma-separated xbps repositories on the live medium, searched before --repo (slinit comes from here); paths without a repository index are skipped")
	fs.StringVar(&extraRepo, "extra-repo", defaultExtraRepo,
		"comma-separated remote xbps repositories searched ahead of --repo (defaults to the Sunlight mirror, layered over Void); signature-checked, so their key must be in --keys (the Sunlight key is built in)")
	fs.StringVar(&keys, "keys", defaultKeysDir, "directory of xbps signing keys to seed into the target")
	fs.StringVar(&arch, "arch", "", "XBPS_TARGET_ARCH for the base system (default: host architecture)")
	fs.StringVar(&pkgs, "packages", defaultPackages, "comma-separated base packages to install")
	fs.BoolVar(&showVersion, "version", false, "print version and exit")
	var completion string
	fs.StringVar(&completion, "completion", "", "print a shell completion script for bash or fish, and exit")
	var listDisks bool
	fs.BoolVar(&listDisks, "list-disks", false, "list the machine's disks and exit")
	var textUI bool
	fs.BoolVar(&textUI, "text", false,
		"use the plain text prompt installer instead of the full-screen TUI (for serial consoles)")
	var offline bool
	fs.BoolVar(&offline, "offline", false,
		"install only from --local-repo, without a remote mirror (same as the config's offline)")
	var skipSize, skipAll bool
	fs.BoolVar(&skipSize, "skip-validation-size", false,
		"skip the minimum-size and disk-fit checks (same as the config's skipValidationSize)")
	fs.BoolVar(&skipAll, "skip-validation-all", false,
		"skip config validation, all but the safety checks (same as the config's skipValidationAll); use with care")
	var forceDestructive bool
	fs.BoolVar(&forceDestructive, "force-destructive", false,
		"wipe a target disk even when a RAID array or LVM volume group on it also uses another disk, which that breaks (clr-installer's flag)")
	var reboot bool
	fs.BoolVar(&reboot, "reboot", false,
		"reboot into the installed system when the install finishes (same as the config's postReboot)")
	var logFile string
	fs.StringVar(&logFile, "log", "",
		"also write the install's output to this file (for debugging an unattended or serial install)")

	// clr-installer's options, under its names, so a command line written
	// for it runs here too.
	var configPath string
	fs.StringVar(&configPath, "config", "", "the config file (the same as giving it as the argument)")
	fs.StringVar(&configPath, "c", "", "short for --config")
	var cryptFile string
	fs.StringVar(&cryptFile, "crypt-file", "", "read the LUKS passphrase from this file")
	var genpass string
	fs.StringVar(&genpass, "genpass", "",
		"print the crypt hash of this password for a config's users, and exit; \"-\" reads it without echo")
	var aliases aliasFlag
	fs.Var(&aliases, "block-device", "name:file, a storage alias for the config's targetMedia (repeatable); replaces the config's alias of that name")
	fs.Var(&aliases, "b", "short for --block-device")
	var copyNetwork, allowInsecure bool
	fs.BoolVar(&copyNetwork, "copy-network", false, "copy the live system's network configuration into the installed system (the config's copyNetwork)")
	fs.BoolVar(&allowInsecure, "allow-insecure-http", false, "allow plain http:// for the repository, a config, image or report URL, and https-to-http redirects (the config's allowInsecureHTTP)")
	var swapFileSize string
	fs.StringVar(&swapFileSize, "swap-file-size", "", "create a swap file of this size, e.g. 2G (the config's swapFileSize)")
	var logLevel int
	fs.IntVar(&logLevel, "log-level", 3,
		"how much of the install is shown on the terminal: 5 (verbose), 4 (debug) and 3 (info) show every command and its output, 2 (warning) and 1 (error) only the stages and the result; others are clamped to 1-5; the log file always has everything (clr-installer's levels)")
	fs.IntVar(&logLevel, "l", 3, "short for --log-level")
	var resume bool
	fs.BoolVar(&resume, "resume", false,
		"with --apply, continue an install that failed while installing the base system: nothing is partitioned or formatted again; after a reboot of the live medium the encrypted volumes, arrays and volume groups are brought back first (the passphrase is asked once)")
	var resumeFrom string
	fs.StringVar(&resumeFrom, "resume-from", "",
		"as --resume, but from the named stage: "+strings.Join(controller.ResumableStages(), ", ")+" (the stage is run again from its start)")
	var jsonYAML string
	fs.StringVar(&jsonYAML, "json-yaml", "",
		"convert this old JSON install descriptor (.json) into a config written beside it (.yaml), and exit (clr-installer's option)")
	fs.StringVar(&jsonYAML, "j", "", "short for --json-yaml")
	var messageURL string
	fs.StringVar(&messageURL, "message-url", "",
		"look for installer messages (pre-install-msg.yaml, post-install-msg.yaml) under this URL and show them (the config's message-url)")
	var unmount bool
	fs.BoolVar(&unmount, "unmount", false,
		"unmount everything under --root - what a staged install (--through) leaves mounted - and exit")
	var inTargetCmd string
	fs.StringVar(&inTargetCmd, "in-target", "",
		"run this shell command inside the mounted target at --root, and exit (after a staged install)")
	var reportURL string
	fs.StringVar(&reportURL, "report-url", "",
		"POST the install's progress to this URL as JSON, stage by stage (the config's report-url)")
	var image string
	fs.StringVar(&image, "image", "",
		"install the base system by unpacking this root filesystem image (tar or squashfs) instead of installing packages (the config's image)")
	var discover string
	fs.StringVar(&discover, "discover", "",
		"print a config describing the layout already on this disk (e.g. /dev/sda), as a starting point for an install into its partitions, and exit")
	var systemCheck bool
	fs.BoolVar(&systemCheck, "system-check", false,
		"check that this machine can install the config - tools, disks, repository - and exit without touching anything")
	var diagnostics string
	fs.StringVar(&diagnostics, "diagnostics", "",
		"where a failed install saves its diagnostics archive (default: a dated .tar.gz in /tmp)")
	var template string
	fs.StringVar(&template, "template", "", "write a template config to this .yaml file, and exit")
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: slinstaller [--apply] [config.yaml]\n\n")
		fmt.Fprintf(os.Stderr, "With a config file, reads a clr-installer-format config and prints its plan;\n")
		fmt.Fprintf(os.Stderr, "--apply carries it out. With no file, runs the interactive installer.\n\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		// -h/--help printed the usage, as asked: that is not a failure.
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}

	if completion != "" {
		return printCompletion(os.Stdout, fs, completion)
	}

	// The target tree is mounted on and unmounted under: never the live
	// root — `--unmount --root /` detached the running system's / — and
	// never a path relative to wherever the installer was started.
	if !filepath.IsAbs(root) || filepath.Clean(root) == "/" {
		return fmt.Errorf("--root %q must be an absolute path other than / (e.g. /mnt)", root)
	}
	root = filepath.Clean(root)
	// Nor a directory of the live system: --unmount --root /dev detached
	// the live /dev's mounts, and an install would mount over them.
	for _, sys := range []string{"/dev", "/proc", "/sys", "/run", "/boot", "/etc", "/usr", "/var", "/home", "/root", "/bin", "/sbin", "/lib", "/lib64", "/.snapshots"} {
		if root == sys || strings.HasPrefix(root, sys+"/") {
			return fmt.Errorf("--root %s is in the live system's %s; use a directory of its own, e.g. /mnt", root, sys)
		}
	}
	// These themselves are the live system's (--unmount --root /tmp took
	// down the live /tmp); a directory made under one is the operator's.
	for _, sys := range []string{"/tmp", "/opt", "/srv", "/media"} {
		if root == sys {
			return fmt.Errorf("--root %s is the live system's own %s; use a directory of its own, e.g. /mnt", root, sys)
		}
	}
	// A foreign architecture installs packages whose configure scripts
	// cannot run in the chroot here (xbps-reconfigure, the bootloader):
	// a full install failed at base-install, after the wipe. Only a staged
	// run, which stops before them, may ask for one.
	if apply && arch != "" && through == "" && !strings.HasPrefix(arch, hostXBPSArch()) {
		return fmt.Errorf("--arch %s is not this machine's (%s): its packages cannot be configured here; use it only with --through", arch, hostXBPSArch())
	}

	// clr-installer's levels are 1 to 5 (5 verbose, shown here as 4) and
	// it clamps anything else with a warning; so does this.
	if logLevel < 1 || logLevel > 5 {
		clamped := min(max(logLevel, 1), 5)
		fmt.Fprintf(os.Stderr, "warning: --log-level %d is not 1 (error) to 5 (verbose); using %d\n", logLevel, clamped)
		logLevel = clamped
	}

	// Stage names are known now: a misspelt one was found only once the
	// disk path had been retyped, or not at all in a dry run.
	for flagName, stage := range map[string]string{"--through": through, "--resume-from": resumeFrom} {
		if stage == "" {
			continue
		}
		known := controller.StageNames()
		if flagName == "--resume-from" {
			known = controller.ResumableStages()
		}
		if !slices.Contains(known, stage) {
			if flagName == "--resume-from" {
				return fmt.Errorf("--resume-from: an install cannot be resumed at %q; it can be from %s", stage, strings.Join(known, ", "))
			}
			return fmt.Errorf("%s %q is not a stage; it is one of %s", flagName, stage, strings.Join(known, ", "))
		}
	}

	if showVersion {
		fmt.Println("slinstaller", version)
		return nil
	}

	// The modes that do one thing and exit: two given, all but one were
	// dropped without a word (--discover with --json-yaml converted
	// nothing).
	var modes []string
	for name, on := range map[string]bool{"--list-disks": listDisks, "--genpass": genpass != "", "--template": template != "",
		"--json-yaml": jsonYAML != "", "--discover": discover != "", "--unmount": unmount, "--in-target": inTargetCmd != ""} {
		if on {
			modes = append(modes, name)
		}
	}
	if len(modes) > 1 {
		sort.Strings(modes)
		return fmt.Errorf("%s are mutually exclusive: each does one thing and exits", strings.Join(modes, " and "))
	}

	if listDisks {
		return printDisks()
	}

	if genpass != "" {
		pw := genpass
		if pw == "-" {
			in := bufio.NewReader(os.Stdin)
			pw = tui.ReadSecret(in, os.Stderr, "Password")
			if pw == "" {
				return fmt.Errorf("--genpass -: no password was read")
			}
			// Typed at a terminal, unseen: asked twice, or a typo became a
			// hash nobody can log in with. (Piped, it is read once.)
			if isTerminal(os.Stdin) {
				if again := tui.ReadSecret(in, os.Stderr, "Password again"); again != pw {
					return fmt.Errorf("--genpass -: the passwords do not match")
				}
			}
		}
		if err := config.ValidPassword(pw); err != nil {
			return err
		}
		hash, err := tui.CryptHash(pw)
		if err != nil {
			return err
		}
		fmt.Println(hash)
		return nil
	}

	if template != "" {
		return writeTemplate(template)
	}

	if discover != "" {
		return printDiscovered(os.Stdout, os.Stderr, discover)
	}

	if jsonYAML != "" {
		return convertDescriptor(jsonYAML)
	}
	if unmount {
		return unmountTarget(root)
	}
	if inTargetCmd != "" {
		return inTarget(root, inTargetCmd)
	}

	// One reader for all terminal input, so the interactive front-end and
	// the later prompts don't fight over buffered stdin.
	stdin := bufio.NewReader(os.Stdin)

	rest := fs.Args()
	if configPath != "" {
		if len(rest) > 0 {
			return fmt.Errorf("give the config either with --config or as the argument, not both")
		}
		rest = []string{configPath}
	}
	// A config given as a URL is fetched once, here; from then on it is a
	// file like any other.
	if len(rest) == 1 && isURL(rest[0]) {
		path, err := fetchConfig(rest[0], allowInsecure)
		if err != nil {
			return err
		}
		defer os.RemoveAll(filepath.Dir(path))
		rest[0] = path
	}
	if resumeFrom != "" {
		resume = true
	} else if resume {
		resumeFrom = "base-install"
	}
	if resume && (!apply || len(rest) == 0 || through != "") {
		return fmt.Errorf("--resume continues a full install from a config file: use it with --apply and the config, without --through")
	}
	if systemCheck && len(rest) == 0 {
		return fmt.Errorf("--system-check needs the config to check the machine against")
	}
	// repoFlag preserves the original --repo so the full-screen "Back to
	// installer" loop below can re-resolve it from a re-run wizard each time.
	repoFlag := repo
	var cfg *config.Config
	var interactivePass string
	var fullscreen bool
	var err error
	// Held from the first step that may touch a disk until the install ends.
	var lock *os.File
	defer func() {
		if lock != nil {
			lock.Close()
		}
	}()
restart:
	repo = repoFlag
	switch len(rest) {
	case 1:
		cfg, err = config.LoadWith(rest[0], aliases)
	case 0:
		// No config file: drive the interactive front-end, which builds
		// one. It installs, so it needs root.
		if os.Geteuid() != 0 {
			return fmt.Errorf("interactive install must run as root (or pass a config file for a dry run)")
		}
		// Before the questions, not after them: the answers were thrown away.
		if arch != "" && !strings.HasPrefix(arch, hostXBPSArch()) {
			return fmt.Errorf("--arch %s is not this machine's (%s): its packages cannot be configured here", arch, hostXBPSArch())
		}
		// Full-screen wizard on a real terminal; the plain prompt flow
		// over a serial console or a pipe, where it would not render.
		if !textUI && fullScreenCapable() {
			cfg, interactivePass, err = tui.RunFullscreen(skipSize, skipAll)
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
		for _, a := range rest[1:] {
			if strings.HasPrefix(a, "-") {
				return fmt.Errorf("%s comes after the config file; flags go before it", a)
			}
		}
		return fmt.Errorf("expected at most one config file")
	}
	if err != nil {
		return err
	}

	// A key the config carries that nothing reads is most likely a typo
	// (encryptd: true installs unencrypted). Said before anything is
	// checked: a misspelt mountpoint otherwise showed only as "no
	// partition mounts /", the typo itself never named.
	for _, k := range cfg.UnknownKeys() {
		fmt.Fprintf(os.Stderr, "warning: config key %s is not one slinstaller knows; it is ignored\n", k)
	}

	// Disks named by serial, WWN, model or a stable path become the kernel
	// names they have on this machine, before anything is checked or shown.
	if cfg.NeedsDisks() {
		devs, err := blockdev.List()
		if err != nil {
			return fmt.Errorf("cannot list the disks to resolve the config's disk matches: %w", err)
		}
		var disks []config.DiskInfo
		for _, d := range devs {
			if d.Type == "disk" || d.Type == "loop" {
				disks = append(disks, config.DiskInfo{Name: d.Name, Path: d.Path, Serial: d.Serial, WWN: d.WWN, Model: d.Model})
			}
		}
		if err := cfg.ResolveDisks(disks); err != nil {
			return err
		}
		for _, m := range cfg.TargetMedia {
			if m != nil && m.Match != nil {
				fmt.Fprintf(os.Stderr, "disk with %s is /dev/%s\n", m.Match, m.Name)
			}
		}
	}

	// --offline is the CLI equivalent of the config's offline: install only
	// from the local repository, never the network.
	if offline {
		cfg.Offline = true
	}
	// The validation-skip flags OR with the config's, so either source can
	// turn a check off.
	if skipSize {
		cfg.SkipValidationSize = true
	}
	if skipAll {
		cfg.SkipValidationAll = true
	}
	// --reboot is the CLI equivalent of the config's postReboot.
	if reboot {
		cfg.PostReboot = true
	}
	// clr-installer's flags for config fields, likewise.
	if copyNetwork {
		cfg.CopyNetwork = true
	}
	if allowInsecure {
		cfg.AllowInsecureHTTP = true
	}
	if swapFileSize != "" {
		cfg.SwapFileSize = swapFileSize
	}
	if image != "" {
		// A flag's relative path is the shell's, from the current directory
		// (the config's own image: is read beside the config, below): it was
		// looked up beside the config, and a file of that name there was
		// installed instead without a word.
		if !config.IsRemote(image) && !filepath.IsAbs(image) {
			if abs, err := filepath.Abs(image); err == nil {
				image = abs
			}
		}
		cfg.Image = image
	}
	if reportURL != "" {
		cfg.ReportURL = reportURL
	}
	if messageURL != "" {
		cfg.MessageURL = messageURL
	}

	// The config's proxy is for everything this run fetches, the
	// reachability check before the install included.
	for _, kv := range cfg.ProxyEnv() {
		k, v, _ := strings.Cut(kv, "=")
		os.Setenv(k, v)
		os.Setenv(strings.ToUpper(k), v)
	}

	// A swap sized "auto" is sized for this machine's memory.
	cfg.ResolveAutoSwap(system.MemTotalBytes())

	// Validate before anything else: this is the last point at which
	// nothing has been touched.
	if err := cfg.Validate(); err != nil {
		return err
	}

	// A whole-disk image is not an install: there is no layout to plan and
	// nothing to configure. It has its own, much shorter, path.
	if cfg.DiskImage != "" {
		if fullscreen || len(rest) == 0 {
			return fmt.Errorf("disk-image comes from a config file")
		}
		if through != "" || resume || systemCheck {
			return fmt.Errorf("disk-image writes one image and is done: --through, --resume and --system-check do not apply")
		}
		return installDiskImage(cfg, rest[0], apply, forceDestructive, stdin)
	}

	// Catch a mistyped timezone, keymap or language here, before anything is wiped,
	// but only when the live system actually has the data to check against
	// (so a minimal medium that lacks it does not reject a valid value).
	if !cfg.SkipValidationAll {
		if err := checkLocale(cfg.Timezone, cfg.Keyboard, cfg.Language); err != nil {
			return err
		}
	}

	plan, err := cfg.Plan()
	if err != nil {
		return err
	}
	// What the partition stage runs, LUKS, RAID and LVM included: the
	// disk's own commands alone left pvcreate, mdadm --create and
	// luksFormat out of what the operator read before --apply.
	cmds, err := controller.PartitionCommands(plan)
	if err != nil {
		return err
	}

	// The plan is shown whether or not it will run, so --apply executes
	// exactly what a dry run printed. The full-screen front-end shows its
	// own progress view instead, so the plain command list is skipped there.
	refuses := false // the dry run has said the install would be refused
	if !fullscreen {
		// A resume runs no partition stage: its commands, and what it
		// releases first, shown as the plan, were followed by "nothing is
		// partitioned or formatted". (A resume is never a dry run, which
		// is all refuses is for.)
		if !resume {
			fmt.Print(storage.FormatCommands(cmds))
			refuses = printRelease(plan, forceDestructive)
		}
		// A Safe install's new partitions are checked against the disk's
		// table when it runs; the dry run says now what it would refuse,
		// rather than show a mkfs of an existing partition and leave it at
		// that.
		if !plan.Wipe && !apply {
			if err := plan.CheckCreateNumbers(); err != nil && strings.Contains(err.Error(), "is to be created") {
				fmt.Fprintf(os.Stderr, "\n(it will refuse: %v)\n", err)
				refuses = true
			}
		}
	}
	// And again, in short, where the plan ends, so it is in view when the
	// operator confirms.
	if keys := cfg.UnknownKeys(); len(keys) > 0 {
		fmt.Fprintf(os.Stderr, "warning: ignored, not keys slinstaller knows (see above): %s\n", strings.Join(keys, ", "))
	}

	if !apply && !systemCheck {
		// Not an invitation to --apply what it has just said will be
		// refused. (Still exit 0: whether a disk is in use is a fact of
		// the machine the dry run runs on, not of the config.)
		if refuses {
			fmt.Fprintln(os.Stderr, "\nDry run. As it stands, --apply would refuse this - see \"it will refuse\" above.")
			return nil
		}
		if plan.Wipe {
			fmt.Fprintf(os.Stderr,
				"\nDry run. Re-run with --apply to partition %s - this destroys its contents.\n",
				plan.Disk)
		} else {
			fmt.Fprintf(os.Stderr,
				"\nDry run (reusePartitions). Re-run with --apply to format the listed partitions on %s - the table is kept, but formatted partitions lose their data.\n",
				plan.Disk)
		}
		return nil
	}

	// Resolve the base-system install: an explicit --repo wins, then the
	// config's swupdMirror, then Void's default repo.
	if repo == "" {
		if cfg.SwupdMirror != "" {
			repo = cfg.SwupdMirror
		} else if !cfg.Offline {
			repo = defaultRepo
		}
	}
	// An offline install must not reach the network: drop a remote (http)
	// repo so the base comes only from the live medium's local repository.
	if cfg.Offline && strings.HasPrefix(repo, "http") {
		repo = ""
	}
	// Extra remote repositories (the Sunlight mirror). Dropped offline, like
	// the remote repo above — they too would reach the network.
	// targetRepos are the mirrors the installed system updates from, kept
	// whatever this install itself fetches from: an offline or image install
	// left them out of the target's xbps.d, and its first update then saw
	// Void alone.
	var extraRepos, targetRepos []string
	for _, r := range strings.Split(extraRepo, ",") {
		if r = strings.TrimSpace(r); r != "" {
			targetRepos = append(targetRepos, r)
		}
	}
	if !cfg.Offline {
		extraRepos = append(extraRepos, targetRepos...)
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
	if cfg.Bootloader != nil && cfg.Bootloader.ProbeOtherOS {
		addPkg("os-prober")
	}
	// Bonds, VLANs and bridges are made at boot with ip(8).
	for _, ni := range cfg.NetworkInterfaces {
		if ni != nil && (ni.Bond != nil || ni.VLAN != nil || ni.Bridge != nil) {
			addPkg("iproute2")
			break
		}
	}
	// The configured kernel (clr's kernel-native/kernel-lts → linux/linux-lts)
	// and any extra packages the config asks for.
	addPkg(cfg.KernelPackage())
	for _, p := range cfg.Packages {
		addPkg(p)
	}
	localRepos := presentRepos(strings.Split(localRepo, ","))

	// From an image nothing is fetched or installed as packages: the image
	// is the base system. What the layout would have added has to be in it.
	fromImage := cfg.Image != ""
	if fromImage && !config.IsRemote(cfg.Image) {
		if !filepath.IsAbs(cfg.Image) {
			dir := "."
			if len(rest) == 1 {
				dir = filepath.Dir(rest[0])
			}
			if abs, err := filepath.Abs(filepath.Join(dir, cfg.Image)); err == nil {
				cfg.Image = abs
			}
		}
		if fi, err := os.Stat(cfg.Image); err != nil || fi.IsDir() {
			return fmt.Errorf("image %s is not a file that can be read", cfg.Image)
		}
		if underRoot(cfg.Image, root) {
			return fmt.Errorf("image %s is under the target root %s and would be hidden when the target is mounted there", cfg.Image, root)
		}
	}
	if fromImage {
		if need := append(layoutPackages(plan), cfg.Packages...); len(need) > 0 {
			fmt.Fprintf(os.Stderr, "note: installing from an image, no packages are installed; the image has to hold: %s\n", strings.Join(need, ", "))
		}
		repo, extraRepos, packages = "", nil, nil
	}

	// A local repository under the target root would be shadowed the moment
	// the target filesystem is mounted there: xbps would then read an empty
	// directory and the base install would fail (the classic "mounted the 9p
	// share at /mnt/share, installed to /mnt" mistake). Catch it before any
	// disk is touched and say where to move the repo.
	for _, r := range localRepos {
		if underRoot(r, root) {
			return fmt.Errorf("local repository %s is under the target root %s and would be hidden when the target is mounted there - move it outside %s (e.g. mount the share at /run/slshare)", r, root, root)
		}
	}

	// Everything the install prints — the command trace, stage headers and
	// sub-command output — also goes to a log file, which a completed install
	// copies to the target as the full-screen one does: --log's file, or
	// the default one. The full-screen front-end keeps its own. Without a
	// default file the install still runs; a --log that cannot be written is
	// an error, as it was asked for.
	logOut := io.Writer(os.Stderr)
	runner := storage.VerboseRunner
	logPath := logFile
	if logPath == "" && !fullscreen && !systemCheck {
		logPath = tui.InstallLogPath()
	}
	// A system check writes nothing to the log: a --log given with it was
	// emptied for nothing.
	if systemCheck {
		logPath = ""
	}
	// Refused before the log is opened (and the lock taken, below): a run
	// that cannot go on leaves no "new run" mark in a failed install's log.
	// --arch again, now that the wizard or the text flow may have made this
	// an install (the early check saw no --apply) and for --system-check,
	// which said "passed" for what --apply refuses.
	if arch != "" && through == "" && !strings.HasPrefix(arch, hostXBPSArch()) {
		return fmt.Errorf("--arch %s is not this machine's (%s): its packages cannot be configured here; use it only with --through", arch, hostXBPSArch())
	}
	if !systemCheck && geteuid() != 0 {
		return fmt.Errorf("--apply must run as root")
	}
	// And the lock before it too: a second run beside a running install
	// opened (and wrote into) that install's log before the lock refused it.
	if !systemCheck && lock == nil {
		if lock, err = lockInstall(); err != nil {
			return err
		}
	}
	var quiet io.Writer // set when the commands' output goes to the file alone
	if logPath != "" {
		var lf *os.File
		var err error
		if logFile == "" {
			lf, err = tui.OpenInstallLog(logPath) // the default, in /tmp
		} else {
			// Appended to, after a mark: not emptied by a run that its
			// checks then refuse.
			lf, err = os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
			if err == nil {
				if fi, serr := lf.Stat(); serr == nil && fi.Size() > 0 {
					fmt.Fprintf(lf, "\n==== slinstaller, a new run: %s ====\n", time.Now().Format(time.RFC3339))
				}
			}
		}
		switch {
		case err == nil:
			defer lf.Close()
			logOut = io.MultiWriter(os.Stderr, tui.LogWriter(lf))
			if logLevel <= 2 {
				// Quiet: the commands and their output go to the file
				// alone; the terminal keeps the stage headers.
				quiet = tui.LogWriter(lf)
				runner = storage.StreamRunner(quiet)
				break
			}
			runner = storage.StreamRunner(logOut)
		case logFile != "":
			return fmt.Errorf("--log: %w", err)
		default:
			logPath = ""
			fmt.Fprintf(os.Stderr, "warning: no install log file: %v\n", err)
		}
	}

	// Seed the Sunlight mirror's signing key next to the live medium's keys,
	// so the install verifies what it pulls from the mirror. A temp dir,
	// because the live keys directory may be read-only.
	if merged, err := seedSunlightKey(keys); err != nil {
		return fmt.Errorf("seed the Sunlight repo key: %w", err)
	} else {
		defer os.RemoveAll(merged)
		keys = merged
	}

	opts := controller.Options{
		Root:             root,
		Through:          through,
		LocalRepos:       localRepos,
		ExtraRepos:       extraRepos,
		TargetRepos:      targetRepos,
		Repo:             repo,
		KeysDir:          keys,
		Arch:             arch,
		Packages:         packages,
		ForceDestructive: forceDestructive,
		// Stream progress and command output so the operator sees the
		// install work rather than a silent pause.
		Runner:     runner,
		Log:        logOut,
		Output:     logOut,
		ResumeFrom: resumeFrom,

		LogFile: logPath,
	}
	if len(rest) == 1 {
		if abs, err := filepath.Abs(rest[0]); err == nil {
			opts.ConfigDir = filepath.Dir(abs)
		}
	}
	if quiet != nil {
		opts.Output = quiet
	}
	report := newReporter(cfg.ReportURL, cfg.Hostname, cfg.AllowInsecureHTTP)
	if report != nil {
		opts.Progress = report.stage
	}

	if !systemCheck {
		if geteuid() != 0 {
			return fmt.Errorf("--apply must run as root")
		}
		if lock == nil {
			if lock, err = lockInstall(); err != nil {
				return err
			}
		}
	}

	// A profile's vendor-specific arguments, CPU lists and hugepages belong
	// to a machine, not to a config: make them right for this one before
	// they are checked and installed.
	refitKernelArgs(cfg, os.Stderr)

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

	// A plain-http remote repo is refused unless the config opts into it,
	// staged run or not (it sat with the checks --through skips): the base
	// is fetched before the disk is anything to salvage, so a
	// man-in-the-middle swap of the mirror is worth guarding against.
	// https and local repositories are always allowed.
	if strings.HasPrefix(repo, "http://") && !cfg.AllowInsecureHTTP {
		return fmt.Errorf("repository %s is plain http; set allowInsecureHTTP (--allow-insecure-http), or use an https mirror", repo)
	}
	for _, r := range extraRepos { // the mirrors too
		if strings.HasPrefix(r, "http://") && !cfg.AllowInsecureHTTP {
			return fmt.Errorf("--extra-repo %s is plain http; set allowInsecureHTTP (--allow-insecure-http), or use https", r)
		}
	}

	if through == "" {
		// UEFI-only: a legacy/BIOS boot of the installer has no
		// /sys/firmware/efi, so grub-install/efibootmgr would fail at the
		// bootloader stage — after the disk is wiped. Fail here instead.
		if !system.IsUEFI() {
			return fmt.Errorf("the installer medium is not booted in UEFI mode (%s is absent); Sunlight Linux is UEFI-only - reboot it in UEFI mode",
				"/sys/firmware/efi")
		}

		// An offline install has no remote repo, so the base must come from a
		// local repository on the live medium. Fail here, before wiping, if
		// there is none.
		if cfg.Offline && len(localRepos) == 0 && !fromImage && reachesBaseInstall(through, resumeFrom) {
			return fmt.Errorf("offline install needs a local repository (--local-repo), but none is present")
		}

		// Isolation CPU lists and hugepage counts are computed for one
		// machine and saved literally, so a config written elsewhere can
		// name CPUs this machine lacks or reserve more memory than it has —
		// which shows up only at the first boot, as a system that does not
		// come up. The installer always runs on the target, so check here.
		if !cfg.SkipValidationAll && cfg.KernelArguments != nil {
			if probs := system.CheckKernelArgs(cfg.KernelArguments.Add); len(probs) > 0 {
				return fmt.Errorf("kernel-arguments do not fit this machine (were they saved on another one?):\n  - %s\nfix them, re-run the wizard here, or set skipValidationAll to install anyway",
					strings.Join(probs, "\n  - "))
			}
		}

		required := append(plan.RequiredTools(), "chroot")
		if cfg.PowerAction() == "kexec" {
			required = append(required, "kexec")
		}
		if fromImage {
			required = append(required, controller.ImageTools(cfg.Image)...)
		} else {
			required = append(required, "xbps-install")
		}
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
			return fmt.Errorf("missing tools on the live system: %s - install them and retry",
				strings.Join(missing, ", "))
		}

		// The Sunlight packages (slinit, sunlight-base, …) come from the live
		// medium's repository or from the Sunlight mirror. With neither, the
		// install would fail at base-install — after the disk is wiped — so
		// fail here instead. (The mirror is checked to be reachable below.)
		// (Only for a run that installs packages: a resume past base-install
		// fetches nothing, and was refused — or kept waiting a minute — on
		// a network it no longer needs.)
		fetches := reachesBaseInstall(through, resumeFrom)
		if len(extraRepos) == 0 && fetches {
			if err := checkLocalPackages(packages, localRepos, localRepo); err != nil {
				return err
			}
		}

		// Fetching the base system needs the repository; check it is
		// reachable before wiping, not after. Local (non-http) repos are
		// skipped.
		if strings.HasPrefix(repo, "http") && fetches {
			waiting := func() { fmt.Fprintln(os.Stderr, "waiting for the network...") }
			if err := network.WaitForConnectivity(repo, time.Minute, waiting); err != nil {
				return fmt.Errorf("%w - check the network/proxy, or pass --repo", err)
			}
		}
		// The mirrors too: xbps drops a repository it cannot reach without
		// a word, and the install then took dbus and the rest from Void
		// alone — the skew the mirror is there to prevent.
		for _, r := range extraRepos {
			if strings.HasPrefix(r, "http") && fetches {
				waiting := func() { fmt.Fprintf(os.Stderr, "waiting for the Sunlight mirror %s...\n", r) }
				if err := network.WaitForConnectivity(r, time.Minute, waiting); err != nil {
					return fmt.Errorf("%w - the Sunlight mirror is needed; check the network/proxy, or pass --extra-repo", err)
				}
			}
		}

		// And that the package set resolves from them: a typo in packages,
		// a kernel no repository carries, an offline repository missing
		// part of the base all failed at base-install, after the wipe.
		if reachesBaseInstall(through, resumeFrom) && !fromImage && !cfg.SkipValidationAll {
			if err := resolvePackages(opts, cfg); err != nil {
				return err
			}
		}
	}

	// Every install, staged or not, puts an unsigned GRUB on the disk.
	if system.SecureBoot() {
		fmt.Fprintln(os.Stderr, "warning: Secure Boot is on: the firmware will not start the unsigned GRUB this installs; turn it off in the firmware setup before rebooting")
	}

	// The disk checks run on every install, staged or not: a --through run
	// cuts and formats the disk like a full one, and skipping these with the
	// tool checks above let it take a partition, a missing path, a mounted
	// disk or a disk sharing an array with another for its target. (A
	// resume runs them too, as it always has; it cannot be staged.)
	{
		// Confirm each target disk exists and is a whole disk before we
		// wipe it — a config written for another machine must not
		// silently repartition the wrong thing (or a partition).
		devs, err := blockdev.List()
		if err != nil {
			return err
		}
		if err := checkTargetDisks(plan.DiskList(), devs); err != nil {
			return err
		}

		if plan.Wipe {
			// Wiping a disk that shares a RAID array or volume group with a
			// disk the install does not wipe breaks that one too.
			vgs := blockdev.VolumeGroups()
			var shared []string
			for _, disk := range plan.DiskList() {
				shared = append(shared, blockdev.Shared(devs, disk, plan.DiskList(), vgs)...)
			}
			shared = append(shared, blockdev.SharedMembers(devs, plan.DiskList())...)
			if len(shared) > 0 && !forceDestructive {
				return fmt.Errorf("wiping %s would break other disks:\n  %s\npass --force-destructive to wipe it anyway",
					strings.Join(plan.DiskList(), ", "), strings.Join(shared, "\n  "))
			}
			// A group this install creates must not have its name taken by
			// one the wipe leaves: vgcreate would fail after the wipe.
			if clash := blockdev.VGNameClashes(devs, vgs, plan.VGNames(), plan.DiskList()); len(clash) > 0 {
				return fmt.Errorf("the layout's volume groups clash with groups already on the machine:\n  %s\nname this install's group differently (volumeGroup:)",
					strings.Join(clash, "\n  "))
			}
			// The tools that release the disks before partitioning.
			var tools []string
			for _, disk := range plan.DiskList() {
				if d := blockdev.FindByPath(devs, disk); d != nil {
					for _, c := range blockdev.Teardown(*d) {
						tools = append(tools, c[0])
					}
				}
			}
			if missing := storage.MissingTools(tools); len(missing) > 0 {
				return fmt.Errorf("missing tools on the live system to release the target disk: %s",
					strings.Join(missing, ", "))
			}
		}

		// Reuse install: the reused partitions are not created here, so they
		// must already exist on the disk. A Safe/alongside install's Create
		// partitions are cut in free space, so they are expected NOT to exist.
		if !plan.Wipe {
			// A partition formatted here that is one member of an array or
			// group with members elsewhere breaks it (Shared is for wiped
			// disks; this is the reuse install's own).
			// A shrunk one too: a btrfs resize without a devid resizes devid
			// 1 — on another disk, for a filesystem spanning one — and the
			// partition was then cut under its data.
			var formatted []string
			for _, p := range plan.Parts {
				if !p.LV && !p.Raid && !p.Create && (!p.Preserve || p.ResizeMiB > 0) {
					formatted = append(formatted, p.Device)
				}
			}
			shared := blockdev.SharedMembers(devs, formatted)
			// And an LVM physical volume whose group has others (lsblk's
			// UUID for a PV is its own; pvs knows the groups).
			for vg, pvs := range blockdev.VolumeGroups() {
				// The PVs this install does not format or shrink: only
				// those are harmed (a group wholly reformatted is not).
				others := slices.DeleteFunc(slices.Clone(pvs), func(x string) bool { return slices.Contains(formatted, x) })
				for _, f := range formatted {
					if slices.Contains(pvs, f) && len(others) > 0 {
						shared = append(shared, fmt.Sprintf("%s is a physical volume of %s, which also has %s", f, vg, strings.Join(others, ", ")))
					}
				}
			}
			if len(shared) > 0 && !forceDestructive {
				return fmt.Errorf("formatting or shrinking %s would break what it is a member of:\n  %s\npass --force-destructive to go on anyway",
					strings.Join(formatted, ", "), strings.Join(shared, "\n  "))
			}
			for _, p := range plan.Parts {
				if p.LV || p.Raid || p.Create {
					continue
				}
				d := blockdev.FindByPath(devs, p.Device)
				if d == nil {
					return fmt.Errorf("reusePartitions: %s does not exist - create the partitions first, or drop reusePartitions to repartition", p.Device)
				}
				if d.Type != "part" {
					return fmt.Errorf("reusePartitions: %s is a %s, not a partition", p.Device, d.Type)
				}
				// What is kept has to be what the config says it is: a config
				// written for another disk, or before the disk was changed,
				// must not have its partitions taken for these.
				if p.Preserve && p.Filesystem != "" && d.FsType != p.Filesystem {
					return fmt.Errorf("reusePartitions: %s is to be kept as %s, but holds %s - the config does not describe this disk (see --discover)",
						p.Device, p.Filesystem, orDash(d.FsType))
				}
				if p.ResizeMiB == 0 {
					continue
				}
				// A resize only ever shrinks. (That the partition holds what
				// the config says, and so something that can be shrunk, was
				// checked just above.)
				// Resuming, the first attempt shrank it already (a resume
				// never resizes): at its new size it is not refused.
				if p.ResizeMiB<<20 >= d.Size && !((resume || resumeFrom != "") && p.ResizeMiB<<20 == d.Size) {
					return fmt.Errorf("resize: %s is %s; resizing it to %s would not shrink it", p.Device, humanSize(d.Size), humanSize(p.ResizeMiB<<20))
				}
			}
		}

		// Disk fit: checked before wiping so an oversize layout fails here
		// rather than partway through parted. Skipped for a reuse install
		// (nothing is cut) and when size validation is off.
		if plan.Wipe && !cfg.SkipValidationSize && !cfg.SkipValidationAll {
			rootMin, rootMinLabel := cfg.RootMinimum()
			if err := checkDiskFit(plan, devs, rootMin, rootMinLabel); err != nil {
				return err
			}
		}

	}

	if systemCheck {
		if through != "" {
			return fmt.Errorf("--system-check checks a full install; drop --through")
		}
		fmt.Fprintln(os.Stderr, "\nSystem check passed: this machine can install this config. Nothing was changed.")
		return nil
	}

	// LUKS passphrase, when the layout is encrypted. The interactive
	// front-end already collected it; otherwise take it from --crypt-file
	// or the environment for an unattended install, or prompt.
	// Read once and gone, encrypted layout or not: left set, every child
	// inherited it — xbps-install, the package scripts xbps-reconfigure
	// runs in the target, the config's hooks.
	envPassphrase := os.Getenv("SLINSTALLER_LUKS_PASSPHRASE")
	os.Unsetenv("SLINSTALLER_LUKS_PASSPHRASE")
	if encrypted {
		opts.Passphrase = interactivePass
		if opts.Passphrase == "" && cryptFile != "" {
			data, err := os.ReadFile(cryptFile)
			if err != nil {
				return fmt.Errorf("--crypt-file: %w", err)
			}
			// The file's own trailing newline is not part of the passphrase.
			opts.Passphrase = strings.TrimRight(string(data), "\r\n")
		}
		if opts.Passphrase == "" {
			opts.Passphrase = envPassphrase
		}
		// An unattended passphrase is not refused (cryptsetup takes it),
		// but what the prompts would refuse is said: one GRUB's console
		// cannot type (non-ASCII, over 94 characters) locks an encrypted
		// /boot out at the first boot.
		if opts.Passphrase != "" && interactivePass == "" {
			if err := config.ValidPassphrase(opts.Passphrase); err != nil {
				fmt.Fprintf(os.Stderr, "warning: the LUKS passphrase is used as given, but the prompts would refuse it (GRUB may not be able to take it at boot): %v\n", err)
			}
		}

		if opts.Passphrase == "" {
			// Read it with echo off so the passphrase is not shown on screen,
			// and on a terminal twice, as a typo here locks the disk. A pipe
			// gives it once, as it always has.
			// A resume opens volumes that exist, with the passphrase they
			// have: asked once, not as a new one to be typed twice.
			if isTerminal(os.Stdin) && !resume {
				if opts.Passphrase, err = tui.PromptNewPassphrase(stdin, os.Stderr); err != nil {
					return err
				}
			} else {
				opts.Passphrase = tui.ReadSecret(stdin, os.Stderr, "LUKS passphrase")
			}
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
		next, rerr := tui.RunInstall(cfg, opts, plan)
		// Reported first — the result and, on a failure, the diagnostics —
		// then what the operator chose on the final screen.
		if rerr == nil || rerr.Error() != "aborted" {
			report.finish(rerr)
		}
		if rerr != nil && rerr.Error() != "aborted" {
			reportFailure(diagnostics, cfg, tui.InstallLogPath(), rerr)
		}
		switch next {
		case "back":
			// Re-run the wizard and everything downstream of it.
			fmt.Fprintln(os.Stderr, "\nReturning to the configuration menu...")
			goto restart
		case "reboot", "poweroff":
			if err := powerCommand(next); err != nil && rerr == nil {
				return err
			}
		}
		return rerr
	}

	// A resumed install erases nothing: it mounts what the first attempt
	// made and carries on, so there is nothing to confirm by retyping.
	showMessage(cfg.MessageURL, preInstallMessage)
	if resume {
		fmt.Fprintf(os.Stderr, "\nResuming on %s from %s: nothing is partitioned or formatted.\n",
			strings.Join(plan.DiskList(), ", "), resumeFrom)
	} else if !confirm(stdin, plan) {
		return fmt.Errorf("aborted")
	}
	err = installStoppable(cfg, opts)
	report.finish(err)
	if err != nil {
		reportFailure(diagnostics, cfg, logPath, err)
		return err
	}
	fmt.Fprintln(os.Stderr, "\nInstall complete.")
	if through == "" {
		showMessage(cfg.MessageURL, postInstallMessage)
	}
	if through != "" {
		fmt.Fprintf(os.Stderr, "(stopped after stage %q; target left mounted at %s)\n", through, root)
		return nil
	}
	// Not with postArchive: false, which keeps it off the target. (A copy
	// that failed was warned about where it failed.)
	if logPath != "" && (cfg.PostArchive == nil || *cfg.PostArchive) {
		fmt.Fprintln(os.Stderr, "The install log is saved in /root/slinstaller.log on the installed system.")
	}
	// postReboot: an unattended install that should come up into the new
	// system reboots once the full run has finished (not a staged --through).
	if action := cfg.PowerAction(); action != "" {
		return powerAfter(action, cfg.PowerStateDelay)
	}
	return nil
}

// powerSleep waits before a power action (a variable so tests need not).
var powerSleep = time.Sleep

// powerAfter announces a power action, waits the configured delay, and
// carries it out.
func powerAfter(action string, delay int) error {
	if delay > 0 {
		fmt.Fprintf(os.Stderr, "install finished: %s in %d seconds (Ctrl+C to stay)...\n", action, delay)
		powerSleep(time.Duration(delay) * time.Second)
	} else {
		fmt.Fprintf(os.Stderr, "install finished: %s...\n", action)
	}
	return powerCommand(action)
}

// installStoppable runs the install with SIGINT and SIGTERM turned into a
// request to stop after the current stage. Left to their default, Ctrl+C
// killed the installer between two commands: the target stayed mounted,
// the encrypted volumes open, and no diagnostics were written. (Ctrl+C on
// the terminal also reaches the command running, which then fails; the
// stage fails with it and the usual cleanup runs.)
func installStoppable(cfg *config.Config, opts controller.Options) error {
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sigs)
	stop := make(chan struct{})
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-sigs:
			// The stop first, then the word of it: said first, the stage
			// after could begin in between (the test that waits for the
			// word saw the mount stage start).
			close(stop)
			fmt.Fprintln(os.Stderr, "\nInterrupted: winding the install down after this step (a Ctrl+C on the terminal stops the running command too). Ctrl+C again leaves now.")
		case <-done:
			return
		}
		// A second one is the way out of a step that hangs, which the stop
		// between steps would wait on for ever.
		select {
		case <-sigs:
			fmt.Fprintln(os.Stderr, "\nLeaving now; the step running is abandoned and the target may still be mounted.")
			// A Ctrl+C on the terminal reaches the commands too; a SIGTERM
			// sent to the installer does not, and they went on writing.
			tui.AbandonChildren()
			os.Exit(130)
		case <-done:
		}
	}()
	opts.Stop = stop
	return controller.Install(cfg, opts)
}

// checkTargetDisks confirms each target disk exists and is a whole disk
// before it is wiped: a config written for another machine must not
// silently repartition the wrong thing (or a partition).
func checkTargetDisks(disks []string, devs []blockdev.Device) error {
	for _, disk := range disks {
		switch d := blockdev.FindByPath(devs, disk); {
		case d == nil:
			return fmt.Errorf("target disk %s is not among the machine's block devices", disk)
		case d.Type != "disk" && d.Type != "loop":
			return fmt.Errorf("target %s is a %s, not a whole disk", disk, d.Type)
		case d.Size == 0:
			// An empty card reader, a detached loop device: nothing to
			// install to, and no size for the fit check to go by.
			return fmt.Errorf("target disk %s has no capacity (size 0): is there a medium in it?", disk)
		case d.Type == "disk" && !blockdev.Installable(*d):
			// zram, an eMMC's boot or RPMB area: not a disk a system goes on.
			return fmt.Errorf("target %s is not a disk a system can be installed to (zram, or an eMMC boot area)", disk)
		default:
			// A mounted filesystem, active swap or the live medium on
			// the target: installing would pull it from under the
			// running system. No flag overrides this.
			if why := d.InUse(); why != "" {
				return fmt.Errorf("target disk %s is in use: %s", disk, why)
			}
		}
	}
	return nil
}

// checkDiskFit checks that each disk holds its fixed-size partitions plus
// the GPT overhead, and leaves the partition that fills the rest at least
// its minimum (rootMin for a root).
func checkDiskFit(plan *storage.Plan, devs []blockdev.Device, rootMin uint64, rootMinLabel string) error {
	return plan.CheckFit(func(disk string) (uint64, bool) {
		d := blockdev.FindByPath(devs, disk)
		if d == nil {
			return 0, false // refused by checkTargetDisks
		}
		return d.Size, true
	}, rootMin, rootMinLabel)
}

// reachesBaseInstall says whether a fresh run with these flags installs
// packages: not a resumed one past it, nor one stopped before it.
func reachesBaseInstall(through, resumeFrom string) bool {
	if resumeFrom != "" && resumeFrom != "base-install" {
		return false
	}
	if through == "" {
		return true
	}
	for _, s := range controller.StageNames() {
		if s == "base-install" {
			return true
		}
		if s == through {
			return false
		}
	}
	return true
}

// resolveRun runs the dry run's commands and returns their output; a
// variable so tests stand it in.
var resolveRun = func(cmds [][]string) (string, error) {
	var out bytes.Buffer
	for _, c := range cmds {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		cmd := exec.CommandContext(ctx, c[0], c[1:]...)
		cmd.Stdout, cmd.Stderr = &out, &out
		err := cmd.Run()
		cancel()
		if err != nil {
			return out.String(), err
		}
	}
	return out.String(), nil
}

// resolvePackages dry-runs the base install in a scratch root, before the
// disk is touched. It refuses only when xbps says the package set cannot be
// had; a dry run that fails otherwise (xbps missing here, the network
// flapping) is a warning, not a verdict. With kernel-fallback, a kernel
// that is not found is retried as the fallback, as the install does.
func resolvePackages(opts controller.Options, cfg *config.Config) error {
	if _, err := exec.LookPath("xbps-install"); err != nil {
		return nil // not a live system: nothing to resolve with
	}
	try := func(packages []string) ([]string, error) {
		scratch, err := os.MkdirTemp("", "slinstaller-resolve-")
		if err != nil {
			return nil, err
		}
		defer os.RemoveAll(scratch)
		cmds, err := xbps.Options{
			Repo: opts.Repo, LocalRepos: opts.LocalRepos, ExtraRepos: opts.ExtraRepos,
			KeysDir: opts.KeysDir, Arch: opts.Arch, ProxyEnv: cfg.ProxyEnv(), Packages: packages,
		}.ResolveCommands(scratch)
		if err != nil {
			return nil, err
		}
		out, err := resolveRun(cmds)
		if err == nil {
			return nil, nil
		}
		if bad := xbps.Unresolved(out); len(bad) > 0 {
			return bad, nil
		}
		return nil, err
	}
	fmt.Fprintln(os.Stderr, "checking that the packages resolve...")
	bad, err := try(opts.Packages)
	if kernel, fallback := cfg.KernelPackage(), cfg.KernelFallback; len(bad) > 0 && kernel != "" && fallback != "" {
		swapped := make([]string, len(opts.Packages))
		for i, p := range opts.Packages {
			if p == kernel {
				p = fallback
			}
			swapped[i] = p
		}
		if bad2, err2 := try(swapped); len(bad2) == 0 && err2 == nil {
			fmt.Fprintf(os.Stderr, "note: %s does not resolve; the install will use the fallback %s\n", kernel, fallback)
			return nil
		}
	}
	switch {
	case len(bad) > 0:
		// xbps drops a repository it could not sync without a word, so a
		// package it carries reads as "not found": say so.
		return fmt.Errorf("the packages do not resolve from the repositories (nothing was touched):\n  %s\n(if a repository was unreachable just now, its packages read as not found: retry)", strings.Join(bad, "\n  "))
	case err != nil:
		fmt.Fprintf(os.Stderr, "warning: could not check that the packages resolve (%v); going on\n", err)
	}
	return nil
}

// stdinIsTerminal says whether answers are being typed; a variable for tests.
var stdinIsTerminal = func() bool { return isTerminal(os.Stdin) }

// geteuid is os.Geteuid; a variable so tests reach the checks after it.
var geteuid = os.Geteuid

// hostXBPSArch is this machine's architecture as xbps names it.
func hostXBPSArch() string { return xbpsArch(runtime.GOARCH) }

// xbpsArch is the xbps name of a Go architecture (GOARCH); one xbps does
// not name otherwise is the same.
func xbpsArch(goarch string) string {
	switch goarch {
	case "amd64":
		return "x86_64"
	case "arm64":
		return "aarch64"
	case "386":
		return "i686"
	}
	return goarch
}

// checkLocale rejects a timezone, keymap or language the live system does
// not know. An empty one is not checked: it means "leave the target's
// default", as system.Timezone, system.Keymap and system.Locale apply it, and the serial text flow
// never asks for either. Checking "" as a name refused every such install
// before it began. A variable so tests do not depend on what data the
// machine running them has.
var checkLocale = system.CheckLocale

// powerCommand reboots, powers off or halts the machine, for the config's
// postReboot and power-state. The action is the command's name.
func powerCommand(action string) error {
	cmd := exec.Command(action)
	if action == "kexec" {
		// Start the kernel the install loaded. What is on disk is synced
		// first: kexec does not go through a shutdown.
		unix.Sync()
		cmd = exec.Command("kexec", "-e")
	}
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s: %w", action, err)
	}
	return nil
}

// printRelease shows what a wiping install will release on its disks
// before partitioning (blockdev.Teardown), as the controller finds them
// now. It is read from the live system, so it is shown apart from the
// plan's own commands.
func printRelease(plan *storage.Plan, force bool) (refuses bool) {
	devs, err := blockdev.List()
	if err != nil {
		return false
	}
	// A reuse install releases nothing, but a disk in use is refused all
	// the same (checkTargetDisks): said here too, rather than "Re-run with
	// --apply" over a config --apply refuses.
	if !plan.Wipe {
		for _, disk := range plan.DiskList() {
			if d := blockdev.FindByPath(devs, disk); d != nil {
				if why := d.InUse(); why != "" {
					fmt.Fprintf(os.Stderr, "\n(it will refuse: %s is in use: %s)\n", disk, why)
					refuses = true
				}
			}
		}
		return refuses
	}
	var disks []blockdev.Device
	var names []string
	for _, disk := range plan.DiskList() {
		if d := blockdev.FindByPath(devs, disk); d != nil {
			disks, names = append(disks, *d), append(names, disk)
		}
	}
	if len(disks) == 0 {
		return
	}
	// What the partition stage runs: one teardown for all the disks.
	fmt.Fprintf(os.Stderr, "\nBefore partitioning %s, the install first runs:\n", strings.Join(names, ", "))
	for _, c := range blockdev.TeardownAll(disks) {
		fmt.Fprintf(os.Stderr, "  %s\n", storage.ShowCommand(c))
	}
	for _, d := range disks {
		if why := d.InUse(); why != "" {
			fmt.Fprintf(os.Stderr, "  (it will refuse: %s)\n", why)
			refuses = true
		}
	}
	// What --apply refuses before the confirmation, said here: the dry run
	// otherwise invited an --apply that then refused.
	vgs := blockdev.VolumeGroups()
	var shared []string
	for _, disk := range plan.DiskList() {
		shared = append(shared, blockdev.Shared(devs, disk, plan.DiskList(), vgs)...)
	}
	shared = append(shared, blockdev.SharedMembers(devs, plan.DiskList())...)
	if len(shared) > 0 && !force {
		fmt.Fprintf(os.Stderr, "  (it will refuse, unless --force-destructive: wiping would break other disks: %s)\n", strings.Join(shared, "; "))
		refuses = true
	}
	for _, c := range blockdev.VGNameClashes(devs, vgs, plan.VGNames(), plan.DiskList()) {
		fmt.Fprintf(os.Stderr, "  (it will refuse: %s; name this install's group differently)\n", c)
		refuses = true
	}
	return refuses
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
		if why := d.InUse(); why != "" {
			fmt.Printf("  (not installable: %s)\n", why)
		} else if !blockdev.Installable(d) {
			fmt.Printf("  (not installable: no room for a system - zram, an eMMC boot area or an empty reader)\n")
		}
		for _, p := range d.Children {
			fmt.Printf("  %-12s %9s  %s\n", p.Path, humanSize(p.Size), p.Type)
		}
	}
	return nil
}

// refitKernelArgs makes a saved config's kernel arguments right for the
// machine being installed. A wizard profile adapts its arguments to the
// hardware — IOMMU, nested KVM and the P-State mode to the CPU vendor, and,
// for the profiles that isolate CPUs, the isolcpus=/nohz_full=/rcu_nocbs=/
// irqaffinity= lists and the hugepage count to the CPUs and memory — and the
// saved config holds the result for the machine the wizard ran on. When
// kernel-profile names a profile, those arguments are recomputed for this
// machine and the change is reported to w; every other argument is left as
// written. A config with kernel-profile Custom, or none, is not touched: its
// arguments are the operator's own.
func refitKernelArgs(cfg *config.Config, w io.Writer) {
	isolate, hugepages, known := tui.KernelProfileIsolation(cfg.KernelProfile)
	if !known {
		return
	}
	var before []string
	if cfg.KernelArguments != nil {
		before = cfg.KernelArguments.Add
	}
	after := system.RefitVendor(before, system.CPUVendor())
	if isolate {
		after, _, _ = system.RefitIsolation(after, hugepages)
	}
	if strings.Join(before, " ") == strings.Join(after, " ") {
		return
	}
	if cfg.KernelArguments == nil {
		cfg.KernelArguments = &config.KernelArguments{}
	}
	cfg.KernelArguments.Add = after

	// Report only what differs, not the whole command line.
	only := func(in, notIn []string) string {
		seen := map[string]bool{}
		for _, a := range notIn {
			seen[a] = true
		}
		var out []string
		for _, a := range in {
			if !seen[a] {
				out = append(out, a)
			}
		}
		if len(out) == 0 {
			return "(none)"
		}
		return strings.Join(out, " ")
	}
	fmt.Fprintf(w, "\nkernel-profile %s: kernel arguments adapted to this machine\n  was: %s\n  now: %s\n",
		cfg.KernelProfile, only(before, after), only(after, before))
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

// holdsNow says what a partition about to be formatted holds at present, so
// the operator sees what is erased and not only what replaces it.
func holdsNow(devs []blockdev.Device, path string) string {
	d := blockdev.FindByPath(devs, path)
	if d == nil || d.FsType == "" {
		return ""
	}
	if d.Label != "" {
		return fmt.Sprintf("  (now %s, labelled %q)", d.FsType, d.Label)
	}
	return fmt.Sprintf("  (now %s)", d.FsType)
}

// confirm requires the operator to retype the target disk. A yes/no
// prompt is too easy to answer on the wrong machine; retyping the path
// makes the operator look at which disk they are about to erase. With
// several disks (a RAID layout) all are listed and the first is retyped.
// describeDisk is a disk as the confirmation names it: its path, with its
// size and model when the listing has them, so that the operator retyping
// the path sees which disk it is (two alike differ by size, a virtio disk
// has no model).
func describeDisk(path string) string {
	devs, _ := blockdev.List()
	d := blockdev.FindByPath(devs, path)
	if d == nil || d.Size == 0 {
		return path
	}
	if d.Model != "" {
		return fmt.Sprintf("%s (%s, %s)", path, humanSize(d.Size), d.Model)
	}
	return fmt.Sprintf("%s (%s)", path, humanSize(d.Size))
}

func confirm(in *bufio.Reader, plan *storage.Plan) bool {
	disks := plan.DiskList()
	if !plan.Wipe {
		// Reuse install: the partition table is untouched; only the listed
		// partitions are formatted (and the preserved ones mounted as-is).
		fmt.Fprintln(os.Stderr, "\nReusing the existing partitions (the disk is NOT repartitioned).")
		devs, _ := blockdev.List() // best effort: only to say what is there now
		for _, p := range plan.Parts {
			if p.LV || p.Raid {
				continue
			}
			if p.ResizeMiB > 0 {
				fmt.Fprintf(os.Stderr, "  SHRINK %s to %s (its data is kept)\n", p.Device, humanSize(p.ResizeMiB<<20))
			}
			if p.Create {
				fmt.Fprintf(os.Stderr, "  CREATE %s as %s -> %s (in unallocated space)\n", p.Device, p.Filesystem, orDash(p.Mountpoint))
				continue
			}
			if p.Preserve {
				fmt.Fprintf(os.Stderr, "  KEEP   %s -> %s (not formatted)\n", p.Device, orDash(p.Mountpoint))
			} else {
				fmt.Fprintf(os.Stderr, "  FORMAT %s as %s -> %s%s\n", p.Device, p.Filesystem, orDash(p.Mountpoint), holdsNow(devs, p.Device))
			}
		}
		for _, p := range plan.Parts {
			if p.ResizeMiB > 0 {
				fmt.Fprintln(os.Stderr, "A SHRINK that is interrupted can lose what is on the partition - have a backup.")
				break
			}
		}
		fmt.Fprintf(os.Stderr, "This ERASES the partitions marked FORMAT. Type the disk path (%s) to confirm: ", disks[0])
	} else if len(disks) == 1 {
		fmt.Fprintf(os.Stderr, "\nAbout to PARTITION and ERASE %s.\nType the disk path to confirm: ", describeDisk(disks[0]))
	} else {
		fmt.Fprintln(os.Stderr, "\nAbout to PARTITION and ERASE these disks:")
		for _, d := range disks {
			fmt.Fprintf(os.Stderr, "  %s\n", describeDisk(d))
		}
		fmt.Fprintf(os.Stderr, "Type the first disk path (%s) to confirm: ", disks[0])
	}
	// A mismatch says what was wanted and asks again, three tries in all:
	// "sda" for "/dev/sda" ended the run, throwing away every answer of the
	// text flow, passwords included. An ended input still aborts.
	// Retries only for someone typing: piped answers that are off by one
	// would otherwise confirm with whichever later line names the disk.
	tries := 1
	if stdinIsTerminal() {
		tries = 3
	}
	for try := 1; ; try++ {
		line, err := in.ReadString('\n')
		got := strings.TrimSpace(line)
		if got == disks[0] && err == nil { // cut off before Enter is not typed
			return true
		}
		if err != nil || got == "" || try >= tries {
			return false
		}
		fmt.Fprintf(os.Stderr, "%q is not %s - type it exactly (an empty line aborts): ", got, disks[0])
	}
}

// orDash renders an empty mountpoint as a dash.
func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// underRoot reports whether path sits inside the target root — the repo
// would be hidden once the target filesystem is mounted at root. http(s)
// repositories and a root of "/" (nothing is meaningfully "under" it) are
// never under. Paths are compared cleaned and absolute.
func underRoot(path, root string) bool {
	if strings.HasPrefix(path, "http://") || strings.HasPrefix(path, "https://") {
		return false
	}
	root = filepath.Clean(root)
	if root == "/" || root == "." {
		return false
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	return abs == root || strings.HasPrefix(abs, root+string(os.PathSeparator))
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
				return fmt.Errorf("%s is only in the live medium's repository, and none was found at %s - "+
					"pass --local-repo, or leave it out of --packages to install a runit system", p, tried)
			}
		}
	}
	return nil
}

// aliasFlag collects --block-device name:file values.
type aliasFlag []*config.StorageAlias

func (a *aliasFlag) String() string {
	var parts []string
	for _, x := range *a {
		parts = append(parts, x.Name+":"+x.File)
	}
	return strings.Join(parts, ",")
}

func (a *aliasFlag) Set(v string) error {
	name, file, ok := strings.Cut(v, ":")
	if !ok || name == "" || file == "" {
		return fmt.Errorf("want name:file, e.g. disk:/dev/nvme0n1")
	}
	*a = append(*a, &config.StorageAlias{Name: name, File: file})
	return nil
}

// writeTemplate writes a starting config, as clr-installer's --template
// does: the standard whole-disk layout (ESP, swap, root) on a disk named
// by the alias "disk", so pointing it at a machine is one edit of the
// alias (or --block-device disk:/dev/...). Accounts are left out: a user
// needs a password hash, which --genpass prints.
func writeTemplate(path string) error {
	if filepath.Ext(path) != ".yaml" {
		return fmt.Errorf("template file %q must end in .yaml", path)
	}
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("%s exists; not overwriting it", path)
	}
	cfg := &config.Config{
		BlockDevices: []*config.StorageAlias{{Name: "disk", File: "/dev/sda"}},
		TargetMedia: []*config.Media{{
			Name: "${disk}", Type: "disk",
			Children: []*config.Media{
				{Name: "${disk}1", Type: "part", FsType: "vfat", Mountpoint: "/boot", Size: "512M"},
				{Name: "${disk}2", Type: "part", FsType: "swap", Size: "2G"},
				{Name: "${disk}3", Type: "part", FsType: "ext4", Mountpoint: "/", Size: "0"},
			},
		}},
		Hostname: "sunlight",
		Timezone: "UTC",
		Keyboard: "us",
		Language: "en_US.UTF-8",
		Version:  1,
	}
	if err := config.Save(path, cfg); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "Wrote %s. Add users (password hashes from --genpass) and point the disk alias at the target.\n", path)
	return nil
}

// seedSunlightKey returns a keys directory holding the live medium's xbps
// keys plus the embedded Sunlight repo key, so an install can verify the
// Sunlight mirror's signatures. The caller removes the directory.
func seedSunlightKey(liveKeys string) (string, error) {
	dir, err := os.MkdirTemp("", "slinstaller-keys-")
	if err != nil {
		return "", err
	}
	// `cp -a dir/. keys/` gives the target's /var/db/xbps/keys this
	// directory's mode: MkdirTemp's 0700, not the 0755 xbps has. The keys
	// are public.
	if err := os.Chmod(dir, 0o755); err != nil {
		return "", err
	}
	// The live medium's keys (Void's), when the directory is there.
	if ents, err := os.ReadDir(liveKeys); err == nil {
		for _, e := range ents {
			if e.IsDir() {
				continue
			}
			b, err := os.ReadFile(filepath.Join(liveKeys, e.Name()))
			if err != nil {
				os.RemoveAll(dir)
				return "", err
			}
			if err := os.WriteFile(filepath.Join(dir, e.Name()), b, 0o644); err != nil {
				os.RemoveAll(dir)
				return "", err
			}
		}
	}
	key, err := sunlightKeyFS.ReadFile("keys/sunlight-repo.plist")
	if err != nil {
		os.RemoveAll(dir)
		return "", err
	}
	if err := os.WriteFile(filepath.Join(dir, sunlightKeyFile), key, 0o644); err != nil {
		os.RemoveAll(dir)
		return "", err
	}
	return dir, nil
}
