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
