#!/bin/bash
# build.sh - prepare the slinstaller VM demo.
#
# Produces everything run.sh needs to boot a UEFI VM and install into it:
#
#   - a static slinstaller binary          (_output/slinstaller)
#   - the demo config, targeting /dev/vda  (_output/config.yaml)
#   - a blank target disk                  (_output/target.qcow2)
#   - a Void Linux live ISO as the medium  (_cache/void-live.iso)
#
# The live ISO supplies the tools slinstaller drives (xbps, parted,
# mkfs.*, grub, efibootmgr); the binary and config reach the guest over a
# 9p share, so the ISO itself is never modified.
#
# The ISO filename carries a date, so there is no stable URL to hardcode.
# build.sh discovers the newest base image, or you point it at your own:
#
#   VOID_ISO=/path/to/void-live-x86_64-*.iso ./build.sh   # use a local ISO
#   DISK_SIZE=20G ./build.sh                                # bigger target disk
#
# Flags:
#   --cleanupdisk   wipe the target disk back to blank and reset the OVMF
#                   NVRAM, for a clean reinstall (keeps the binary + ISO)
#   --clean         remove everything in _output (keeps the cached ISO)
#   --help
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"
OUTPUT_DIR="${SCRIPT_DIR}/_output"
CACHE_DIR="${SCRIPT_DIR}/_cache"
DISK_SIZE="${DISK_SIZE:-10G}"
VOID_LIVE_DIR="${VOID_LIVE_DIR:-https://repo-default.voidlinux.org/live/current}"
TARGET="${OUTPUT_DIR}/target.qcow2"
VARS_FILE="${OUTPUT_DIR}/OVMF_VARS.fd"

CLEANUP_DISK=0
CLEAN=0
for arg in "$@"; do
    case "$arg" in
        --cleanupdisk) CLEANUP_DISK=1 ;;
        --clean)       CLEAN=1 ;;
        --help|-h)     grep -E '^# ' "$0" | sed 's/^# //'; exit 0 ;;
        *)             echo "Unknown option: $arg (see --help)" >&2; exit 1 ;;
    esac
done

mkdir -p "${OUTPUT_DIR}" "${CACHE_DIR}"

if [ "${CLEAN}" -eq 1 ]; then
    echo "→ cleaning _output (keeping the cached ISO)"
    rm -rf "${OUTPUT_DIR}"
    mkdir -p "${OUTPUT_DIR}"
fi
if [ "${CLEANUP_DISK}" -eq 1 ]; then
    echo "→ wiping target disk and OVMF NVRAM for a clean reinstall"
    rm -f "${TARGET}" "${VARS_FILE}"
fi

echo "→ building static slinstaller"
( cd "${REPO_ROOT}" && CGO_ENABLED=0 go build -o "${OUTPUT_DIR}/slinstaller" ./cmd/slinstaller )

echo "→ staging demo config (targets /dev/vda) and guest-prep script"
cp "${SCRIPT_DIR}/config.yaml" "${OUTPUT_DIR}/config.yaml"
cp "${SCRIPT_DIR}/guest-prep.sh" "${OUTPUT_DIR}/guest-prep.sh"

if [ ! -f "${TARGET}" ]; then
    echo "→ creating blank target disk ${TARGET} (${DISK_SIZE})"
    qemu-img create -f qcow2 "${TARGET}" "${DISK_SIZE}" >/dev/null
else
    echo "→ target disk exists (${TARGET}); --cleanupdisk wipes it"
fi

ISO="${CACHE_DIR}/void-live.iso"
if [ -n "${VOID_ISO:-}" ]; then
    echo "→ using ISO from \$VOID_ISO: ${VOID_ISO}"
    cp -f "${VOID_ISO}" "${ISO}"
elif [ -f "${ISO}" ]; then
    echo "→ live ISO already cached (${ISO})"
else
    echo "→ discovering newest Void live ISO under ${VOID_LIVE_DIR}"
    name="$(curl -fsSL "${VOID_LIVE_DIR}/" \
        | grep -oE 'void-live-x86_64-[0-9]+-base\.iso' | sort -u | tail -1 || true)"
    if [ -z "${name}" ]; then
        echo "!! could not find a live ISO automatically." >&2
        echo "   Download one from ${VOID_LIVE_DIR}/ and re-run as:" >&2
        echo "     VOID_ISO=/path/to/void-live-x86_64-*.iso ./build.sh" >&2
        exit 1
    fi
    echo "→ downloading ${name}"
    curl -fSL -o "${ISO}.tmp" "${VOID_LIVE_DIR}/${name}"
    mv "${ISO}.tmp" "${ISO}"
fi

echo ""
echo "Done. Next: ./run.sh   (then install inside the guest — see README.md)"
