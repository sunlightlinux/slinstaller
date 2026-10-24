# Architecture

slinstaller is built around one document and one boundary.

## The config is the document

Everything in slinstaller either produces a config or consumes one. The
config describes an installation completely: the target disk and its
partition layout, identity, accounts, network, software and hooks. It is
YAML, so it is also the file an unattended install is driven by.

The config is **clr-installer's format**, not a format of our own. A
clr-installer `config.yaml` loads here and describes the same install —
that compatibility is the point. `pkg/config` is slinstaller's own
parser for that schema (`targetMedia` and its partition children, plus
`users`, `networkInterfaces`, `keyboard`, `language`, `timezone`,
`kernel-arguments`, `bundles`, pre/post hooks); only the file format is
shared, not the code. Parsing is tolerant: the lsblk probe fields a real
clr-installer config carries and the Clear Linux specifics it names load
without error, and sizes are accepted in every form clr-installer writes.

This is the same relationship slinit has to dinit — a Go reimplementation
that stays compatible with the upstream's config, with its own code.

## Front-ends produce, the core consumes

That is the boundary, and it is the architecture rather than a
convention:

```
  front-ends                    core
  ----------                    ----
  cmd/slinstaller  --\
  (a config file)     \
                       >--->  config.Config  --->  controller  --->  pkg/storage, ...
  (terminal UI)       /        (validated)
  (graphical UI)   --/
```

A front-end's only job is to produce a valid config. It must not perform
an installation step, and it must not know how a step is carried out —
how slinit materialises a service, how a partition is formatted. A
front-end that does either has crossed the boundary this design exists to
hold. Today there is one front-end, `cmd/slinstaller`, which reads a
config from a file; a terminal and a graphical front-end are the obvious
later additions, and they produce the same document.

## The core is a port of clr-installer

The core — the subsystems that actually install — is ported from
[clr-installer](https://github.com/clearlinux/clr-installer), package by
package, into `pkg/`. "Port" means the mechanisms and semantics are
clr-installer's; the code and the scope are ours:

- It is driven by `config.Config` (clr-installer's schema), reduced to a
  `storage.Plan`, not by clr-installer's `BlockDevice` model.
- It is reduced to what Sunlight installs — UEFI + GPT + an ESP. RAID,
  LVM, encryption and clr's advanced paths are not carried over. They
  arrive if and when a layout needs them, not before.
- Clear Linux specifics — `swupd`, telemetry, bundles — are dropped in
  favour of the Void lineage slinstaller inherits (`xbps`, slinit).

### Ported so far

- **`pkg/config`** — reads clr-installer's config schema and produces a
  `storage.Plan` from the target disk's layout.
- **`pkg/storage`** — partition the disk and make its filesystems.
  `parted` for the GPT label, the partitions and the ESP type,
  `mkfs.*` / `mkswap` for filesystems. The destructive work is a command
  list computed purely from the plan (`Plan.Commands`); nothing writes to
  a disk except `RunCommands`, so there is one gate to hold and one list
  to audit.

### Next

- Mount the target tree parent-first, bind-mount `proc`/`sys`/`dev` for
  chroot, and write `fstab` by UUID.
- A controller that sequences the subsystems from a `config.Config`.
- Base-system install (`xbps`), bootloader registration (`efibootmgr`),
  and first-boot configuration (slinit as PID 1).

## Safety

An installer destroys data by design, so the destructive path is kept
narrow and visible:

- **Dry run by default.** `cmd/slinstaller` prints the plan and touches
  nothing; `--apply` runs exactly that plan.
- **Gated apply.** `--apply` requires root and makes the operator retype
  the target disk path.
- **Repartition is explicit.** A config that declares a `targetMedia`
  layout repartitions that disk — the same meaning clr-installer gives
  it — so the plan wipes it, and the apply gate above is what stands
  between that plan and the disk.
