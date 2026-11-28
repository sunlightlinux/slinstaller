# Port status

Where slinstaller stands against
[clr-installer](https://github.com/clearlinux/clr-installer)'s
feature set, package by package. slinstaller reads clr-installer's config
format and provides the same mechanisms in code of its own — clr-installer
is GPL-3.0 and a design reference only (see
[architecture.md](architecture.md#the-core-covers-clr-installers-feature-set-in-its-own-code))
—
and it drops everything Clear Linux- or systemd-specific. "Ported" below
means the feature exists in slinstaller, not that code was carried over.

_Snapshot: 2026-11-28, v2.3.1 (2.3.0 and 2.3.1 added no feature). Update as
the port proceeds._

## Legend

- ✅ **ported** — reimplemented, working (own code)
- 🔁 **replaced** — clr's Clear-specific version swapped for the Sunlight one
- 🟡 **partial** — the common path works; gaps noted
- ❌ **not yet** — portable, not done
- ⛔ **won't port** — Clear/systemd-specific or a UI toolkit we don't use

## By clr-installer package

| clr pkg | ~LOC | role | status | slinstaller |
|---------|-----:|------|--------|-------------|
| storage | 4984 | partition/mkfs/mount/fstab/crypt/RAID/LVM | ✅/❌ | `pkg/storage` + `pkg/luks` + `pkg/lvm` + `pkg/mdadm` (partition, mkfs, mount, fstab, LUKS, LVM, software RAID, swap file, fill-disk, reuse of existing partitions, install into free space). ❌ loop/image, advanced/server-desktop validation modes |
| model | 1130 | config schema | 🔁 | `pkg/config` — reads clr's schema |
| controller | 1131 | install sequencing | ✅ | `pkg/controller`. Offline installs use a local repository on the live medium. Gap: ISO gen (⛔) |
| network | 922 | network config | 🔁 | `pkg/network` — dhcpcd (clr uses systemd-networkd/NM); + connectivity check |
| swupd | 727 | Clear base install | 🔁 | `pkg/xbps` — base install via xbps. `swupdMirror` names the xbps repository, `autoUpdate` installs a daily update service, `offline` uses a local repository. Clear's bundle names are carried but ignored (`packages` names real ones) |
| user | 601 | accounts | ✅ | `pkg/system` — useradd, password hash, wheel+sudo, SSH keys, GECOS, validation, root locked once an administrator exists. ❌ PAM temp config |
| encrypt | 53 | LUKS | ✅ | `pkg/luks` |
| hostname/timezone/keyboard/language | ~580 | first-boot | ✅ | `/etc/hostname`, `/etc/localtime`, `KEYMAP` in `/etc/rc.conf`, `/etc/locale.conf` + libc-locales |
| proxy | 84 | proxy | ✅ | httpsProxy → xbps env |
| syscheck | 69 | pre-install checks | 🟡 | preflight: tools + disk exist + repo and mirror reachable + the package set dry-run against them. clr's CPU-feature checks ⛔ Clear |
| massinstall | 244 | unattended from file | ✅ | the file front-end (`slinstaller config.yaml`) |
| args/utils/progress | ~1255 | CLI/helpers | ✅ | own (flags, stdlib, VerboseRunner) |
| tui | 6742 | full-screen UI | ✅ | `pkg/tui` — a full-screen wizard in clr's Required/Advanced layout (bubbletea), and a plain prompt flow for serial consoles and pipes. Adds Sunlight's own kernel sections (see [config.md](config.md#kernel-settings)) |
| gui | 1959 | GTK GUI | ⛔ | — |
| isoutils | 757 | ISO generation | ⛔ | disk installer, not an image builder |
| telemetry | 414 | Clear telemetry | ⛔ | — |
| kernel | 89 | Clear kernel bundles | 🔁 | clr's bundle names map to Sunlight's `linux` / `linux-lts` packages; any other value is a package name |

## Beyond clr-installer

slinstaller's own additions, as optional config keys (see
[config.md](config.md#beyond-clr-installers-format)): disks
named by serial, WWN, model or stable path; fstab mount options and btrfs
subvolumes; bootloader options (serial menu, removable-media path, no NVRAM,
other systems); `write-files`, `power-state`, the http and no-proxy settings;
swap sized from memory, RAID hot spares, whole-disk wipe modes. On the
command line: `--system-check`, a diagnostics archive on failure, an
install lock, and shell completions (`--completion bash|fish`).

Also: installing the base system from a tar or squashfs image (`image`),
describing a disk's present layout as a config (`--discover`), and shrinking
an existing ext2/3/4 partition to make room (`resize`).

For servers: spare ESPs on the other disks of an array, LVM and LUKS on
RAID, volume groups across partitions; bonds, VLANs, bridges and static
routes. Also: a static interface applied to the live system from the
full-screen installer, ntfs shrinking, kept partitions checked against the
disk, whole-disk images (`disk-image`), `--resume`, `--unmount`,
`--in-target`, a config from a URL, progress reports (`report-url`), and
clr-installer's role names in the partition table read by `--discover`.

Later still: crypttab options, GPT entry and filesystem identifiers, fstab
pass, RAID metadata, hooks after any stage, `install-type`, a fallback
kernel, images from a URL, btrfs shrinking, the shrink offered from Safe
Installation, and `--log-level`.

Verified on real block devices by the `loop` CI job: a plain install's table
and filesystems, `--discover`, shrinking ext4, ntfs and btrfs with the data
intact, btrfs subvolumes with their mount options, and a partitioned RAID 1
array with an ESP on each disk. Everything past the
partition and mount stages - the bootloader, a first boot, RAID with a disk
removed, bonds and VLANs on the installed system - still wants a VM.
A whole install, every stage, also runs in a user namespace with real
mounts and recorded or stand-in commands
(`pkg/controller/e2e_ns_test.go`), for plain, encrypted, LVM,
LVM-on-LUKS, RAID 1, encrypted-`/home` and reuse layouts; it checks the
files the stages write, not that the result boots.

And the last of both lists: `--resume-from` any stage, partitioned RAID
arrays, `kernel-remove`, `--json-yaml` and installer messages
(`message-url`).

Then `power-state: kexec` and `system-upgrade`.

For later, by decision: zfs, bcache, erofs, iSCSI, multipath and NVMe over
TCP.

Not done, because it cannot be: shrinking xfs, which the filesystem does not
allow. Everything else left out is another distribution's own (its package
manager, its boot manager, its telemetry, its image builder) or a platform
this installer does not
target (s390, BIOS/MBR, extlinux).

## What's left

**Portable, not done:**

1. loop/image (`MakeImage`) — installing into an image file. Niche; a config
   that asks for it is refused by validation rather than misread.
2. clr's advanced / server-desktop validation modes.
3. PAM temporary config during account creation.

**Done (this is the bulk of a disk installer):** partition, LUKS, LVM,
software RAID, reuse and free-space installs, swap file, fill-disk, base
install, GRUB-EFI, fstab, first-boot
(hostname/timezone/keymap/locale/users/sudo/ssh, root lock), dhcpcd
networking, hooks + `$chrootDir`/`$chrooted`, httpsProxy, kernel-arg
add/remove, config validation, save-config, resolv.conf, preflight
(tools/disk/repo, with a connectivity retry), offline installs, automatic
updates, three front-ends (file, full-screen wizard, prompt flow), disk
discovery, a QEMU/OVMF demo.

## Coverage, honestly

- **The disk-installer engine and both interactive front-ends are ported.**
  What is left of the portable part is the short list above.
- **The rest of clr-installer is not ours to port**: the GTK GUI, ISO
  generation, telemetry and the Clear-specific parts of swupd.
- **Ported is not the same as verified.** The unit suite covers the logic
  through recorded commands and stubbed sysfs; partitioning, mkfs, mounts and
  the bootloader are only proven by an install in a VM under UEFI firmware,
  which is done by hand and not recorded per release (each release's
  CHANGELOG entry says how far it was verified before the cut).
