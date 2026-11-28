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

> **Interim:** the base system still comes mostly from **Void's**
> official repo; only Sunlight's own packages do not. A default install asks
> for `sunlight-base` (Void's `base-system` with runit taken out and slinit
> and its boot services put in), `grub-x86_64-efi` and `slpam`, and boots
> **slinit**. xbps searches, in order, the live medium's own repository
> (`--local-repo`, default `/usr/share/slinstaller/repo`), the signed
> Sunlight mirror (`--extra-repo`), then Void (`--repo`), and takes each
> package from the first that has it (so the mirror's copies of Void
> packages win too). With neither a local repository nor the mirror,
> preflight refuses a default install. `--packages base-system,grub-x86_64-efi,glibc-locales` still
> installs a plain runit system. See [doc/packaging.md](doc/packaging.md).

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
| **Encryption** | LUKS on any partition typed `crypt`, or with `encrypted: true` on a `type: lvm` partition or a RAID array (cryptsetup, crypttab, `rd.luks` cmdline, initramfs rebuild) |
| **LVM, RAID** | volume groups and logical volumes (`lvm2`), software RAID arrays (`mdadm`) — see [Servers](#beyond-clr-installers-format) |
| **Swap** | a swap partition, or a swap file (`swapFileSize`) |
| **Base system** | `xbps-install` from a repository, with the signing keys and a chrooted reconfigure |
| **Bootloader** | GRUB for UEFI (`grub-install` + `grub-mkconfig`), kernel arguments from the config |
| **Kernel settings** | command-line profiles, console log level, CPU governor and P-State mode, kdump reservation — see [Kernel settings](#kernel-settings) |
| **First boot** | hostname, timezone, console keymap, locale, user accounts (with `wheel` sudo for admins and SSH keys) |
| **Network** | `dhcpcd` (DHCP by default; static interfaces become `/etc/dhcpcd.conf` stanzas) |
| **Hooks** | pre/post-install commands, chrooted or not, with `$chrootDir`/`$chrooted` |

With no config file it runs interactively: a full-screen wizard on a real
terminal, or a plain prompt flow over a serial console or a pipe (`--text`
forces the latter). Deliberately out of scope: BIOS/MBR, systemd, Clear Linux
specifics.

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

The plan is every command the partition stage runs — `parted`,
`cryptsetup`, `mdadm`, `pvcreate`/`vgcreate`/`lvcreate`, `mkfs` — in the
order `--apply` runs them; what the live system still holds on those
disks (arrays, volume groups, old signatures) is listed after it, as
what is released first. What `--apply` would refuse on this machine (a
disk in use, one shared with a disk the install leaves, a volume group
name already taken) is said as "it will refuse"; the dry run still exits
0 — that is a fact of the machine, not of the config — while
`--system-check` exits non-zero for it.

Useful flags: `--repo`/`--keys`/`--arch`/`--packages` for the base
install, `--root` for the target mount tree, and `--through <stage>` to run
only up to a stage (for VM testing). An encrypted layout takes its
passphrase from `--crypt-file`, `SLINSTALLER_LUKS_PASSPHRASE` or a prompt —
never from the config. `slinstaller --help` lists every flag;
`slinstaller --completion bash` (or `fish`) prints a completion script
for them (see [`completions/`](completions)).

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

### Validation rules

Besides the obvious (one root, an ESP, known filesystems, crypt hashes
rather than passwords), these are refused before any disk is touched,
because each used to fail after the wipe or install a system that did not
work:

- **Disk names** are kernel device names (`sda`, `nvme0n1`, `dm-0`):
  letters, digits and `. _ : + -`; to name a disk by serial or path use
  `match:`. No two devices of the layout may end up at one path (a disk
  `md0` beside an array `md0`).
- **Partition names.** On a new table a disk's partitions are named 1..n,
  in order, on their own disk (`sda1`, `sda2`, …), and the partition that
  fills the disk is the last on it. On a Safe/`reusePartitions` install, a
  `create` partition is named with the number parted will give it (the
  lowest free one); the dry run says so when it is not. A partition is
  not both `create` (cut now) and `preserve` (kept).
- **Encryption.** `encrypted:` is read on `type: lvm` partitions and on
  raid arrays only; a plain partition is encrypted with `type: crypt`. A
  volume group's physical volumes are all encrypted or none. A keyfile
  is only made on an encrypted root: an encrypted `/home`, swap or
  `/boot` on a plain root asks for its passphrase at boot.
- **LVM.** Volume group and logical volume names are ones LVM accepts
  (letters, digits, `+ _ . -`, not starting with `-`, not a name LVM keeps
  for itself). Logical volume sizes are checked; a number without a unit
  is bytes, as everywhere else in the config. A group name the machine
  already has on a disk the install does not wipe is refused.
- **Sizes** are bytes, or a number with a unit as clr-installer reads it:
  `K`, `M`, `G`, `T`, `P`, alone or followed by `B` or `iB`, any case, all
  powers of 1024 (`512M`, `150MiB`, `2.6GiB`, `10GB` = 10 × 2^30).
  `swapFileSize: 0` makes no swap file; otherwise it is at least 1 MiB.
  An xfs filesystem is at least 300 MiB (mkfs.xfs's minimum), on the LUKS
  mapper when it is encrypted.
- **Mountpoints** are clean absolute paths without spaces (`/home`, not
  `/home/`), not under `/proc`, `/sys`, `/dev` or `/run`. A swap has no
  mountpoint, and vfat cannot be the root. `mount-options` and
  `crypt-options` are comma-separated without spaces.
- **Administrators.** At least one administrator has a password, unless
  the config sees to sudo itself: a `write-files` entry under
  `/etc/sudoers*`, or post-install or stage hooks. (Root is locked once
  there is an administrator, and sudo asks for a password.)
- **Other disks.** Wiping a disk (or formatting a partition, in a reuse
  install) that is one member of a RAID array, ZFS pool or multi-device
  btrfs with a member on another disk is refused — assembled or not — as
  is a disk sharing an active array or volume group; `--force-destructive`
  overrides it. The teardown checks its own listing again, so a disk
  plugged in after the confirmation cannot slip an array in. RAID array
  names are one path component (`md0`, `root`).
- **Packages.** With `xbps-install` on the live system, the package set
  is dry-run against the repositories (`-n`, into a scratch root); a
  package xbps cannot find or resolve is refused. The Sunlight mirror
  and `swupdMirror` must be reachable; `swupdMirror` is an `https://`
  URL (`http://` with `allowInsecureHTTP`) or an absolute directory.
- **Network.** `gateway` and `routes` are IPv4 (IPv6 routes come from
  router advertisements); `dns` is IP addresses separated by spaces;
  interface names are letters, digits and `. _ : -`.
- **Packages and hooks.** `packages`, `kernel`, `kernel-fallback` and
  `kernel-remove` are package names (not beginning with `-`, no space or
  `/`); an `env:` name is not empty and holds no `=`, space or control
  character, nor begins with `-`.
- **Kernel arguments.** An argument holding `'`, `$`, `;`, `\`, `` ` ``
  or a line break is refused: GRUB's own parser of `grub.cfg` breaks it.
- **Plain http.** A `report-url`, an `image` or a `disk-image` over
  `http://` needs `allowInsecureHTTP` (`--allow-insecure-http`), as a
  repository and a config named by a URL do.

`skipValidationSize` (`--skip-validation-size`) turns off the size and
disk-fit checks. `skipValidationAll` (`--skip-validation-all`) also turns
off the rest of the config check, the package dry run, and the time zone,
keymap, language and kernel arguments checked against this machine. It
does not turn off what keeps a value from reaching a device or a boot
script it should not: disk, RAID, volume group and logical volume names,
interface names, bond modes, package and env names, fstab and crypttab
fields, and control characters (a line break) in the hostname and network
fields are still checked, and so are the other disks and a plain http
repository.

Config keys nothing reads are not refused, but each is named in a warning
(`encryptd: true` would otherwise install unencrypted without a word).

### Beyond clr-installer's format

These keys are slinstaller's own. All are optional, and a config that uses
none of them is a plain clr-installer config.

```yaml
targetMedia:
- type: disk
  match: { serial: S5GXNX0T123456 }     # or wwn, model, path: /dev/disk/by-id/...
  wipe: discard                          # superblock (default), zero, random, discard
  children:
  - { type: part, fstype: vfat, mountpoint: /boot, size: 512M }
  - { type: part, fstype: swap, size: auto, mount-options: discard }
  - type: part
    fstype: btrfs
    mountpoint: /
    mount-options: noatime,compress=zstd
    subvolumes:
    - { name: "@",     mountpoint: / }
    - { name: "@home", mountpoint: /home }
bootloader:
  terminal: serial        # the boot menu on the kernel's serial console too
  removable: true         # also install at EFI/BOOT/BOOTX64.EFI
  update-nvram: true      # false leaves the firmware's boot entries alone
  probe-other-os: false   # true lists other systems in the menu
httpProxy: http://proxy:3128
noProxy: localhost,10.0.0.0/8
power-state: poweroff     # reboot, poweroff, halt or kexec when the install ends
write-files:
- { path: /etc/motd, content: "Welcome.\n", permissions: "0644" }
```

- **`match`** names the disk by what does not change between boots. Exactly
  one disk has to fit; none, or several, stops the install.
- **`size: auto`** (a swap partition or volume, or `swapFileSize`) is sized
  from the machine's memory, between 1 and 8 GiB.
- **`spare: true`** on a `type: raid` partition makes it a hot spare.
- **`wipe`** other than `superblock` goes over the whole disk and takes as
  long as the disk is large.

**Installing from an image.** `image: base.tar.zst` (or `--image`) unpacks a
root filesystem image — a tar archive or a squashfs — instead of installing
packages. The image is the whole base system: kernel, GRUB and whatever the
layout needs to boot have to be in it.

**Installing beside what is there.** `--discover /dev/sda` prints the disk's
present layout as a config that keeps every partition; edit it to say where
the new system goes. An existing ext2/3/4, ntfs or btrfs partition can give up room for it:

```yaml
reusePartitions: true
targetMedia:
- name: sda
  type: disk
  children:
  - { name: sda1, type: part, fstype: vfat, mountpoint: /boot/efi, preserve: true }
  - { name: sda2, type: part, fstype: ext4, preserve: true, resize: 40G }   # shrunk, data kept
  - { name: sda3, type: part, fstype: ext4, mountpoint: /, create: true }   # in the room that leaves
```

The filesystem is shrunk first and the partition after it, and a `create`
partition with no `start` goes in the largest free region. Shrinking a
filesystem is not something to do without a backup.

**Servers.** A root on RAID is only redundant if the machine can still boot
with a disk gone, so a second disk can carry a spare ESP; and an array can
hold a volume group or an encrypted volume rather than a filesystem:

```yaml
targetMedia:
- name: sda
  type: disk
  children:
  - { name: sda1, type: part, fstype: vfat, mountpoint: /boot/efi, size: 512M }
  - { name: sda2, type: raid, raid: md0 }
- name: sdb
  type: disk
  children:
  - { name: sdb1, type: part, fstype: vfat, size: 512M, esp: true }   # spare ESP
  - { name: sdb2, type: raid, raid: md0 }
raid:
- name: md0
  level: 1
  encrypted: true            # LUKS on the array (optional)
  volumeGroup: vg0           # the array is the group's physical volume
  children:
  - { name: root, fstype: ext4, mountpoint: /, size: 40G }
  - { name: var,  fstype: xfs,  mountpoint: /var }
networkInterfaces:
- name: bond0
  bond: { members: [eth0, eth1], mode: 802.3ad }
- name: bond0.100
  vlan: { id: 100, link: bond0 }
  addrs: [{ ip: 10.0.100.2, netmask: 255.255.255.0 }]
  gateway: 10.0.100.1
  routes: [{ to: 10.1.0.0/16, via: 10.0.100.254 }]
report-url: https://deploy.example/events   # progress, stage by stage, as JSON
```

A spare ESP needs the main one at `/boot/efi` (at `/boot` the kernels would
live on one disk only). Several `type: lvm` partitions may share a
`volumeGroup`. Bonds, VLANs and bridges are created at each boot by a
service the installer writes; a `bridge: { members: [...] }` entry works the
same way.

**A whole-disk image.** `disk-image: disk.raw.zst` with a single disk in
`targetMedia` (no layout) writes the image over the disk and stops: the image
already is the partitioned, configured system.

More on the command line: the config may be an `https://` URL (`http://`
needs `--allow-insecure-http`); `--resume`
continues an install that failed installing the base system without
partitioning again, and `--resume-from <stage>` does so from any later
stage — also after a reboot of the live medium: the encrypted volumes,
arrays and volume groups are brought back first, and the passphrase is
asked once. A resume needs the mark an unfinished install leaves in its
target (`/.slinstaller-in-progress`, made at the mount, removed when
the install completes): without it the disk holds no install to resume,
and nothing is done to it; `--json-yaml old.json` converts the JSON descriptor of
clr-installer's predecessor into a config; `--unmount` takes down what a `--through` run left
mounted and `--in-target "cmd"` runs a command inside it. A partition table
whose partitions are named `CLR_BOOT`, `CLR_ROOT`, `CLR_SWAP`,
`CLR_MNT_/path` (with `_F` to format; one holding no filesystem at all is
formatted anyway, as in clr-installer) comes out of `--discover` already
saying what goes where.

**Finer points**, all optional:

| key | on | what it does |
|---|---|---|
| `crypt-options: discard` | an encrypted volume | extra crypttab options (the kernel command line, for the root) |
| `partition-type`, `partition-uuid`, `partition-name` | a partition | its entry in the GPT |
| `uuid` | a filesystem | its UUID (a volume id for vfat) |
| `passno`, `freq` | a filesystem | its fstab fsck pass and dump frequency |
| `metadata: "1.0"` | a `raid` entry | the array's superblock format |
| `stage-hooks: { partition: [...] }` | the config | commands after a named stage |
| `install-type: server` / `desktop` | the config | a larger smallest root (4 GB / 20 GB) |
| `kernel-fallback: linux-lts` | the config | the kernel to try when the configured one cannot be installed |
| `power-state-delay: 30` | the config | seconds before the machine reboots or powers off |
| `system-upgrade: true` | the config | the installed system brought up to date before the bootloader is set up |
| `kernel-remove: [linux6.18]` | the config | kernel packages removed once the base system is in |
| `partitions: [...]` | a `raid` entry | a partition table on the array itself (`/dev/md0p1`, …) |
| `message-url` | the config | where installer messages are looked for and shown |

`power-state: kexec` loads the installed kernel and starts it directly,
without going back through the firmware.

`image` and `disk-image` may be `https://` URLs: the image is unpacked or
written as it downloads, so it only has to fit on the target. `resize` also
works on btrfs, and Safe Installation in the full-screen installer offers to
shrink a partition when the disk has no unallocated space. `--log-level 2`
keeps the terminal to the stages and the result.

On the command line, `--system-check config.yaml` runs every pre-install
check and stops without changing anything. A failed install leaves a
diagnostics archive in `/tmp` (`--diagnostics` names it): the error, the log,
the disks, and the config without its password hashes. Only one `--apply`
runs at a time.

## Kernel settings

The wizard's kernel sections all end up as ordinary config, so a saved
config replays them:

```yaml
kernel-profile: Server            # which profile the arguments came from
kernel-arguments:
  add:
  - mitigations=auto
  - transparent_hugepage=madvise
  - cpufreq.default_governor=schedutil
  - console=ttyS0,115200          # mirrored from the live system
console-log-level: 3              # 0 (emergency) to 7 (debug)
```

| Key / section | What it does |
|---------------|--------------|
| `kernel-arguments` | `add` goes on the GRUB command line; `remove` is left out of it |
| `kernel-profile` | names the Kernel Command Line profile used (Server by default). The install recomputes what the profile derives from the hardware — see below |
| `console-log-level` | sets `loglevel=N` on the command line and `kernel.printk = N 4 1 7` in `/etc/sysctl.d/10-printk.conf`; overrides a `loglevel=` among the arguments |
| Kernel Command Line | a profile picker (Server, Desktop, Realtime, Telco/RAN, virtualization, GPU, …) or Custom arguments; Details shows what a profile adds |
| CPU Frequency Governor | `cpufreq.default_governor=`; only the governors the chosen driver mode has are offered |
| AMD / Intel P-State Driver | `amd_pstate=` / `intel_pstate=` for the isolation profiles; shown only on the matching CPU |
| Crash Dump (kdump) | `crashkernel=` and `panic=`. Reserves the memory only — see the limit below |

`kernel-profile` and `console-log-level` are Sunlight extensions to
clr-installer's format; both are optional and a clr-installer config without
them is unaffected.

Three things to know:

- **Arguments are adapted to the machine the wizard runs on.** IOMMU and
  nested-KVM arguments are written for the detected CPU vendor, and the
  Realtime and Telco/RAN profiles compute `isolcpus=`, `nohz_full=`,
  `rcu_nocbs=`, `irqaffinity=` and the hugepages (1 GiB pages, or 2 MiB
  ones on an x86 CPU without them) from that
  machine's CPUs, NUMA nodes and RAM, and the saved config holds the result.
  Those values belong to the hardware, so when `kernel-profile` names a
  profile, `--apply` recomputes them for the machine it runs on and reports
  what changed: the vendor arguments for any profile (`intel_iommu`↔`amd_iommu`,
  `kvm-intel`↔`kvm-amd`, and the P-State mode where the other vendor has the
  same one), and the CPU lists and hugepages for the isolating profiles. Every
  other argument is installed as written, so a saved config moves between
  machines, and between Intel and AMD. A config with `kernel-profile: Custom`
  (or none) is never rewritten — there `--apply` only refuses, before
  touching the disk, a CPU list naming CPUs the machine lacks or hugepages
  that would leave it under 1 GiB.

  Two things the install does not put right, left to services on the
  installed system: `intel_idle.max_cstate`, dropped when a config goes to
  AMD, is not restored if it later returns to Intel; and a governor chosen
  for a P-State mode the other vendor lacks is kept as written.
- **The profiles run on the stock kernel.** They rely on dynamic preemption
  (`preempt=full`), `nohz_full` and RCU callback offloading, which the
  packaged `linux` kernel has. That is low-latency tuning, not a
  `PREEMPT_RT` kernel.
- **kdump is not complete yet.** `crashkernel=` reserves the memory, but
  nothing on the installed system loads a crash kernel into it or saves the
  dump: that needs `kexec-tools`, `makedumpfile` and a boot service, which
  the installer does not set up.

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
- [doc/port-status.md](doc/port-status.md) — what is ported from clr-installer, what is left
- [CHANGELOG.md](CHANGELOG.md) &middot; [STABILITY.md](STABILITY.md) — what changed, what is stable
- [CONTRIBUTING.md](CONTRIBUTING.md) · [SECURITY.md](SECURITY.md) · [CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md)
- [CLAUDE.md](CLAUDE.md) / [EXAMPLES.md](EXAMPLES.md) — guidelines for LLM-assisted work

## License

[Apache License 2.0](LICENSE).
