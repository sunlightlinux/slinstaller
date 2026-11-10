# Changelog

All notable changes to slinstaller are documented here. Format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and
[Semantic Versioning](https://semver.org/spec/v2.0.0.html).

slinstaller is in early bootstrap: there are no releases yet, and
nothing below is a stability promise. See [STABILITY.md](STABILITY.md)
for what will and will not be allowed to change once releases begin.

## [Unreleased]

### Added

- Kernel Command Line offers a scrollable, type-to-filter list of predefined
  profiles — Server, Desktop, Desktop LowLatency, LowLatency, Realtime,
  Audio/Video Workstation, Telco/RAN, HPC/Compute, Virtualization Host (KVM),
  GPU Passthrough (VFIO), Intel GPU Workstation, Containers/Cloud, Hardened
  (AppArmor), Laptop/Power Save, Debug/Verbose Boot, and Custom. The
  highlighted profile previews its arguments, and a trade-off note warns when
  a profile weakens security or power (`mitigations=off`, `idle=poll`) or
  needs a machine-specific step (pinning CPUs). Picking a profile fills the
  arguments; Custom opens the manual Add/Remove form.
- Safe Installation (install alongside an existing system): the Installation
  Media dialog's Safe mode now works. It reuses the disk's existing EFI
  System Partition and adds a root partition in the largest unallocated
  region, leaving every existing partition — and any other OS — intact; no
  disk is wiped. New `pkg/storage` helpers detect free space and the ESP
  (`FreeRegions`, `LargestFree`, `ExistingESP`, `NextPartitionNumber`,
  parsing `parted ... print free`), a Plan can cut a `Create` partition at an
  explicit offset on a non-wiped disk, and the config carries it (`create` +
  `start` on a `reusePartitions` child). *Limitations:* it needs an existing
  ESP and enough free space — it does not yet resize/shrink a partition to
  make room — and the root is not encrypted in this mode. VM-unverified so
  far.

### Changed

- Test Network Settings now runs its connectivity check behind an animated
  progress bar in the popup (which pulses while the check runs), the way
  clr-installer shows it, instead of a text spinner.
- The Configure Network Interfaces sub-page now shows each interface's
  current live addresses (ipv4/netmask/ipv6/netmask, read-only) the way
  clr-installer does, above the Automatic (DHCP) / Static IP choice — so you
  can see the interface's state and still configure a static address. The
  interface list shows the live IPv4 next to a DHCP interface.
- A new LUKS passphrase typed at the installer is asked for twice and must be
  8 to 94 printable ASCII characters, as in clr-installer: a typo here locked
  the disk it was set on. *Compatibility:* only what is typed at a terminal is
  checked; a passphrase from `--crypt-file`, the environment or a pipe is used
  as given, so an unattended install does not change.
- A disk that is in use is no longer offered or installed to: one with a
  mounted filesystem, active swap, or the live medium itself (the TUI and
  the text flow leave it out; a config naming it is refused before
  anything is touched; `--list-disks` marks it). clr-installer leaves such
  disks out too. *Compatibility:* a config targeting a disk the running
  system uses now stops with the reason instead of wiping it.
- Wiping a disk that shares a RAID array or LVM volume group with a disk
  the install does not wipe is refused unless `--force-destructive` is
  given (clr-installer's flag), as wiping one member breaks the others.
- Hooks run the way clr-installer runs them: `$chrootDir`, `$chrooted`,
  `$yamlDir` (new: the config file's directory) and the config's `env` are
  substituted into the command, in `$name` and `${name}` forms, as well as
  set in its environment, and the command runs under `bash -l -c` (`sh -c`
  where there is no bash). *Compatibility:* a hook written for `sh` that
  relied on `$name` reaching the shell unexpanded, or on sh-only
  behaviour, sees the clr-installer behaviour now. Inside a chroot hook
  `$chrootDir` stays `/`, where clr-installer passes the live mount path.
- When an administrator account is created, root's password login is
  locked (`usermod --lock`, with today as its last change), as
  clr-installer does: root is reached through sudo. *Compatibility:* an
  install that relied on root's state from the base system now has it
  locked; one with no admin account is unchanged.
- Logins are checked as clr-installer checks them: at most 31 characters,
  and not a name the base system already uses for an account or group
  (root, wheel, users, audio, ...), which useradd would fail on partway
  through the install. The TUI's account form also asks for the password
  twice and requires 8 to 255 characters, saying why on the form itself;
  the text flow applies the same rules.
- A passing network test in the TUI sets `copyNetwork`, as clr-installer's
  does, so the network set up on the live system (Wi-Fi included) carries
  into the installed one; the result popup says so.
- A config whose storage alias names an image file (clr-installer's
  install into an image) is refused by name; it used to be read as a
  device path such as `/dev/release.img`.

- The full-screen dialog is clr-installer's window: 80x24, centred, whatever
  the size of the terminal (smaller only on a smaller one). It used to grow
  with the terminal to 90x36, which made it much taller than clr-installer's.
  As there, the Required tab's section list now scrolls.
- The install log view is easier to read: each stage starts with its
  `==> stage` marker on a grey bar (the full-screen install used to leave
  the markers out), lines with an error are on a red bar and warnings in
  yellow, a line too long for the panel continues on the next row instead
  of being cut, and xbps's download progress lines are left out, as each is
  followed by the download's final line. A blank row now separates the log
  from its Back button. Its header says where the view is: "following new
  lines" at the end, "paused at line 340 of 649" when scrolled up, and "end
  of log" once the install is over.
- The install progress screen shows how long the install has run, and each
  stage how long it took (or has been running); the final screen says how
  long the install took.

- The full-screen interface takes Sunlight Linux's orange-to-red colours
  in place of clr-installer's blue: a burnt orange dialog, an amber accent
  for the selection, active tab, buttons and progress bar, and dark red for
  popups and failures. The layout is unchanged. Each colour names its
  16-colour console form too, since the console has no orange and the
  nearest match turned every orange into bright red; there it is the VGA
  brown (dark orange) with a red accent, as the console has only 8
  background colours and shows a bright one (the amber's nearest) as black. The
  progress bar picks its own console colour, and made bright red of the
  amber; it is named outright there too, as bright yellow.
- The install progress screen lists the stages instead of the raw command
  output: each is marked `[ OK ]` when done, with a turning spinner while it
  runs (that stage's line in bold yellow), `[FAIL]` if it fails, and blank while it waits. The full command log
  is behind a **Details** button (Enter or `d`), scrollable with the arrows,
  PgUp/PgDn and Home/End, and on the final screen too (`d`); it keeps the
  last 10000 lines, where only 500 were kept. The marks are ASCII because
  the console font has no tick or cross glyph.
- Buttons sit at the bottom of the dialog on every page that has them, just
  above the footer, as clr-installer's control frame does. They used to
  follow the content, so a short page (Manage User with no users) showed
  them a third of the way down with the rest of the dialog empty below.
- The buttons in a row are all as wide as the widest, labels centred, on
  every page: the hub, the lists, Installation Media, Manage User, the
  install confirmation and the final screen. To fit four even buttons in
  the dialog, the final screen's "Back to installer" is now "Back to menu".
- The TUI no longer has a Telemetry section: Sunlight Linux collects none,
  so the choice did nothing. A config's telemetry keys still load and are
  ignored, as before.
- Button rows are centred in the dialog on every page. A row too wide to
  leave a margin on both sides, the final screen's four, draws its buttons
  tighter instead of touching the right edge.
- The hub's section list reaches down to the buttons: five sections in the
  80x24 dialog, where it showed four with three empty rows below them.

### Added

- Automatic OS Updates is wired up: the config's `autoUpdate` (on unless
  set to false, as in clr-installer) installs a `sunlight-autoupdate` slinit
  service that updates the system from its xbps repositories once a day,
  the counterpart of Clear Linux's swupd update timer. It is a slinit
  calendar timer (`cron-calendar = daily`) at a fixed random time within
  the hour after midnight, caught up at the next boot if the machine was
  off; it updates xbps first, then the system. With `autoUpdate: false`
  the service is installed but not enabled (`slinitctl enable
  sunlight-autoupdate` turns it on). The TUI's Automatic OS Updates
  section sets it.
- Before a disk is repartitioned, what the live system runs on it is
  released: RAID arrays assembled from an earlier install are stopped,
  LVM and LUKS device-mapper volumes removed, and old signatures erased
  with `wipefs`, as clr-installer clears them before partitioning. Left in
  place they kept the disk busy for `parted`, or came back on the next
  boot. A dry run lists these commands as they stand on the machine.
- A partition's `label` and `options` in a clr-installer config are
  honoured: the label goes to mkfs (with each filesystem's flag, cut to its
  length limit), the options after slinstaller's own mkfs options.
- clr-installer's command-line options: `-c`/`--config`, `--crypt-file`
  (the LUKS passphrase from a file), `--genpass` (print a password hash
  for a config's users; `-` reads it without echo),
  `-b`/`--block-device name:file` (point a storage alias at a disk),
  `--template` (write a starting config), `--copy-network`,
  `--allow-insecure-http`, `--swap-file-size` and `--force-destructive`.
- The full-screen install writes its log to `/tmp/slinstaller.log` on the
  live system as it runs, and a completed install copies it to
  `/root/slinstaller.log` on the installed system (mode 0600, beside the
  saved config), as clr-installer archives its own log. Until now the log
  lived only in the view and was gone once the installer exited.
  `controller.Options.LogFile` names the file to archive.
- The text and unattended installs keep the same log: `/tmp/slinstaller.log`
  (or `--log`'s file) while running, `/root/slinstaller.log` on the installed
  system after a complete install. `--log` now shows progress live on the
  terminal too, and its file holds lines as the terminal showed them.

### Fixed

- Choosing `man-pages` in the TUI failed the install at base-install, after
  the disk had been wiped: it asked for a package `mandoc`, which Void calls
  `mdocml`.
- A popup cut a black band across the dialog: it replaced the whole rows it
  covered. It now replaces only the cells under it.
- A password hash no longer appears in the install log: the `useradd -p`
  command is shown with `<password hash>` in its place, in the log view,
  the log file and `--log`.
- Filled boxes in on-screen text on the Linux console: its font has no em
  dash, ellipsis, tick or cross, so "Installing — step", "Finishing…", the
  network test's ✓/✗ and many descriptions showed a box. The installer's
  on-screen text, its messages and the config's validation errors use
  "-", "..." and [ OK ]/[FAIL] instead; a clipped log line ends in "...".
- Space did nothing on a disk row of the Installation Media page, though
  the footer offers it to choose; it now picks the disk, as Enter does.
- Black blocks between buttons: the spaces after a button's style reset
  fell back to the terminal's background. They keep the dialog's colour.
- Black cells at the right of the install screen, and half-redrawn lines in
  its log: command output reached the log with its control characters.
  mke2fs prints its progress with backspaces ("0/16" erased and overwritten
  by "done"), xbps redraws its download line with "\r"; lipgloss counted
  them as zero-width while the terminal still moved the cursor, so the row
  came out short and the rest was never drawn over. Each line is now turned
  into what a terminal would show for it before it is logged.
- The Cancel / Confirm buttons on the selection lists (timezone, language,
  keyboard, kernel) and on Select Additional Bundles could not be reached:
  they were drawn only, with Confirm always lit. Tab now moves focus from the
  list to Cancel, Confirm and back (Shift+Tab the other way, Left/Right
  between the buttons, Up back to the list), the focused button is the lit
  one, and Enter on Cancel leaves without changing anything.
- The install progress screen left a blank band between the command log
  and the footer: the log was capped at 20 lines and sized from an estimate
  of the window. It now fills exactly the rows the dialog has left.
- An install that set no timezone or keymap was refused before it began,
  with `timezone "" is not a known zoneinfo zone`. The pre-wipe check added
  for both took the empty value for a name, though empty means "leave the
  target's default" when the install applies it, and the serial text flow
  never asks for either — so every serial-console install failed. Only a
  timezone or keymap that is set is checked now.
- A `--local-repo` under the target root (e.g. a 9p share mounted at
  `/mnt/share` while installing to `/mnt`) is now refused in preflight: the
  repository would be hidden the moment the target filesystem is mounted,
  and the base install would fail with "package not found" partway through.
  The error says to move the repo outside the root. The VM demo's guest
  instructions now mount the share at `/run/slshare`, outside `/mnt`.
- Passwords and passphrases typed at the text prompt flow and the LUKS
  passphrase prompt are no longer echoed on screen: input is read with
  terminal echo turned off (and still read normally on a pipe/non-terminal,
  e.g. an unattended run). The full-screen TUI already masked them.
- A fully offline install (a local repository on the live medium, no remote
  mirror) failed at base-install with "xbps: no repository": the install
  command always required a remote `Repo` and emitted an empty `-R`. xbps
  now accepts a local-only repository set — every `-R` is local and the
  network is never reached — so `offline` installs (and a bare
  `--local-repo` with no `--repo`) work.

### Changed

- The full-screen dialog keeps a stable size: its body is padded to a fixed
  height (tracking the terminal, bounded), so the blue box no longer shrinks
  when entering a short section and grows back on return to the menu.
- The hub's section list now scrolls within that fixed height, with a
  scrollbar (▲/▼ and a thumb) on the right showing there is more below — as
  clr-installer does. The Advanced tab, which has more sections than fit, no
  longer makes the dialog taller than the Required tab; the selected row is
  highlighted at a uniform two-line height so the list scrolls cleanly.

### Added

- Configure Installation Media is now clr's front dialog: three modes
  (Destructive, Advanced, Safe), the disk list, an Enable Encryption
  checkbox and Confirm / Partition / Rescan Media / Cancel buttons.
  Destructive + Confirm lays down a standard whole-disk layout (512M ESP at
  /boot, 2G swap, root filling the rest; root encrypted when the box is
  ticked, prompting for the passphrase); Advanced (or the Partition button)
  opens the manual partition editor; Safe (install alongside) is shown but
  reports it is not yet supported.
- Smaller clr parities in the full-screen wizard: Kernel Command Line now
  has both Add and Remove argument fields (→ `kernel-arguments` add/remove);
  Swupd Mirror now has the "Allow installation over insecure connections
  (http://)" checkbox (→ `allowInsecureHTTP`); Telemetry and Automatic OS
  Updates now show clr's explanatory paragraph above the Enabled/Disabled
  choice; and the hub's selected row is drawn as a taller teal bar.
- Select Additional Bundles is now a checklist with descriptions ([x]/[ ]),
  as clr-installer does, instead of a free text field. The ticked bundles'
  xbps packages (a small curated set — vim, git, curl, htop, openssh, …) are
  written to the config's `packages`.
- Timezone, Language and Keyboard are now chosen from scrollable lists of
  what the live system offers (zoneinfo zones, UTF-8 locales, console
  keymaps), with a scrollbar and type-to-filter, as clr-installer does —
  instead of a free text field (which stays as a fallback when the data is
  absent). New `pkg/system` enumerators `ListTimezones`, `ListKeymaps` and
  `ListLocales` back them.
- The full-screen install's final screen now has action buttons — Reboot,
  Power off and Exit to shell — navigable with Tab/arrows and activated with
  Enter, instead of only "[Enter] exit". Reboot and power-off run after the
  interface is torn down, so they execute cleanly.
- Save Configuration Settings now writes the wizard's config to a file
  (clr parity): it builds the clr-format `config.yaml` from the current
  choices and saves it (never including the LUKS passphrase), so the layout
  can be inspected or re-run later with `slinstaller --apply <file>`. New
  `config.Save` writes a config back in the shape `Load` reads.
- Configure Network Interfaces is now clr's interface sub-page (phase 3):
  the machine's live interfaces are listed with their current setting
  (Automatic DHCP or a static address), and selecting one opens a
  per-interface window to choose DHCP or a static IP (CIDR address, gateway,
  DNS). Several interfaces can be configured; each static one becomes a
  `networkInterfaces` entry in the built config.
- Manage User is now clr's account table (phase 2): a Login / User Name /
  Password / Admin table with Add New User, Revert, Confirm and Cancel, and
  a form (login, full name, masked password, administrator yes/no). The
  wizard can define several accounts, mark which are administrators, edit an
  existing row (Enter) or delete it (d) — replacing the previous
  single-admin prompt. Passwords are hashed when the config is built; the
  install needs at least one administrator.
- Full-screen TUI reorganised to match clr-installer's menu 1:1 (phase 1):
  the Required tab is Choose Timezone, Choose Language, Configure the
  Keyboard, Configure Installation Media, Manage User, Telemetry; the
  Advanced tab is Configure Network Interfaces, Proxy, Test Network
  Settings, Select Additional Bundles, Assign Hostname, Kernel Command Line,
  Kernel Selection, Swupd Mirror, Automatic OS Updates, Save Configuration
  Settings — in clr's exact order and wording (e.g. "Warning: No Media
  Selected", "No HTTPS proxy URL set"). Hostname moved to Advanced. The LUKS
  passphrase is now collected by a popup raised from the media dialog when
  an encrypted partition is added, as clr does, rather than a standalone
  section. "Test Network Settings" runs a real connectivity check behind a
  small animated spinner popup. Telemetry, Additional Bundles and Automatic
  OS Updates are present for fidelity (Telemetry/Auto-updates are
  Enabled/Disabled toggles; Bundles edits the extra-packages list). Further
  pieces — the multi-user Manage User table and the network-interface
  sub-page — follow in later phases.
- `--reboot` flag (the CLI equivalent of the config's `postReboot`) and
  `--log <file>` (tees the install's command trace, stage headers and
  sub-command output to a file as well as the terminal, for debugging an
  unattended or serial install).
- Timezone and keymap validation: a mistyped `timezone` or `keyboard` is
  caught before the disk is touched, checked against the live system's
  zoneinfo and console-keymap data — and only when that data is present, so
  a minimal medium that lacks it never rejects a valid value.
- Reuse-partitions (no-wipe) install: `reusePartitions: true` installs into
  the disk's **existing** partitions without repartitioning — the partition
  table is left untouched, each declared partition is matched to its device
  by name, formatted unless marked `preserve: true`, then mounted. This is
  how an install shares an existing ESP or keeps a `/home` across a
  reinstall (clr-installer's advanced/manual mode). Only plain partitions
  are supported for now (no crypt/lvm/raid). Preflight confirms each
  partition exists, and the confirmation lists which are formatted vs kept.
  See `demo/vm/config-reuse.yaml`.
- Pre-wipe size validation (clr-installer parity): `Validate` now rejects an
  EFI System Partition below 100 MB, a root below 2 GB, or any partition
  below 1 MiB, and preflight refuses a layout whose fixed partitions plus
  GPT overhead do not fit the target disk — all **before** the disk is
  touched, so an oversize or undersized layout fails up front rather than
  partway through `parted`. `skipValidationSize`/`--skip-validation-size`
  and `skipValidationAll`/`--skip-validation-all` turn these (or all
  validation) off, as in clr.
- `--offline` flag: the command-line equivalent of the config's `offline`,
  so an offline install (local repository only, no network) can be run
  interactively without hand-writing a config.
- Full-screen TUI expanded to clr-installer's section set. The hub's two
  tabs now hold, under Required: Installation Media (disk + the partition
  editor in one dialog, like clr's media_config), Timezone, Keyboard,
  Language, Manage User, Hostname; under Advanced: Network Interface
  (a single static interface, blank = DHCP), Network Proxy, Storage Mirror,
  Kernel Selection (→ `linux`/`linux-lts`), Kernel Command Line, Additional
  Packages, Encryption Passphrase. Each opens its own titled dialog. The
  passphrase entry and the "not ready" warning are now bordered popup
  windows floated over the hub, the way clr-installer draws them. All the
  new sections feed the produced config (`kernel-arguments`, `httpsProxy`,
  `swupdMirror`, `packages`, `networkInterfaces`), so the wizard can now
  build the same configs the file front-end accepts.
- Storage aliases (clr-installer's `block-devices`): a `targetMedia` disk
  written as `${alias}` and its partitions as `${alias}1` now expand to
  real device names at load time, using the `block-devices: [{name, file}]`
  declarations — the disk to the device's base name (`/dev/nvme0n1` →
  `nvme0n1`) and its partitions to that name plus the kernel's partition
  suffix (`nvme0n1p1`; empty for the `sd*`/`vd*` family). Both `${alias}`
  and `$alias` forms, matching clr-installer. A layout can be written once
  and pointed at different disks by changing one `file:`.
- UEFI preflight: a full `--apply` now refuses to start when the installer
  medium was not booted in UEFI mode (`/sys/firmware/efi` absent), instead
  of failing at the bootloader stage after the disk is already wiped.
- `copyNetwork` (config): copies the live system's network configuration
  (`/etc/wpa_supplicant`, a hand-edited `/etc/dhcpcd.conf`) into the target,
  so an install done over Wi-Fi stays reachable after first boot.
- `postReboot` (config): an unattended `--apply` reboots into the installed
  system when the full run finishes.
- `env` (config): extra variables made available to pre/post-install hook
  commands, on top of the `$chrootDir`/`$chrooted` the installer always sets
  (clr-installer parity).
- Kernel selection: the config's `kernel` (clr's `kernel-native` /
  `kernel-lts` / `kernel-kvm`) now maps to the Sunlight kernel package
  (`linux` / `linux-lts`); any other value is taken as a literal package
  name, and is installed into the target.
- `packages` (config): extra xbps packages to install into the target —
  slinstaller's functional stand-in for Clear's untranslatable `bundles`
  (which stays carried-but-ignored).
- `offline` and `allowInsecureHTTP` (config): an offline install uses only
  a local repository on the live medium and skips the network precheck; a
  plain-`http://` remote mirror is refused unless `allowInsecureHTTP` is set
  (https and local repos are always allowed).
- `pkg/config` — reads clr-installer's config schema (`targetMedia` with
  its partition children, plus `users`, `networkInterfaces`, `keyboard`,
  `language`, `timezone`, `kernel-arguments`, `bundles` and pre/post
  hooks). slinstaller's own parser for that format, not a copy of
  clr-installer's code: an unmodified clr-installer `config.yaml` loads
  and produces a correct partition plan. Parsing tolerates the lsblk
  probe fields and Clear Linux specifics real configs carry, and the
  three size forms clr-installer writes (byte count, integer+unit,
  decimal+unit), and a missing size / 0 / "rest" on the last partition to
  mean "fill the disk" (as clr-installer uses size 0). `Validate` reports
  every problem at once before any disk is touched — unknown filesystem,
  non-absolute or duplicate mountpoint, no or more than one root, no ESP,
  an encrypted ESP, a bad hostname or swapFileSize — while staying
  clr-compatible (it does not require fields a valid clr config may omit,
  like a login account). Tested against clr-installer's own fixtures.
- `pkg/blockdev` — discovers the machine's block devices with `lsblk -J`
  (size parsed whether lsblk emits a number or a string). Lean next to
  clr-installer's full BlockDevice tree: enough to confirm the target disk
  exists and is a whole disk before anything is wiped, and to list what is
  available. `cmd/slinstaller --list-disks` prints them, and a full
  `--apply` now refuses a target that is missing or is a partition.
- `pkg/storage` — the first core subsystem ported from
  [clr-installer](https://github.com/clearlinux/clr-installer), reduced
  to a single UEFI target: GPT labelling and partitioning with `parted`
  (the ESP marked with parted's `esp` flag — no `sgdisk` dependency), and
  filesystem creation with `mkfs.*` /
  `mkswap`. The destructive work is expressed as a command list
  (`Plan.Commands`) that executes nothing until handed to `RunCommands`,
  so a dry run shows exactly what an apply would do. Also mounts the
  target tree parent-first (`Mounter`), bind-mounts `proc`/`sys`/`dev`
  for chroot, and renders `fstab` by UUID (`Plan.Fstab`) — listing every
  mount, since Sunlight boots slinit and has no systemd gpt-auto to defer
  standard mounts to. A config `swapFileSize` creates a swap file on root
  (`SwapFileCommands`) instead of a swap partition — clr-installer's
  swapFileSize, run as a controller stage after fstab.
- `pkg/luks` — LUKS encryption with cryptsetup. A partition typed `crypt`
  in the config (clr-installer's marker) is luksFormat'd and opened to a
  mapper, and its filesystem is made and mounted there
  (`storage.Partition.EffectiveDevice`). The whole install takes **one
  passphrase**, splitting the unlock between the two things that run at boot.
  The **root** volume is opened by the dracut initramfs (`rd.luks.uuid` for
  the root volume only, `rd.luks.crypttab=0` so dracut does not also walk
  crypttab and prompt twice); dracut asks for the passphrase, unless `/boot`
  is encrypted, where GRUB already asked and a keyfile embedded in the
  encrypted initramfs (`rd.luks.key`) opens it. The **non-root** volumes (a
  crypt `/home`, `/data`, …) are opened after root is mounted by Void's runit
  crypttab service, from an `/etc/crypttab` that lists only them, with a
  random keyfile on the now-unlocked encrypted root (`luks.AddKey` adds it as
  a second key slot) — so no second prompt, and the keyfile never sits on
  unencrypted storage. The passphrase comes from `SLINSTALLER_LUKS_PASSPHRASE`
  or a prompt — never from the config, and never in a process's argv.
- `pkg/lvm` — LVM: a partition typed `lvm` is a physical volume carrying
  a `volumeGroup`, and its children are logical volumes. The controller
  runs `pvcreate`/`vgcreate`/`lvcreate` between partitioning and mkfs, the
  filesystems are made and mounted on the logical volumes
  (`storage.Partition.EffectiveDevice`), an LV may fill the group, and a
  root on LVM gets `rd.lvm.lv` on the kernel command line with an
  initramfs rebuild so it assembles at boot.
- **LVM-on-LUKS** — an `lvm` partition with `encrypted: true` puts the
  whole group inside one LUKS container: the partition is luksFormat'd, and
  `pvcreate`/`vgcreate` (and so every logical volume and filesystem) run on
  the mapper rather than the raw partition — the common "encrypted LVM"
  layout, one passphrase for root, home and swap. The controller unlocks
  the container before LVM assembly and, for a root on an encrypted group,
  puts the container's `rd.luks.uuid` beside the group's `rd.lvm.lv` on the
  kernel command line (with the initramfs rebuild) so boot unlocks then
  activates. The full-screen partition editor offers it as the
  `lvm-encrypted` type; `demo/vm/config-lvm-luks.yaml` is a worked example.
- `pkg/mdadm` — **software RAID** (0, 1, 5, 6, 10) with mdadm, clr-installer's
  RAID levels. This brings **multi-disk layouts**: `targetMedia` may list
  several disks, each partitioned, and a partition typed `raid` with a
  `raid: <name>` tag is a member of the array named in the config's new
  top-level `raid` list (`{name, level, fstype, mountpoint}`). The array
  carries the filesystem at `/dev/md/<name>`: parted cuts and flags the
  members, `mdadm --create` assembles the array before mkfs, and for boot the
  controller writes `/etc/mdadm.conf` (from `mdadm --detail --scan`), forces
  dracut's `mdraid` module (a `/etc/dracut.conf.d` drop-in — in the chroot
  dracut cannot tell the target boots from an array), adds `rd.md.uuid` for a
  root on RAID, and rebuilds the initramfs, so the array assembles at boot.
  The ESP stays a plain partition on one disk (firmware reads one disk, not
  an array).
  `Validate` checks the level, the member count per level and that the ESP is
  not on RAID; the full `--apply` preflight confirms every target disk and
  pulls in `mdadm`, and the confirmation lists all disks to be erased.
  `demo/vm/config-raid1.yaml` is a RAID1 example (`demo/vm/run.sh
  --two-disks` boots a second disk for it). RAID is config-file-driven; the
  full-screen editor stays single-disk for now.
- `pkg/xbps` — the base-system install, the counterpart to clr-installer's
  swupd step, following how Void bootstraps a rootfs: seed the repository
  signing keys into the target, `xbps-install` from the repository, then a
  chrooted `xbps-reconfigure -fa`. Expressed as a command list; the
  repository, keys, architecture and package set are the caller's to supply.
- `pkg/grub` — the UEFI bootloader install: chrooted `grub-install` (which
  writes the loader to the ESP and registers the firmware boot entry) and
  `grub-mkconfig`. Kernel arguments from the config are rendered into
  `/etc/default/grub`. clr-installer uses clr-boot-manager, which is
  Clear-specific; GRUB is Sunlight's choice, as it is Void's. When `/boot`
  is on an encrypted device (the ESP is at `/boot/efi` and the kernel lives
  on the encrypted root), the controller sets `GRUB_ENABLE_CRYPTODISK=y`
  before `grub-install` and formats that one LUKS volume with `pbkdf2`
  instead of the LUKS2 argon2id default, since GRUB can only unlock a
  pbkdf2 container — without it GRUB rejects the right passphrase as
  invalid. Putting the ESP at `/boot` keeps the kernel unencrypted, avoids
  the extra GRUB-stage passphrase prompt, and keeps argon2id everywhere.
- `pkg/system` — first-boot config: hostname (`/etc/hostname`), timezone
  (the `/etc/localtime` symlink), console keymap (`KEYMAP` in
  `/etc/rc.conf`, which Void's core services and slinit's OpenRC
  compatibility both read), locale (`/etc/locale.conf` plus uncommenting
  the glibc `libc-locales` entry, with the controller regenerating
  locales), and user accounts (`useradd` with the crypt-hash password via
  `-p`, `wheel` for admins, and `authorized_keys` for any SSH keys), plus
  `EnableService` to turn on a runit service in the target. An `admin`
  account now also gets working sudo: the controller writes a
  `%wheel`-enabling `/etc/sudoers.d/wheel` (0440) when any user is admin,
  so `admin: true` means sudo, not just group membership.
- `pkg/network` — the installed system's networking, via dhcpcd rather
  than clr-installer's systemd-networkd/NetworkManager (Sunlight has no
  systemd). Each `networkInterfaces` entry from the config is mapped to a
  `/etc/dhcpcd.conf` stanza: its addresses (IPv4 and IPv6), gateway
  (`routers`), DNS servers (`domain_name_servers`, space-separated) and
  search `domain` (`domain_search` — clr's interface `domain`, now parsed).
  An interface counts as static when it has addresses and is not explicitly
  `dhcp: "true"` (clr's rule), so an unset `dhcp` with addresses is still
  static; a DHCP interface produces no stanza and is left to dhcpcd's
  default, which is enabled so a plain install still comes up with network.
  A static interface's addresses and gateway are validated by
  `config.Validate` (via `network.ValidAddr`) before any disk is touched,
  so a bad IP or netmask fails up front rather than partway through the
  install. `VerifyConnectivity` checks the repository is reachable before a
  disk is wiped, so an offline install fails early rather than partway
  through the base download.
- Three smaller clr-installer parities: install hooks get `$chrootDir`
  and `$chrooted` in their environment, as clr expands them; the config's
  `httpsProxy` is passed to `xbps-install` for the download; and
  `kernel-arguments`'s `remove` list is honoured (its entries are dropped
  from what slinstaller writes to the kernel command line).
- `pkg/controller` — sequences the whole install from a config:
  pre-install hooks → partition → mount → bind meta filesystems →
  base-system install → fstab → bootloader → first-boot → network →
  post-install hooks. A full install runs end to end (gated by the front-end on root
  and disk confirmation); `Options.Through` stops after a named stage for
  a staged VM run, and a failure unmounts what it mounted. `Options.Progress`
  reports each stage as it starts (completed/total + name), so a front-end
  can drive a progress bar without the controller knowing about any UI.
- `pkg/tui` — an interactive front-end: with no config file,
  `cmd/slinstaller` asks for a target disk, the root filesystem, whether
  to encrypt the root (with a LUKS passphrase, confirmed), swap (a
  partition, a file, or none) and its size, a hostname and an admin
  account (password hashed with openssl, confirmed), shows a summary to
  confirm, and installs through the same path as a config file. On a real
  terminal it runs as a **full-screen menu styled after the Clear Linux
  minimal-ISO installer** (bubbletea/bubbles/lipgloss): a filled royal-blue
  dialog centred on a black screen, a bracketed title, a black tab strip with
  `[R] Required options` / `[A] Advanced options` (the active one a teal
  block), and each section shown as a bold white label over a cyan `» value`
  line, separated by rules, with a tall teal bar (magenta label) on the
  selected row. Required
  holds disk, the **manual partition editor**, keyboard, language, timezone
  and the admin user; Advanced holds passphrase and hostname — edited in any
  order, `[Tab]` switches tabs. `Cancel` and `Install` sit at the foot, with
  Install lit only once the layout is complete (a disk, a root, an ESP, a
  user, and a passphrase if encrypting), over the footer hint
  "Use [Tab] or the arrow keys [Up and Down] to navigate". The partition
  editor adds, deletes and nests entries: plain,
  encrypted (`crypt`) or LVM partitions, with logical volumes under an
  LVM group — the full layout the config file can express. Pressing Install
  runs the install **inside the interface**: a final "this will ERASE
  &lt;disk&gt;" confirmation, then a **progress bar** with the current stage
  (driven by the controller's `Progress` callback) and a live tail of the
  command output (`storage.SinkRunner` streams it into the view instead of to
  stderr), ending on a success or failure screen — no more plain-text scroll
  after the styled menu. Over a serial console or a pipe it falls back to the
  plain prompt flow and streamed-text install; both build the same config.
  The full-screen UI is chosen only when the terminal can report a window
  size (so a serial console — which usually reports 0x0, and on which
  clr-installer's own TUI crashes — takes the prompt flow), and `--text`
  forces the prompt flow regardless.
- `cmd/slinstaller` — the file front-end: reads a config, validates it,
  and prints the partitioning plan. With `--apply` it runs the install
  through the controller, gated on running as root and on the operator
  retyping the target disk path; `--through` and `--root` steer a staged
  VM run, and `--repo`/`--keys`/`--arch`/`--packages` set the base-system
  install. With no repository given it defaults to the config's
  `swupdMirror`, then to Void's official repo — a stopgap until Sunlight
  has its own.
- `demo/uefi-basic.yaml` — a minimal UEFI install in clr-installer's
  config format.
- `demo/vm` — a QEMU + OVMF harness that boots a Void live ISO with a
  blank virtio disk and the slinstaller binary over a 9p share, so the
  whole install runs end to end under real UEFI, and `run.sh --installed`
  boots the result. slinstaller's counterpart to slinit's QEMU demo.
