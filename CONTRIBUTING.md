# Contributing to slinstaller

Thank you for your interest in contributing to slinstaller, the Sunlight Linux system installer!

> **Status: early bootstrap.** The first iteration is a fork of
> **void-installer** — the installer shipped in
> [void-mklive](https://github.com/void-linux/void-mklive) — as a beta stopgap;
> the long-term target is a ground-up rewrite in **Go**, modeled on
> [clr-installer](https://github.com/clearlinux/clr-installer). Match whichever
> codebase the change you are making touches.

## Reporting Issues

- Use the GitHub issue tracker.
- Installers are environment-sensitive: include the firmware mode (must be **UEFI**), the target disk layout, what you were installing, and the exact failure.
- **Never paste passphrases or other secrets** from an install log.

## Submitting Changes

1. Fork the repository
2. Create a feature branch (`git checkout -b installer/my-change`)
3. Make your changes
4. Test in a VM — QEMU + OVMF for UEFI — **never against a real disk**
5. Commit with a clear message
6. Push to your fork and open a Pull Request

## Safety Rules (read first)

- **Destructive operations are gated.** Anything that partitions, formats, or writes a bootloader must be explicit and confirmable. Never widen what gets wiped as a side effect of an unrelated change.
- **UEFI-only.** Sunlight Linux does not support BIOS/MBR boot. Don't add legacy boot paths without an explicit decision.
- **Test in a VM.** Partition / format / bootloader steps must be exercised in QEMU + OVMF against a scratch image, not on hardware.

## Code Style

- **Go target:** standard Go conventions (`gofmt`, `go vet`); keep changes focused; add tests for new logic.
- **Stopgap (void-installer fork):** match void-installer's shell style; keep POSIX `sh` where it already is.

## License

By contributing, you agree that your contributions will be licensed under the Apache License 2.0.
