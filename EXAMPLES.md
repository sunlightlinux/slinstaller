# EXAMPLES.md

Concrete examples for each of the four principles in [CLAUDE.md](CLAUDE.md),
grounded in real slinstaller scenarios (disk partitioning, filesystems,
bootloader/EFI, mirror selection). Snippets use Go (the long-term target).

> Derived from [forrestchang/andrej-karpathy-skills](https://github.com/forrestchang/andrej-karpathy-skills)
> (MIT). Examples rewritten for Go + slinstaller.

---

## 1. Think Before Coding

### Example 1 — Ambiguous, Destructive Request

**User request:** "Wipe the disk before installing."

**What LLMs often do (wrong, dangerous assumption):**

```go
// Silently nukes the whole device.
exec.Command("wipefs", "-a", "/dev/sda").Run()
exec.Command("sgdisk", "--zap-all", "/dev/sda").Run()
```

**Problem:** "wipe the disk" is dangerously ambiguous:

- **Whole device** (`/dev/sda`) — destroys every partition, including data
  the user may keep.
- **A single partition** (`/dev/sda3`) — reuse an existing layout.
- **Just the ESP** — keep the data partition, reinstall the bootloader.
- And *which* device? The installer must not assume `/dev/sda`.

Picking silently can erase the wrong disk. This is irreversible.

**What should happen (surface the choice):**

> "Wipe the disk" needs two answers before I touch anything:
>
> 1. **Which device?** (e.g. `/dev/nvme0n1` vs `/dev/sda` — I'll list
>    candidates with size/model so you confirm the right one.)
> 2. **What scope?** Whole device (new GPT), a single partition, or just the
>    ESP?
>
> I'll show the planned operations and require an explicit confirmation before
> running anything destructive.

### Example 2 — Multiple Interpretations

**User request:** "Make the install faster."

**What LLMs often do (pick silently):**

```go
// Skips fsync/verification to save time.
xbpsArgs = append(xbpsArgs, "--no-verify")
```

**What should happen:**

> "Faster" could mean several things with different risk:
>
> 1. **Parallel package downloads** — bandwidth-bound; safe win.
> 2. **Pick a closer mirror** — measure latency, choose nearest; safe.
> 3. **Skip signature/checksum verification** — faster, but defeats
>    supply-chain integrity. I would not do this by default.
>
> Which trade-off do you want? I'd start with mirror selection + parallel
> downloads and leave verification on.

---

## 2. Simplicity First

### Example 1 — Over-abstraction

**User request:** "Add a helper to create the partition layout."

**What LLMs often do (overengineered):**

```go
type Partitioner interface {
    Partition(disk string) (Layout, error)
}

type GPTPartitioner struct{ scheme SchemeStrategy }
type MBRPartitioner struct{ /* ... */ }
type LVMPartitioner struct{ inner Partitioner }

type PartitionerFactory struct{}
func (PartitionerFactory) Build(cfg Config) Partitioner { /* ... */ }
```

**Problem:** Sunlight Linux is UEFI-only with one supported layout (ESP +
root). There is no MBR partitioner, no LVM, no second scheme. The interface +
factory + decorator are all speculative — and an MBR path actively contradicts
the UEFI-only invariant.

**What should happen (minimum code):**

```go
// createLayout writes a GPT with an ESP and a root partition.
func createLayout(disk string, espSizeMiB int) error {
    return sgdisk(disk,
        "--zap-all",
        fmt.Sprintf("--new=1:0:+%dMiB", espSizeMiB), "--typecode=1:ef00", // ESP
        "--new=2:0:0", "--typecode=2:8304",                              // root
    )
}
```

One function, no interfaces. Add a `Partitioner` interface when a second real
layout shows up — not before.

### Example 2 — Speculative Features

**User request:** "Let the user pick the filesystem for root."

**What LLMs often do (feature creep):**

```go
type FSConfig struct {
    Type       string   // ext4, xfs, btrfs, zfs, f2fs, ...
    Subvolumes []string // btrfs
    Compression string  // zstd, lzo, ...
    RaidLevel  int      // zfs/btrfs raid
    Encryption EncryptionConfig
    Quota      bool
}
```

**Problem:** the ask was "pick the filesystem." Subvolumes, compression, RAID,
and quotas are invented. Ship the choice that was requested.

**What should happen:**

```go
// supportedRootFS are the filesystems the installer can create for root.
var supportedRootFS = []string{"ext4", "xfs"}

func mkfsRoot(part, fstype string) error {
    switch fstype {
    case "ext4":
        return run("mkfs.ext4", "-F", part)
    case "xfs":
        return run("mkfs.xfs", "-f", part)
    default:
        return fmt.Errorf("unsupported root filesystem %q", fstype)
    }
}
```

Add btrfs/subvolumes when someone actually asks for them.

---

## 3. Surgical Changes

### Example 1 — Drive-by "Improvements"

**User request:** "Fix the hostname so it's written to `/etc/hostname` without
a trailing newline issue."

**What LLMs often do (too much):**

```diff
 func configureSystem(root, hostname, disk string) error {
-    os.WriteFile(filepath.Join(root, "etc/hostname"), []byte(hostname), 0644)
+    // While we're here, also re-zap and re-partition to be safe.
+    if err := createLayout(disk, 512); err != nil {
+        return err
+    }
+    if err := os.WriteFile(filepath.Join(root, "etc/hostname"),
+        []byte(strings.TrimSpace(hostname)+"\n"), 0644); err != nil {
+        return err
+    }
+    return installBootloader(root, disk)
 }
```

**Problems:**
- Added a **re-partition** of `disk` to a hostname fix — catastrophic and
  totally unrelated.
- Re-ran the bootloader install as a side effect.
- The actual fix is one line.

**What should happen (surgical):**

```diff
-    os.WriteFile(filepath.Join(root, "etc/hostname"), []byte(hostname), 0644)
+    os.WriteFile(filepath.Join(root, "etc/hostname"),
+        []byte(strings.TrimSpace(hostname)+"\n"), 0644)
```

One line. Nothing destructive is touched.

### Example 2 — Widening Destructive Scope

**User request:** "Also create a swap partition."

**What LLMs often do (changes the wipe target):**

```diff
-func createLayout(disk string, espSizeMiB int) error {
+func createLayout(disk string, espSizeMiB, swapMiB int) error {
     return sgdisk(disk,
-        "--zap-all",
+        "--zap-all",                                  // (unchanged)
         fmt.Sprintf("--new=1:0:+%dMiB", espSizeMiB), "--typecode=1:ef00",
-        "--new=2:0:0", "--typecode=2:8304",
+        fmt.Sprintf("--new=2:0:+%dMiB", swapMiB), "--typecode=2:8200", // swap
+        "--new=3:0:0", "--typecode=3:8304",                            // root
+    )
+}
+// ...and quietly switch every caller from /dev/sda to all detected disks.
+func allDisks() []string { /* ... */ }
```

**Problems:**
- The swap addition is fine — but the diff *also* changed callers to operate on
  "all detected disks," silently widening what gets wiped.
- A swap-partition request must not change *which devices* are destroyed.

**What should happen:**

Add swap to the **same** single target disk, leave device selection alone:

```diff
         fmt.Sprintf("--new=1:0:+%dMiB", espSizeMiB), "--typecode=1:ef00",
+        fmt.Sprintf("--new=2:0:+%dMiB", swapMiB), "--typecode=2:8200", // swap
-        "--new=2:0:0", "--typecode=2:8304",
+        "--new=3:0:0", "--typecode=3:8304",                            // root
```

The wipe still targets exactly the one disk the user confirmed.

---

## 4. Goal-Driven Execution

### Example 1 — Reproduce, Then Fix

**User request:** "The install finishes but the machine won't boot in UEFI."

**What LLMs often do (fix without reproducing):**

```go
// Adds a second grub-install on a hunch.
run("grub-install", "--target=x86_64-efi", "--efi-directory=/boot/efi", disk)
run("grub-install", "--removable", disk) // "just in case"
```

Result: still may not boot; now with two competing installs.

**What should happen (reproduce in a VM):**

```
Plan:
1. Reproduce under QEMU + OVMF (UEFI firmware) with a scratch qcow2.
   Verify: the VM drops to the UEFI shell / "no bootable device" — bug
   reproduced.
2. Inspect the ESP after install: is /EFI/BOOT/BOOTX64.EFI (or the GRUB entry)
   present? Is the efibootmgr entry created? Is the ESP typecode ef00 and
   mounted at /boot/efi during grub-install?
   Verify: identify the exact missing piece (e.g. efibootmgr entry never
   written because NVRAM isn't writable in the live env).
3. Fix the one cause (e.g. also install a removable-path fallback
   /EFI/BOOT/BOOTX64.EFI when NVRAM can't be updated).
   Verify: fresh VM install now boots to a login prompt.
4. Re-run the VM install end to end.
   Verify: boots cleanly, slinit reaches the target.
```

Each step has an independent pass/fail. No speculative "just in case" installs.

### Example 2 — Multi-Step Feature with Verification

**User request:** "Add full-disk encryption (LUKS) as an option."

**What LLMs often do (all at once):**

One huge change: LUKS setup, keyfile handling, crypttab, initramfs hooks,
GRUB cryptodisk, and a TPM-unlock path — committed together, untested.

**What should happen (incremental):**

```
Plan:
1. LUKS format + open the root partition (passphrase only).
   Verify: unit test for the cryptsetup arg construction; VM install with LUKS
   completes the copy step.
2. Wire up crypttab + initramfs so the target unlocks at boot.
   Verify: VM reboots, prompts for passphrase, boots to login.
3. GRUB cryptodisk / ESP config so the bootloader can reach the encrypted
   root.
   Verify: cold-boot VM from firmware reaches the passphrase prompt.
4. Only after 1-3 are green: TPM auto-unlock.
   Verify: VM with emulated TPM boots without a prompt; passphrase still works
   as fallback.
```

Ship steps 1-3 as a usable feature; skip 4 until someone needs it.

---

## Anti-patterns Summary

| Principle | Anti-pattern | Fix |
|---|---|---|
| Think Before Coding | Silently `wipefs` the whole `/dev/sda` for "wipe the disk" | List candidate devices + scope; confirm before any destructive op |
| Simplicity First | Partitioner interface + factory for one UEFI layout | One `createLayout` function; add an interface at the second layout |
| Surgical Changes | A hostname fix that re-partitions the disk | Change only the one line; never touch destructive scope incidentally |
| Goal-Driven | Second `grub-install` on a hunch for a no-boot | Reproduce in QEMU+OVMF, find the missing ESP/efibootmgr piece, fix that |

## Key Insight

The "overcomplicated" examples aren't obviously wrong — interfaces, extra
filesystem options, and "just in case" installs all look thorough. The problem
is **timing and blast radius**: in an installer, premature complexity and
incidental changes don't just add bugs, they can **destroy the wrong disk** or
**ship an unbootable system**.

- Destructive operations are irreversible — get the *device* and *scope* right
  before anything else.
- A change's footprint must match its intent: a label fix never re-partitions.
- The only honest test for partition/format/boot steps is a VM (QEMU + OVMF),
  not a real disk.

**Good slinstaller code solves today's install step safely and verifiably —
never widening what gets wiped, never assuming which disk.**
