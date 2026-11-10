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
// arguments boots correctly on AMD (and vice versa): the iommu token keeps its
// flags on the matching vendor (e.g. intel_iommu=on,igfx_off stays as-is on
// Intel) but becomes a plain <vendor>_iommu=on when switched, since flags like
// igfx_off are Intel-only; kvm-intel/kvm-amd.nested become kvm-<vendor>.nested;
// and intel_idle.max_cstate= (Intel-only) is dropped on AMD. An unknown/non-x86
// vendor leaves the arguments untouched.
func VendorKernelArgs(args, vendor string) string {
	if vendor != "intel" && vendor != "amd" {
		return args
	}
	var out []string
	for _, t := range strings.Fields(args) {
		switch {
		case strings.HasPrefix(t, "intel_iommu="):
			if vendor == "intel" {
				out = append(out, t) // keep flags such as igfx_off
			} else {
				out = append(out, "amd_iommu=on")
			}
		case strings.HasPrefix(t, "amd_iommu="):
			if vendor == "amd" {
				out = append(out, t)
			} else {
				out = append(out, "intel_iommu=on")
			}
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
