# tests

Integration and acceptance tests that need more than a Go unit test.

The honest test for an installer is an install. Unit tests live beside
the code they cover under `pkg/` and never touch a disk; the tests here
exercise the real destructive path the only safe way — a VM. A run boots
the live medium under QEMU with OVMF (UEFI firmware), points slinstaller
at a scratch qcow2, and asserts the result boots to a login prompt.

The install path is runnable end to end now; the VM harness that
exercises it lives in [`demo/vm`](../demo/vm) (`build.sh` + `run.sh`,
QEMU + OVMF against a scratch disk). Automated acceptance tests built on
that harness land here.

## `loop/` — the storage stages on loop devices

[`loop/run.sh`](loop/run.sh) runs the installer's partition and mount stages
against loop devices: files that are real disks to parted, mkfs and the
kernel. It checks what the unit suite cannot — that the commands do what is
expected of them:

- a plain install lays down the table, the ESP flag and the filesystems;
- `--discover` describes that layout, preserving all of it;
- `resize` shrinks a partition holding data and leaves the data, the
  partition's start and identity, and its neighbours as they were; a size
  the data does not fit in is refused with nothing changed;
- a btrfs filesystem ends up with its subvolumes mounted where the config
  says, with its mount options, on a disk wiped with `wipe: discard`.

It needs root and stops before the base system:

```sh
go build -o slinstaller ./cmd/slinstaller
sudo tests/loop/run.sh ./slinstaller
```

CI runs it on every push (the `loop` job).

## The other CI jobs

These live in [`.github/workflows/tests.yml`](../.github/workflows/tests.yml),
not here:

- `unit` — gofmt, `go vet`, `go test -race ./...` and a 95% coverage
  floor. It includes the tests that run as root in
  a user namespace: `--apply`'s checks before a wipe
  (`cmd/slinstaller/apply_ns_test.go`), the mount stages
  (`pkg/storage/mount_ns_test.go`, `pkg/controller/mount_ns_test.go`), and
  an install through every stage with real mounts and recorded or stand-in
  commands (`pkg/controller/e2e_ns_test.go`). They skip themselves where a
  user namespace cannot mount; CI lifts the runner's restriction and fails
  if they skipped anyway.
- `fuzz` — every `Fuzz*` target, found with `go test -list`, for 20 s each.
- `dry-run` — every `demo/*.yaml` and `demo/vm/config*.yaml`, and a
  `--template` config, must still plan.
