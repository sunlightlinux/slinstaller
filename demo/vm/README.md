# slinstaller VM demo

A reproducible QEMU environment for testing the install flow end to end
under UEFI — the only honest test for partitioning, `mkfs`, mounting and
the bootloader. It is the installer's counterpart to slinit's QEMU demo.

```bash
./build.sh        # build slinstaller, fetch a Void live ISO, make a blank disk
./run.sh          # boot the live ISO with a blank /dev/vda + a 9p share
# ... install from inside the guest (below) ...
./run.sh --installed   # boot the disk you just installed, to prove it comes up
```

## How it works

slinstaller drives standard tools (`xbps`, `parted`, `sgdisk`, `mkfs.*`,
`grub`, `efibootmgr`). Rather than build a live medium that carries them,
the demo boots a **Void Linux live ISO**, which already has them, and
hands the guest the slinstaller binary and config over a **9p share** —
the ISO is never modified. QEMU runs under **OVMF**, so the guest is in
real UEFI mode and the bootloader step exercises the same `efibootmgr`
path a physical machine would.

The target is a blank qcow2 attached as virtio (`/dev/vda`); the demo
`config.yaml` targets exactly that, so an `--apply` inside the VM can
only touch the scratch disk. OVMF's NVRAM is a writable copy under
`_output`, so the boot entry the install registers survives into
`./run.sh --installed`.

## Installing inside the guest

Log in at the live prompt as `root` (Void live password: `voidlinux`),
then:

```sh
mkdir -p /mnt/share
mount -t 9p -o trans=virtio slshare /mnt/share
/mnt/share/slinstaller /mnt/share/config.yaml           # dry run: print the plan
/mnt/share/slinstaller --apply /mnt/share/config.yaml   # do it (retype /dev/vda to confirm)
poweroff
```

Then `./run.sh --installed` boots the freshly installed disk.

> **Interim:** with no Sunlight repository yet, the base system comes from
> Void's official repo, so what installs is a Void base with **runit**,
> not slinit — enough to prove the installer's plumbing. slinit takes
> over once it is in a repo we control.

## Requirements

- `qemu-system-x86_64` and `qemu-img`
- OVMF / edk2 firmware (`OVMF_CODE.fd`); set `OVMF_CODE=...` if it is not
  in a standard location
- `curl` (to fetch the ISO), and network in the guest (the base install
  pulls packages) — QEMU user networking provides it
- KVM recommended; falls back to software emulation

## Knobs

| Where | Variable / flag | Effect |
|-------|-----------------|--------|
| build.sh | `VOID_ISO=/path.iso` | use a local ISO instead of downloading |
| build.sh | `DISK_SIZE=20G` | size of the blank target disk |
| run.sh | `--fresh` | wipe the target disk back to blank first |
| run.sh | `--mem=N` | guest memory in MB (default 2048) |
| run.sh | `--installed` | boot the installed disk instead of the ISO |
| run.sh | `--vnc` | show the guest over VNC on :0 (headless) |
| run.sh | `--serial` | serial-only (`-nographic`); needs `console=ttyS0` added at the ISO's GRUB menu to show anything past the firmware |
| run.sh | `--display=gtk` | force a QEMU UI backend |
| run.sh | `OVMF_CODE=/path` | point at your OVMF firmware |

The live ISO uses a graphical console, so run.sh opens a QEMU window when
a desktop is present and falls back to VNC otherwise — serial-only shows
only the firmware and then goes quiet. Interact in the window or VNC.

Artifacts live in `_output/` and `_cache/`, both gitignored.
