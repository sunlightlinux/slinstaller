# Port status

Where slinstaller stands against [clr-installer](https://github.com/clearlinux/clr-installer),
package by package. slinstaller reimplements clr-installer's mechanisms in
its own code and reads its config format; it is not a copy, and it drops
everything Clear Linux- or systemd-specific.

_Snapshot: 2026-11-14. Update as the port proceeds._

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
| syscheck | 69 | pre-install checks | 🟡 | preflight: tools + disk exist + repo reachable. clr's CPU-feature checks ⛔ Clear |
| massinstall | 244 | unattended from file | ✅ | the file front-end (`slinstaller config.yaml`) |
| args/utils/progress | ~1255 | CLI/helpers | ✅ | own (flags, stdlib, VerboseRunner) |
| tui | 6742 | full-screen UI | ✅ | `pkg/tui` — a full-screen wizard in clr's Required/Advanced layout (bubbletea), and a plain prompt flow for serial consoles and pipes. Adds Sunlight's own kernel sections (see the README) |
| gui | 1959 | GTK GUI | ⛔ | — |
| isoutils | 757 | ISO generation | ⛔ | disk installer, not an image builder |
| telemetry | 414 | Clear telemetry | ⛔ | — |
| kernel | 89 | Clear kernel bundles | 🔁 | clr's bundle names map to Sunlight's `linux` / `linux-lts` packages; any other value is a package name |

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
  the bootloader are only proven by an install in a VM under UEFI firmware.
