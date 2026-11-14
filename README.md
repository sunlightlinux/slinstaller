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
  `rcu_nocbs=`, `irqaffinity=` and the 1 GiB hugepage count from that
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
