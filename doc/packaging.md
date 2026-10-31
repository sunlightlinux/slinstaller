# Packaging slinstaller onto a live ISO

This is the hand-off for putting slinstaller onto a Sunlight Linux live
ISO. slinstaller is the installer that runs *on* the live medium; it
partitions the target, installs the base system into it, sets up the
bootloader, and configures first boot. It does none of that work itself —
it drives the system's own tools (`parted`, `cryptsetup`, `xbps-install`,
`grub-install`, …), so the live ISO must carry those tools, and the target
packages must carry theirs.

## 1. The binary

slinstaller is a single static Go binary with no shared-library
dependencies — drop it anywhere on the ISO (`/usr/bin/slinstaller`):

```sh
CGO_ENABLED=0 go build -ldflags "-s -w -X main.version=<version>" \
    -o slinstaller ./cmd/slinstaller
```

`CGO_ENABLED=0` keeps it statically linked; `-s -w` strips it;
`-X main.version=` stamps `slinstaller --version`. It builds for the ISO's
architecture (set `GOARCH` to cross-build).

## 2. Tools the live ISO must carry

slinstaller shells out to these on the live system; the install fails in
preflight (before touching a disk) if one needed for the chosen layout is
missing. Package names below are Void's — the interim base — and move with
the repo.

**Always:**

- `parted` — partitioning (no `sgdisk`/`gptfdisk` needed)
- `util-linux` — `partprobe`, `blkid`, `lsblk`, `mkswap`, `fallocate`
- `dosfstools` — `mkfs.vfat` for the ESP
- `e2fsprogs` — `mkfs.ext4` (and fsck at boot on the target)
- `xbps` — `xbps-install` fetches the base system
- `coreutils` — `chroot`, `chmod`
- `openssl` — hashes the password typed in the interactive installer
- `eudev` — `udevadm settle` after partitioning
- a network stack **and CA certificates** — the base system is fetched over
  HTTPS, so DNS, a route and trust anchors must be up on the live system

**Per layout (only if the operator picks that filesystem/feature):**

- `btrfs-progs` — btrfs · `xfsprogs` — xfs · `f2fs-tools` — f2fs
- `cryptsetup` — LUKS (any `type: crypt`, or an encrypted LVM/`/boot`)
- `lvm2` — LVM (`pvcreate`/`vgcreate`/`lvcreate`)
- `mdadm` — RAID

If the ISO should handle every layout out of the box, include all of
these. The installer's preflight names any that are missing.

## 3. xbps signing keys

The base-system install verifies package signatures, so the repository's
signing keys must be present on the live system at **`/var/db/xbps/keys`**.
During the interim (Void's repo) these are Void's keys; when the
sunlightlinux repository exists, ship its keys here and point
`--repo`/the config's `swupdMirror` at it.

## 3a. The local repository: slinit

slinit is not in any remote repository yet, so the ISO carries its own:
an xbps repository at **`/usr/share/slinstaller/repo`** holding the
`slinit`, `slinit-services` and `sunlight-base` packages from slpkgs,
with its index (`scripts/make-iso.sh` in sunlight-os builds it):

```sh
mkdir -p <rootfs>/usr/share/slinstaller/repo
cp slinit-*.xbps slinit-services-*.xbps sunlight-base-*.xbps \
    <rootfs>/usr/share/slinstaller/repo/
xbps-rindex -a <rootfs>/usr/share/slinstaller/repo/*.xbps
```

slinstaller passes it to `xbps-install` ahead of the remote repository
(`--local-repo` names others; a path without an index is skipped). A
local repository needs no signing key — xbps verifies signatures only
for remote ones. If the package set asks for one of these and no local
repository is found, the install stops in preflight, before any disk is
touched.

## 4. Tools that run in the target, not on the ISO

These are invoked **inside the chroot** of the freshly installed system, so
they come from the *target* packages, not the live ISO:

- `grub-install`, `grub-mkconfig`, `efibootmgr` — from `grub-x86_64-efi`
- `xbps-reconfigure` — from `xbps`
- `useradd` — from `shadow`
- `dracut` — builds the initramfs (crypt/lvm/mdraid modules, keyfile embed)

The default target package set is `sunlight-base, grub-x86_64-efi`;
slinstaller adds the filesystem/feature tools the layout
needs (`btrfs-progs`, `xfsprogs`, `f2fs-tools`, `cryptsetup`, `lvm2`,
`mdadm`) to what the target installs, so the installed system can fsck,
mount, unlock and assemble its own storage at boot.

## 5. Firmware: UEFI only

Sunlight Linux is UEFI-only. The ISO **must boot in UEFI mode**, so that
`/sys/firmware/efi/efivars` exists — slinstaller bind-mounts it into the
chroot and `grub-install`/`efibootmgr` need it to register the boot entry.
A legacy/BIOS boot of the ISO cannot install.

## 6. Running it

- `slinstaller` with no arguments → the interactive installer. On a real
  terminal it is the full-screen menu; over a serial console or a pipe it
  falls back to the text prompt flow. `--text` forces the text flow.
- `slinstaller --apply config.yaml` → an unattended install from a config
  file (clr-installer's format; see `demo/vm/*.yaml` for worked examples,
  including LUKS, LVM, LVM-on-LUKS and RAID). Without `--apply` it prints
  the partitioning plan and touches nothing — a dry run.
- It must run as **root**, and it confirms the target disk before wiping.
- The LUKS passphrase is never in the config: it is prompted, or taken from
  `SLINSTALLER_LUKS_PASSPHRASE` for an unattended encrypted install.

To autostart it on the live medium, launch `slinstaller` on the console
login (e.g. from the live user's shell profile or a getty), leaving a
shell available if the operator exits it.

## 7. Interim vs. target state

The installed system boots **slinit**, and has no runit. Its base is
**`sunlight-base`**: Void's `base-system` with runit-void taken out and
`slinit-services` put in. The two cannot share a system — both own
`/usr/bin/init` and `halt`/`poweroff`/`reboot`/`shutdown` — and
`slinit-services` declares the conflict, so xbps refuses the pair
rather than letting one silently overwrite the other. Everything else
in the base, and the packages themselves, still come from Void's
official repository; only slinit, its services and the metapackage
come from the local one.

slinstaller also puts `init=/usr/bin/slinit` on the kernel command line
whenever slinit is in the target (`/usr/bin/init` points at it too), and
writes `/etc/machine-id`: slinit reads it as PID 1 before any service
runs, and Void creates none.

`--packages base-system,grub-x86_64-efi,glibc-locales` still installs a
plain runit system, with no local repository needed — the demo VMs,
which run without the ISO, need this.

When Sunlight has a remote repository of its own:

- point `--repo` / the config's `swupdMirror` at the sunlightlinux repo and
  ship its keys in `/var/db/xbps/keys`;
- the local repository becomes unnecessary.

Nothing in slinstaller's code is Void-specific beyond these defaults (the
repo URL, the keys path, and the package names in
`cmd/slinstaller/main.go`); the mechanisms — parted, mkfs, xbps bootstrap,
GRUB-EFI, dracut, crypttab — are Sunlight's own.
