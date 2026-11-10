package system

import (
	"os"
	"strings"
)

// cpuinfoFile is the live CPU info; a variable so tests can point it
// elsewhere.
var cpuinfoFile = "/proc/cpuinfo"

// CPUVendor returns the x86 CPU vendor for kernel-argument selection:
// "intel", "amd", or "" (unknown / not x86 — e.g. an ARM host, where the
// vendor-specific arguments do not apply).
func CPUVendor() string {
	data, err := os.ReadFile(cpuinfoFile)
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(line, "vendor_id") {
			continue
		}
		_, v, _ := strings.Cut(line, ":")
		switch strings.TrimSpace(v) {
		case "GenuineIntel":
			return "intel"
		case "AuthenticAMD", "HygonGenuine":
			return "amd"
		}
		return ""
	}
	return ""
}

// VendorKernelArgs rewrites the CPU-vendor-specific tokens in a kernel
// command line for the given vendor, so a profile written with Intel
// arguments boots correctly on AMD (and vice versa): intel_iommu=/amd_iommu=
// become <vendor>_iommu=on, kvm-intel/kvm-amd.nested become
// kvm-<vendor>.nested, and intel_idle.max_cstate= (Intel-only) is dropped on
// AMD. An unknown/non-x86 vendor leaves the arguments untouched.
func VendorKernelArgs(args, vendor string) string {
	if vendor != "intel" && vendor != "amd" {
		return args
	}
	var out []string
	for _, t := range strings.Fields(args) {
		switch {
		case strings.HasPrefix(t, "intel_iommu=") || strings.HasPrefix(t, "amd_iommu="):
			out = append(out, vendor+"_iommu=on")
		case t == "kvm-intel.nested=1" || t == "kvm-amd.nested=1":
			out = append(out, "kvm-"+vendor+".nested=1")
		case strings.HasPrefix(t, "intel_idle.max_cstate="):
			if vendor == "intel" {
				out = append(out, t) // Intel-only; drop on AMD (processor.max_cstate covers it)
			}
		default:
			out = append(out, t)
		}
	}
	return strings.Join(out, " ")
}
