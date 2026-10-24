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
  so a dry run shows exactly what an apply would do.
- `cmd/slinstaller` — the file front-end: reads a config, validates it,
  and prints the partitioning plan. With `--apply` it carries the plan
  out, gated on running as root and on the operator retyping the target
  disk path.
- `demo/uefi-basic.yaml` — a minimal UEFI install in clr-installer's
  config format.
