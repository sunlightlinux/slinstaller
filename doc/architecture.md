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

### The packages

- **`pkg/config`** — reads clr-installer's config schema and produces a
  `storage.Plan` from the target disk's layout.
- **`pkg/storage`** — partition the disk and make its filesystems.
  `parted` for the GPT label, the partitions and the ESP type,
  `mkfs.*` / `mkswap` for filesystems. The destructive work is a command
  list computed purely from the plan (`Plan.Commands`) and run by
  `RunCommands`, so there is one list to audit. The one exception is
  `cryptsetup`, which the controller runs itself because it is fed the
  passphrase on stdin.
- **`pkg/luks`, `pkg/lvm`, `pkg/mdadm`** — encrypted volumes, volume groups
  and software RAID arrays, layered under the filesystems.
- **`pkg/blockdev`** — discovers the machine's disks and what is in use.
- **`pkg/controller`** — sequences the install from a `config.Config`:
  partition, mount (parent-first, with `proc`/`sys`/`dev` bound for chroot),
  base install, `fstab` by UUID, bootloader, first boot, network.
- **`pkg/xbps`** — the base-system install.
- **`pkg/grub`** — GRUB for UEFI and the kernel command line.
- **`pkg/system`** — first-boot configuration (hostname, time zone, keymap,
  locale, accounts, slinit services) and the machine detection the wizard's
  kernel sections use.
- **`pkg/network`** — `dhcpcd` configuration and the connectivity check.
- **`pkg/tui`** — the full-screen wizard and the plain prompt flow.

What is still to port is tracked in [port-status.md](port-status.md).

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
