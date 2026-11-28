# slinstaller

**The system installer for Sunlight Linux, written in Go.** It runs as
root from a UEFI-booted live medium, partitions the target disk, installs
the base system with xbps, sets up GRUB for UEFI and configures a first
boot under [slinit](https://github.com/sunlightlinux/slinit).

| Project | Details |
|---|---|
| **Latest release** | v2.3.1 (2026-11-28), a git tag — see [CHANGELOG.md](CHANGELOG.md). Unreleased on `main`: a `govulncheck` CI job |
| **Verified so far** | Unit suite (95% coverage floor), fuzzing, whole installs in a user namespace with stand-in tools, and the partition and mount stages on real loop devices — see [Testing](#testing). **No 2.x release has been installed in a VM and booted.** The installs recorded as booting date from October 2026, before 2.0.0; the one whose init is recorded (f815a96) came up under runit, and none booting slinit as PID 1 is recorded |
| **Stability** | [STABILITY.md](STABILITY.md) — early bootstrap: flags, output and Go packages may change; the one contract is reading clr-installer's config format |
| **Requires** | Go 1.25+ to build (`go.mod` names `toolchain go1.27.4`, the one CI builds and tests with); to run: root, UEFI firmware, and the tools listed in [doc/packaging.md](doc/packaging.md) |
| **License** | [Apache 2.0](LICENSE) |

slinstaller reads the config format of
[clr-installer](https://github.com/clearlinux/clr-installer), Clear Linux's
installer, so an existing clr-installer `config.yaml` describes the same
install here, and its wizard follows clr-installer's layout. clr-installer
is GPL-3.0 and slinstaller is Apache-2.0, so clr-installer is a design
reference only: the config schema and the behaviour are implemented, its
code is not used. The base system comes from the Void lineage (xbps,
Void's repository) with Sunlight's own packages — `sunlight-base`,
slinit, `slpam` — from the live medium's repository or the signed Sunlight
mirror; see [doc/packaging.md](doc/packaging.md) for the order in which
xbps searches them.

**Deliberately out of scope:** BIOS/MBR booting, systemd, and Clear Linux's
own parts (swupd, bundles, telemetry); a graphical front-end and ISO
generation; zfs, bcache, erofs, iSCSI,
multipath and NVMe over TCP (by decision, for later); and shrinking xfs,
which the filesystem does not allow.

> **New here?** Read [doc/config.md](doc/config.md): the config format,
> the rules a config is checked against before any disk is touched, and
> the keys slinstaller adds to clr-installer's. Building a live ISO? Read
> [doc/packaging.md](doc/packaging.md).

## Build

```bash
go build -o slinstaller ./cmd/slinstaller
```

For a live medium, build it static and stamped (`CGO_ENABLED=0`,
`-X main.version=`); [doc/packaging.md](doc/packaging.md) has the line and
what the ISO must carry.

## Quick start

slinstaller is destructive, so it defaults to a dry run and makes you
confirm. A minimal config is at the top of
[doc/config.md](doc/config.md); [`demo/`](demo) and [`demo/vm/`](demo/vm)
hold worked ones (LUKS, LVM, LVM on LUKS, RAID 1, reuse).

```bash
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
target disk missing, a partition, of size 0 or zram, a disk in use, one
shared with a disk the install leaves, a volume group name already
taken, a Safe install's new partition on a disk that is not GPT or that
parted would number otherwise) is said as "it will refuse"; the dry run
still exits 0 — that is a fact of the machine, not of the config — while
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
> [`demo/vm`](demo/vm), a harness that boots a Void live ISO under OVMF,
> hands in the binary over a 9p share, installs to a scratch disk and
> boots the result. Never on a disk with data you care about.

## What it does

The install runs as an ordered sequence of stages; `--through` stops after
any of them, and `--resume-from` restarts an unfinished install from
`base-install` or a later stage:

```
pre-install hooks → partition → mount → bind /proc,/sys,/dev
  → base system (xbps) → fstab → swap file → bootloader (GRUB-EFI)
  → first-boot config → network → save config → post-install hooks
```

| Area | What it does |
|------|--------------|
| **Disk** | GPT, an EFI System Partition, partitions by `parted`; `mkfs.*` / `mkswap`; the last partition can fill the disk |
| **Encryption** | LUKS on any partition typed `crypt`, or with `encrypted: true` on a `type: lvm` partition or a RAID array (cryptsetup, crypttab, `rd.luks` cmdline, initramfs rebuild) |
| **LVM, RAID** | volume groups and logical volumes (`lvm2`), software RAID arrays (`mdadm`) — see [Servers](doc/config.md#beyond-clr-installers-format) |
| **Swap** | a swap partition, or a swap file (`swapFileSize`) |
| **Base system** | `xbps-install` from a repository, with the signing keys and a chrooted reconfigure |
| **Bootloader** | GRUB for UEFI (`grub-install` + `grub-mkconfig`), kernel arguments from the config |
| **Kernel settings** | command-line profiles, console log level, CPU governor and P-State mode, kdump reservation — see [Kernel settings](doc/config.md#kernel-settings) |
| **First boot** | hostname, timezone, console keymap, locale, user accounts (with `wheel` sudo for admins and SSH keys) |
| **Network** | `dhcpcd` (DHCP by default; static interfaces become `/etc/dhcpcd.conf` stanzas) |
| **Hooks** | pre/post-install commands, chrooted or not, with `$chrootDir`/`$chrooted` |

With no config file it runs interactively: a full-screen wizard on a real
terminal, or a plain prompt flow over a serial console or a pipe (`--text`
forces the latter). Both produce the same config a file would, and the
same core installs it — see [doc/architecture.md](doc/architecture.md).

On a target that has slinit and its boot service, the bootloader stage
adds `init=/usr/bin/slinit` to the kernel command line unless the config
sets an `init=` of its own; a default install (`sunlight-base`) is such a
target. `--packages base-system,grub-x86_64-efi,glibc-locales` installs a
plain Void system with runit instead.

## Documentation

| Document | What it covers |
|---|---|
| [doc/config.md](doc/config.md) | The config: format, validation rules, slinstaller's own keys, kernel settings |
| [doc/architecture.md](doc/architecture.md) | The config as the one document, the front-end/core boundary, the clean-room rule toward clr-installer, the packages, the safety gates |
| [doc/packaging.md](doc/packaging.md) | Putting slinstaller on a live ISO: build line, tools, keys, local repository, offline installs |
| [doc/port-status.md](doc/port-status.md) | Coverage of clr-installer's feature set, package by package, and what is left |
| [demo/vm/README.md](demo/vm/README.md) | The QEMU + OVMF harness: building, installing inside the guest, booting the result |
| [tests/README.md](tests/README.md) | The loop-device stage tests and every CI job |
| [completions/README.md](completions/README.md) | bash and fish completions, generated from the flags |
| [third_party/bubbletea/README.slinstaller.md](third_party/bubbletea/README.slinstaller.md) | Why bubbletea is vendored, and what was left out |
| [CHANGELOG.md](CHANGELOG.md) | Every release, with what each one verified |
| [STABILITY.md](STABILITY.md) | What may and may not change between releases |
| [SECURITY.md](SECURITY.md) | Reporting a vulnerability; the security-relevant areas |
| [CONTRIBUTING.md](CONTRIBUTING.md) · [CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md) | How to contribute |
| [CLAUDE.md](CLAUDE.md) · [EXAMPLES.md](EXAMPLES.md) | Guidelines for LLM-assisted work, with installer examples |

Cross-project documents in
[sldocs](https://github.com/sunlightlinux/sldocs/tree/e87265b7c88cda95b234eb1d42c7e9faa19f2916):
the [install pipeline
spec](https://github.com/sunlightlinux/sldocs/blob/e87265b7c88cda95b234eb1d42c7e9faa19f2916/specs/slinstaller-install-pipeline.md);
the requirements that an
[installed system boots
slinit](https://github.com/sunlightlinux/sldocs/blob/e87265b7c88cda95b234eb1d42c7e9faa19f2916/requirements/slinstaller-installed-system-boots-slinit.md)
and [updates from the Sunlight
mirror](https://github.com/sunlightlinux/sldocs/blob/e87265b7c88cda95b234eb1d42c7e9faa19f2916/requirements/slinstaller-target-updates-from-sunlight-mirror.md)
(both partially met); and four findings — the
[mirror silently
dropped](https://github.com/sunlightlinux/sldocs/blob/e87265b7c88cda95b234eb1d42c7e9faa19f2916/findings/slinstaller-mirror-silently-dropped.md),
[`skipValidationAll` widening
destruction](https://github.com/sunlightlinux/sldocs/blob/e87265b7c88cda95b234eb1d42c7e9faa19f2916/findings/slinstaller-skipvalidationall-widened-destruction.md),
the [terminal-query
stall](https://github.com/sunlightlinux/sldocs/blob/e87265b7c88cda95b234eb1d42c7e9faa19f2916/findings/slinstaller-terminal-query-stall.md)
and [2.3.0 built with a vulnerable
Go](https://github.com/sunlightlinux/sldocs/blob/e87265b7c88cda95b234eb1d42c7e9faa19f2916/findings/slinstaller-built-with-vulnerable-go.md).

## Testing

```bash
go vet ./... && go test ./...
sudo tests/loop/run.sh ./slinstaller     # partition and mount stages on loop devices
```

| Layer | What it proves | Where |
|---|---|---|
| Unit suite | The logic, through recorded commands and stubbed sysfs; CI runs it with `-race` and a 95% coverage floor | beside the code under `pkg/`, `cmd/` |
| User namespace | A whole install, every stage, with real mounts and stand-in tools, for plain, encrypted, LVM, LVM-on-LUKS, RAID 1, encrypted-`/home` and reuse layouts; it checks the files the stages write, not that the result boots | `pkg/controller/e2e_ns_test.go` and the other `*_ns_test.go` |
| Fuzzing | 15 targets that check properties, 20 s each in CI | `Fuzz*` in `pkg/`, `cmd/` |
| Loop devices | Partitioning, `--discover`, shrinking ext4/ntfs/btrfs with data intact, btrfs subvolumes, a partitioned RAID 1 | [tests/loop](tests/loop) |
| Dry run | Every demo config, and a `--template` config, still plans | CI `dry-run` job |
| VM install | GRUB, a real `xbps-install`, the first boot — the only test of what follows the mount. Manual, not in CI; not run for any 2.x release | [demo/vm](demo/vm) |

[tests/README.md](tests/README.md) describes each CI job.

## Contributing

Read [CONTRIBUTING.md](CONTRIBUTING.md) first: test destructive changes
in a VM, never on a real disk; keep every destructive step gated; UEFI
only. Within the Sunlight tree, slinstaller lands at `src/installer/`
through [slmanifests](https://github.com/sunlightlinux/slmanifests):

```bash
repo init -u https://github.com/sunlightlinux/slmanifests.git -b main
repo sync -j4
```

## License

[Apache License 2.0](LICENSE).
