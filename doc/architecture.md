# Architecture

slinstaller is built around one document and one boundary.

## The descriptor is the document

Everything in slinstaller either produces a descriptor or consumes one.
A descriptor (`pkg/descriptor`) describes an installation completely:
the target disk and its partition layout, identity, accounts, network,
software source, services, bootloader and hooks. It serialises to YAML,
so it is also the file an unattended install is driven by and the record
an installed system keeps of how it was built.

The descriptor is slinstaller's own model. It is derived from what
installing Sunlight Linux actually requires, not translated from any
other installer's internal types — a zero descriptor is deliberately not
installable, and nothing in it defaults to a destructive value.

## Front-ends produce, the core consumes

That is the boundary, and it is the architecture rather than a
convention:

```
  front-ends                    core
  ----------                    ----
  cmd/slinstaller  --\
  (a YAML file)       \
                       >--->  descriptor.Descriptor  --->  controller  --->  pkg/storage, ...
  (terminal UI)       /         (validated)
  (graphical UI)   --/
```

A front-end's only job is to produce a valid descriptor. It must not
perform an installation step, and it must not know how a step is carried
out — how slinit materialises a service, how a partition is formatted. A
front-end that does either has crossed the boundary this design exists
to hold. Today there is one front-end, `cmd/slinstaller`, which reads a
descriptor from a file; a terminal and a graphical front-end are the
obvious later additions, and they produce the same document.

## The core is a port of clr-installer

The core — the subsystems that actually install — is ported from
[clr-installer](https://github.com/clearlinux/clr-installer), package by
package, into `pkg/`. "Port" means the mechanisms are clr-installer's;
the inputs and the scope are ours:

- It is driven by our descriptor, not clr-installer's `BlockDevice`
  model.
- It is reduced to what Sunlight installs — UEFI + GPT + an ESP. RAID,
  LVM, encryption and clr's advanced paths are not carried over. They
  arrive if and when a layout needs them, not before.
- Clear Linux specifics — `swupd`, telemetry, bundles — are dropped in
  favour of the Void lineage slinstaller inherits (`xbps`, slinit).

### Ported so far

- **`pkg/storage`** — partition the disk and make its filesystems.
  `parted` for the GPT label and partitions, `sgdisk` for type codes,
  `mkfs.*` / `mkswap` for filesystems. The destructive work is a command
  list computed purely from the descriptor (`Plan.Commands`); nothing
  writes to a disk except `RunCommands`, so there is one gate to hold and
  one list to audit.

### Next

- Mount the target tree in `MountOrder`, bind-mount `proc`/`sys`/`dev`
  for chroot, and write `fstab` by UUID.
- A controller that sequences the subsystems from a descriptor.
- Base-system install (`xbps`), bootloader registration (`efibootmgr`),
  and first-boot configuration (slinit as PID 1).

## Safety

An installer destroys data by design, so the destructive path is kept
narrow and visible:

- **Dry run by default.** `cmd/slinstaller` prints the plan and touches
  nothing; `--apply` runs exactly that plan.
- **Gated apply.** `--apply` requires root and makes the operator retype
  the target disk path.
- **Authorisation in the document.** A layout cannot be declared without
  `wipe: true`, because a descriptor travels between machines and the
  guard must travel with it.
