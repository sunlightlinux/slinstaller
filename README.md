# slinstaller

The system installer for **Sunlight Linux**, a UEFI-only Linux distribution.
Written in **Go** as a port and adaptation of
[clr-installer](https://github.com/clearlinux/clr-installer): the core
subsystems are reimplemented (not copied) and reduced to what Sunlight
installs, and it reads clr-installer's config format, so an existing
clr-installer `config.yaml` runs here. This is the same relationship
[slinit](https://github.com/sunlightlinux/slinit) has to dinit.

A full install runs end to end under UEFI: it partitions and formats the
disk, lays down the base system, installs GRUB, and configures first boot.

> **Interim:** Sunlight has no package repository of its own yet, so until
> it does slinstaller installs the base system from **Void's** official
> repo. That means a test install currently comes up with **runit**, not
> slinit — enough to prove the installer end to end; slinit takes over once
> it is in a repo we control.

## What it installs

The install runs as an ordered sequence; each step is its own package:

```
pre-install hooks → partition → mount → bind /proc,/sys,/dev
  → base system (xbps) → fstab → swap file → bootloader (GRUB-EFI)
  → first-boot config → network → save config → post-install hooks
```

| Area | What it does |
|------|--------------|
| **Disk** | GPT, an EFI System Partition, partitions by `parted`; `mkfs.*` / `mkswap`; the last partition can fill the disk |
| **Encryption** | LUKS on any partition typed `crypt` (cryptsetup, crypttab, `rd.luks` cmdline, initramfs rebuild) |
| **Swap** | a swap partition, or a swap file (`swapFileSize`) |
| **Base system** | `xbps-install` from a repository, with the signing keys and a chrooted reconfigure |
| **Bootloader** | GRUB for UEFI (`grub-install` + `grub-mkconfig`), kernel arguments from the config |
| **First boot** | hostname, timezone, console keymap, locale, user accounts (with `wheel` sudo for admins and SSH keys) |
| **Network** | `dhcpcd` (DHCP by default; static interfaces become `/etc/dhcpcd.conf` stanzas) |
| **Hooks** | pre/post-install commands, chrooted or not, with `$chrootDir`/`$chrooted` |

Not yet: a full-screen TUI (there is an interactive prompt flow), LVM and
RAID. Deliberately out of scope: BIOS/MBR, systemd, Clear Linux specifics.

## Using it

slinstaller runs **as root from a live medium**, against the machine's
disk. It is destructive, so it defaults to a dry run and makes you confirm.

```bash
# Build
go build -o slinstaller ./cmd/slinstaller

# See the disks
./slinstaller --list-disks

# From a config file: print the plan (touches nothing), then carry it out
./slinstaller config.yaml
sudo ./slinstaller --apply config.yaml      # retype the disk path to confirm

# Or interactively (no config file): pick a disk and answer a few questions
sudo ./slinstaller
```

Useful flags: `--repo`/`--keys`/`--arch`/`--packages` for the base
install, `--root` for the target mount tree, and `--through <stage>` to run
only up to a stage (for VM testing). An encrypted layout takes its
passphrase from `SLINSTALLER_LUKS_PASSPHRASE` or a prompt — never from the
config.

> ⚠️ Partitioning, formatting and bootloader installs are **irreversible**.
> Try changes in a VM (QEMU + OVMF) against a scratch image — see
> [`demo/vm`](demo/vm), a reproducible harness that boots a live ISO and
> installs end to end. Never on a disk with data you care about.

## The config

The config is clr-installer's format. A minimal UEFI install:

```yaml
targetMedia:
- name: sda
  type: disk
  children:
  - { name: sda1, type: part,  fstype: vfat, mountpoint: /boot, size: 512M }
  - { name: sda2, type: part,  fstype: swap, size: 2G }
  - { name: sda3, type: part,  fstype: ext4, mountpoint: / }   # no size = fill
hostname: sunlight
keyboard: us
language: en_US.UTF-8
users:
- login: me
  password: "$6$...crypt hash..."   # never a plaintext password
  admin: true                        # wheel + sudo
```

Mark a partition `type: crypt` to encrypt it. `Validate` reports every
problem at once before any disk is touched. See [`demo/uefi-basic.yaml`](demo/uefi-basic.yaml)
and [`doc/architecture.md`](doc/architecture.md).

## Where this fits

slinstaller is one of the projects in the Sunlight Linux tree, wired up
via [slmanifests](https://github.com/sunlightlinux/slmanifests):

```bash
repo init -u https://github.com/sunlightlinux/slmanifests.git -b main
repo sync -j4
# slinstaller lands at src/installer/
```

## Documentation

- [doc/architecture.md](doc/architecture.md) — the config, the boundary, the port
- [CHANGELOG.md](CHANGELOG.md) · [STABILITY.md](STABILITY.md) — what changed, what's stable
- [CONTRIBUTING.md](CONTRIBUTING.md) · [SECURITY.md](SECURITY.md) · [CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md)
- [CLAUDE.md](CLAUDE.md) / [EXAMPLES.md](EXAMPLES.md) — guidelines for LLM-assisted work

## License

[Apache License 2.0](LICENSE).
