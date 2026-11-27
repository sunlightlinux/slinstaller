# Changelog

All notable changes to slinstaller are documented here. Format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and
[Semantic Versioning](https://semver.org/spec/v2.0.0.html).

slinstaller is in early bootstrap: nothing below is a stability promise.
See [STABILITY.md](STABILITY.md) for what will and will not be allowed to
change.

## [Unreleased]

### Changed

- **No LUKS keyfile on an unencrypted root.** An encrypted `/home`, swap
  or `/boot` on a plain root got a keyfile in `/etc/cryptkeys` - on that
  plain root, beside the volume it opens, so the encryption kept nobody
  holding the disk out. Such a volume now asks for its passphrase at
  boot (`none` in crypttab). With an encrypted root nothing changes.

### Fixed

- **Sizes in clr-installer's documented units load.** `150MiB`, `2.6GiB`,
  `1GB`, `512KB` and `1P` — clr-installer's own syntax example writes
  `150MiB` — were refused as "not a byte count". They are read as
  clr-installer reads them: every unit a power of 1024, `GB` included.
- **`swapFileSize` is made the size the config check read.** It went to
  `fallocate` as written, which refuses `2e9`, `1048576.5` and `0` (all
  accepted by the check) after the disk was wiped and the system
  installed, and reads `1GB` as 10^9 bytes. It is given the bytes now,
  and `0` is no swap file, as clr-installer documents it.
- **The dry run shows the whole partition stage.** It printed the disk's
  own commands only: for an LVM, RAID or encrypted layout, `pvcreate`,
  `vgcreate`, `lvcreate`, `mdadm --create` and `cryptsetup luksFormat`
  ran under `--apply` without having been shown. They are printed now in
  the order the stage runs them, and a test checks the stage runs that
  list and nothing else.
- **An account the target already has gets what the config says of it.**
  A login already in the target's `/etc/passwd` (an image that carries it)
  skipped `useradd` and nothing else was done: it kept the image's
  password and stayed out of `wheel` while root was locked for there
  being an administrator. It is given the config's group, name and
  password with `usermod`.
- **Packages and kernels that are not package names are refused**, and
  env names that break a hook (empty, with `=`, a leading `-`, a space or
  a control character), with `skipValidationAll` too. `packages: [-r, /]`
  reached `xbps-install` as options and installed into the live system's
  root; an empty env name replaced every `$` in the hooks, and one
  beginning with `-` was an option to `env`.
- **Revert, Cancel and Esc on Manage User** go back to the accounts the
  table was opened with, as clr-installer's do. Revert deleted every
  account, those confirmed on an earlier visit too, and Cancel kept the
  changes.
- **Wizard:** Esc in the static network form puts the interface back as
  it was (the address and gateway typed before it were installed while
  the live system kept the old ones); an IPv6 gateway is refused in the
  form, not only at Install; Install asks for a passphrase left for later
  (there was no other way back to it); a list's filter takes a pasted
  name and Backspace removes a whole character (a pasted "Zürich" was
  ignored and Enter chose the list's first entry); Test Network Settings
  goes through the proxy typed in the wizard.
- **The installed system's primary console is the live one's.** A
  `console=` repeated last on the live command line kept its first place,
  so `console=tty0 console=ttyS0,115200n8 console=tty0` installed a
  system whose `/dev/console` was the serial port.
- `crypt-options`, `mount-options`, mountpoints and subvolumes holding a
  space or a line break are refused with `skipValidationAll` too: they
  split into two kernel arguments or shifted the fields of fstab and
  crypttab.
- A partition kept with no mountpoint (a Windows MSR from `--discover`,
  a spare ESP) was probed for fstab and, with no signature, failed the
  install at the fstab stage, after the base system was in.
- An xfs filesystem under 300 MiB (mkfs.xfs's minimum since xfsprogs
  5.19), fixed-size or filling the rest, is refused before the wipe, not
  by mkfs after it.
- A volume group name the machine already has on a disk the install does
  not wipe is refused before the wipe; `vgcreate` failed after it.
- `--unmount` and `--in-target` are refused with another one-shot mode
  (`--in-target CMD --unmount` unmounted the target and never ran CMD).

## [2.2.0] - 2026-11-27

Most of what follows moves a failure from the middle of an install —
after the disk was wiped — to before anything is touched. A config that
used to fail that way, or to install something that could not boot, may
now be refused by the config check (see [STABILITY.md](STABILITY.md)).

What is verified, and how far: the unit suite (with `-race`, 95.7%
coverage), the `dry-run` and `loop` jobs and a fuzz job run on every
push; a full install through every stage runs in a user namespace with
stand-in tools, and the package dry run and `xbps-reconfigure -a` were
checked against a real xbps 0.60.7. No install in a VM was made for this
release: the bootloader, a first boot, `kexec` and Secure Boot are
unit- and namespace-tested only.

### Changed

- **Fuzzing.** Ten fuzz targets check properties, not only the absence of
  a panic: the shared-member and teardown verdicts before a wipe, every
  plan (with `skipValidationAll` too) naming clean device paths on the
  disks it lists, what sh reads back from `/etc/default/grub`, the
  network files, masking however the output is split. CI fuzzes each for
  20 s (targets found with `go test -list`, not listed); about 70 tests
  more kill the mutants a mutation pass found alive.
- **Shell completions.** `slinstaller --completion bash|fish` prints a
  completion script made from the flags themselves (stage names after
  `--through`/`--resume-from`, config files otherwise); the generated
  scripts ship in `completions/`, and a test keeps them current.

**The command line**

- `--root` is absolute, not `/`, not in a live-system directory (`/dev`,
  `/run`, `/etc`, …) and not `/tmp`, `/opt`, `/srv` or `/media` themselves;
  `--through`/`--resume-from` stage names are checked first; a relative
  `--image` is the current directory's; any two of `--list-disks`,
  `--genpass`, `--template`, `--json-yaml` and `--discover` are refused
  together; `--log-level` takes 1-5 (clr-installer's) and clamps others
  with a warning; `--list-disks` marks disks nothing can be installed to;
  `--genpass -` asks twice at a terminal.
- `--json-yaml` reads clr-installer's own descriptors (`"Version":
  "latest"`), and a user's key file — public-key lines only; a private
  key is refused. `--discover` formats a `CLR_*` role partition that
  holds no filesystem, as clr-installer does.
- A dry run says when `--apply` would refuse (a disk in use), rather
  than inviting it.
- **The install log is appended to**, after a "==== slinstaller, a new
  run ====" line, not emptied: a run its checks refused, or a second one
  beside a running install (the lock is taken first now), emptied the log
  a failed install left for its post-mortem. A resume past base-install
  no longer needs (or waits a minute for) the network; a foreign `--arch`
  is refused for the wizard's install and by `--system-check` too.

**Files written into the target**

- None is written through a link onto the live system: xbps.d, dracut
  drop-ins, mdadm.conf, fstab, `/etc/default/grub`, crypttab (new, 0600),
  the saved config and log, dhcpcd.conf, the first-boot files, write-files
  — a link there is replaced (resolved inside the target), not followed;
  an image's `/root` link is followed inside it. rc.local is taken only
  when appended to, and stays executable.
- A resolv.conf the config writes (write-files, a hook), or an image's
  own, is kept at the end; only the install-time copy is taken out.
- ESP detection matches partition flags whole (`legacy_boot` is not the
  ESP); a matched disk's named partitions must be its own; a hook's
  leftover background process no longer holds the install; "Back to
  menu" after a failure reports the failure.

**Secrets**

- Hook `env:` values named as secrets (…TOKEN, …PASSWORD, …KEY, …) are
  masked in commands, output, errors and reports; write-files contents
  and secret env values are left out of the diagnostics archive; an
  installer message (`message-url`) keeps no terminal controls and no
  https→http redirect; a download that stalls is given up after ten
  minutes under 1 KB/s.

**Confirming and stopping**

- The full-screen confirmation selects **Cancel** to begin with: one Enter
  — the key used on every screen before it — no longer starts erasing a
  disk (Tab or the arrows move to Install). It names every disk with its
  size and model, not only the first; an install that keeps the partition
  table lists what it shrinks, creates, keeps and formats, with the backup
  warning, instead of saying the disk is erased; and its hint no longer
  says Esc goes back (it leaves). The text flow's disk list, its summary
  and the retyped confirmation show the size and model too: two alike
  disks differ only by size, and a virtio disk has no model.
- With Secure Boot enforced, the installer says so — on the full-screen
  confirmation and before a text install — since the firmware will not
  start the unsigned GRUB it installs. It is a warning, not a refusal.
- **Ctrl+C stops the install after the current step** instead of quitting
  at once, which orphaned the running command (mkfs, xbps-install), left
  the target mounted and the LUKS volumes open, wrote no diagnostics and
  freed the lock for a second run on the same disk. The install now
  unmounts and is reported like any failure, with its diagnostics archive.
  - Full-screen: a SIGTERM from outside counts as a Ctrl+C. A second one
    leaves the step at once, for a step that never ends: the commands the
    step started are ended, the install gets up to 15 s to unmount, and
    the result is reported.
  - Text path: SIGINT and SIGTERM are the same request. A Ctrl+C on the
    terminal also reaches the running command, so the step usually fails
    and the install winds down from there. A second SIGINT/SIGTERM ends
    the running commands and exits at once with status 130.
- `--help` exits 0 instead of reporting "flag: help requested" as an error.

**The config and its checks**

- **The package set is dry-run before the disk is touched.** The base
  install's own `xbps-install`, with `-n`, into a throwaway root with the
  same repositories, keys, proxy and arch: a misspelt package, a kernel
  no repository carries or an offline repository missing part of the
  base used to fail at base-install, after the wipe. Only xbps's verdict
  on the package set refuses; any other failure of the dry run is a
  warning. A kernel that does not resolve is tried as `kernel-fallback`.
- **The Sunlight mirror** is the default over https, is checked to be
  reachable before anything is touched (xbps silently drops a
  repository it cannot reach, and the install took dbus and the rest
  from Void alone), and is enough on its own: a live medium without a
  local repository no longer refuses a default install. Offline and
  image installs record it in the target's `/etc/xbps.d` all the same,
  and an image gets its signing key.
- **`swupdMirror`** is an `https://` URL, `http://` with
  `allowInsecureHTTP`, or an absolute directory; anything else was read
  by xbps as a relative path, in place of the repository.
- **`--arch`** other than this machine's is refused unless the run is
  staged with `--through`: the installed packages cannot be configured
  in a chroot here.
- **clr-installer's image keys** (`iso`, `isoPublisher`,
  `isoApplicationId`, `keepImage`) and `copySwupd` are ignored without a
  warning; `postArchive: false` keeps the install log off the target.

- **Config keys nothing reads are warned about**, with their path:
  `encryptd: true` used to load without a word and install unencrypted.
  The warnings come right after the config is read — before a validation
  error the misspelling may cause — and again, briefly, where the plan
  ends. Nothing is refused; clr-installer's probe and telemetry keys,
  which slinstaller knows and ignores, are not reported.
- **A logical volume size without a unit is now bytes.** A bare number is
  bytes everywhere else in a config, but it reached `lvcreate -L`
  unchanged, where the default unit is MiB: `1073741824` asked for a
  pebibyte. *This changes the meaning of such a size*: a config that
  wrote `size: 10240` for 10 GiB now asks for 10 KiB (and is refused as
  too small) — write `10G`. A size with a K/M/G/T unit goes to lvcreate
  as written; anything else (a bare number, a decimal, an exponent) is
  converted to whole MiB, rounded up.
- A config, image, disk image or `report-url` over plain `http://`, and an
  `https://` URL that redirects to `http://`, now need
  `allowInsecureHTTP` / `--allow-insecure-http`, as a plain http
  repository already did. Only the URL as given used to be checked, and
  `report-url` (which carries the hostname and the install's errors)
  needed no allowance at all. curl gets `--proto-redir =https` for an
  https image.
- The full-screen wizard checks the config it built — validation, the
  partition plan, and that the layout fits the disk (honouring
  `--skip-validation-size`/`-all`) — when Install is pressed, and shows
  what is wrong while the answers are still there. A bad hostname, a
  duplicate mountpoint or a root larger than the disk used to end the
  program after the wizard, and every section had to be filled in again.
- The text flow checks the swap size and the hostname as they are typed,
  and asks again on an answer that is neither of the choices: a typo
  (`btfrs`, `ys` at "Encrypt?") used to be taken as the default, so an
  ext4 or unencrypted system was installed. An ended input still gets the
  default (no, at "Proceed?"), so a pipe cannot loop or proceed. A swap
  size of 0 says to choose none.

**Resume**

- **A resume needs the install it resumes.** `--resume` used to mount
  whatever the disk held — another system, or an old install of the same
  layout after a stop in the pre-install hooks — and install into it with
  no confirmation. The mount stage marks the target
  (`/.slinstaller-in-progress`), a complete install removes the
  mark, and a resume without it is refused. A resize install can be
  resumed (its partition already shrunk is not refused), a first-boot
  stopped while generating the locale generates it on resume, and a stop
  says which `--resume-from` goes on.
- **`--resume` works after a reboot of the live medium.** It used to fail
  at the mount ("mount /dev/mapper/X: no such file or directory"):
  nothing re-opened the encrypted volumes, assembled the arrays or
  activated the volume groups. A resume now brings back whatever of that
  stack is missing (a same-boot resume runs nothing), and asks for the
  passphrase once — it opens what exists, it is not a new one to type
  twice.

**Faster**

- **One initramfs build fewer.** base-install ran `chroot
  xbps-reconfigure -fa` after `xbps-install -r`, which had already
  configured every package (its scripts run chrooted): a second full
  pass, dracut included. It is `-a` now (`-fa` still with `--arch`, under
  which xbps runs no script). A plain install that adds a DKMS module
  gets one rebuild after it.
- `udevadm settle` waits 30 seconds at most, not 120, at each call; the
  Details log view builds only the rows it shows.
- An encrypted, LVM or RAID install rebuilds its initramfs with `dracut
  --regenerate-all --force` instead of a third `xbps-reconfigure -fa`,
  which re-ran every package's configure script (and grub-mkconfig) for
  that one file. A target without dracut still gets `-fa`.
- The keyfile's LUKS slot (non-root volumes, an encrypted `/boot`) uses
  pbkdf2 at cryptsetup's minimum iterations: the key is 2048 random bytes,
  so argon2id's ~2 s and up to 1 GiB per volume bought nothing, at install
  or at every boot. The passphrase slot keeps its strong default.
- The full-screen log view (Details) wraps and classifies each line once,
  as it arrives, instead of all 10000 on every frame: 1.3 ms a frame
  instead of 37 ms, so an open log view no longer holds back the commands
  whose output it shows.
- The network check before an install makes one request per attempt, each
  bounded by what is left of its minute: an unreachable mirror took two
  10 s timeouts per attempt and 66 s in all.
- After `wipe: discard` on a disk, mkfs is told not to discard its
  partitions a second time (`-E nodiscard`, `-K`, `-t 0`): tens of
  seconds on some SATA/USB bridges. `wipe: zero` writes zeros rather than
  trimming, so mkfs still discards there.

### Fixed

**Disks and partitions**

- **What the confirmation did not name is not touched.** A RAID name like
  `../sdz` pointed mdadm, parted and mkfs at /dev/sdz; names are one path
  component now. A member of an array, pool or multi-device btrfs that
  spans another disk is refused even when not assembled (lsblk's UUIDs),
  for wiped disks, disk images and a reuse install's formatted
  partitions. The teardown re-checks its own listing. A resume looks for
  its mark on the root mounted read-only, with no journal replayed,
  before mounting anything writable.
- **`skipValidationAll` skips the layout checks, not the safety ones.** It
  turned the whole config check off, and with it the only check on a
  RAID name (`../sdz` planned `mkfs.ext4 /dev/md/../sdz`), a disk name
  (`../sdz`, `""`), an LV or VG name, an interface name or bond mode
  (written unquoted into `/etc/rc.local`, run by sh as root at every
  boot) and a line break in a network field or the hostname (a new
  directive in `dhcpcd.conf`, a new entry in `/etc/hosts`). Those are
  refused with it set too; a layout it is meant to force through still
  goes. A keyboard or language that is not a plain name is single-quoted
  in `/etc/rc.conf` and `/etc/locale.conf` (`KEYMAP="us$(cmd)"` ran cmd).
- A secret printed across the 64 KiB point of a long line with no line
  break was shown in part; a NaN or Inf in parted's output was read as a
  position on the disk; a volume group of physical volumes all too small
  for LVM's own space was not checked.
- **Intel RST/DDF containers, multipath and dmraid** are released before
  the wipe: lsblk names them "container", "mpath" and "dmraid", which
  were not taken for holders, and wipefs then failed with EBUSY after
  the confirmation. zram devices, an eMMC's boot areas and empty card
  readers are no longer offered as disks.
- **Partition names are checked against what parted makes.** A
  partition's device came from its `name` unchecked, while parted numbers
  what it cuts itself. A child named for another disk (`nvme0n1p2` under
  `sda`) was formatted though only `sda` was confirmed; a gap (`sda1`,
  `sda5`), a duplicate or an empty list entry had mkfs hit a missing or
  the wrong partition after the table was wiped; a fill partition last in
  the list but not on the disk gave overlapping ranges. A new table now
  has to be numbered 1..n on its own disk, and the fill partition has to
  be the last on the disk.
- **A Safe install could format an existing partition.** parted cuts a
  `create` partition at the lowest free number, but it was formatted as
  the device its name gave: on a disk with `sda1`..`sda4`, a new
  partition the plan called `sda2` became `sda5`, and mkfs formatted the
  existing `sda2`. The partition stage checks each number against the
  disk's table before anything — a shrink included — is touched, and the
  dry run reads the table the same way and says what parted will number
  it.
- **The partition editor after a Safe install formatted existing
  partitions.** The Safe layout's entries stayed in the editor's list,
  where new entries are numbered by position: adding a 4G swap ran
  `mkswap` on the disk's existing third partition. The editor (and
  Advanced) now starts from an empty layout, which wipes the disk as the
  confirmation says.
- A staged run (`--through`) skipped every disk check along with the tool
  checks, and would partition and format a partition given as the disk, a
  disk in use, or one sharing an array with a disk the install keeps. The
  disk checks now run on every install, staged or resumed.
- A root that fills its disk was never held to the root minimum: a 3 GB
  disk with a 512M ESP and 2G swap was wiped and given a 0.3 GB root.
  What is left for each disk's filling partition now has to reach its
  minimum (the install type's root minimum for `/`). A disk that reports
  size 0 (an empty card reader) is refused as a target.
- Absurd sizes wrapped instead of being refused: `inf`, `nan`, `1e30` or
  `16777216T` became an arbitrary number or 0, and an oversized layout
  could pass the fit check as a tiny one. Both size parsers refuse them,
  and the fit check saturates its sum.
- Mountpoints were compared and written to fstab as given: `/home/` and
  `/home` passed as two, `/boot/../tmp` passed, `/a b` broke the fstab
  line. A mountpoint has to be a clean absolute path without spaces or
  control characters, not under `/proc`, `/sys`, `/dev` or `/run` — on
  partitions, logical volumes, arrays and their partitions, and btrfs
  subvolumes alike.
- Any entry mounted at `/` counted as the root, so a swap at `/` validated
  and the install had no root filesystem. A swap with a mountpoint and a
  vfat root are refused.
- Reinstalling over a RAID array or volume group spanning several disks
  failed while releasing the disks: each disk's teardown stopped the
  shared array again. The disks are released together, each holder once.
- A Safe install's shrink stopped when `e2fsck` repaired the filesystem
  (exit 1 or 2), though the check exists to make the shrink safe; only 4
  and above stop it now.
- The full-screen shrink popup read a size differently from the config:
  `60000` was accepted and then refused at Install, and `59.5G` was
  refused. It now reads sizes as the config does, wants a unit and at
  least a MiB.
- A swap file on a btrfs root was copy-on-write (and compressed under
  `compress=`), which the kernel refuses: the install succeeded and
  `swapon` failed at every boot. It is marked No_COW before it is
  allocated.
- The end of an install (and `--unmount`) only detached the target's
  filesystems lazily, with no sync: "Install complete." could print with
  the data still in the page cache. Everything is synced and unmounted
  properly; a filesystem still held open is detached, and the log says
  which. The target is unmounted even when putting its resolv.conf back
  fails.

**Encryption, RAID and LVM**

- **Logical volumes have to fit their volume group** before the wipe: a
  500G LV on a 64G disk failed at lvcreate, after partitioning.
- The LUKS keyfile is synced before it goes into the key slots; every
  encrypted PV of an encrypted /boot's group is GRUB-readable (pbkdf2).
- `encrypted: true` on a plain partition, a RAID member or a logical
  volume was ignored and the volume installed unencrypted. It is read on
  a `type: lvm` partition and a RAID array only; elsewhere it is refused,
  pointing at `type: crypt` or at the array.
- A volume group over an encrypted and a plain physical volume was
  accepted: the root, and with an encrypted `/boot` the initramfs and the
  key that opens every LUKS volume, could land on the plain one. Such a
  group is refused.
- Two LUKS containers could get the same mapper name (two encrypted swaps,
  `/var/lib` beside `/var-lib`, an array named `root` beside an encrypted
  root), and the second `luksOpen` failed after the wipe. The later one
  now gets its device's name added (`cryptswap-sdb2`).
- A Safe install with Enable Encryption ticked installed unencrypted
  without a word; it is refused, saying why. The box ticked and left
  without Confirm no longer shows [x] over an unencrypted layout.
- A resume after the keyfile was added to some encrypted volumes but not
  all skipped them all, and crypttab pointed the rest at a key they did
  not hold. Each volume the keyfile does not open is given it.
- A resume failed on an array the live system had assembled by itself at
  boot under a name of its own (`md127`): the members were busy. Such an
  array is stopped and assembled under the install's name.
- `/etc/mdadm.conf` on the installed system listed every array the live
  system had assembled, data arrays on other disks included. It lists the
  install's own arrays.
- A logical volume that fills its group, declared before a sized one, was
  created first and took everything; the next `lvcreate` failed after the
  wipe. Sized volumes are created first.
- Logical volume sizes were not checked (`10GB` reached `lvcreate`), and
  volume group and logical volume names LVM refuses (a space, a leading
  `-`, `.`/`..`, names LVM keeps such as `snapshot*` or `*_rimage`)
  passed and failed after the wipe; a space also broke the fstab line.
  Both are refused.

**Boot**

- With an encrypted `/boot`, the initramfs keyfile was named without its
  key device, so dracut looked for it on every disk, never found it, and
  asked for the passphrase a second time. It is `rd.luks.key=<path>:/`.
- The root volume's `discard` crypt option never reached the initramfs;
  it is passed as `rd.luks.allow-discards=<uuid>` too.
- The recovery-mode boot entries could not boot an encrypted, LVM or RAID
  root, and came up as runit: every argument went into
  `GRUB_CMDLINE_LINUX_DEFAULT`, which recovery entries leave out. What the
  system cannot boot without is in `GRUB_CMDLINE_LINUX` now. Kernel
  arguments are quoted for the shell that sources `/etc/default/grub`; one
  holding `' $ ; \ `` ` `` or a line break is refused, as GRUB's own parser of
  grub.cfg breaks it (a `'` stopped grub-mkconfig at the end of the install).
- `init=/usr/bin/slinit` was added whenever the slinit binary was in the
  target, even with no slinit boot service (a Void base with the slinit
  package added): the system did not boot. It now needs the boot service
  too, and an `init=` the config adds itself is no longer overridden.
- `power-state: kexec` failed on every btrfs root mounted from a
  subvolume (GRUB names the files `/@/boot/…`), and with an early
  microcode image listed first it loaded that instead of the initramfs.
- Each install or resume added another set of firmware boot entries for
  the spare ESPs, the new ones first in BootOrder. An entry with the same
  label is removed before it is made again.
- Replaying a profile on a CPU of the other vendor turned an explicit
  `intel_iommu=off` into `amd_iommu=on` (and back). Off stays off, and
  `kvm_intel.nested=1` is translated like `kvm-intel.nested=1`.
- `system-upgrade` ran before the Sunlight mirror was written to the
  target's `/etc/xbps.d`, so the upgrade resolved every package against
  Void alone. The mirror is recorded first.

**First boot**

- The language was never checked: `language: xx_YY.garbage` went to the
  target's `LANG`. It is checked like the timezone and keymap, against
  the locales the live system's C library lists (not on musl). A language
  spelled `en_US.utf8` is now also generated (the codeset is compared as
  glibc compares it).
- A user's full name with a colon or line break was refused by `useradd`
  at first boot, after the wipe; the config check refuses it now.
- A config whose administrators had no password between them (an SSH key
  only) installed a system nobody could become root on: root's password
  is locked once there is an administrator, and sudo asks for one. It is
  refused, unless the config sees to sudo itself (a `write-files` entry
  under `/etc/sudoers*`, or post-install or stage hooks).
- The installed system's `/var/db/xbps/keys` was mode 0700; it is 0755.

**Network**

- Two or more static routes on an interface were written on one
  `static routes=` line, which dhcpcd refuses whole. Each route has its
  own line.
- An IPv6 gateway or static route passed validation and was dropped at
  boot (dhcpcd's static `routers`/`routes` are IPv4 only). They are
  refused, saying that IPv6 routes come from router advertisements.
- Interface names, DNS servers and the search domain were barely checked,
  yet go unquoted into the boot script that makes bonds, VLANs and
  bridges (run as root) and into dhcpcd.conf. Names are letters, digits
  and `. _ : -`; DNS entries are IP addresses (space-separated); domains
  are domain names.
- A netmask such as `33` or `200` was written to dhcpcd.conf as is, and an
  address's IP was not compared with its declared family. Both are
  checked.
- With `copyNetwork`, the `denyinterfaces` line for bond and bridge
  members landed after the live system's dhcpcd.conf; inside an
  `interface` block dhcpcd ignored it and asked for a lease on every
  member. It goes before the first block.
- The full-screen network form took any address: one without its prefix
  was dropped later and the interface installed on DHCP while the hub
  showed it static. Address, gateway and DNS are checked as typed.
- A static address applied to the live system flushed the interface's
  IPv6 link-local address too; only global addresses are flushed.

**Images**

- A `disk-image` write reported success when the download or the
  decompression failed (the pipeline's status was `dd`'s), and the
  machine rebooted onto half an image. Every stage is checked. A signed
  URL (`img.raw.xz?sig=…`) is decompressed as its path says, and `.bz2`
  is read too.
- An image was unpacked with the live `/proc`, `/sys` and `/dev` bound
  into the target: its `dev/`, `proc/` and `sys/` entries landed on the
  live ones, and its `etc/resolv.conf` was overwritten. The binds come off
  for the unpack; the image's resolv.conf is kept.
- An install from an image kept the image's machine-id, random seed and
  SSH host keys, shared by every machine installed from it. They are
  removed after the unpack: first boot writes a machine-id and seed of
  its own, and the host keys are made anew (`ssh-keygen -A`, when the
  image has it).

**Security**

- **Save Configuration** wrote with os.WriteFile, whose 0600 applies only
  to a new file: an existing 0644 file kept its mode, with the accounts'
  password hashes in it, and a link was written through. It writes a
  new 0600 file and renames it into place.
- The default install log in `/tmp` was opened as root with
  O_CREAT|O_TRUNC, so a link another user left there had root overwrite
  the file it pointed to (and a FIFO hung the install). A link is not
  followed, anything there that is not root's own regular file is
  replaced by a fresh 0600 one, and the diagnostics archive is created
  exclusively.
- Links in a preserved `/home`, an image or a reused root were followed
  onto the live or new system: `authorized_keys -> ../../../etc/shadow`
  had the keys written over the new shadow file; an absolute
  `/etc/dhcpcd.conf` or `/etc/ssh` link had the live system's file
  rewritten or its host keys removed; the target's resolv.conf link
  had the live one written. Such links are replaced or resolved inside
  the target, a home that is itself a link is refused, and a preserved
  `.ssh` is no longer `chown -R`'d (a hard link could hand over another
  user's file).
- `SLINSTALLER_LUKS_PASSPHRASE` stayed in the environment of every command
  the install ran, package scripts and hooks included. It is read and
  cleared at the start, for any layout.
- A failed command's error, the dry run and the logs showed secrets: a
  `useradd -p` hash reached the screen, the diagnostics archive and a
  `report-url`, and proxy credentials every log. Crypt hashes and the
  password in a URL (`http://user:***@proxy`, `@`, `#` and `?` in it
  included) are masked everywhere, the `report-url` warning included.

**Resume and failures**

- A failed or stopped run left the live system's resolv.conf as the
  installed disk's, with the target's own set aside: rebooted into, the
  system was pinned to the live medium's DNS. Every way out of an install
  now puts it back, and a resume recognises its own leftover copy by a
  mark rather than by comparing it with a live file that may have changed
  since. Mounts that could not be released are logged as warnings — what
  explains a re-run that finds the mapper or array busy.
- The diagnostics archive now has `/proc/mdstat`, the device-mapper tree,
  the mount table and inactive arrays, what explains a failure to release
  a disk.
- Probe failures (blkid, parted, lsblk, mdadm, cryptsetup, openssl) said
  only `exit status N`; the tool's own message is kept.
- `--system-check` named a missing tool once per place it is needed;
  each is named once.

**Full-screen and text front-ends**

- **No 5 s wait at start on a terminal that does not answer.** bubbletea
  v1's package `init()` asked the terminal for its background colour (OSC
  11 + DSR) before `main`, in `--text` too: on a QEMU serial console the
  first output came after 5.01 s, with the query left on the screen and a
  late reply landing in the first answer typed. bubbletea v1.2.4 is
  vendored in `third_party/bubbletea` without `tea_init.go` (MIT, LICENSE
  kept, wired with a go.mod `replace`); the query is made only before a
  full-screen program. First output now after 0.01 s. An update of
  bubbletea means updating that copy by hand.
- **The wizard keeps the disk the user chose.** Rescan Media kept the
  choice as an index, so a USB stick listed ahead of it moved the target
  (and the layout made for it) to another disk; it is found again by
  path, and one that is gone is unchosen. The media dialog's Esc and
  Cancel put back the disk it opened with. Enable Encryption ticked with
  a layout that encrypts nothing is refused at Install.
- **Hidden prompts** keep a passphrase's spaces and put echo back on
  Ctrl+C; the disk prompt ends on a read error and picks no disk; the
  retype confirmation, at a terminal, asks again (three tries).
- The wizard checks what the installer would refuse after it — kernel
  arguments this machine cannot meet, an unknown time zone, keymap or
  language — at Install, where it can be fixed; these ended the program
  with every answer lost. Esc and Cancel leave Kdump, Kernel Command
  Line, Swupd Mirror and Bundles as they were opened; a colon in a full
  name is refused at its step.
- A very long output line (over 1 MiB) hung the full-screen install: the
  runner stopped reading and the command blocked on the pipe. The rest is
  drained.

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
