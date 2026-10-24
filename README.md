# slinstaller

The system installer for **Sunlight Linux**, a UEFI-only Linux distribution.

> **Status: early bootstrap.** This repository is being seeded. slinstaller is
> a new installer, written in **Go** as a port and adaptation of
> [clr-installer](https://github.com/clearlinux/clr-installer): its core
> subsystems are ported from clr-installer and reduced to what Sunlight Linux
> installs, and reads clr-installer's config format so an existing
> clr-installer `config.yaml` runs here.

## What it does

slinstaller partitions the target disk, lays down the Sunlight Linux base
system, installs the bootloader, and configures the first boot — including
[slinit](https://github.com/sunlightlinux/slinit) as PID 1.

## Target requirements

- **UEFI firmware.** Sunlight Linux is UEFI-only: the installer creates a GPT
  layout with an EFI System Partition (ESP) and registers a boot entry with
  `efibootmgr`. BIOS/MBR boot is not supported.

## Building / Running

Implementation is in progress; the parts that exist build and run today.

```bash
go build ./...                      # produces the slinstaller binary
go test ./...
go run ./cmd/slinstaller demo/uefi-basic.yaml   # print a partitioning plan (dry run)
```

Without `--apply`, slinstaller only prints the plan and touches nothing.

> ⚠️ The installer performs **destructive, irreversible** operations
> (partitioning, formatting, bootloader install). Always exercise changes in a
> VM (QEMU + OVMF for UEFI) against a scratch image — never on a disk with data
> you care about.

## Where this fits

slinstaller is one of the five projects in the Sunlight Linux tree, wired up
via [slmanifests](https://github.com/sunlightlinux/slmanifests):

```bash
repo init -u https://github.com/sunlightlinux/slmanifests.git -b main
repo sync -j4
# slinstaller lands at src/installer/
```

## Documentation

- [CONTRIBUTING.md](CONTRIBUTING.md) — how to contribute (and the safety rules)
- [SECURITY.md](SECURITY.md) — reporting security issues
- [CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md) — community standards
- [CLAUDE.md](CLAUDE.md) / [EXAMPLES.md](EXAMPLES.md) — guidelines for
  LLM-assisted work on the installer

## License

[Apache License 2.0](LICENSE).
