#!/bin/bash
# The storage half of an install, for real, on loop devices.
#
# The unit suite checks which commands the installer would run. It cannot
# check that those commands do what is expected of them: that sfdisk really
# shrinks one partition and leaves its neighbours alone, that a btrfs
# filesystem really ends up with its subvolumes mounted where the config
# says. This does, against block devices that are files — a loop device is
# a real disk to parted, mkfs and the kernel, and nothing on it is anyone's
# data.
#
# It stops at the partition and mount stages (--through). The base system,
# the bootloader and a first boot need a live medium and UEFI firmware;
# that is the VM harness in demo/vm.
#
# Needs root (losetup, mount) and: parted, e2fsprogs, dosfstools,
# btrfs-progs, util-linux. Usage:  sudo tests/loop/run.sh <slinstaller binary>
set -euo pipefail

BIN="${1:?usage: run.sh <slinstaller binary>}"
BIN="$(readlink -f "$BIN")"
[ "$(id -u)" = 0 ] || { echo "run.sh: needs root" >&2; exit 2; }

WORK="$(mktemp -d)"
LOOPS=()
RAID_UP=0
cleanup() {
	[ "$RAID_UP" = 1 ] && mdadm --stop /dev/md0 >/dev/null 2>&1 || true
	# Unmount whatever a staged run left mounted, then release the disks.
	for m in $(findmnt -rn -o TARGET | grep "^$WORK" | sort -r); do umount "$m" || true; done
	for l in "${LOOPS[@]}"; do losetup -d "$l" 2>/dev/null || true; done
	rm -rf "$WORK"
}
trap cleanup EXIT

fail() { echo "FAIL: $*" >&2; exit 1; }
pass() { echo "ok: $*"; }

# new_disk <MiB> — a blank disk of that size; prints its device.
new_disk() {
	local img="$WORK/disk-$RANDOM.img" dev
	truncate -s "${1}M" "$img"
	dev="$(losetup --find --show --partscan "$img")"
	LOOPS+=("$dev")
	echo "$dev"
}

size_mib() { echo $(( $(blockdev --getsize64 "$1") >> 20 )); }
fstype()   { blkid -p -s TYPE -o value "$1" 2>/dev/null || true; }

# install <through> <config> <disk> — run the installer up to a stage,
# confirming the disk as an operator would.
install() {
	local through="$1" cfg="$2" disk="$3"
	echo "$disk" | "$BIN" --apply --through "$through" --root "$WORK/target" \
		--log "$WORK/install.log" "$cfg" || { cat "$WORK/install.log" >&2; fail "install through $through"; }
	udevadm settle 2>/dev/null || true
}

mkdir -p "$WORK/target"

# --- 1. A plain install: the table and the filesystems ---------------------
# 3 GiB: the root that fills the disk has to be left its 2 GB minimum, which
# the disk-fit check holds staged runs to as well.
disk="$(new_disk 3072)"; name="$(basename "$disk")"
cat > "$WORK/plain.yaml" <<YAML
targetMedia:
- name: $name
  type: disk
  children:
  - { name: ${name}p1, type: part, fstype: vfat, mountpoint: /boot, size: 256M }
  - { name: ${name}p2, type: part, fstype: swap, size: 128M }
  - { name: ${name}p3, type: part, fstype: ext4, mountpoint: / }
YAML
install partition "$WORK/plain.yaml" "$disk"
[ "$(fstype "${disk}p1")" = vfat ] || fail "plain: p1 is '$(fstype "${disk}p1")', want vfat"
[ "$(fstype "${disk}p2")" = swap ] || fail "plain: p2 is '$(fstype "${disk}p2")', want swap"
[ "$(fstype "${disk}p3")" = ext4 ] || fail "plain: p3 is '$(fstype "${disk}p3")', want ext4"
# The first partition starts at parted's alignment, so it is a megabyte or
# so short of the 256 it ends at.
p1="$(size_mib "${disk}p1")"
[ "$p1" -ge 250 ] && [ "$p1" -le 256 ] || fail "plain: p1 is $p1 MiB, want about 256"
# The root takes what is left: everything but the two before it and the
# table's own megabytes.
[ "$(size_mib "${disk}p3")" -gt 1600 ] || fail "plain: the root did not fill the disk ($(size_mib "${disk}p3") MiB)"
parted -m -s "$disk" print | grep -q '^1:.*esp' || fail "plain: p1 is not flagged as the ESP"
pass "plain install: table, ESP flag, vfat + swap + ext4"

# --- 2. --discover describes it, keeping everything -------------------------
"$BIN" --discover "$disk" > "$WORK/found.yaml" 2> "$WORK/found.notes"
grep -q 'reusePartitions: true' "$WORK/found.yaml" || fail "discover: not a reuse config"
# Counted on the entries themselves: the file's own header mentions
# "preserve: true" too.
[ "$(grep -c '^ *preserve: true' "$WORK/found.yaml")" = 3 ] || { cat "$WORK/found.yaml" >&2; fail "discover: not every partition is preserved"; }
grep -q "name: ${name}p3" "$WORK/found.yaml" && grep -q 'fstype: ext4' "$WORK/found.yaml" || fail "discover: the root is not described"
pass "discover: the layout just made, all of it preserved"

# --- 3. Shrink a partition that holds data, and install beside it ----------
disk="$(new_disk 2048)"; name="$(basename "$disk")"
parted -s "$disk" mklabel gpt
parted -s "$disk" mkpart EFI fat32 1MiB 257MiB
parted -s "$disk" set 1 esp on
parted -s "$disk" mkpart data ext4 257MiB 1800MiB
partprobe "$disk"; udevadm settle 2>/dev/null || true
mkfs.vfat -F32 "${disk}p1" >/dev/null
mkfs.ext4 -q -F "${disk}p2"
uuid_before="$(blkid -p -s PART_ENTRY_UUID -o value "${disk}p2")"
start_before="$(cat "/sys/class/block/$(basename "${disk}p2")/start")"
mkdir -p "$WORK/data"
mount "${disk}p2" "$WORK/data"
head -c 200M /dev/urandom > "$WORK/data/payload"
sum_before="$(sha256sum < "$WORK/data/payload")"
umount "$WORK/data"

cat > "$WORK/shrink.yaml" <<YAML
reusePartitions: true
targetMedia:
- name: $name
  type: disk
  children:
  - { name: ${name}p1, type: part, fstype: vfat, mountpoint: /boot/efi, preserve: true }
  - { name: ${name}p2, type: part, fstype: ext4, preserve: true, resize: 800M }
  - { name: ${name}p3, type: part, fstype: ext4, mountpoint: /, create: true }
YAML
install partition "$WORK/shrink.yaml" "$disk"

[ "$(size_mib "${disk}p2")" = 800 ] || fail "shrink: p2 is $(size_mib "${disk}p2") MiB, want 800"
# The partition is still the same partition: where it starts and who it is.
[ "$(cat "/sys/class/block/$(basename "${disk}p2")/start")" = "$start_before" ] || fail "shrink: p2 moved"
[ "$(blkid -p -s PART_ENTRY_UUID -o value "${disk}p2")" = "$uuid_before" ] || fail "shrink: p2 lost its partition UUID"
e2fsck -f -n "${disk}p2" >/dev/null || fail "shrink: the shrunk filesystem is not clean"
mount -o ro "${disk}p2" "$WORK/data"
[ "$(sha256sum < "$WORK/data/payload")" = "$sum_before" ] || fail "shrink: the data changed"
umount "$WORK/data"
# The ESP was neither moved nor formatted (its filesystem is still there).
[ "$(fstype "${disk}p1")" = vfat ] || fail "shrink: the ESP was disturbed"
# The new root is in the room the shrink left, and after the old partition.
[ "$(fstype "${disk}p3")" = ext4 ] || fail "shrink: p3 is '$(fstype "${disk}p3")', want ext4"
[ "$(size_mib "${disk}p3")" -gt 900 ] || fail "shrink: the new root is only $(size_mib "${disk}p3") MiB"
p2_end=$(( start_before + $(cat "/sys/class/block/$(basename "${disk}p2")/size") ))
[ "$(cat "/sys/class/block/$(basename "${disk}p3")/start")" -ge "$p2_end" ] || fail "shrink: the new root overlaps the old partition"
pass "shrink: 1543 -> 800 MiB with its data intact, new root in the freed space"

# A size the data does not fit in must be refused with nothing changed.
cat > "$WORK/toosmall.yaml" <<YAML
reusePartitions: true
targetMedia:
- name: $name
  type: disk
  children:
  - { name: ${name}p1, type: part, fstype: vfat, mountpoint: /boot/efi, preserve: true }
  - { name: ${name}p2, type: part, fstype: ext4, preserve: true, resize: 100M }
  - { name: ${name}p3, type: part, fstype: ext4, mountpoint: /, preserve: true }
YAML
if echo "$disk" | "$BIN" --apply --through partition --root "$WORK/target" --log "$WORK/install.log" "$WORK/toosmall.yaml" >/dev/null 2>&1; then
	fail "shrink: a size smaller than the data was accepted"
fi
[ "$(size_mib "${disk}p2")" = 800 ] || fail "shrink: a refused resize still changed the partition"
mount -o ro "${disk}p2" "$WORK/data"
[ "$(sha256sum < "$WORK/data/payload")" = "$sum_before" ] || fail "shrink: a refused resize damaged the data"
umount "$WORK/data"
pass "shrink: a size below the data is refused, partition and data untouched"

# --- 3a. A new partition named for one that exists is refused --------------
# parted numbers a new partition itself (the lowest free number); a config
# that names an existing one would have had mkfs format it. Read from the
# real table, before anything is touched.
disk="$(new_disk 2048)"; name="$(basename "$disk")"
parted -s "$disk" mklabel gpt
parted -s "$disk" mkpart EFI fat32 1MiB 257MiB
parted -s "$disk" set 1 esp on
parted -s "$disk" mkpart data ext4 257MiB 800MiB
partprobe "$disk"; udevadm settle 2>/dev/null || true
mkfs.vfat -F32 "${disk}p1" >/dev/null
mkfs.ext4 -q -F "${disk}p2"
uuid_before="$(blkid -p -s UUID -o value "${disk}p2")"
for n in 2 3; do
	cat > "$WORK/create$n.yaml" <<YAML
reusePartitions: true
targetMedia:
- name: $name
  type: disk
  children:
  - { name: ${name}p1, type: part, fstype: vfat, mountpoint: /boot/efi, preserve: true }
  - { name: ${name}p$n, type: part, fstype: ext4, mountpoint: /, create: true, start: 900M, size: 1000M }
YAML
done
if echo "$disk" | "$BIN" --apply --through partition --root "$WORK/target" --log "$WORK/install.log" "$WORK/create2.yaml" >"$WORK/create2.out" 2>&1; then
	fail "create: a new partition named for the existing p2 was accepted"
fi
grep -q "parted will number it 3" "$WORK/create2.out" || { cat "$WORK/create2.out" >&2; fail "create: the refusal does not say what parted will number it"; }
[ "$(blkid -p -s UUID -o value "${disk}p2")" = "$uuid_before" ] || fail "create: the existing p2 was formatted"
[ ! -e "${disk}p3" ] || fail "create: a partition was cut although the install was refused"
install partition "$WORK/create3.yaml" "$disk"
[ "$(fstype "${disk}p3")" = ext4 ] && [ "$(blkid -p -s UUID -o value "${disk}p2")" = "$uuid_before" ] || fail "create: p3 not made, or p2 touched"
pass "create: a partition named for an existing one is refused untouched; named as parted numbers it, it is made"

# --- 3b. The same beside an ntfs partition, as next to another system -------
if command -v mkfs.ntfs >/dev/null && command -v ntfscp >/dev/null; then
	disk="$(new_disk 2048)"; name="$(basename "$disk")"
	parted -s "$disk" mklabel gpt
	parted -s "$disk" mkpart EFI fat32 1MiB 257MiB
	parted -s "$disk" set 1 esp on
	parted -s "$disk" mkpart other ntfs 257MiB 1800MiB
	partprobe "$disk"; udevadm settle 2>/dev/null || true
	mkfs.vfat -F32 "${disk}p1" >/dev/null
	mkfs.ntfs -Q -F "${disk}p2" >/dev/null
	head -c 100M /dev/urandom > "$WORK/payload.bin"
	ntfscp -f "${disk}p2" "$WORK/payload.bin" payload.bin
	sum_before="$(sha256sum < "$WORK/payload.bin")"
	cat > "$WORK/ntfs.yaml" <<YAML
reusePartitions: true
targetMedia:
- name: $name
  type: disk
  children:
  - { name: ${name}p1, type: part, fstype: vfat, mountpoint: /boot/efi, preserve: true }
  - { name: ${name}p2, type: part, fstype: ntfs, preserve: true, resize: 800M }
  - { name: ${name}p3, type: part, fstype: ext4, mountpoint: /, create: true }
YAML
	install partition "$WORK/ntfs.yaml" "$disk"
	[ "$(size_mib "${disk}p2")" = 800 ] || fail "ntfs: p2 is $(size_mib "${disk}p2") MiB, want 800"
	[ "$(fstype "${disk}p2")" = ntfs ] || fail "ntfs: p2 is no longer ntfs"
	[ "$(ntfscat -f "${disk}p2" payload.bin | sha256sum)" = "$sum_before" ] || fail "ntfs: the data changed"
	[ "$(fstype "${disk}p3")" = ext4 ] || fail "ntfs: the new root was not made"
	pass "shrink ntfs: 1543 -> 800 MiB with its data intact, new root beside it"
else
	echo "skip: no ntfs tools (ntfs-3g) for the ntfs shrink case"
fi

# --- 3c. And a btrfs partition, which is shrunk while mounted ----------------
disk="$(new_disk 2048)"; name="$(basename "$disk")"
parted -s "$disk" mklabel gpt
parted -s "$disk" mkpart EFI fat32 1MiB 257MiB
parted -s "$disk" set 1 esp on
parted -s "$disk" mkpart data btrfs 257MiB 1800MiB
partprobe "$disk"; udevadm settle 2>/dev/null || true
mkfs.vfat -F32 "${disk}p1" >/dev/null
mkfs.btrfs -q -f "${disk}p2"
mount "${disk}p2" "$WORK/data"
head -c 100M /dev/urandom > "$WORK/data/payload"
sum_before="$(sha256sum < "$WORK/data/payload")"
umount "$WORK/data"
cat > "$WORK/btrfs-shrink.yaml" <<YAML
reusePartitions: true
targetMedia:
- name: $name
  type: disk
  children:
  - { name: ${name}p1, type: part, fstype: vfat, mountpoint: /boot/efi, preserve: true }
  - { name: ${name}p2, type: part, fstype: btrfs, preserve: true, resize: 900M }
  - { name: ${name}p3, type: part, fstype: ext4, mountpoint: /, create: true }
YAML
install partition "$WORK/btrfs-shrink.yaml" "$disk"
[ "$(size_mib "${disk}p2")" = 900 ] || fail "btrfs shrink: p2 is $(size_mib "${disk}p2") MiB, want 900"
btrfs check --readonly "${disk}p2" >/dev/null 2>&1 || fail "btrfs shrink: the shrunk filesystem does not check clean"
mount -o ro "${disk}p2" "$WORK/data"
[ "$(sha256sum < "$WORK/data/payload")" = "$sum_before" ] || fail "btrfs shrink: the data changed"
umount "$WORK/data"
[ "$(fstype "${disk}p3")" = ext4 ] || fail "btrfs shrink: the new root was not made"
pass "shrink btrfs: 1543 -> 900 MiB with its data intact, new root beside it"

# --- 4. btrfs subvolumes, mount options, a discarded disk -------------------
disk="$(new_disk 3072)"; name="$(basename "$disk")"
cat > "$WORK/btrfs.yaml" <<YAML
targetMedia:
- name: $name
  type: disk
  wipe: discard
  children:
  - { name: ${name}p1, type: part, fstype: vfat, mountpoint: /boot, size: 256M }
  - name: ${name}p2
    type: part
    fstype: btrfs
    mountpoint: /
    mount-options: noatime,compress=zstd
    subvolumes:
    - { name: "@", mountpoint: / }
    - { name: "@home", mountpoint: /home }
YAML
install mount "$WORK/btrfs.yaml" "$disk"
opts() { findmnt -rn -o OPTIONS "$1"; }
findmnt -rn "$WORK/target" >/dev/null || fail "btrfs: the root is not mounted"
case "$(opts "$WORK/target")" in *subvol=/@*) ;; *) fail "btrfs: / is not the @ subvolume: $(opts "$WORK/target")";; esac
case "$(opts "$WORK/target")" in *compress=zstd*) ;; *) fail "btrfs: / is not mounted with compression: $(opts "$WORK/target")";; esac
case "$(opts "$WORK/target/home")" in *subvol=/@home*) ;; *) fail "btrfs: /home is not the @home subvolume: $(opts "$WORK/target/home")";; esac
[ "$(findmnt -rn -o FSTYPE "$WORK/target/boot")" = vfat ] || fail "btrfs: /boot is not the ESP"
# What is written at / lands in @, not in the top of the filesystem.
touch "$WORK/target/marker"
mkdir -p "$WORK/top"; mount -o subvolid=5 "${disk}p2" "$WORK/top"
[ -e "$WORK/top/@/marker" ] && [ -d "$WORK/top/@home" ] || fail "btrfs: the subvolumes are not where the mounts say"
umount "$WORK/top"
pass "btrfs: @ at /, @home at /home, compress=zstd, on a discarded disk"

# --- 5. RAID 1 over two disks, the array partitioned ------------------------
if command -v mdadm >/dev/null && [ ! -e /dev/md0 ]; then
	disk="$(new_disk 1024)"; name="$(basename "$disk")"
	disk2="$(new_disk 1024)"; name2="$(basename "$disk2")"
	cat > "$WORK/raid.yaml" <<YAML
targetMedia:
- name: $name
  type: disk
  children:
  - { name: ${name}p1, type: part, fstype: vfat, mountpoint: /boot/efi, size: 128M }
  - { name: ${name}p2, type: raid, raid: md0 }
- name: $name2
  type: disk
  children:
  - { name: ${name2}p1, type: part, fstype: vfat, size: 128M, esp: true }
  - { name: ${name2}p2, type: raid, raid: md0 }
raid:
- name: md0
  level: 1
  partitions:
  - { fstype: ext4, mountpoint: /, size: 400M }
  - { fstype: ext4, mountpoint: /srv }
skipValidationSize: true
YAML
	# Both disks are confirmed by the first one's path.
	echo "$disk" | "$BIN" --apply --through partition --root "$WORK/target" \
		--log "$WORK/install.log" "$WORK/raid.yaml" || { cat "$WORK/install.log" >&2; fail "install through partition (raid)"; }
	udevadm settle 2>/dev/null || true
	RAID_UP=1
	[ "$(cat /sys/block/md0/md/level)" = raid1 ] || fail "raid: md0 is not a RAID 1 array"
	[ "$(cat /sys/block/md0/md/raid_disks)" = 2 ] || fail "raid: md0 does not have two members"
	[ "$(fstype /dev/md0p1)" = ext4 ] && [ "$(fstype /dev/md0p2)" = ext4 ] || fail "raid: the array's partitions were not made (p1 '$(fstype /dev/md0p1)', p2 '$(fstype /dev/md0p2)')"
	[ "$(size_mib /dev/md0p1)" -ge 390 ] && [ "$(size_mib /dev/md0p1)" -le 400 ] || fail "raid: p1 is $(size_mib /dev/md0p1) MiB, want about 400"
	# Each disk has an ESP: the mounted one and the spare.
	for d in "$disk" "$disk2"; do
		parted -m -s "$d" print | grep -q '^1:.*esp' || fail "raid: $d has no ESP-flagged partition"
		[ "$(fstype "${d}p1")" = vfat ] || fail "raid: ${d}p1 is not vfat"
	done
	mdadm --stop /dev/md0 >/dev/null 2>&1 || true
	RAID_UP=0
	pass "raid 1: array assembled from two disks and partitioned, an ESP on each disk"
else
	echo "skip: no mdadm, or /dev/md0 is taken, for the RAID case"
fi

echo "all loop-device cases passed"
