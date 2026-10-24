#!/bin/bash
# run.sh - boot the slinstaller VM demo in QEMU under UEFI (OVMF).
# Exit VM: Ctrl+A, X
#
# Two modes:
#
#   ./run.sh              # boot the Void live ISO with a blank target
#                         #   disk (/dev/vda) and a 9p share holding the
#                         #   slinstaller binary + config. Install from
#                         #   inside the guest (see README.md).
#   ./run.sh --installed  # boot the target disk itself, no ISO, to check
#                         #   the installed system comes up. Uses the same
#                         #   OVMF NVRAM, so the boot entry slinstaller
#                         #   registered is still there.
#
# Options:
#   --fresh        # recreate the target disk blank before booting
#   --mem N        # guest memory in MB (default 2048)
#   --help
#
# UEFI firmware: run.sh looks for OVMF_CODE in the usual places. If yours
# is elsewhere, set OVMF_CODE=/path/to/OVMF_CODE.fd.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
OUTPUT_DIR="${SCRIPT_DIR}/_output"
CACHE_DIR="${SCRIPT_DIR}/_cache"

TARGET="${OUTPUT_DIR}/target.qcow2"
ISO="${CACHE_DIR}/void-live.iso"
VARS="${OUTPUT_DIR}/OVMF_VARS.fd"
MEMORY="${MEMORY:-2048}"

INSTALLED=0
FRESH=0
for arg in "$@"; do
    case "$arg" in
        --installed) INSTALLED=1 ;;
        --fresh)     FRESH=1 ;;
        --mem)       echo "use --mem=N" >&2; exit 1 ;;
        --mem=*)     MEMORY="${arg#--mem=}" ;;
        --help|-h)   grep -E '^# ' "$0" | sed 's/^# //'; exit 0 ;;
        *)           echo "Unknown option: $arg (see --help)" >&2; exit 1 ;;
    esac
done

if [ ! -f "${TARGET}" ]; then
    echo "Error: ${TARGET} not found. Run ./build.sh first." >&2
    exit 1
fi
if [ "${FRESH}" -eq 1 ]; then
    echo "→ recreating blank target disk"
    size="$(qemu-img info --output=json "${TARGET}" | grep -oE '"virtual-size": [0-9]+' | grep -oE '[0-9]+')"
    rm -f "${TARGET}"
    qemu-img create -f qcow2 "${TARGET}" "${size}" >/dev/null
fi

# Locate the OVMF firmware (read-only code) and seed a writable VARS copy.
# Naming varies a lot across distros: Debian/Fedora use OVMF_CODE*.fd,
# openSUSE/qemu use ovmf-x86_64-*-code.bin, Arch uses edk2 paths.
if [ -z "${OVMF_CODE:-}" ]; then
    for c in \
        /usr/share/OVMF/OVMF_CODE.4m.fd \
        /usr/share/OVMF/OVMF_CODE_4M.fd \
        /usr/share/OVMF/OVMF_CODE.fd \
        /usr/share/edk2/x64/OVMF_CODE.4m.fd \
        /usr/share/edk2/x64/OVMF_CODE.fd \
        /usr/share/edk2/ovmf/OVMF_CODE.fd \
        /usr/share/edk2-ovmf/x64/OVMF_CODE.fd \
        /usr/share/qemu/ovmf-x86_64-4m-code.bin \
        /usr/share/qemu/ovmf-x86_64-code.bin \
        /usr/share/qemu/OVMF_CODE.fd; do
        [ -f "$c" ] && OVMF_CODE="$c" && break
    done
fi
if [ -z "${OVMF_CODE:-}" ] || [ ! -f "${OVMF_CODE}" ]; then
    echo "Error: OVMF firmware not found. Install edk2/OVMF, or set OVMF_CODE=/path/to/OVMF_CODE.fd" >&2
    exit 1
fi
echo "→ OVMF firmware: ${OVMF_CODE}"
if [ ! -f "${VARS}" ]; then
    # Find the matching writable VARS template next to the CODE image.
    tmpl=""
    for cand in \
        "${OVMF_VARS:-}" \
        "${OVMF_CODE/-code./-vars.}" \
        "${OVMF_CODE/code/vars}" \
        "${OVMF_CODE/CODE/VARS}"; do
        [ -n "$cand" ] && [ -f "$cand" ] && tmpl="$cand" && break
    done
    if [ -n "$tmpl" ]; then
        echo "→ OVMF NVRAM template: ${tmpl}"
        cp "$tmpl" "${VARS}"
    else
        echo "→ OVMF NVRAM: no template found, starting blank"
        truncate -s 540672 "${VARS}" # 528K, standard 4M-split VARS size
    fi
fi

KVM_ARGS=()
if [ -w /dev/kvm ]; then
    KVM_ARGS=(-enable-kvm -cpu host)
else
    echo "Note: KVM not available, using software emulation (slower)"
    KVM_ARGS=(-cpu qemu64)
fi

COMMON=(
    "${KVM_ARGS[@]}"
    -machine q35
    -m "${MEMORY}"
    -drive "if=pflash,format=raw,readonly=on,file=${OVMF_CODE}"
    -drive "if=pflash,format=raw,file=${VARS}"
    -drive "file=${TARGET},format=qcow2,if=virtio"
    -nographic
    -serial mon:stdio
)

if [ "${INSTALLED}" -eq 1 ]; then
    echo "Booting the installed target disk (Ctrl+A, X to exit)"
    exec qemu-system-x86_64 "${COMMON[@]}" -nic none
fi

if [ ! -f "${ISO}" ]; then
    echo "Error: ${ISO} not found. Run ./build.sh first." >&2
    exit 1
fi

echo "Booting the Void live ISO with a blank /dev/vda (Ctrl+A, X to exit)"
echo "Inside the guest, install with:"
echo "    mkdir -p /mnt/share && mount -t 9p -o trans=virtio slshare /mnt/share"
echo "    /mnt/share/slinstaller --apply /mnt/share/config.yaml"
echo "    poweroff   # then: ./run.sh --installed"
echo ""
exec qemu-system-x86_64 "${COMMON[@]}" \
    -cdrom "${ISO}" \
    -boot d \
    -virtfs "local,path=${OUTPUT_DIR},mount_tag=slshare,security_model=none" \
    -nic "user,model=virtio-net-pci"
