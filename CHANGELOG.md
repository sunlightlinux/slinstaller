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
  initramfs rebuild so it assembles at boot. (v1 is LVM on plain
  partitions; LVM-on-LUKS is a later step.)
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
  systemd). Static interfaces from the config become `/etc/dhcpcd.conf`
  stanzas; dhcpcd is enabled so DHCP covers the rest, and so a plain
  install still comes up with network. `VerifyConnectivity` checks the
  repository is reachable before a disk is wiped, so an offline install
  fails early rather than partway through the base download.
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
  a staged VM run, and a failure unmounts what it mounted.
- `pkg/tui` — an interactive front-end: with no config file,
  `cmd/slinstaller` asks for a target disk, the root filesystem, whether
  to encrypt the root (with a LUKS passphrase, confirmed), swap (a
  partition, a file, or none) and its size, a hostname and an admin
  account (password hashed with openssl, confirmed), shows a summary to
  confirm, and installs through the same path as a config file. On a real
  terminal it runs as a **full-screen clr-installer-style menu**
  (bubbletea/bubbles/lipgloss): a hub of sections — disk, a **manual
  partition editor**, passphrase, keyboard, language, timezone, hostname,
  admin user — each showing its value and a ✓/✗, edited in any order, with
  an Install button that lights up only once the layout is complete
  (a disk, a root, an ESP, a user, and a passphrase if encrypting), beside
  Cancel. The partition editor adds, deletes and nests entries: plain,
  encrypted (`crypt`) or LVM partitions, with logical volumes under an
  LVM group — the full layout the config file can express. Over a serial
  console or a pipe it falls back to the plain prompt flow; both build the
  same config.
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
