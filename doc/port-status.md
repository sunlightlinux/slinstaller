# Port status

Where slinstaller stands against [clr-installer](https://github.com/clearlinux/clr-installer),
package by package. slinstaller reimplements clr-installer's mechanisms in
its own code and reads its config format; it is not a copy, and it drops
everything Clear Linux- or systemd-specific.

_Snapshot: 2026-10-24. Update as the port proceeds._

## Legend

- ✅ **ported** — reimplemented, working (own code)
- 🔁 **replaced** — clr's Clear-specific version swapped for the Sunlight one
- 🟡 **partial** — the common path works; gaps noted
- ❌ **not yet** — portable, not done
- ⛔ **won't port** — Clear/systemd-specific or a UI toolkit we don't use

## By clr-installer package

| clr pkg | ~LOC | role | status | slinstaller |
|---------|-----:|------|--------|-------------|
| storage | 4984 | partition/mkfs/mount/fstab/crypt/RAID/LVM | ✅/❌ | `pkg/storage` + `pkg/luks` (partition, mkfs, mount, fstab, LUKS, swap file, fill-disk). ❌ RAID, LVM, loop/image, advanced/server-desktop validation modes |
| model | 1130 | config schema | 🔁 | `pkg/config` — reads clr's schema |
| controller | 1131 | install sequencing | ✅ | `pkg/controller`. Gaps: ISO gen (⛔), offline content (⛔) |
| network | 922 | network config | 🔁 | `pkg/network` — dhcpcd (clr uses systemd-networkd/NM); + connectivity check |
| swupd | 727 | Clear base install | 🔁 | `pkg/xbps` — base install via xbps. Rest (bundles/mirror/offline/autoupdate) ⛔ Clear-specific |
| user | 601 | accounts | ✅ | `pkg/system` — useradd, password hash, wheel+sudo, SSH keys, GECOS, validation. ❌ lock-root, PAM temp config |
| encrypt | 53 | LUKS | ✅ | `pkg/luks` |
| hostname/timezone/keyboard/language | ~580 | first-boot | ✅ | `/etc/hostname`, `/etc/localtime`, `KEYMAP` in `/etc/rc.conf`, `/etc/locale.conf` + libc-locales |
| proxy | 84 | proxy | ✅ | httpsProxy → xbps env |
| syscheck | 69 | pre-install checks | 🟡 | preflight: tools + disk exist + repo reachable. clr's CPU-feature checks ⛔ Clear |
| massinstall | 244 | unattended from file | ✅ | the file front-end (`slinstaller config.yaml`) |
| args/utils/progress | ~1255 | CLI/helpers | ✅ | own (flags, stdlib, VerboseRunner) |
| tui | 6742 | full-screen UI | 🟡 | `pkg/tui` — interactive prompt flow (no deps, serial-friendly). ❌ full-screen |
| gui | 1959 | GTK GUI | ⛔ | — |
| isoutils | 757 | ISO generation | ⛔ | disk installer, not an image builder |
| telemetry | 414 | Clear telemetry | ⛔ | — |
| kernel | 89 | Clear kernel bundles | ⛔ | kernel comes from base-system |

## What's left, by priority

**Portable, worth doing:**

1. **TUI full-screen** (bubbletea) over the prompt-flow skeleton — the one
   large portable gap. Adds dependencies; decision pending.
2. **LVM**, then **RAID** — advanced storage. Complex, rare for Sunlight.
3. loop/image (`MakeImage`) — niche (building disk images).
4. Small: lock-root option; connectivity retry.

**Done (this is the bulk of a disk installer):** partition, LUKS, swap
file, fill-disk, base install, GRUB-EFI, fstab, first-boot
(hostname/timezone/keymap/locale/users/sudo/ssh), dhcpcd networking,
hooks + `$chrootDir`/`$chrooted`, httpsProxy, kernel-arg add/remove,
config validation, save-config, resolv.conf, preflight (tools/disk/repo),
two front-ends (file + interactive), disk discovery, a QEMU/OVMF demo.

## Coverage, honestly

- **The disk-installer engine that matters: ~85–90%.** Everything that
  installs a working UEFI system, plus validation and encryption.
- **clr-installer as a whole: ~35–40%** — but roughly half of clr is
  TUI + GUI + Clear-only code, most of which is not ours to port.
- **The one big portable gap is the full-screen TUI.** The rest is small
  polish, advanced storage (LVM/RAID), or out of scope.
