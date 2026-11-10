package system

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCPUVendor(t *testing.T) {
	saved := cpuinfoFile
	defer func() { cpuinfoFile = saved }()
	for in, want := range map[string]string{
		"GenuineIntel": "intel",
		"AuthenticAMD": "amd",
		"HygonGenuine": "amd",
		"Other":        "",
	} {
		f := filepath.Join(t.TempDir(), "cpuinfo")
		os.WriteFile(f, []byte("processor\t: 0\nvendor_id\t: "+in+"\nmodel name\t: x\n"), 0o644)
		cpuinfoFile = f
		if got := CPUVendor(); got != want {
			t.Errorf("CPUVendor(%s) = %q, want %q", in, got, want)
		}
	}
}

func TestVendorKernelArgsAMD(t *testing.T) {
	in := "intel_iommu=on iommu=pt kvm-intel.nested=1 intel_idle.max_cstate=0 processor.max_cstate=1 quiet"
	got := VendorKernelArgs(in, "amd")
	want := "amd_iommu=on iommu=pt kvm-amd.nested=1 processor.max_cstate=1 quiet"
	if got != want {
		t.Errorf("AMD rewrite:\n got %q\nwant %q", got, want)
	}
	// Intel keeps its own tokens.
	if got := VendorKernelArgs(in, "intel"); got != in {
		t.Errorf("Intel rewrite changed args: %q", got)
	}
	// Unknown vendor leaves everything untouched.
	if got := VendorKernelArgs(in, ""); got != in {
		t.Errorf("unknown vendor changed args: %q", got)
	}
}
