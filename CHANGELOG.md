# Changelog

All notable changes to slinstaller are documented here. Format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and
[Semantic Versioning](https://semver.org/spec/v2.0.0.html).

slinstaller is in early bootstrap: there are no releases yet, and
nothing below is a stability promise. See [STABILITY.md](STABILITY.md)
for what will and will not be allowed to change once releases begin.

## [Unreleased]

### Added

- `pkg/descriptor` — the document that describes an installation
  completely: target disk and partition layout, identity, accounts,
  network, software source, services, bootloader and hooks. Front-ends
  produce it; the controller consumes it. Includes `Validate`, which
  reports every checkable problem at once before anything is touched,
  and `MountOrder`, which sorts partitions so a parent mounts before
  anything beneath it.
- `pkg/storage` — the first core subsystem ported from
  [clr-installer](https://github.com/clearlinux/clr-installer), reduced
  to a single UEFI target: GPT labelling, partitioning with `parted`,
  GPT type codes with `sgdisk`, and filesystem creation with `mkfs.*` /
  `mkswap`. The destructive work is expressed as a command list
  (`Plan.Commands`) that executes nothing until handed to `RunCommands`,
  so a dry run shows exactly what an apply would do.
- `cmd/slinstaller` — the file front-end: reads a YAML descriptor,
  validates it, and prints the partitioning plan. With `--apply` it
  carries the plan out, gated on running as root and on the operator
  retyping the target disk path.
- `demo/uefi-basic.yaml` — a minimal, valid UEFI descriptor.
