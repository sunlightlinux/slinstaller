#!/bin/sh
# guest-prep.sh — run inside the Void live guest before slinstaller.
#
# The base live ISO carries mkfs.*, mkswap, grub and efibootmgr, but not
# parted — nor cryptsetup/lvm2 for the encrypted and LVM configs. This
# refreshes xbps and installs the tools the installs need.
#
#   sh /mnt/share/guest-prep.sh
set -eu

xbps-install -Suy xbps
xbps-install -Sy parted cryptsetup lvm2

echo "Ready. Now, e.g.:"
echo "  /mnt/share/slinstaller --apply /mnt/share/config.yaml        # basic"
echo "  /mnt/share/slinstaller --apply /mnt/share/config-luks.yaml   # encrypted root"
echo "  /mnt/share/slinstaller --apply /mnt/share/config-lvm.yaml    # LVM"
echo "  /mnt/share/slinstaller                                       # interactive wizard"
