# tests

Integration and acceptance tests that need more than a Go unit test.

The honest test for an installer is an install. Unit tests live beside
the code they cover under `pkg/` and `cmd/` and never touch a disk. What
is here is `loop/`, which runs the partition and mount stages on loop
devices.

There is no automated VM test yet. The test of everything after the
mount — GRUB, a real `xbps-install`, the first boot — is an install in a
VM under UEFI firmware followed by booting the installed disk, and that is
done by hand with [`demo/vm`](../demo/vm) (`build.sh` + `run.sh`, QEMU +
OVMF against a scratch disk). The maintainer runs such installs
routinely, including with 2.3.x, but they are not recorded here per
release; each CHANGELOG entry says how far its release was verified
before the cut. An acceptance test built on that harness, booting the
result to a login prompt, would land
here.

## `loop/` — the storage stages on loop devices

[`loop/run.sh`](loop/run.sh) runs the installer's partition and mount stages
against loop devices: files that are real disks to parted, mkfs and the
kernel. It checks what the unit suite cannot — that the commands do what is
expected of them:

- a plain install lays down the table, the ESP flag and the filesystems;
- `--discover` describes that layout, preserving all of it;
- `resize` shrinks an ext4 partition holding data and leaves the data, the
  partition's start and identity, and its neighbours as they were; a size
  the data does not fit in is refused with nothing changed;
- a `create` partition named for one that exists is refused with nothing
  changed, and one named as parted numbers it is made;
- an ntfs and a btrfs partition are shrunk the same way, data intact,
  with a new root beside them (the ntfs case runs when `mkfs.ntfs` and
  `ntfscp` are there);
- a btrfs filesystem ends up with its subvolumes mounted where the config
  says, with its mount options, on a disk wiped with `wipe: discard`;
- a RAID 1 array over two disks is assembled and partitioned, with an ESP
  on each disk (when `mdadm` is there and `/dev/md0` is free).

It needs root and stops before the base system:

```sh
go build -o slinstaller ./cmd/slinstaller
sudo tests/loop/run.sh ./slinstaller
```

CI runs it on every push (the `loop` job). Every config the script
writes must pass validation; `pkg/config/loopcases_test.go` checks that
in the unit suite, so a new rule that breaks one fails there first.

## The other CI jobs

These live in [`.github/workflows/tests.yml`](../.github/workflows/tests.yml),
not here:

- `unit` — gofmt, `go vet`, `go test -race ./...` and a 95% coverage
  floor. It includes the tests that run as root in
  a user namespace: `--apply`'s checks before a wipe
  (`cmd/slinstaller/apply_ns_test.go`), the mount stages
  (`pkg/storage/mount_ns_test.go`, `pkg/controller/mount_ns_test.go`), and
  an install through every stage with real mounts and recorded or stand-in
  commands (`pkg/controller/e2e_ns_test.go`), for a plain, an encrypted,
  an LVM, an LVM-on-LUKS, a RAID 1, an encrypted-`/home`-on-a-plain-root
  and a reuse layout. They skip themselves where a user namespace cannot
  mount; CI lifts the runner's restriction and fails if they skipped
  anyway, or if fewer than four of them ran.
- `fuzz` — every `Fuzz*` target, found with `go test -list`, for 20 s each.
- `govulncheck` — known vulnerabilities reachable from the code, in the
  Go `go.mod` names (`toolchain`) and in the modules. It can fail with no
  code change when the vulnerability database grows; the fix is a newer
  toolchain or module, then a patch release.
- `dry-run` — every `demo/*.yaml` and `demo/vm/config*.yaml`, and a
  `--template` config, must still plan.
