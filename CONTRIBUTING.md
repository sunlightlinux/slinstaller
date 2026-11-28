# Contributing to slinstaller

Thank you for your interest in contributing to slinstaller, the Sunlight
Linux system installer!

> **Status: early bootstrap.** slinstaller is a new installer, written in
> **Go**, that reads the config format of
> [clr-installer](https://github.com/clearlinux/clr-installer). A config key
> slinstaller reads must mean what it means in clr-installer; a new behaviour
> gets a new key.
>
> **Licence: clean room.** clr-installer is GPL-3.0 and slinstaller is
> Apache-2.0, so clr-installer is a design reference only. Do not copy,
> translate or paraphrase its code, or mirror its structure; take behaviour
> from its documentation and example configs. [CLAUDE.md](CLAUDE.md) states
> the rule in full.

## Reporting Issues

- Use the GitHub issue tracker.
- Installers are environment-sensitive: include the firmware mode (must be
  **UEFI**), the target disk layout, what you were installing, and the exact
  failure.
- **Never paste passphrases or other secrets** from an install log.

## Submitting Changes

1. Fork the repository
2. Create a feature branch (`git checkout -b installer/my-change`)
3. Make your changes
4. Run `go vet ./...` and `go test ./...`; test destructive changes in a VM
— QEMU + OVMF for UEFI, see [demo/vm](demo/vm) — **never against a real
disk**
5. Commit with a clear message
6. Push to your fork and open a Pull Request

## Safety Rules (read first)

- **Destructive operations are gated.** Anything that partitions, formats,
  or writes a bootloader must be explicit and confirmable. Never widen what
  gets wiped as a side effect of an unrelated change.
- **UEFI-only.** Sunlight Linux does not support BIOS/MBR boot. Don't add
  legacy boot paths without an explicit decision.
- **Test in a VM.** Partition / format / bootloader steps must be exercised
  in QEMU + OVMF against a scratch image, not on hardware.

## Code Style

- Standard Go conventions (`gofmt`, `go vet`); keep changes focused; add
  tests for new logic.

## License

By contributing, you agree that your contributions will be licensed under
the Apache License 2.0.
