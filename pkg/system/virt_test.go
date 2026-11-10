package system

import (
	"os"
	"path/filepath"
	"testing"
)

func TestVirtualMachine(t *testing.T) {
	dir := t.TempDir()
	origCPU, origDMI := cpuinfoFile, dmiDir
	defer func() { cpuinfoFile, dmiDir = origCPU, origDMI }()
	cpuinfoFile = filepath.Join(dir, "cpuinfo")
	dmiDir = filepath.Join(dir, "dmi")
	if err := os.MkdirAll(dmiDir, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(p, s string) {
		t.Helper()
		if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	write(filepath.Join(dmiDir, "sys_vendor"), "QEMU\n")
	write(cpuinfoFile, "vendor_id\t: GenuineIntel\nflags\t\t: fpu vme hypervisor lahf_lm\n")
	if name, ok := VirtualMachine(); !ok || name != "QEMU" {
		t.Errorf("guest: %q %v", name, ok)
	}

	// Bare metal fills DMI too: only the CPU flag decides.
	write(filepath.Join(dmiDir, "sys_vendor"), "Dell Inc.\n")
	write(cpuinfoFile, "vendor_id\t: GenuineIntel\nflags\t\t: fpu vme lahf_lm\n")
	if name, ok := VirtualMachine(); ok {
		t.Errorf("bare metal taken for a VM: %q", name)
	}

	// A hypervisor flag as part of another word does not count.
	write(cpuinfoFile, "flags\t\t: fpu nohypervisor_x\n")
	if _, ok := VirtualMachine(); ok {
		t.Error("matched a substring")
	}

	// A guest whose firmware names nothing is still a guest.
	if err := os.Remove(filepath.Join(dmiDir, "sys_vendor")); err != nil {
		t.Fatal(err)
	}
	write(cpuinfoFile, "flags\t\t: fpu hypervisor\n")
	if name, ok := VirtualMachine(); !ok || name != "" {
		t.Errorf("unnamed guest: %q %v", name, ok)
	}
}
