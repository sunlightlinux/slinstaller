# CLAUDE.md

Behavioral guidelines for LLM-assisted work on **slinstaller** — the system
installer for Sunlight Linux (a UEFI-only distribution).

> Derived from
> [forrestchang/andrej-karpathy-skills](https://github.com/forrestchang/andrej-karpathy-skills)
> (MIT), based on [Andrej Karpathy's
> observations](https://x.com/karpathy/status/2015883857489522876)
> on LLM coding pitfalls. Adapted for slinstaller: Go idioms, installer
> correctness, and the fact that an installer **destroys data by design**.

> **Status:** a new installer, written in **Go**. It reads the config format
> of [clr-installer](https://github.com/clearlinux/clr-installer), so an
> existing clr-installer `config.yaml` describes the same install here, and
> covers clr-installer's disk-installer feature set reduced to what Sunlight
> Linux installs. Its code is its own: see the clean-room rule below.

**Tradeoff:** These guidelines bias toward caution over speed. An installer
runs as root and performs **irreversible** operations (partitioning, `mkfs`,
bootloader writes). "Move fast" here means "destroy the wrong disk fast." When
in doubt, stop and ask.

See [EXAMPLES.md](EXAMPLES.md) for concrete examples grounded in installer
scenarios.

---

## 1. Think Before Coding

**Don't assume. Don't hide confusion. Surface tradeoffs.**

Before implementing:
- State assumptions explicitly. If uncertain, ask — especially about *which
  device* an operation touches.
- If multiple interpretations exist, present them — don't pick silently.
  Installers have many near-synonyms: "wipe" vs "format" vs "partition",
  *disk* vs *partition* vs *filesystem* vs *mountpoint*, ESP vs `/boot` vs boot
  partition, "erase the disk" (whole device) vs "erase a partition". Surface
  which is meant.
- If a simpler approach exists, say so. Push back when warranted.
- If something is unclear, stop. Name what's confusing. Ask.
- **Check config compatibility first.** For anything that looks like a
  feature request, check whether clr-installer's config format already has a
  key for it, and what that key means *as clr-installer documents it*. A key
  slinstaller reads must mean what it means there (STABILITY.md); a new
  behaviour gets a new key. Settle that from clr-installer's documentation
  and config examples first — and see the clean-room rule below.

## 2. Simplicity First

**Minimum code that solves the problem. Nothing speculative.**

- No features beyond what was asked. No BIOS/MBR path, ever (UEFI-only), and
  no ZFS, bcache or other storage the project has deferred, unless explicitly
  requested. LUKS, LVM and software RAID exist as layers under the
  filesystems; extend them, do not add parallel ones.
- No abstractions for single-use code.
- No "flexibility" (pluggable partitioners, filesystem strategy hierarchies)
  for a single supported layout. Add a second implementation when a second
  layout actually arrives.
- No error handling for impossible scenarios. Validate at boundaries (user
  input, the block device, the network mirror); trust internal invariants.

**Go-specific:**
- Prefer `switch` over strategy-pattern struct hierarchies.
- Don't add interfaces for a single implementer.
- Error wrapping: `fmt.Errorf("op: %w", err)` is usually enough.
- Don't sprinkle `context.Context` unless cancellation is genuinely needed
  (long downloads and `mkfs` are the real candidates).

Ask yourself: "Would a senior Go engineer say this is overcomplicated?"
If yes, simplify.

## 3. Surgical Changes

**Touch only what you must. Clean up only your own mess.**

When editing existing code:
- Don't "improve" adjacent code, comments, or formatting.
- Don't refactor things that aren't broken.
- Match existing style (`gofmt` + `go vet`).
- Don't rename identifiers in passing. Renames are their own commit.

**Installer-specific — the safety rule that overrides convenience:**
- **Never widen what gets destroyed.** A change asked to fix a label, a
  hostname, or a package name must not alter which device/partition is wiped,
  formatted, or `dd`-ed. Destructive scope changes are their own, explicitly
  reviewed change.
- **Keep destructive operations gated.** Confirmation prompts, dry-run paths,
  and "is this the right disk?" guards exist on purpose — don't bypass them to
  streamline a flow.

The test: every changed line should trace directly to the user's request.

## 4. Goal-Driven Execution

**Define success criteria. Loop until verified.**

Transform tasks into verifiable goals:
- "Add encryption" → "A VM install with LUKS boots to a login prompt; the
  partition is unlocked on first boot."
- "Fix the bug" → "Reproduce it in a VM, then make a test/install pass."
- "Refactor X" → "`go test ./...` passes before and after; a VM install still
  completes."

For multi-step tasks, state a brief plan:
```
1. [Step] → verify: [check]
2. [Step] → verify: [check]
```

**Verification in slinstaller:**
- `go build ./...` — full build, catches typos fast (Go target).
- `go vet ./...` — catches misuse before tests.
- `go test ./...` — unit tests (logic that doesn't touch real disks).
- **VM install** — boot the live medium under QEMU with OVMF (UEFI firmware)
  and run the installer against a scratch qcow2. This is the only honest test
  for partitioning / `mkfs` / bootloader steps.
- **Never** exercise destructive steps on a real disk or outside a VM.

Strong success criteria let you loop independently. Weak criteria
("make it install") require constant clarification.

---

## slinstaller Project Context

### Positioning
- The Sunlight Linux system installer. Partitions the disk, installs the base
  system, sets up the bootloader, and configures first boot (including slinit
  as PID 1).
- A new installer, written in Go, compatible with clr-installer's config
  format and written independently of its code.

### Invariants
- **UEFI-only.** GPT + an EFI System Partition; boot entry via `efibootmgr`.
  No BIOS/MBR.
- **Destructive operations are gated.** Confirmable and dry-runnable; never
  run against hardware during development. A VM install is the test for what
  follows the mount. VM installs are run by hand and are not recorded per
  release (the CHANGELOG's "What is verified" paragraphs say what was done
  before each cut), so do not claim a stage works on a real machine on the
  strength of the unit or namespace tests.
- **Installs slinit**, not systemd/OpenRC, as the init system of the target.
- **Base system is Sunlight Linux** (the `slpkgs` set; `xbps` package
  tooling, inherited from the Void lineage).

### The clean-room rule (licence)
- **clr-installer is GPL-3.0; slinstaller is Apache-2.0.** clr-installer is a
  *design reference only* — the first commit (`b54aac8`) states the rule.
  Code written from GPL-3.0 source is a derivative work whatever it is renamed
  to, so:
  - **Never** copy, translate or paraphrase clr-installer's code, and do not
    mirror its package structure, type names or function layout. Do not add
    it, or any module of it, as a dependency.
  - **Do** take behaviour from the outside first: its documentation, its
    config schema and example configs, and what the program observably
    does. That is what config compatibility needs.
  - When only its source can say what a config key does, record the
    behaviour in your own words (a test case, a doc line), close the source,
    and write slinstaller's code from that record — never with the source
    open beside it.
- "Port" in older commits and docs means *the same mechanisms and config
  semantics*, not translated code.

### Reference sources
- **clr-installer**: <https://github.com/clearlinux/clr-installer> (archived)
  — the config format slinstaller reads and the feature set it measures
  itself against ([doc/port-status.md](doc/port-status.md)); documentation
  and configs only, under the rule above.
- **slinit**: <https://github.com/sunlightlinux/slinit> — the init system the
  installer configures on the target, and the repository whose layout
  slinstaller follows.

### Commit style
- **Bundle, don't over-split.** One commit per session of work. Commit
  messages: `area: short summary` + a paragraph on *why*, not *what* (the diff
  already shows what).
