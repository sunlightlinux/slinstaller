# Changelog

All notable changes to slinstaller are documented here. Format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and
[Semantic Versioning](https://semver.org/spec/v2.0.0.html).

slinstaller is in early bootstrap: nothing below is a stability promise.
See [STABILITY.md](STABILITY.md) for what will and will not be allowed to
change.

## [Unreleased]

### Changed

- The full-screen confirmation selects **Cancel** to begin with: one Enter
  no longer starts erasing a disk (Tab to Install, then Enter). It names
  every disk with its size and model, and an install that keeps the
  partition table lists what it shrinks, creates, keeps and formats
  instead of saying the disk is erased. Its hint no longer says Esc goes
  back (it leaves).
- The text flow and the retyped confirmation show each disk's size and
  model, not the path alone.
- A text-flow answer that is neither of the choices is asked again: a typo
  (`btfrs`, `ys` at "Encrypt?") used to be taken as the default, so an
  unencrypted or ext4 system was installed. An ended input still gets the
  default (no, at "Proceed?").
- The full-screen wizard checks the config it built (validation and the
  partition plan) when Install is pressed, and shows what is wrong while
  the answers are still there. A bad hostname or a duplicate mountpoint
  used to end the program after the wizard, and every section had to be
  filled in again.
- The text flow checks the swap size and the hostname as they are typed
  and asks again; a typo there used to end the flow after every question
  had been answered. A swap size of 0 says to choose none, not that a
  partition "fills its disk but is not the last".
- `--help` exits 0 instead of reporting "flag: help requested" as an error.

### Fixed

- A user's full name with a colon in it was refused by `useradd` at first
  boot, after the disk was wiped; it is refused by the config check now
  (the wizard's included).
- Replaying a profile on a CPU of the other vendor turned an explicit
  `intel_iommu=off` into `amd_iommu=on` (and back): an IOMMU switched off
  to get past broken firmware came back on. Off stays off now, and
  `kvm_intel.nested=1` is translated like `kvm-intel.nested=1`.
- A language spelled `en_US.utf8` passed the check but was never
  generated (the libc-locales line is `en_US.UTF-8`); the codeset is
  compared as glibc compares it.
- Two or more static routes on an interface were written on one
  `static routes=` line, which dhcpcd refuses whole: the interface came up
  with none. Each route has its own line now.
- An IPv6 gateway or static route passed validation, but dhcpcd's static
  `routers`/`routes` are IPv4 only and dropped it at boot. They are
  refused, saying that IPv6 routes come from router advertisements.
- Interface names, DNS servers and the search domain were barely checked,
  yet go unquoted into the boot script that makes bonds, VLANs and bridges
  (run as root) and into dhcpcd.conf: a name like `$(cmd)` or a DNS value
  with a newline passed. Names are letters, digits and `. _ : -`; DNS
  entries must be IP addresses (space-separated); domains must be domain
  names.
- A `disk-image` write reported success when the download or the
  decompression failed: the pipeline's status was `dd`'s alone, so a
  truncated image half-written to the disk, or a failed fetch, was
  "written" and the machine rebooted. Every stage is checked now. A signed
  URL (`img.raw.xz?sig=...`) is decompressed as its path says, and `.bz2`
  is read too.
- A swap file on a btrfs root was made copy-on-write (and compressed
  under `compress=`), which the kernel refuses: the install succeeded and
  `swapon` failed at every boot. It is marked No_COW before it is
  allocated.
- `/etc/mdadm.conf` on the installed system listed every array the live
  system had assembled (`mdadm --detail --scan`), data arrays on other
  disks included. It lists the install's own arrays now.
- An install from an image kept the image's machine-id, saved random seed
  and SSH host keys, so every machine installed from one image shared
  them. They are removed after the unpack; the first-boot stage writes a
  machine-id and seed of the machine's own, and the host keys are made
  anew (`ssh-keygen -A`).
- A resume after the keyfile was added to some encrypted volumes but not
  all skipped them all, and crypttab pointed the rest at a key they did
  not hold. Each volume the keyfile does not open is given it now.
- When the install-time resolv.conf could not be taken back out, the end
  of the install returned before unmounting, leaving the target mounted.
  It is unmounted either way now.
- `encrypted: true` on a plain partition, a RAID member or a logical
  volume was ignored and the volume installed unencrypted. It is read on
  an `lvm` partition (and a RAID array) only; elsewhere it is refused,
  pointing at `type: crypt` or at the array.
- **No planted file in /tmp is written through as root.** The default
  install log `/tmp/slinstaller.log` was opened with O_CREAT|O_TRUNC, so
  a symlink or hard link another user left there had root truncate and
  write the file it pointed to, a file of theirs got the log, and a FIFO
  hung the install. A link is no longer followed, anything at that path
  that is not root's own regular file is replaced by a fresh 0600 one,
  and the dated diagnostics archive is created exclusively.
- **An https URL can no longer be redirected to plain http.** Only the
  URL as given was checked: a config, progress report or remote image
  fetched over https followed a server's redirect to http. Such a
  redirect is now refused unless plain http is allowed
  (`--allow-insecure-http` / `allowInsecureHTTP`), and curl is run with
  `--proto-redir =https` for an https image. A plain http `report-url`
  now needs the same allowance.
- **Probe failures say why.** blkid, parted, lsblk, mdadm, cryptsetup and
  openssl run for their output reported only `exit status N` when they
  failed, dropping the tool's own message. That message is now part of
  the error.
- **A Safe install's shrink no longer stops when e2fsck fixes something.**
  `e2fsck -f -p` exits 1 or 2 after correcting errors, which counted as a
  failure and aborted the resize of a filesystem that was now clean. Exit
  codes 1 and 2 now let the shrink go on; 4 and above still stop it.
- **Absurd sizes are rejected instead of wrapping.** A config size of
  `inf`, `nan`, `1e30` or a byte count past 64 bits was accepted and
  converted to an arbitrary number, and a plan size like `16777216T`
  wrapped to 0. Both parsers now refuse them, and the disk-fit check
  saturates its sum, so an oversized layout fails before the disk is
  wiped rather than passing as a tiny one.
- **A static address's prefix and family are checked.** A numeric netmask
  such as `33`, `-1` or `200` was accepted and written to dhcpcd.conf as
  is, and an IPv4 address marked `version: 1` (or an IPv6 one marked 0)
  passed validation. A prefix must now be 0..32 for IPv4 and 0..128 for
  IPv6, and the IP must match its declared family.
- **A very long output line no longer hangs the TUI install.** The TUI
  runner read command output with a 1 MiB line limit and stopped reading
  at the first longer line; the command then blocked writing to the pipe
  and the install never finished. The rest of the output is now drained.
- **A root that fills the disk was never held to the root minimum.** The
  disk-fit check summed only the fixed sizes, so a 3 GB disk with a 512M
  ESP, 2G swap and a filling root was wiped and given a 0.3 GB root. What
  is left for each disk's filling partition now has to reach its minimum
  (the root's, by install type, for `/`). A disk that reports size 0 (an
  empty card reader) is refused as a target instead of skipping the
  check.
- **The language was never checked.** `language: xx_YY.garbage` was
  written to the target's `LANG` as given. It is now checked, like the
  timezone and keymap, against the locales the live system's C library
  lists (glibc's `SUPPORTED` or `/etc/default/libc-locales`), and left
  unchecked where there is no such list (musl).
- **A misspelt config key was silently ignored.** `encryptd: true`
  loaded without a word and the install was not encrypted. Keys nothing
  reads are now listed as warnings, with their path, where the plan
  ends; the config still loads (clr-installer's probe and telemetry keys
  are known and not warned about).
- **Two LUKS containers could get the same mapper name.** Every
  encrypted swap was `cryptswap`, `/var/lib` and `/var-lib` were both
  `cryptvar-lib`, and an encrypted array named `root` was `cryptroot`
  beside an encrypted root partition, so the second `luksOpen` failed
  after the disks were wiped. The later one now gets its device's name
  added (`cryptswap-sdb2`); two encrypted volumes of one group work too.
- **Mountpoints are checked for shape.** They were compared and written
  to fstab as given: `/home/` and `/home` both passed as different
  mountpoints, `/boot/../tmp` passed, and `/a b` produced a broken fstab
  line. A mountpoint now has to be a clean absolute path without spaces
  or control characters, and not under `/proc`, `/sys`, `/dev` or `/run`,
  on partitions, logical volumes, RAID arrays and their partitions, and
  btrfs subvolumes alike.
- **A swap or vfat partition could pass for the root.** Any entry
  mounted at `/` was counted as the root, so `{fstype: swap, mountpoint: /}`
  validated and the install had no root filesystem. A swap with a
  mountpoint and a vfat root are refused.
- An install from an image unpacked it with the live system's `/proc`,
  `/sys` and `/dev` bound into the target: the image's own `dev/`,
  `proc/` and `sys/` entries landed on the live ones, and its
  `etc/resolv.conf` was overwritten. The binds come off for the unpack
  and go back after; the image's resolv.conf is kept.
- Each install or resume added another set of firmware boot entries for
  the spare ESPs ("Sunlight (sdb)"), the new ones first in BootOrder.
  An entry with the same label is removed before it is made again.
- With an encrypted `/boot`, the initramfs keyfile was named without its
  key device (`rd.luks.key=<path>`), so dracut looked for it on every disk,
  never found it, and asked for the passphrase a second time. It is
  `rd.luks.key=<path>:/` now: the key is in the initramfs.
- The root volume's `discard` crypt option never reached the initramfs:
  dracut's crypt module reads no `rd.luks.options`. It is passed as
  `rd.luks.allow-discards=<uuid>` as well.
- The recovery-mode boot entries could not boot an encrypted, LVM or RAID
  root: every argument went into `GRUB_CMDLINE_LINUX_DEFAULT`, which
  recovery entries leave out. What the system cannot boot without
  (`rd.*`, `init=`, `root=`, `resume=`, `console=`) is in
  `GRUB_CMDLINE_LINUX` now. Kernel arguments are quoted for the shell
  that sources `/etc/default/grub`, so `$` and backticks are kept.
- A config whose administrators had no password between them (an SSH key
  only) installed a system nobody could become root on: root's password
  is locked once there is an administrator, and sudo asks for one. It is
  refused now, unless the config sees to sudo itself (a `write-files`
  entry under `/etc/sudoers*`, or hooks).
- A staged run (`--through`) skipped the disk checks along with the tool
  checks: it would partition and format a partition given as the disk, a
  disk in use, or one sharing an array with another. The disk checks now
  run whenever the partition stage does; only `--resume` goes without.
- Ctrl+C while installing quit the program at once: the command running
  was orphaned, the target stayed mounted and no diagnostics were written.
  It now asks the install to stop after the current step (full-screen and
  text alike, and SIGTERM too); the install unmounts, and the failure is
  reported with its diagnostics.
- Reinstalling over a RAID array or volume group spanning several disks
  failed while releasing the disks: each disk's teardown stopped the
  shared array again (`mdadm --stop /dev/md127`: no such device). The
  disks are released together, each holder once.
- **Partition names are checked against what parted makes.** A partition's
  device came from its `name` unchecked, and parted numbers partitions
  itself. A child named for another disk (`nvme0n1p2` under `sda`) was
  formatted though only `sda` was confirmed; a gap (`sda1`, `sda5`) or a
  duplicate (`sda1` twice) had mkfs hit a missing or the wrong partition
  after the table was wiped; an empty list entry left a gap. A new table
  now has to be numbered 1..n on its own disk, and the fill partition has
  to be the last on the disk, not in the list.
- **A Safe install could format an existing partition.** A `create`
  partition is cut by parted at the lowest free number, but formatted as
  the device its name (or position) gave: on a disk with `sda1`..`sda4`,
  a new partition the plan called `sda2` became `sda5`, and mkfs
  formatted the existing `sda2`. The partition stage now checks each
  number against the disk's table before anything is touched.
- On a preserved `/home`, the previous owner's links were followed: an
  `authorized_keys -> ../../../etc/shadow` made the ssh-keys step write
  over the new shadow file. Links in the home are replaced, a home that
  is itself a link is refused.
- The target's own `/etc/resolv.conf` was written through (a link to
  `/run/...` landed on the live system's file) or overwritten and then
  deleted (an image's). It is set aside for the install and put back.
- `SLINSTALLER_LUKS_PASSPHRASE` stayed in the environment of every command
  the install ran, package scripts and hooks included.
- A failed command's error printed the command unmasked: a `useradd -p`
  password hash reached the screen, the diagnostics archive and a
  `report-url`. Errors, dry runs and logs now mask hashes and the password
  in a URL (`https_proxy=http://user:***@proxy`); the diagnostics config
  masks URL passwords as well as account hashes.
- `system-upgrade` ran the target's `xbps-install -Syu` before the Sunlight
  mirror was written to its `/etc/xbps.d`, so the upgrade resolved every
  package against Void alone. The mirror is recorded first now.
- The end of an install (and `--unmount`) only detached the target's
  filesystems lazily, with no sync: "Install complete." could print while
  the data was still in the page cache, and a machine switched off then
  booted a dirty ESP. Everything is synced and unmounted properly now; a
  filesystem still held open is detached, and the log says which.

## [2.1.1] - 2026-11-18

Everything since 2.0.0. The code is that of 2.1.0, which was tagged before
this section was written; 2.1.1 adds the section and nothing else.

Every new config key is optional, and a config that uses none of them is a
plain clr-installer config that installs as it did.

What is verified, and how far: the unit suite runs on every push, and the
`loop` job runs the partition and mount stages on real block devices — a
plain install, `--discover`, shrinking ext4, ntfs and btrfs with the data
intact, btrfs subvolumes, and a partitioned RAID 1 with an ESP on each
disk. Everything past the mount — the bootloader, a first boot, `kexec`,
bonds and VLANs on the installed system, an install from an image, a
resumed install — is unit-tested only and still wants a VM.

### Added

**Naming and preparing the disk**

- `match` on a disk names it by serial, WWN, model or a `/dev/disk/by-*`
  path instead of its kernel name, which can change between boots. Exactly
  one disk has to fit; none, or several, stops the install. A
  `block-devices` alias may point at such a path too.
- `wipe` on a disk erases all of it before partitioning (`zero`, `random`,
  `discard`) where removing old signatures, the default, is not enough.
- `partition-type`, `partition-uuid` and `partition-name` set a partition's
  entry in the GPT.

**Filesystems and mounts**

- `mount-options` sets a filesystem's (or swap's) fstab options, and the
  filesystem's own options (`compress=zstd`, `discard`) already apply while
  the install writes to it.
- `subvolumes` splits a btrfs filesystem into subvolumes, each with its own
  mountpoint.
- `uuid` gives a filesystem its UUID; `passno` and `freq` are its fstab
  fsck pass and dump frequency.
- `size: auto` on a swap partition or volume, and `swapFileSize: auto`, size
  swap from the machine's memory, between 1 and 8 GiB.

**Encryption, LVM and RAID**

- `crypt-options` (e.g. `discard`) on an encrypted volume: into crypttab for
  one opened after boot, onto the kernel command line for the root.
- A `raid` entry may be `encrypted` (LUKS on the array), carry a
  `volumeGroup` with logical volumes (LVM on the array), both, or
  `partitions` — a partition table on the array itself.
- Several `lvm` partitions, on one disk or several, may share a volume
  group.
- `spare: true` makes a RAID member a hot spare; `metadata` chooses the
  array's superblock format.
- `esp: true` makes a vfat partition on a second disk a spare EFI System
  Partition: it is given a copy of the bootloader, the loader at the
  removable-media path and a firmware entry of its own, so a machine whose
  root is on RAID still boots with the first disk gone. It needs the main
  ESP at `/boot/efi`.

**Installing beside what is there**

- `--discover <disk>` prints the layout already on a disk as a config that
  keeps every partition, with the disk named by its serial. A partition
  table whose partitions are named for their roles in clr-installer's
  convention (`CLR_BOOT`, `CLR_ROOT`, `CLR_SWAP`, `CLR_MNT_/path`, `_F` to
  format) comes out already saying what goes where.
- `resize` shrinks an existing ext2/3/4, ntfs or btrfs partition in a
  `reusePartitions` install, keeping its data: the filesystem first, then
  the partition. xfs cannot be shrunk and is refused.
- A `create: true` partition with no `start` is placed in the disk's largest
  free region, found once any shrink is done.
- Safe Installation in the full-screen installer offers to shrink a
  partition when the disk has no unallocated space, and records the layout
  for the install to carry out after its own confirmation.
- A partition kept in a `reusePartitions` install is checked to hold what
  the config says it holds, and the confirmation says what each partition
  to be formatted holds now, and what is shrunk, kept and created.

**The base system**

- The Sunlight package mirror is layered ahead of the main repository by
  default (`--extra-repo`), its signing key built into the installer and
  seeded into the target, and it is recorded in the installed system's
  `/etc/xbps.d` so the system updates from it too.
- `image` (or `--image`) installs the base system by unpacking a tar or
  squashfs root filesystem image instead of installing packages. As an
  `https://` URL a tar image is unpacked as it downloads.
- `disk-image` writes a whole-disk image over the single disk in
  `targetMedia`, in place of an install; it too may be an `https://` URL.
- `system-upgrade` brings the installed system up to date from its
  repositories before the bootloader is set up.
- `kernel-fallback` names a kernel to try when the configured one cannot be
  installed; `kernel-remove` removes kernel packages once the base system
  is in place.

**Bootloader**

- `bootloader`: `terminal: serial` shows the boot menu on the kernel's
  serial console too; `removable` also installs the loader at the
  removable-media path; `update-nvram: false` leaves the firmware's entries
  alone; `probe-other-os` lists other systems in the menu.

**Network**

- `bond`, `vlan` and `bridge` interfaces and static `routes`. The links are
  created at each boot of the installed system by a service the installer
  writes; dhcpcd then addresses them.
- A static interface set in the full-screen installer is put in force on the
  live system too, so an install on a network without DHCP can reach its
  repository.
- `httpProxy` and `noProxy` complete `httpsProxy`.

**The installed system**

- `write-files` writes files into the installed system, kept inside the
  target whatever links a path crosses.
- A slinit target is given a random seed for its first boot
  (`/var/lib/seedrng/seed.no-credit`): the first boot had no previous one to
  have saved a seed, and is when the SSH host keys are generated.

**Running an install**

- `--system-check` runs the pre-install checks against a config and exits,
  changing nothing and needing no root.
- `--apply` takes a lock, so two installs cannot run at once.
- A failed install leaves a diagnostics archive — the error, the log, the
  disks, the config without its password hashes; `--diagnostics` names it.
- `--resume` continues an install that failed installing the base system
  without partitioning again, and `--resume-from <stage>` does so from any
  later stage. The stages are safe to run a second time.
- `--unmount` takes down what a `--through` run left mounted, and
  `--in-target` runs a command inside it.
- `stage-hooks` runs commands after a named stage, where `pre-install` and
  `post-install` only bracketed the whole install.
- `power-state` reboots, powers off or halts when the install ends, or with
  `kexec` starts the installed kernel directly; `power-state-delay` waits
  first.
- `report-url` is sent the install's progress as JSON, stage by stage, and
  its result.
- The config may be given as an `https://` URL.
- `install-type` (`server`, `desktop`) raises the smallest root a config may
  ask for to 4 GB or 20 GB.

**From clr-installer's command line**

- `--log-level`: at 2 or 1 the terminal shows only the stages and the
  result; the log file always has everything.
- `--json-yaml` converts the JSON descriptor of clr-installer's predecessor
  into a config.
- `message-url` shows installer messages fetched from a server before the
  install is confirmed and after it succeeds.

### Changed

- A dry run lists everything the install does to the disk: a whole-disk
  wipe, a shrink, an array's own partition table and btrfs subvolumes are
  shown with the rest.
- The confirmation of an install into existing partitions marks a partition
  cut into free space as `CREATE`, apart from the ones it formats.
- Continuous integration checks more than the unit suite: that the tests
  which run as root in a user namespace really ran, a coverage floor, a dry
  run of every shipped config, and the storage stages on loop devices
  (`tests/loop/run.sh`).

### Fixed

- Safe Installation from the full-screen installer named its new root
  partition `/dev/sdX3` where the config wants `sdX3`, so the install cut
  the partition and then failed making a filesystem on `/dev//dev/sdX3`.
- A device given as a path where a name is expected (`name: /dev/sda`) is
  refused by validation; it was planned as `/dev//dev/sda`.
- The config's proxy now applies to the reachability check made before the
  install. It used only the environment, so on a proxied network a config
  that set the proxy was refused for having no network.
- The pre-install check asked for `mkfs.<filesystem>` even for partitions it
  keeps, refusing an install beside a filesystem the live medium cannot
  make.
- Storage aliases: with two aliases where one name starts with the other
  (`disk` and `disk2`), an unbraced `$disk2` could expand as `$disk`
  followed by `2` — the wrong device. And an alias for a disk whose name
  ends in a digit but is not nvme, loop or mmcblk (md0, for one) named its
  partitions without the `p` the kernel gives them.
- A failing `cryptsetup` is reported by the step that failed
  (`luks: luksFormat: …`); the message used to name a flag.
- A failed install's error on the final screen is wrapped by character, so
  a message with a non-ASCII path is no longer cut inside a character.

## [2.0.0] and earlier

What was recorded up to 2.0.0, before the changelog was kept by release.

### Fixed

- The prompt flow no longer spins forever when its input ends (a closed pipe,
  a serial line that dropped) at a password or passphrase prompt: an ended
  input read as an empty answer, which the prompt refused and asked for again,
  endlessly. It now stops with an error. Nothing was erased either way — the
  final confirmation defaults to no.
- The plain prompt flow now installs the same default kernel command line as
  the full-screen wizard (the Server profile, recorded as `kernel-profile`) and
  shows it, with the kept console, in its summary. It used to install no kernel
  tuning at all.
- The plain prompt flow now keeps the live system's `console=` arguments for
  the installed system, as the full-screen wizard does. It is the flow that
  runs over a serial console, so an install made through it used to come up
  with no serial console.
- The CPU Frequency Governor list now follows the driver mode the installed
  system will run in, not only how the live medium booted: a P-State mode chosen
  for an isolation profile decides it (active offers performance/powersave;
  passive, guided and disable offer the classic governors). A governor that the
  newly chosen mode or profile does not have is reset, with a notice.
- The Custom kernel profile now honours the CPU Frequency Governor section; it
  was shown in the hub but never reached the command line.
- `console-log-level` outside 0-7 is refused by validation instead of being
  written to sysctl.d. In the kdump form, whitespace is removed from
  `crashkernel=` (it would split the argument at boot) and a non-numeric
  `panic=` is refused in place rather than silently dropped.

### Added

- A saved config is now portable between Intel and AMD as well. When
  `kernel-profile` names any wizard profile, `--apply` rewrites the
  vendor-specific arguments for the CPU it runs on — `intel_iommu`↔`amd_iommu`,
  `kvm-intel.nested`↔`kvm-amd.nested`, the Intel-only `intel_idle.max_cstate`
  — and carries the P-State mode across where both vendors have it (active,
  passive, disable); a mode with no counterpart is dropped. `Custom` is never
  rewritten.
- A saved config is now portable across machines for the isolation profiles.
  When `kernel-profile` names Realtime, Telco/RAN or Telco/RAN Max Perf,
  `--apply` recomputes `isolcpus=`, `nohz_full=`, `rcu_nocbs=`, `irqaffinity=`
  and (for Telco) the 1 GiB hugepage count for the machine being installed,
  replaces the saved values, and reports the change. Other arguments are kept
  as written, and `kernel-profile: Custom` (or no profile) is never rewritten.
  This makes `kernel-profile` operative for those profiles rather than purely
  informational.
- Preflight now checks that the kernel arguments fit the machine being
  installed: an `isolcpus=`/`nohz_full=`/`rcu_nocbs=`/`irqaffinity=` list
  naming CPUs it does not have, `isolcpus=` covering every CPU, or a hugepage
  reservation leaving under 1 GiB is refused before the disk is touched. These
  values are saved literally, so this catches a config replayed from other
  hardware. `skipValidationAll` turns it off.
- A GitHub Actions workflow runs gofmt, `go vet`, `go build` and the unit
  suite with the race detector on every push and pull request; until now
  these were only run by hand.
- The README documents the kernel settings (profiles, `kernel-profile`,
  `console-log-level`, governor, P-State, kdump) and their limits: computed
  CPU lists are specific to the machine the wizard ran on, the profiles are
  low-latency tuning on the stock kernel rather than `PREEMPT_RT`, and kdump
  only reserves memory. The kdump form and the Realtime description say the
  same in the wizard.
- The saved configuration records the Kernel Command Line profile used in a new
  `kernel-profile` field (e.g. `kernel-profile: Server`), next to the expanded
  `kernel-arguments`. It is informational — the arguments are what apply — and a
  Sunlight extension, omitted when unset.
- The Kernel Command Line profile picker gains a Details button that opens the
  highlighted profile's description, full parameters and trade-off/auto-computed
  note in a separate window, replacing the inline preview that used to sit below
  the list. The list now uses the full dialog height, so many more profiles are
  visible at once. Each profile has a one-line description.
- New "CPU Frequency Governor" section on the Advanced tab sets
  `cpufreq.default_governor=` (performance / schedutil / ondemand / conservative
  / powersave, or not set). It is the single authority: the chosen governor
  replaces whatever a profile carried. Default is unset (keep the profile's own
  governor). Pairs with the P-State sections for frequency control. On an
  EPP-capable CPU (active-mode HWP/CPPC) the choices are restricted to the
  governors that actually apply there — performance and powersave — detected
  from sysfs. New `pkg/system.EPPCapable`.
- New "Crash Dump (kdump)" section on the Advanced tab configures the kernel
  crash-dump reservation and panic behaviour: a two-field form for
  `crashkernel=` (prefilled with a RAM-scaled range,
  `1G-4G:192M,4G-64G:256M,64G-:512M`) and `panic=` (seconds to auto-reboot
  after a panic, prefilled 5). Both are appended to the kernel command line.
  Leaving the crashkernel field empty disables kdump; a non-numeric panic value
  is ignored. Off by default.
- New "Kernel Log Level" section on the Advanced tab sets the console log
  verbosity (0 emergency … 7 debug) in two places at once: `loglevel=N` on the
  kernel command line and `kernel.printk = N 4 1 7` via `/etc/sysctl.d/10-printk.conf`
  on the installed system. The level is the single authority — it overrides any
  `loglevel=` a kernel profile already carried. The default is unset (kernel
  default, nothing written). New config field `console-log-level` (a Sunlight
  extension), `system.PrintkSysctl`.
- Kernel Command Line moved from the Advanced tab to the Required tab, and now
  defaults to the Server profile (shown as "Server profile" in the hub) instead
  of being unset — so a plain install gets sensible server tuning without
  opening the section. Picking Custom from a predefined profile starts the
  Add/Remove form blank rather than pre-filled with the profile's arguments.
- Kernel Command Line offers a scrollable, type-to-filter list of predefined
  profiles — Server, Desktop, Desktop LowLatency, LowLatency, Realtime,
  Audio/Video Workstation, Telco/RAN, HPC/Compute, Virtualization Host (KVM),
  GPU Passthrough (VFIO), Intel GPU Workstation, Containers/Cloud, Hardened
  (AppArmor), Laptop/Power Save, Debug/Verbose Boot, and Custom. The
  highlighted profile previews its arguments, and a trade-off note warns when
  a profile weakens security or power (`mitigations=off`, `idle=poll`).
  Picking a profile fills the arguments; Custom opens the manual Add/Remove
  form.
- Added an "NVIDIA GPU (proprietary)" profile: blacklists nouveau
  (`rd.driver.blacklist=nouveau nouveau.modeset=0 module_blacklist=nouveau`) and
  enables the proprietary driver's KMS (`nvidia-drm.modeset=1`), alongside the
  Intel and AMD GPU profiles. Its note warns the GPU has no driver until the
  proprietary NVIDIA driver is installed. (NVIDIA Grace / ARM64 tuning is out of
  scope while the installer is x86_64-only.)
- Added an "AMD Instinct (GPU compute)" profile for AMD Instinct GPU-compute
  nodes: the required `iommu=pt`, `pci=realloc=off` and `pci=bfsort`
  (reliable GPU enumeration/ordering) with the vendor IOMMU (amd_iommu on an
  AMD EPYC host, intel_iommu on an Intel host), plus the recommended
  `numa_balancing=disable`, `tsc=nowatchdog`, `pcie_aspm=off`,
  `processor.max_cstate=0`, `transparent_hugepage=always`, `mitigations=off`
  and the watchdog disables. The more situational extras
  (`modprobe.blacklist=amdgpu`, `nokaslr`, `norandmaps`, `biosdevname=0`) are
  named in the note for Custom rather than enabled by default.
- Added a "GPU Passthrough (consumer/ACS)" profile for passthrough on consumer
  boards: the VFIO base plus `pcie_acs_override=downstream` (split IOMMU groups)
  and `intel_iommu=on,igfx_off` (ignore the iGPU, which stays with the host). The
  strict "GPU Passthrough (VFIO)" profile keeps full isolation. The vendor
  rewriter now preserves IOMMU flags (igfx_off) on the matching CPU vendor and
  drops them only when switching to the other vendor (amd_iommu=on on AMD).
- Added a "Telco/RAN Max Perf" profile for maximum determinism: the Telco/RAN
  base plus the aggressive extras used on real-time production systems —
  `cpuidle.off=1 processor.max_cstate=0` (kill C-states entirely, beyond `idle=poll`),
  `pcie_aspm=off` (PCIe latency) and `audit=0`. Its note warns it draws the
  most power and is the least secure; the balanced Telco/RAN stays the default.
- The Realtime and Telco/RAN profiles were aligned with industry low-latency /
  real-time kernel practice: the computed isolation now uses
  `isolcpus=domain,nohz,managed_irq,<list>` (the `nohz` flag was missing);
  Telco/RAN replaces C-state limiting with `idle=poll` alone (not
  `intel_idle.max_cstate`/`processor.max_cstate` as well) and gains the RCU and
  tick tuning — `skew_tick=1 nohz=on mce=off rcu_nocb_poll
  rcupdate.rcu_cpu_stall_suppress=1 rcupdate.rcu_expedited=1
  rcupdate.rcu_normal_after_boot=1 rcupdate.rcu_task_stall_timeout=0
  rcutree.kthread_prio=99` — plus `net.ifnames=1` and `iommu=pt` with the
  vendor IOMMU (for DPDK/SR-IOV/vRAN). On AMD, these profiles also get
  `amd_pstate=active` (hardware-managed P-states, the AMD counterpart to Intel
  HWP); Intel keeps HWP by default with no `intel_pstate` forced. Realtime
  gains the lighter RCU/tick subset.
- Kernel profiles adapt to the CPU vendor: the installer detects Intel vs AMD
  (`GenuineIntel`/`AuthenticAMD`/`HygonGenuine` in /proc/cpuinfo) and rewrites
  the vendor-specific arguments so an Intel-written profile boots correctly on
  AMD (Ryzen/EPYC/Threadripper) and vice versa — `intel_iommu=`↔`amd_iommu=`,
  `kvm-intel.nested`↔`kvm-amd.nested`, and the Intel-only `intel_idle.max_cstate=`
  dropped on AMD (processor.max_cstate covers it). A non-x86 host leaves the
  arguments untouched. New `pkg/system.CPUVendor` and `VendorKernelArgs`, plus
  an "AMD GPU Workstation" profile (amdgpu) alongside the Intel one.
- The installer detects the live system's consoles and applies them to the
  installed system's kernel command line: it mirrors the live kernel's
  `console=` arguments (which carry the serial speed/format, e.g.
  `console=ttyS0,115200n8`), or, failing that, reads the active consoles from
  sysfs and gives a serial one a default speed. A serial-console install thus
  comes up on the same serial port, and a graphical one on the VGA console.
  The detected consoles are shown in the Kernel Command Line picker and merged
  into the kernel arguments (de-duplicated), regardless of the chosen profile.
  New `pkg/system.DetectConsoles`. Added "General (robust defaults)" and
  "VM Guest" profiles covering common base arguments (tsc=reliable,
  page_alloc.shuffle, rcu_expedited, rootfstype, cryptomgr.notests,
  no_timer_check, noreplace-smp).
- The computed CPU isolation is NUMA-aware: housekeeping cores are reserved on
  each NUMA node (every socket keeps local housekeeping/IRQ cores) instead of
  all coming from the first socket — matching multi-socket telco practice. A
  single-node machine is unaffected.
- The AMD P-State driver mode for the RT/telco profiles is selectable in a new
  "AMD P-State Driver" Advanced section (active / passive / guided / not set),
  defaulting to active; it replaces the previously hard-coded
  `amd_pstate=active` and only applies on AMD isolation profiles.
- An equivalent "Intel P-State Driver" Advanced section (shown only on Intel)
  sets `intel_pstate=` on the RT/telco isolation profiles: not set (default,
  keep HWP — best for real-time), active, passive, no_hwp, disable or force.
  Each P-State section appears only on the matching CPU vendor.
- The Realtime and Telco/RAN profiles now compute CPU isolation from the live
  machine instead of leaving it to Custom: the installer reserves whole
  physical cores for the OS and IRQs — scaled with machine size (about one
  housekeeping logical CPU per 16, so 8 of 128 — a common telco reservation,
  always at least one whole core) and kept as complete SMT-sibling groups so
  no core is split — isolates the rest, and fills
  `isolcpus=domain,nohz,managed_irq=`, `nohz_full=`, `rcu_nocbs=` and
  `irqaffinity=` with the computed CPU lists — following common real-time
  CPU-partitioning practice. Telco/RAN also
  reserves 1 GiB hugepages sized from RAM (about a quarter, always leaving
  4 GiB for the OS). The profile's note reports what was computed, and a
  machine with too few cores is left general-purpose. New `pkg/system`
  `ComputeCPUIsolation` and `HugepagesGiB`.
- Safe Installation (install alongside an existing system): the Installation
  Media dialog's Safe mode now works. It reuses the disk's existing EFI
  System Partition and adds a root partition in the largest unallocated
  region, leaving every existing partition — and any other OS — intact; no
  disk is wiped. New `pkg/storage` helpers detect free space and the ESP
  (`FreeRegions`, `LargestFree`, `ExistingESP`, `NextPartitionNumber`,
  parsing `parted ... print free`), a Plan can cut a `Create` partition at an
  explicit offset on a non-wiped disk, and the config carries it (`create` +
  `start` on a `reusePartitions` child). *Limitations:* it needs an existing
  ESP and enough free space — it does not yet resize/shrink a partition to
  make room — and the root is not encrypted in this mode. VM-unverified so
  far.

### Changed

- Test Network Settings now runs its connectivity check behind an animated
  progress bar in the popup (which pulses while the check runs), the way
  clr-installer shows it, instead of a text spinner.
- The Configure Network Interfaces sub-page now shows each interface's
  current live addresses (ipv4/netmask/ipv6/netmask, read-only) the way
  clr-installer does, above the Automatic (DHCP) / Static IP choice — so you
  can see the interface's state and still configure a static address. The
  interface list shows the live IPv4 next to a DHCP interface.
- A new LUKS passphrase typed at the installer is asked for twice and must be
  8 to 94 printable ASCII characters, as in clr-installer: a typo here locked
  the disk it was set on. *Compatibility:* only what is typed at a terminal is
  checked; a passphrase from `--crypt-file`, the environment or a pipe is used
  as given, so an unattended install does not change.
- A disk that is in use is no longer offered or installed to: one with a
  mounted filesystem, active swap, or the live medium itself (the TUI and
  the text flow leave it out; a config naming it is refused before
  anything is touched; `--list-disks` marks it). clr-installer leaves such
  disks out too. *Compatibility:* a config targeting a disk the running
  system uses now stops with the reason instead of wiping it.
- Wiping a disk that shares a RAID array or LVM volume group with a disk
  the install does not wipe is refused unless `--force-destructive` is
  given (clr-installer's flag), as wiping one member breaks the others.
- Hooks run the way clr-installer runs them: `$chrootDir`, `$chrooted`,
  `$yamlDir` (new: the config file's directory) and the config's `env` are
  substituted into the command, in `$name` and `${name}` forms, as well as
  set in its environment, and the command runs under `bash -l -c` (`sh -c`
  where there is no bash). *Compatibility:* a hook written for `sh` that
  relied on `$name` reaching the shell unexpanded, or on sh-only
  behaviour, sees the clr-installer behaviour now. Inside a chroot hook
  `$chrootDir` stays `/`, where clr-installer passes the live mount path.
- When an administrator account is created, root's password login is
  locked (`usermod --lock`, with today as its last change), as
  clr-installer does: root is reached through sudo. *Compatibility:* an
  install that relied on root's state from the base system now has it
  locked; one with no admin account is unchanged.
- Logins are checked as clr-installer checks them: at most 31 characters,
  and not a name the base system already uses for an account or group
  (root, wheel, users, audio, ...), which useradd would fail on partway
  through the install. The TUI's account form also asks for the password
  twice and requires 8 to 255 characters, saying why on the form itself;
  the text flow applies the same rules.
- A passing network test in the TUI sets `copyNetwork`, as clr-installer's
  does, so the network set up on the live system (Wi-Fi included) carries
  into the installed one; the result popup says so.
- A config whose storage alias names an image file (clr-installer's
  install into an image) is refused by name; it used to be read as a
  device path such as `/dev/release.img`.

- The full-screen dialog is clr-installer's window: 80x24, centred, whatever
  the size of the terminal (smaller only on a smaller one). It used to grow
  with the terminal to 90x36, which made it much taller than clr-installer's.
  As there, the Required tab's section list now scrolls.
- The install log view is easier to read: each stage starts with its
  `==> stage` marker on a grey bar (the full-screen install used to leave
  the markers out), lines with an error are on a red bar and warnings in
  yellow, a line too long for the panel continues on the next row instead
  of being cut, and xbps's download progress lines are left out, as each is
  followed by the download's final line. A blank row now separates the log
  from its Back button. Its header says where the view is: "following new
  lines" at the end, "paused at line 340 of 649" when scrolled up, and "end
  of log" once the install is over.
- The install progress screen shows how long the install has run, and each
  stage how long it took (or has been running); the final screen says how
  long the install took.

- The full-screen interface takes Sunlight Linux's orange-to-red colours
  in place of clr-installer's blue: a burnt orange dialog, an amber accent
  for the selection, active tab, buttons and progress bar, and dark red for
  popups and failures. The layout is unchanged. Each colour names its
  16-colour console form too, since the console has no orange and the
  nearest match turned every orange into bright red; there it is the VGA
  brown (dark orange) with a red accent, as the console has only 8
  background colours and shows a bright one (the amber's nearest) as black. The
  progress bar picks its own console colour, and made bright red of the
  amber; it is named outright there too, as bright yellow.
- The install progress screen lists the stages instead of the raw command
  output: each is marked `[ OK ]` when done, with a turning spinner while it
  runs (that stage's line in bold yellow), `[FAIL]` if it fails, and blank while it waits. The full command log
  is behind a **Details** button (Enter or `d`), scrollable with the arrows,
  PgUp/PgDn and Home/End, and on the final screen too (`d`); it keeps the
  last 10000 lines, where only 500 were kept. The marks are ASCII because
  the console font has no tick or cross glyph.
- Buttons sit at the bottom of the dialog on every page that has them, just
  above the footer, as clr-installer's control frame does. They used to
  follow the content, so a short page (Manage User with no users) showed
  them a third of the way down with the rest of the dialog empty below.
- The buttons in a row are all as wide as the widest, labels centred, on
  every page: the hub, the lists, Installation Media, Manage User, the
  install confirmation and the final screen. To fit four even buttons in
  the dialog, the final screen's "Back to installer" is now "Back to menu".
- The TUI no longer has a Telemetry section: Sunlight Linux collects none,
  so the choice did nothing. A config's telemetry keys still load and are
  ignored, as before.
- Button rows are centred in the dialog on every page. A row too wide to
  leave a margin on both sides, the final screen's four, draws its buttons
  tighter instead of touching the right edge.
- The hub's section list reaches down to the buttons: five sections in the
  80x24 dialog, where it showed four with three empty rows below them.

### Added

- Automatic OS Updates is wired up: the config's `autoUpdate` (on unless
  set to false, as in clr-installer) installs a `sunlight-autoupdate` slinit
  service that updates the system from its xbps repositories once a day,
  the counterpart of Clear Linux's swupd update timer. It is a slinit
  calendar timer (`cron-calendar = daily`) at a fixed random time within
  the hour after midnight, caught up at the next boot if the machine was
  off; it updates xbps first, then the system. With `autoUpdate: false`
  the service is installed but not enabled (`slinitctl enable
  sunlight-autoupdate` turns it on). The TUI's Automatic OS Updates
  section sets it.
- Before a disk is repartitioned, what the live system runs on it is
  released: RAID arrays assembled from an earlier install are stopped,
  LVM and LUKS device-mapper volumes removed, and old signatures erased
  with `wipefs`, as clr-installer clears them before partitioning. Left in
  place they kept the disk busy for `parted`, or came back on the next
  boot. A dry run lists these commands as they stand on the machine.
- A partition's `label` and `options` in a clr-installer config are
  honoured: the label goes to mkfs (with each filesystem's flag, cut to its
  length limit), the options after slinstaller's own mkfs options.
- clr-installer's command-line options: `-c`/`--config`, `--crypt-file`
  (the LUKS passphrase from a file), `--genpass` (print a password hash
  for a config's users; `-` reads it without echo),
  `-b`/`--block-device name:file` (point a storage alias at a disk),
  `--template` (write a starting config), `--copy-network`,
  `--allow-insecure-http`, `--swap-file-size` and `--force-destructive`.
- The full-screen install writes its log to `/tmp/slinstaller.log` on the
  live system as it runs, and a completed install copies it to
  `/root/slinstaller.log` on the installed system (mode 0600, beside the
  saved config), as clr-installer archives its own log. Until now the log
  lived only in the view and was gone once the installer exited.
  `controller.Options.LogFile` names the file to archive.
- The text and unattended installs keep the same log: `/tmp/slinstaller.log`
  (or `--log`'s file) while running, `/root/slinstaller.log` on the installed
  system after a complete install. `--log` now shows progress live on the
  terminal too, and its file holds lines as the terminal showed them.

### Fixed

- Choosing `man-pages` in the TUI failed the install at base-install, after
  the disk had been wiped: it asked for a package `mandoc`, which Void calls
  `mdocml`.
- A popup cut a black band across the dialog: it replaced the whole rows it
  covered. It now replaces only the cells under it.
- A password hash no longer appears in the install log: the `useradd -p`
  command is shown with `<password hash>` in its place, in the log view,
  the log file and `--log`.
- Filled boxes in on-screen text on the Linux console: its font has no em
  dash, ellipsis, tick or cross, so "Installing — step", "Finishing…", the
  network test's ✓/✗ and many descriptions showed a box. The installer's
  on-screen text, its messages and the config's validation errors use
  "-", "..." and [ OK ]/[FAIL] instead; a clipped log line ends in "...".
- Space did nothing on a disk row of the Installation Media page, though
  the footer offers it to choose; it now picks the disk, as Enter does.
- Black blocks between buttons: the spaces after a button's style reset
  fell back to the terminal's background. They keep the dialog's colour.
- Black cells at the right of the install screen, and half-redrawn lines in
  its log: command output reached the log with its control characters.
  mke2fs prints its progress with backspaces ("0/16" erased and overwritten
  by "done"), xbps redraws its download line with "\r"; lipgloss counted
  them as zero-width while the terminal still moved the cursor, so the row
  came out short and the rest was never drawn over. Each line is now turned
  into what a terminal would show for it before it is logged.
- The Cancel / Confirm buttons on the selection lists (timezone, language,
  keyboard, kernel) and on Select Additional Bundles could not be reached:
  they were drawn only, with Confirm always lit. Tab now moves focus from the
  list to Cancel, Confirm and back (Shift+Tab the other way, Left/Right
  between the buttons, Up back to the list), the focused button is the lit
  one, and Enter on Cancel leaves without changing anything.
- The install progress screen left a blank band between the command log
  and the footer: the log was capped at 20 lines and sized from an estimate
  of the window. It now fills exactly the rows the dialog has left.
- An install that set no timezone or keymap was refused before it began,
  with `timezone "" is not a known zoneinfo zone`. The pre-wipe check added
  for both took the empty value for a name, though empty means "leave the
  target's default" when the install applies it, and the serial text flow
  never asks for either — so every serial-console install failed. Only a
  timezone or keymap that is set is checked now.
- A `--local-repo` under the target root (e.g. a 9p share mounted at
  `/mnt/share` while installing to `/mnt`) is now refused in preflight: the
  repository would be hidden the moment the target filesystem is mounted,
  and the base install would fail with "package not found" partway through.
  The error says to move the repo outside the root. The VM demo's guest
  instructions now mount the share at `/run/slshare`, outside `/mnt`.
- Passwords and passphrases typed at the text prompt flow and the LUKS
  passphrase prompt are no longer echoed on screen: input is read with
  terminal echo turned off (and still read normally on a pipe/non-terminal,
  e.g. an unattended run). The full-screen TUI already masked them.
- A fully offline install (a local repository on the live medium, no remote
  mirror) failed at base-install with "xbps: no repository": the install
  command always required a remote `Repo` and emitted an empty `-R`. xbps
  now accepts a local-only repository set — every `-R` is local and the
  network is never reached — so `offline` installs (and a bare
  `--local-repo` with no `--repo`) work.

### Changed

- The full-screen dialog keeps a stable size: its body is padded to a fixed
  height (tracking the terminal, bounded), so the blue box no longer shrinks
  when entering a short section and grows back on return to the menu.
- The hub's section list now scrolls within that fixed height, with a
  scrollbar (▲/▼ and a thumb) on the right showing there is more below — as
  clr-installer does. The Advanced tab, which has more sections than fit, no
  longer makes the dialog taller than the Required tab; the selected row is
  highlighted at a uniform two-line height so the list scrolls cleanly.

### Added

- Configure Installation Media is now clr's front dialog: three modes
  (Destructive, Advanced, Safe), the disk list, an Enable Encryption
  checkbox and Confirm / Partition / Rescan Media / Cancel buttons.
  Destructive + Confirm lays down a standard whole-disk layout (512M ESP at
  /boot, 2G swap, root filling the rest; root encrypted when the box is
  ticked, prompting for the passphrase); Advanced (or the Partition button)
  opens the manual partition editor; Safe (install alongside) is shown but
  reports it is not yet supported.
- Smaller clr parities in the full-screen wizard: Kernel Command Line now
  has both Add and Remove argument fields (→ `kernel-arguments` add/remove);
  Swupd Mirror now has the "Allow installation over insecure connections
  (http://)" checkbox (→ `allowInsecureHTTP`); Telemetry and Automatic OS
  Updates now show clr's explanatory paragraph above the Enabled/Disabled
  choice; and the hub's selected row is drawn as a taller teal bar.
- Select Additional Bundles is now a checklist with descriptions ([x]/[ ]),
  as clr-installer does, instead of a free text field. The ticked bundles'
  xbps packages (a small curated set — vim, git, curl, htop, openssh, …) are
  written to the config's `packages`.
- Timezone, Language and Keyboard are now chosen from scrollable lists of
  what the live system offers (zoneinfo zones, UTF-8 locales, console
  keymaps), with a scrollbar and type-to-filter, as clr-installer does —
  instead of a free text field (which stays as a fallback when the data is
  absent). New `pkg/system` enumerators `ListTimezones`, `ListKeymaps` and
  `ListLocales` back them.
- The full-screen install's final screen now has action buttons — Reboot,
  Power off and Exit to shell — navigable with Tab/arrows and activated with
  Enter, instead of only "[Enter] exit". Reboot and power-off run after the
  interface is torn down, so they execute cleanly.
- Save Configuration Settings now writes the wizard's config to a file
  (clr parity): it builds the clr-format `config.yaml` from the current
  choices and saves it (never including the LUKS passphrase), so the layout
  can be inspected or re-run later with `slinstaller --apply <file>`. New
  `config.Save` writes a config back in the shape `Load` reads.
- Configure Network Interfaces is now clr's interface sub-page (phase 3):
  the machine's live interfaces are listed with their current setting
  (Automatic DHCP or a static address), and selecting one opens a
  per-interface window to choose DHCP or a static IP (CIDR address, gateway,
  DNS). Several interfaces can be configured; each static one becomes a
  `networkInterfaces` entry in the built config.
- Manage User is now clr's account table (phase 2): a Login / User Name /
  Password / Admin table with Add New User, Revert, Confirm and Cancel, and
  a form (login, full name, masked password, administrator yes/no). The
  wizard can define several accounts, mark which are administrators, edit an
  existing row (Enter) or delete it (d) — replacing the previous
  single-admin prompt. Passwords are hashed when the config is built; the
  install needs at least one administrator.
- Full-screen TUI reorganised to match clr-installer's menu 1:1 (phase 1):
  the Required tab is Choose Timezone, Choose Language, Configure the
  Keyboard, Configure Installation Media, Manage User, Telemetry; the
  Advanced tab is Configure Network Interfaces, Proxy, Test Network
  Settings, Select Additional Bundles, Assign Hostname, Kernel Command Line,
  Kernel Selection, Swupd Mirror, Automatic OS Updates, Save Configuration
  Settings — in clr's exact order and wording (e.g. "Warning: No Media
  Selected", "No HTTPS proxy URL set"). Hostname moved to Advanced. The LUKS
  passphrase is now collected by a popup raised from the media dialog when
  an encrypted partition is added, as clr does, rather than a standalone
  section. "Test Network Settings" runs a real connectivity check behind a
  small animated spinner popup. Telemetry, Additional Bundles and Automatic
  OS Updates are present for fidelity (Telemetry/Auto-updates are
  Enabled/Disabled toggles; Bundles edits the extra-packages list). Further
  pieces — the multi-user Manage User table and the network-interface
  sub-page — follow in later phases.
- `--reboot` flag (the CLI equivalent of the config's `postReboot`) and
  `--log <file>` (tees the install's command trace, stage headers and
  sub-command output to a file as well as the terminal, for debugging an
  unattended or serial install).
- Timezone and keymap validation: a mistyped `timezone` or `keyboard` is
  caught before the disk is touched, checked against the live system's
  zoneinfo and console-keymap data — and only when that data is present, so
  a minimal medium that lacks it never rejects a valid value.
- Reuse-partitions (no-wipe) install: `reusePartitions: true` installs into
  the disk's **existing** partitions without repartitioning — the partition
  table is left untouched, each declared partition is matched to its device
  by name, formatted unless marked `preserve: true`, then mounted. This is
  how an install shares an existing ESP or keeps a `/home` across a
  reinstall (clr-installer's advanced/manual mode). Only plain partitions
  are supported for now (no crypt/lvm/raid). Preflight confirms each
  partition exists, and the confirmation lists which are formatted vs kept.
  See `demo/vm/config-reuse.yaml`.
- Pre-wipe size validation (clr-installer parity): `Validate` now rejects an
  EFI System Partition below 100 MB, a root below 2 GB, or any partition
  below 1 MiB, and preflight refuses a layout whose fixed partitions plus
  GPT overhead do not fit the target disk — all **before** the disk is
  touched, so an oversize or undersized layout fails up front rather than
  partway through `parted`. `skipValidationSize`/`--skip-validation-size`
  and `skipValidationAll`/`--skip-validation-all` turn these (or all
  validation) off, as in clr.
- `--offline` flag: the command-line equivalent of the config's `offline`,
  so an offline install (local repository only, no network) can be run
  interactively without hand-writing a config.
- Full-screen TUI expanded to clr-installer's section set. The hub's two
  tabs now hold, under Required: Installation Media (disk + the partition
  editor in one dialog, like clr's media_config), Timezone, Keyboard,
  Language, Manage User, Hostname; under Advanced: Network Interface
  (a single static interface, blank = DHCP), Network Proxy, Storage Mirror,
  Kernel Selection (→ `linux`/`linux-lts`), Kernel Command Line, Additional
  Packages, Encryption Passphrase. Each opens its own titled dialog. The
  passphrase entry and the "not ready" warning are now bordered popup
  windows floated over the hub, the way clr-installer draws them. All the
  new sections feed the produced config (`kernel-arguments`, `httpsProxy`,
  `swupdMirror`, `packages`, `networkInterfaces`), so the wizard can now
  build the same configs the file front-end accepts.
- Storage aliases (clr-installer's `block-devices`): a `targetMedia` disk
  written as `${alias}` and its partitions as `${alias}1` now expand to
  real device names at load time, using the `block-devices: [{name, file}]`
  declarations — the disk to the device's base name (`/dev/nvme0n1` →
  `nvme0n1`) and its partitions to that name plus the kernel's partition
  suffix (`nvme0n1p1`; empty for the `sd*`/`vd*` family). Both `${alias}`
  and `$alias` forms, matching clr-installer. A layout can be written once
  and pointed at different disks by changing one `file:`.
- UEFI preflight: a full `--apply` now refuses to start when the installer
  medium was not booted in UEFI mode (`/sys/firmware/efi` absent), instead
  of failing at the bootloader stage after the disk is already wiped.
- `copyNetwork` (config): copies the live system's network configuration
  (`/etc/wpa_supplicant`, a hand-edited `/etc/dhcpcd.conf`) into the target,
  so an install done over Wi-Fi stays reachable after first boot.
- `postReboot` (config): an unattended `--apply` reboots into the installed
  system when the full run finishes.
- `env` (config): extra variables made available to pre/post-install hook
  commands, on top of the `$chrootDir`/`$chrooted` the installer always sets
  (clr-installer parity).
- Kernel selection: the config's `kernel` (clr's `kernel-native` /
  `kernel-lts` / `kernel-kvm`) now maps to the Sunlight kernel package
  (`linux` / `linux-lts`); any other value is taken as a literal package
  name, and is installed into the target.
- `packages` (config): extra xbps packages to install into the target —
  slinstaller's functional stand-in for Clear's untranslatable `bundles`
  (which stays carried-but-ignored).
- `offline` and `allowInsecureHTTP` (config): an offline install uses only
  a local repository on the live medium and skips the network precheck; a
  plain-`http://` remote mirror is refused unless `allowInsecureHTTP` is set
  (https and local repos are always allowed).
- `pkg/config` — reads clr-installer's config schema (`targetMedia` with
  its partition children, plus `users`, `networkInterfaces`, `keyboard`,
  `language`, `timezone`, `kernel-arguments`, `bundles` and pre/post
  hooks). slinstaller's own parser for that format, not a copy of
  clr-installer's code: an unmodified clr-installer `config.yaml` loads
  and produces a correct partition plan. Parsing tolerates the lsblk
  probe fields and Clear Linux specifics real configs carry, and the
  three size forms clr-installer writes (byte count, integer+unit,
  decimal+unit), and a missing size / 0 / "rest" on the last partition to
  mean "fill the disk" (as clr-installer uses size 0). `Validate` reports
  every problem at once before any disk is touched — unknown filesystem,
  non-absolute or duplicate mountpoint, no or more than one root, no ESP,
  an encrypted ESP, a bad hostname or swapFileSize — while staying
  clr-compatible (it does not require fields a valid clr config may omit,
  like a login account). Tested against clr-installer's own fixtures.
- `pkg/blockdev` — discovers the machine's block devices with `lsblk -J`
  (size parsed whether lsblk emits a number or a string). Lean next to
  clr-installer's full BlockDevice tree: enough to confirm the target disk
  exists and is a whole disk before anything is wiped, and to list what is
  available. `cmd/slinstaller --list-disks` prints them, and a full
  `--apply` now refuses a target that is missing or is a partition.
- `pkg/storage` — the first core subsystem ported from
  [clr-installer](https://github.com/clearlinux/clr-installer), reduced
  to a single UEFI target: GPT labelling and partitioning with `parted`
  (the ESP marked with parted's `esp` flag — no `sgdisk` dependency), and
  filesystem creation with `mkfs.*` /
  `mkswap`. The destructive work is expressed as a command list
  (`Plan.Commands`) that executes nothing until handed to `RunCommands`,
  so a dry run shows exactly what an apply would do. Also mounts the
  target tree parent-first (`Mounter`), bind-mounts `proc`/`sys`/`dev`
  for chroot, and renders `fstab` by UUID (`Plan.Fstab`) — listing every
  mount, since Sunlight boots slinit and has no systemd gpt-auto to defer
  standard mounts to. A config `swapFileSize` creates a swap file on root
  (`SwapFileCommands`) instead of a swap partition — clr-installer's
  swapFileSize, run as a controller stage after fstab.
- `pkg/luks` — LUKS encryption with cryptsetup. A partition typed `crypt`
  in the config (clr-installer's marker) is luksFormat'd and opened to a
  mapper, and its filesystem is made and mounted there
  (`storage.Partition.EffectiveDevice`). The whole install takes **one
  passphrase**, splitting the unlock between the two things that run at boot.
  The **root** volume is opened by the dracut initramfs (`rd.luks.uuid` for
  the root volume only, `rd.luks.crypttab=0` so dracut does not also walk
  crypttab and prompt twice); dracut asks for the passphrase, unless `/boot`
  is encrypted, where GRUB already asked and a keyfile embedded in the
  encrypted initramfs (`rd.luks.key`) opens it. The **non-root** volumes (a
  crypt `/home`, `/data`, …) are opened after root is mounted by Void's runit
  crypttab service, from an `/etc/crypttab` that lists only them, with a
  random keyfile on the now-unlocked encrypted root (`luks.AddKey` adds it as
  a second key slot) — so no second prompt, and the keyfile never sits on
  unencrypted storage. The passphrase comes from `SLINSTALLER_LUKS_PASSPHRASE`
  or a prompt — never from the config, and never in a process's argv.
- `pkg/lvm` — LVM: a partition typed `lvm` is a physical volume carrying
  a `volumeGroup`, and its children are logical volumes. The controller
  runs `pvcreate`/`vgcreate`/`lvcreate` between partitioning and mkfs, the
  filesystems are made and mounted on the logical volumes
  (`storage.Partition.EffectiveDevice`), an LV may fill the group, and a
  root on LVM gets `rd.lvm.lv` on the kernel command line with an
  initramfs rebuild so it assembles at boot.
- **LVM-on-LUKS** — an `lvm` partition with `encrypted: true` puts the
  whole group inside one LUKS container: the partition is luksFormat'd, and
  `pvcreate`/`vgcreate` (and so every logical volume and filesystem) run on
  the mapper rather than the raw partition — the common "encrypted LVM"
  layout, one passphrase for root, home and swap. The controller unlocks
  the container before LVM assembly and, for a root on an encrypted group,
  puts the container's `rd.luks.uuid` beside the group's `rd.lvm.lv` on the
  kernel command line (with the initramfs rebuild) so boot unlocks then
  activates. The full-screen partition editor offers it as the
  `lvm-encrypted` type; `demo/vm/config-lvm-luks.yaml` is a worked example.
- `pkg/mdadm` — **software RAID** (0, 1, 5, 6, 10) with mdadm, clr-installer's
  RAID levels. This brings **multi-disk layouts**: `targetMedia` may list
  several disks, each partitioned, and a partition typed `raid` with a
  `raid: <name>` tag is a member of the array named in the config's new
  top-level `raid` list (`{name, level, fstype, mountpoint}`). The array
  carries the filesystem at `/dev/md/<name>`: parted cuts and flags the
  members, `mdadm --create` assembles the array before mkfs, and for boot the
  controller writes `/etc/mdadm.conf` (from `mdadm --detail --scan`), forces
  dracut's `mdraid` module (a `/etc/dracut.conf.d` drop-in — in the chroot
  dracut cannot tell the target boots from an array), adds `rd.md.uuid` for a
  root on RAID, and rebuilds the initramfs, so the array assembles at boot.
  The ESP stays a plain partition on one disk (firmware reads one disk, not
  an array).
  `Validate` checks the level, the member count per level and that the ESP is
  not on RAID; the full `--apply` preflight confirms every target disk and
  pulls in `mdadm`, and the confirmation lists all disks to be erased.
  `demo/vm/config-raid1.yaml` is a RAID1 example (`demo/vm/run.sh
  --two-disks` boots a second disk for it). RAID is config-file-driven; the
  full-screen editor stays single-disk for now.
- `pkg/xbps` — the base-system install, the counterpart to clr-installer's
  swupd step, following how Void bootstraps a rootfs: seed the repository
  signing keys into the target, `xbps-install` from the repository, then a
  chrooted `xbps-reconfigure -fa`. Expressed as a command list; the
  repository, keys, architecture and package set are the caller's to supply.
- `pkg/grub` — the UEFI bootloader install: chrooted `grub-install` (which
  writes the loader to the ESP and registers the firmware boot entry) and
  `grub-mkconfig`. Kernel arguments from the config are rendered into
  `/etc/default/grub`. clr-installer uses clr-boot-manager, which is
  Clear-specific; GRUB is Sunlight's choice, as it is Void's. When `/boot`
  is on an encrypted device (the ESP is at `/boot/efi` and the kernel lives
  on the encrypted root), the controller sets `GRUB_ENABLE_CRYPTODISK=y`
  before `grub-install` and formats that one LUKS volume with `pbkdf2`
  instead of the LUKS2 argon2id default, since GRUB can only unlock a
  pbkdf2 container — without it GRUB rejects the right passphrase as
  invalid. Putting the ESP at `/boot` keeps the kernel unencrypted, avoids
  the extra GRUB-stage passphrase prompt, and keeps argon2id everywhere.
- `pkg/system` — first-boot config: hostname (`/etc/hostname`), timezone
  (the `/etc/localtime` symlink), console keymap (`KEYMAP` in
  `/etc/rc.conf`, which Void's core services and slinit's OpenRC
  compatibility both read), locale (`/etc/locale.conf` plus uncommenting
  the glibc `libc-locales` entry, with the controller regenerating
  locales), and user accounts (`useradd` with the crypt-hash password via
  `-p`, `wheel` for admins, and `authorized_keys` for any SSH keys), plus
  `EnableService` to turn on a runit service in the target. An `admin`
  account now also gets working sudo: the controller writes a
  `%wheel`-enabling `/etc/sudoers.d/wheel` (0440) when any user is admin,
  so `admin: true` means sudo, not just group membership.
- `pkg/network` — the installed system's networking, via dhcpcd rather
  than clr-installer's systemd-networkd/NetworkManager (Sunlight has no
  systemd). Each `networkInterfaces` entry from the config is mapped to a
  `/etc/dhcpcd.conf` stanza: its addresses (IPv4 and IPv6), gateway
  (`routers`), DNS servers (`domain_name_servers`, space-separated) and
  search `domain` (`domain_search` — clr's interface `domain`, now parsed).
  An interface counts as static when it has addresses and is not explicitly
  `dhcp: "true"` (clr's rule), so an unset `dhcp` with addresses is still
  static; a DHCP interface produces no stanza and is left to dhcpcd's
  default, which is enabled so a plain install still comes up with network.
  A static interface's addresses and gateway are validated by
  `config.Validate` (via `network.ValidAddr`) before any disk is touched,
  so a bad IP or netmask fails up front rather than partway through the
  install. `VerifyConnectivity` checks the repository is reachable before a
  disk is wiped, so an offline install fails early rather than partway
  through the base download.
- Three smaller clr-installer parities: install hooks get `$chrootDir`
  and `$chrooted` in their environment, as clr expands them; the config's
  `httpsProxy` is passed to `xbps-install` for the download; and
  `kernel-arguments`'s `remove` list is honoured (its entries are dropped
  from what slinstaller writes to the kernel command line).
- `pkg/controller` — sequences the whole install from a config:
  pre-install hooks → partition → mount → bind meta filesystems →
  base-system install → fstab → bootloader → first-boot → network →
  post-install hooks. A full install runs end to end (gated by the front-end on root
  and disk confirmation); `Options.Through` stops after a named stage for
  a staged VM run, and a failure unmounts what it mounted. `Options.Progress`
  reports each stage as it starts (completed/total + name), so a front-end
  can drive a progress bar without the controller knowing about any UI.
- `pkg/tui` — an interactive front-end: with no config file,
  `cmd/slinstaller` asks for a target disk, the root filesystem, whether
  to encrypt the root (with a LUKS passphrase, confirmed), swap (a
  partition, a file, or none) and its size, a hostname and an admin
  account (password hashed with openssl, confirmed), shows a summary to
  confirm, and installs through the same path as a config file. On a real
  terminal it runs as a **full-screen menu styled after the Clear Linux
  minimal-ISO installer** (bubbletea/bubbles/lipgloss): a filled royal-blue
  dialog centred on a black screen, a bracketed title, a black tab strip with
  `[R] Required options` / `[A] Advanced options` (the active one a teal
  block), and each section shown as a bold white label over a cyan `» value`
  line, separated by rules, with a tall teal bar (magenta label) on the
  selected row. Required
  holds disk, the **manual partition editor**, keyboard, language, timezone
  and the admin user; Advanced holds passphrase and hostname — edited in any
  order, `[Tab]` switches tabs. `Cancel` and `Install` sit at the foot, with
  Install lit only once the layout is complete (a disk, a root, an ESP, a
  user, and a passphrase if encrypting), over the footer hint
  "Use [Tab] or the arrow keys [Up and Down] to navigate". The partition
  editor adds, deletes and nests entries: plain,
  encrypted (`crypt`) or LVM partitions, with logical volumes under an
  LVM group — the full layout the config file can express. Pressing Install
  runs the install **inside the interface**: a final "this will ERASE
  &lt;disk&gt;" confirmation, then a **progress bar** with the current stage
  (driven by the controller's `Progress` callback) and a live tail of the
  command output (`storage.SinkRunner` streams it into the view instead of to
  stderr), ending on a success or failure screen — no more plain-text scroll
  after the styled menu. Over a serial console or a pipe it falls back to the
  plain prompt flow and streamed-text install; both build the same config.
  The full-screen UI is chosen only when the terminal can report a window
  size (so a serial console — which usually reports 0x0, and on which
  clr-installer's own TUI crashes — takes the prompt flow), and `--text`
  forces the prompt flow regardless.
- `cmd/slinstaller` — the file front-end: reads a config, validates it,
  and prints the partitioning plan. With `--apply` it runs the install
  through the controller, gated on running as root and on the operator
  retyping the target disk path; `--through` and `--root` steer a staged
  VM run, and `--repo`/`--keys`/`--arch`/`--packages` set the base-system
  install. With no repository given it defaults to the config's
  `swupdMirror`, then to Void's official repo — a stopgap until Sunlight
  has its own.
- `demo/uefi-basic.yaml` — a minimal UEFI install in clr-installer's
  config format.
- `demo/vm` — a QEMU + OVMF harness that boots a Void live ISO with a
  blank virtio disk and the slinstaller binary over a 9p share, so the
  whole install runs end to end under real UEFI, and `run.sh --installed`
  boots the result. slinstaller's counterpart to slinit's QEMU demo.
