# Changelog

All notable changes to slinstaller are documented here. Format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and
[Semantic Versioning](https://semver.org/spec/v2.0.0.html).

slinstaller is in early bootstrap: there are no releases yet, and
nothing below is a stability promise. See [STABILITY.md](STABILITY.md)
for what will and will not be allowed to change once releases begin.

## [Unreleased]

### Added

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
  (`storage.Partition.EffectiveDevice`). The controller writes
  `/etc/crypttab` by LUKS UUID, adds `rd.luks.uuid=` to the kernel command
  line for an encrypted root, and rebuilds the initramfs so the system
  unlocks at boot. The passphrase comes from `SLINSTALLER_LUKS_PASSPHRASE`
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
  controller writes `/etc/mdadm.conf` (from `mdadm --detail --scan`) and
  rebuilds the initramfs so the array assembles at boot. The ESP stays a
  plain partition on one disk (firmware reads one disk, not an array).
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
  Clear-specific; GRUB is Sunlight's choice, as it is Void's.
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
