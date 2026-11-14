package system

import (
	"os"
	"path/filepath"
	"strings"
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

func TestVendorKernelArgsIommuFlags(t *testing.T) {
	// igfx_off is kept on Intel, dropped (plain amd_iommu=on) on AMD.
	in := "intel_iommu=on,igfx_off iommu=pt"
	if got := VendorKernelArgs(in, "intel"); got != "intel_iommu=on,igfx_off iommu=pt" {
		t.Errorf("Intel dropped igfx_off: %q", got)
	}
	if got := VendorKernelArgs(in, "amd"); got != "amd_iommu=on iommu=pt" {
		t.Errorf("AMD iommu wrong: %q", got)
	}
	// amd_iommu flags kept on AMD, normalized on Intel.
	if got := VendorKernelArgs("amd_iommu=on iommu=pt", "intel"); got != "intel_iommu=on iommu=pt" {
		t.Errorf("Intel from amd_iommu wrong: %q", got)
	}
}

func TestRefitVendor(t *testing.T) {
	refit := func(args, vendor string) string {
		return strings.Join(RefitVendor(strings.Fields(args), vendor), " ")
	}
	// An AMD host's virtualization config, replayed on Intel and back.
	amd := "quiet amd_iommu=on iommu=pt kvm-amd.nested=1 amd_pstate=passive console=tty0"
	intel := "quiet intel_iommu=on iommu=pt kvm-intel.nested=1 intel_pstate=passive console=tty0"
	if got := refit(amd, "intel"); got != intel {
		t.Errorf("AMD -> Intel:\n got %q\nwant %q", got, intel)
	}
	if got := refit(intel, "amd"); got != amd {
		t.Errorf("Intel -> AMD:\n got %q\nwant %q", got, amd)
	}
	// On the vendor it was written for, nothing changes.
	if got := refit(amd, "amd"); got != amd {
		t.Errorf("AMD on AMD changed: %q", got)
	}
	if got := refit(intel, "intel"); got != intel {
		t.Errorf("Intel on Intel changed: %q", got)
	}

	// A mode the other vendor does not have is dropped, not mistranslated.
	if got := refit("quiet amd_pstate=guided", "intel"); got != "quiet" {
		t.Errorf("amd_pstate=guided on Intel = %q, want it dropped", got)
	}
	for _, mode := range []string{"no_hwp", "force"} {
		if got := refit("quiet intel_pstate="+mode, "amd"); got != "quiet" {
			t.Errorf("intel_pstate=%s on AMD = %q, want it dropped", mode, got)
		}
	}
	// The machine's own P-State argument wins over a translated one.
	if got := refit("intel_pstate=passive amd_pstate=active", "amd"); got != "amd_pstate=active" {
		t.Errorf("own pstate should win: %q", got)
	}
	// Intel-only idle driver limit is dropped on AMD; igfx_off too.
	if got := refit("intel_idle.max_cstate=0 intel_iommu=on,igfx_off", "amd"); got != "amd_iommu=on" {
		t.Errorf("Intel-only arguments on AMD = %q", got)
	}
	// An unknown vendor is not judged.
	if got := refit(amd, ""); got != amd {
		t.Errorf("unknown vendor changed the arguments: %q", got)
	}
}
