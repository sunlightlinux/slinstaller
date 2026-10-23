# tests

Integration and acceptance tests that need more than a Go unit test.

The honest test for an installer is an install. Unit tests live beside
the code they cover under `pkg/` and never touch a disk; the tests here
exercise the real destructive path the only safe way — a VM. A run boots
the live medium under QEMU with OVMF (UEFI firmware), points slinstaller
at a scratch qcow2, and asserts the result boots to a login prompt.

Nothing lives here yet; it is populated as the install path becomes
runnable end to end.
