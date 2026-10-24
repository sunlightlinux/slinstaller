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
  decimal+unit). Tested against clr-installer's own fixtures.
- `pkg/storage` — the first core subsystem ported from
  [clr-installer](https://github.com/clearlinux/clr-installer), reduced
  to a single UEFI target: GPT labelling, partitioning with `parted`,
  GPT type codes with `sgdisk`, and filesystem creation with `mkfs.*` /
  `mkswap`. The destructive work is expressed as a command list
  (`Plan.Commands`) that executes nothing until handed to `RunCommands`,
  so a dry run shows exactly what an apply would do. Also mounts the
  target tree parent-first (`Mounter`), bind-mounts `proc`/`sys`/`dev`
  for chroot, and renders `fstab` by UUID (`Plan.Fstab`) — listing every
  mount, since Sunlight boots slinit and has no systemd gpt-auto to defer
  standard mounts to.
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
- `pkg/system` — the universal part of first-boot config: hostname
  (`/etc/hostname`), timezone (the `/etc/localtime` symlink), and user
  accounts (`useradd` with the crypt-hash password via `-p`, `wheel` for
  admins, and `authorized_keys` for any SSH keys). Keymap, locale and
  network wait on slinit's conventions.
- `pkg/controller` — sequences the whole install from a config:
  pre-install hooks → partition → mount → bind meta filesystems →
  base-system install → fstab → bootloader → first-boot → post-install
  hooks. A full install runs end to end (gated by the front-end on root
  and disk confirmation); `Options.Through` stops after a named stage for
  a staged VM run, and a failure unmounts what it mounted.
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
