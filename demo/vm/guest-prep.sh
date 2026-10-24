#!/bin/sh
# guest-prep.sh — run inside the Void live guest before slinstaller.
#
# The base live ISO carries mkfs.*, mkswap, grub and efibootmgr, but not
# parted. This refreshes xbps itself and installs parted, which is all the
# live system is missing for an install.
#
#   sh /mnt/share/guest-prep.sh
set -eu

xbps-install -Suy xbps
xbps-install -Sy parted

echo "Ready. Now: /mnt/share/slinstaller --apply /mnt/share/config.yaml"
