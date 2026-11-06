#!/bin/sh
# guest-prep.sh — run inside the Void live guest before slinstaller.
#
# The base live ISO carries mkfs.*, mkswap, grub and efibootmgr, but not
# parted — nor cryptsetup/lvm2/mdadm for the encrypted, LVM and RAID
# configs. This refreshes xbps and installs the tools the installs need.
#
#   sh /run/slshare/guest-prep.sh
set -eu

xbps-install -Suy xbps
xbps-install -Sy parted cryptsetup lvm2 mdadm

echo "Ready. Now, e.g.:"
echo "  /run/slshare/slinstaller --apply /run/slshare/config.yaml           # basic"
echo "  /run/slshare/slinstaller --apply /run/slshare/config-luks.yaml      # encrypted root"
echo "  /run/slshare/slinstaller --apply /run/slshare/config-luks-split.yaml # crypt / + crypt /home"
echo "  /run/slshare/slinstaller --apply /run/slshare/config-lvm.yaml       # LVM"
echo "  /run/slshare/slinstaller --apply /run/slshare/config-lvm-luks.yaml  # LVM-on-LUKS"
echo "  /run/slshare/slinstaller --apply /run/slshare/config-raid1.yaml     # RAID1 (two disks)"
echo "  /run/slshare/slinstaller                                          # interactive wizard"
