package tui

import (
	"fmt"
	"strings"

	"github.com/sunlightlinux/slinstaller/pkg/system"
)

// kernelOptions are the kernels offered in the selection dialog; the empty
// first entry keeps the base system's default kernel.
var kernelOptions = []string{"", "kernel-native", "kernel-lts"}

// logLevelOptions are the console log-verbosity choices. The first keeps the
// kernel default (nothing written); the rest set loglevel=N on the kernel
// command line and kernel.printk = N 4 1 7 via sysctl. Index i>0 means
// loglevel N = i-1 (0 emergency … 7 debug).
var logLevelOptions = []string{
	"Kernel default (not set)",
	"0 - emergency (silent)",
	"1 - alert",
	"2 - critical",
	"3 - error",
	"4 - warning",
	"5 - notice",
	"6 - info",
	"7 - debug (verbose)",
}

// logLevelToIndex maps a stored log level (-1 = unset, 0..7) to its index in
// logLevelOptions; logLevelFromIndex is the inverse.
func logLevelToIndex(lv int) int {
	if lv < 0 {
		return 0
	}
	return lv + 1
}

func logLevelFromIndex(i int) int {
	if i <= 0 {
		return -1
	}
	return i - 1
}

// pstateOptions are the amd_pstate driver modes offered for the AMD RT/telco
// isolation profiles; "" (last) adds nothing. The stored value is the mode
// token ("active"/"passive"/"guided") or "".
var pstateOptions = []string{
	"active (default)",
	"passive",
	"guided",
	"disable (unregister the driver)",
	"Not set (kernel default)",
}

var pstateValues = []string{"active", "passive", "guided", "disable", ""}

// Default kdump settings suggested when the section is first opened: a
// RAM-scaled crashkernel reservation (the portable range syntax) and a 5s
// auto-reboot after a panic so a crashed node recovers once the dump is taken.
const (
	defaultCrashkernel = "1G-4G:192M,4G-64G:256M,64G-:512M"
	defaultPanicSecs   = "5"
)

// intelPstateOptions are the intel_pstate driver modes for the Intel RT/telco
// isolation profiles. The default ("", first) adds nothing, keeping HWP — the
// recommendation for real-time, where forcing intel_pstate off HWP hurts
// latency. The stored value is the mode token or "".
var intelPstateOptions = []string{
	"Not set (keep HWP, default)",
	"active",
	"passive",
	"no_hwp (disable HWP)",
	"disable (use acpi-cpufreq)",
	"force",
}

var intelPstateValues = []string{"", "active", "passive", "no_hwp", "disable", "force"}

// governorOptions are the CPU-frequency governors offered. The first ("",
// not set) keeps whatever governor a profile already sets (or the kernel
// default). The chosen one is the authority: it replaces any
// cpufreq.default_governor= a profile carries.
var governorOptions = []string{
	"Not set (profile default)",
	"performance",
	"schedutil",
	"ondemand",
	"conservative",
	"powersave",
}

var governorValues = []string{"", "performance", "schedutil", "ondemand", "conservative", "powersave"}

// activeMode reports whether the installed system's scaling driver will run in
// active mode, where only the performance and powersave governors exist. A
// P-State mode chosen for an isolation profile decides it (the P-State sections
// apply only to those profiles); otherwise the installed system boots the way
// the live one did, so the live EPP detection stands.
func (m model) activeMode() bool {
	if kernelProfileByName(m.kernelProfile).isolate {
		switch m.cpuVendor {
		case "amd":
			if m.amdPstate != "" {
				return m.amdPstate == "active" // passive, guided and disable use the classic governors
			}
		case "intel":
			switch m.intelPstate {
			case "active", "no_hwp", "force":
				return true
			case "passive", "disable":
				return false
			}
		}
	}
	return m.epp
}

// governorChoices returns the governor labels and their values for the
// installed system. In active mode only performance and powersave exist; a
// passive/legacy driver has the classic set.
func (m model) governorChoices() (opts, vals []string) {
	if m.activeMode() {
		return []string{"Not set (profile default)", "performance", "powersave"},
			[]string{"", "performance", "powersave"}
	}
	return governorOptions, governorValues
}

// dropStaleGovernor clears a chosen governor that no longer exists in the
// installed system's driver mode (after the P-State mode or the profile
// changed) and returns its name, or "" when nothing had to change.
func (m *model) dropStaleGovernor() string {
	if m.governor == "" {
		return ""
	}
	if _, vals := m.governorChoices(); containsStr(vals, m.governor) {
		return ""
	}
	old := m.governor
	m.governor = ""
	return old
}

// staleGovernorMsg is the popup body telling the operator a governor was reset.
func staleGovernorMsg(old string) []string {
	return []string{
		"The " + old + " governor does not exist in the",
		"driver mode now selected, so CPU Frequency",
		"Governor was reset to the profile default.",
	}
}

// setGovernor replaces any cpufreq.default_governor= in args with the chosen
// governor (appended), so the CPU Governor section is the single authority.
func setGovernor(args, gov string) string {
	var out []string
	for _, t := range strings.Fields(args) {
		if !strings.HasPrefix(t, "cpufreq.default_governor=") {
			out = append(out, t)
		}
	}
	out = append(out, "cpufreq.default_governor="+gov)
	return strings.Join(out, " ")
}

// defaultKernelProfile is the profile an install gets when the operator does
// not choose one — in the wizard and in the prompt flow alike.
const defaultKernelProfile = "Server"

// kernelProfile is a named set of kernel command-line arguments: a starting
// point the operator picks and then edits as Custom if needed. note flags a
// trade-off (security/power) or a machine-specific step. The args are valid,
// machine-independent defaults — tuning that needs specific CPU ranges
// (isolcpus=, nohz_full=<list>) is left to Custom, since it depends on the
// host's topology.
type kernelProfile struct {
	name    string
	args    string
	note    string
	isolate bool // append computed isolcpus=/nohz_full=/rcu_nocbs=/irqaffinity=
	huge    bool // append computed 1GiB hugepages (DPDK/telco)
}

// telcoArgs is the balanced Telco/RAN real-time base; telcoMaxArgs stacks the
// aggressive extras used on real-time production systems (kill cpuidle
// entirely, PCIe ASPM and audit) for maximum determinism at the cost of power
// and security.
const telcoArgs = "preempt=full idle=poll skew_tick=1 nohz=on mce=off nosoftlockup nowatchdog nmi_watchdog=0 rcu_nocb_poll rcupdate.rcu_cpu_stall_suppress=1 rcupdate.rcu_expedited=1 rcupdate.rcu_normal_after_boot=1 rcupdate.rcu_task_stall_timeout=0 rcutree.kthread_prio=99 clocksource=tsc tsc=reliable net.ifnames=1 intel_iommu=on iommu=pt mitigations=off cpufreq.default_governor=performance"

const telcoMaxArgs = telcoArgs + " cpuidle.off=1 processor.max_cstate=0 pcie_aspm=off audit=0"

var kernelProfiles = []kernelProfile{
	{name: "General (robust defaults)", args: "quiet tsc=reliable page_alloc.shuffle=1 rcupdate.rcu_expedited=1 rootfstype=ext4,btrfs,xfs,f2fs cryptomgr.notests mitigations=auto"},
	{name: "VM Guest", args: "no_timer_check noreplace-smp tsc=reliable rootfstype=ext4,btrfs,xfs,f2fs mitigations=auto"},
	{name: "Server", args: "mitigations=auto transparent_hugepage=madvise cpufreq.default_governor=schedutil"},
	{name: "Desktop", args: "quiet splash mitigations=auto transparent_hugepage=madvise"},
	{name: "Desktop LowLatency", args: "quiet splash preempt=full threadirqs transparent_hugepage=madvise"},
	{name: "LowLatency", args: "preempt=full threadirqs nowatchdog nmi_watchdog=0 pcie_aspm=force pcie_aspm.policy=performance",
		note: "pcie_aspm=force overrides the firmware's ASPM policy for lowest PCIe latency; may be unstable on hardware with quirky ASPM."},
	{name: "Realtime", args: "preempt=full threadirqs nowatchdog nmi_watchdog=0 skew_tick=1 nohz=on nosoftlockup rcu_nocb_poll rcupdate.rcu_expedited=1 rcupdate.rcu_normal_after_boot=1 rcutree.kthread_prio=99 clocksource=tsc tsc=reliable", isolate: true},
	{name: "Audio/Video Workstation", args: "preempt=full threadirqs tsc=reliable nowatchdog nmi_watchdog=0 cpufreq.default_governor=performance pcie_aspm=force pcie_aspm.policy=performance",
		note: "pcie_aspm=force overrides the firmware's ASPM policy for lowest PCIe latency; may be unstable on hardware with quirky ASPM."},
	{name: "Telco/RAN", args: telcoArgs,
		note: "mitigations=off + idle=poll: max performance, less secure, high power. idle=poll replaces C-state limiting.", isolate: true, huge: true},
	{name: "Telco/RAN Max Perf", args: telcoMaxArgs,
		note: "Telco/RAN plus cpuidle.off/max_cstate=0, pcie_aspm=off, audit=0: maximum determinism, highest power draw, weakest security.", isolate: true, huge: true},
	{name: "HPC / Compute", args: "mitigations=off nowatchdog nmi_watchdog=0 processor.max_cstate=1 intel_idle.max_cstate=0 cpufreq.default_governor=performance transparent_hugepage=always",
		note: "mitigations=off: faster, but disables CPU-vulnerability mitigations."},
	{name: "AMD Instinct (GPU compute)", args: "iommu=pt intel_iommu=on pci=realloc=off pci=bfsort numa_balancing=disable mitigations=off pcie_aspm=off processor.max_cstate=0 nmi_watchdog=0 nowatchdog tsc=nowatchdog transparent_hugepage=always quiet",
		note: "AMD Instinct GPU-compute nodes; iommu adapts to the host CPU. Max-tuning extras in Custom: modprobe.blacklist=amdgpu, nokaslr, norandmaps, biosdevname=0."},
	{name: "Virtualization Host (KVM)", args: "intel_iommu=on iommu=pt kvm-intel.nested=1 transparent_hugepage=never"},
	{name: "GPU Passthrough (VFIO)", args: "intel_iommu=on iommu=pt vfio-pci.disable_vga=1 kvm-intel.nested=1 transparent_hugepage=never",
		note: "Strict isolation. For consumer boards that need ACS override, use the consumer/ACS profile."},
	{name: "GPU Passthrough (consumer/ACS)", args: "intel_iommu=on,igfx_off iommu=pt vfio-pci.disable_vga=1 kvm-intel.nested=1 pcie_acs_override=downstream transparent_hugepage=never",
		note: "pcie_acs_override splits IOMMU groups (weakens device isolation); igfx_off keeps the iGPU for the host. Needs Sunlight's ACS-override kernel."},
	{name: "Intel GPU Workstation", args: "i915.force_probe=* i915.enable_guc=3 i915.enable_fbc=1 mitigations=auto"},
	{name: "AMD GPU Workstation", args: "amdgpu.ppfeaturemask=0xffffffff amdgpu.dc=1 amdgpu.gpu_recovery=1 mitigations=auto",
		note: "ppfeaturemask=0xffffffff unlocks power/clock control (CoreCtrl); use only on a discrete AMD GPU."},
	{name: "NVIDIA GPU (proprietary)", args: "rd.driver.blacklist=nouveau nouveau.modeset=0 module_blacklist=nouveau nvidia-drm.modeset=1 mitigations=auto",
		note: "Blocks nouveau for the proprietary NVIDIA driver (install it, or the GPU has no driver). nvidia-drm.modeset=1 enables KMS."},
	{name: "Containers / Cloud", args: "cgroup_no_v1=all cgroup_enable=memory swapaccount=1 transparent_hugepage=madvise mitigations=auto"},
	{name: "Hardened (AppArmor)", args: "apparmor=1 security=apparmor lockdown=integrity mitigations=auto audit=1"},
	{name: "Laptop / Power Save", args: "quiet splash mitigations=auto pcie_aspm=force pcie_aspm.policy=powersave nmi_watchdog=0"},
	{name: "Debug / Verbose Boot", args: "loglevel=7 ignore_loglevel initcall_debug earlyprintk=vga"},
	{name: "Custom", note: "Type the Add / Remove arguments yourself."},
}

// kernelProfileDesc is a one-line description of what each profile is for,
// shown in the Details window (not inline, so the list stays full height).
var kernelProfileDesc = map[string]string{
	"General (robust defaults)":      "Safe, broadly useful boot defaults for a general-purpose system.",
	"VM Guest":                       "Tweaks for running as a virtual-machine guest (timer and SMP compatibility).",
	"Server":                         "Balanced server defaults: schedutil governor and transparent hugepages.",
	"Desktop":                        "Quiet graphical boot for a desktop workstation.",
	"Desktop LowLatency":             "Desktop with a low-latency preemptible kernel for responsiveness.",
	"LowLatency":                     "Low-latency preemption and threaded IRQs, without the full real-time set.",
	"Realtime":                       "Low-latency tuning on the stock kernel (full preemption, not PREEMPT_RT); auto-isolates CPUs.",
	"Audio/Video Workstation":        "Low-latency audio/video production (threaded IRQs, performance governor).",
	"Telco/RAN":                      "Standard telco/RAN real-time profile: CPU isolation, DPDK IOMMU, hugepages.",
	"Telco/RAN Max Perf":             "Telco/RAN pushed to maximum determinism; highest power draw, least secure.",
	"HPC / Compute":                  "Throughput-oriented HPC compute (mitigations off, performance governor).",
	"AMD Instinct (GPU compute)":     "Tuning for AMD Instinct GPU-compute nodes (reliable PCI enumeration, IOMMU).",
	"Virtualization Host (KVM)":      "KVM hypervisor host with IOMMU passthrough and nested virtualization.",
	"GPU Passthrough (VFIO)":         "Strict VFIO GPU passthrough with full IOMMU isolation.",
	"GPU Passthrough (consumer/ACS)": "GPU passthrough on consumer boards that need ACS override.",
	"Intel GPU Workstation":          "Intel integrated-GPU workstation tuning (i915).",
	"AMD GPU Workstation":            "Discrete AMD GPU workstation tuning (amdgpu).",
	"NVIDIA GPU (proprietary)":       "Blocks nouveau so the proprietary NVIDIA driver can load.",
	"Containers / Cloud":             "Container/cloud host: unified cgroups and memory accounting.",
	"Hardened (AppArmor)":            "Security-hardened boot with AppArmor and kernel lockdown.",
	"Laptop / Power Save":            "Laptop power saving (PCIe ASPM, quiet boot).",
	"Debug / Verbose Boot":           "Verbose boot diagnostics for troubleshooting.",
	"Custom":                         "Type your own Add / Remove kernel arguments.",
}

// profileDetails builds the Details popup for a profile: its description, the
// effective parameters for this machine, and any trade-off / auto-computed
// note. Reuses the message-popup rendering (popupMsg[0] is the title).
func (m model) profileDetails(p kernelProfile) []string {
	msg := []string{p.name, ""}
	if d := kernelProfileDesc[p.name]; d != "" {
		msg = append(msg, wrapWords(d, 66, 4)...)
		msg = append(msg, "")
	}
	msg = append(msg, "Parameters:")
	args := m.profileArgs(p)
	if strings.TrimSpace(args) == "" {
		args = "(none - type them in Custom)"
	}
	for _, ln := range wrapWords(args, 64, 20) {
		msg = append(msg, "  "+ln)
	}
	if note := m.profileNote(p); note != "" {
		msg = append(msg, "")
		msg = append(msg, wrapWords("Note: "+note, 66, 5)...)
	}
	return msg
}

// kernelProfileNames is the picker's list; kernelProfileIndex finds a name
// (defaulting to the first entry); kernelProfileByName returns the profile.
func kernelProfileNames() []string {
	out := make([]string, len(kernelProfiles))
	for i, p := range kernelProfiles {
		out[i] = p.name
	}
	return out
}

func kernelProfileIndex(name string) int {
	for i, p := range kernelProfiles {
		if p.name == name {
			return i
		}
	}
	return 0
}

func kernelProfileByName(name string) kernelProfile {
	return kernelProfiles[kernelProfileIndex(name)]
}

// profileArgs is a profile's effective arguments: its base set plus, for a
// CPU-isolation profile, the isolcpus=/nohz_full=/… computed from this
// machine's cores, and the computed 1GiB hugepages for a DPDK/telco profile.
func (m model) profileArgs(p kernelProfile) string {
	// Rewrite CPU-vendor-specific tokens (intel_iommu/kvm-intel/… ↔ amd) to
	// match the detected processor, so an Intel-written profile boots on AMD.
	args := system.VendorKernelArgs(p.args, m.cpuVendor)
	// On AMD, select the P-state driver mode for the isolation profiles
	// (default active = hardware-managed P-states, the AMD equivalent of
	// keeping Intel HWP). Intel keeps HWP by default, so nothing is added there.
	if p.isolate && m.cpuVendor == "amd" && m.amdPstate != "" {
		args = strings.TrimSpace(args + " amd_pstate=" + m.amdPstate)
	}
	// On Intel, intel_pstate is left unset by default (HWP kept, best for RT);
	// a chosen mode is applied to the isolation profiles.
	if p.isolate && m.cpuVendor == "intel" && m.intelPstate != "" {
		args = strings.TrimSpace(args + " intel_pstate=" + m.intelPstate)
	}
	if p.isolate && m.cpuIso.OK {
		args = strings.TrimSpace(args + " " + m.cpuIso.Args)
	}
	if p.huge && m.hugeGiB > 0 {
		args = strings.TrimSpace(args + fmt.Sprintf(" default_hugepagesz=1G hugepagesz=1G hugepages=%d", m.hugeGiB))
	}
	// A chosen CPU-frequency governor overrides whatever the profile set.
	if m.governor != "" {
		args = setGovernor(args, m.governor)
	}
	return args
}

// busyIdle reports kernel arguments that keep idle CPUs running instead of
// sleeping, which is the point of a telco profile on its own hardware.
func busyIdle(args string) bool {
	for _, a := range strings.Fields(args) {
		if a == "idle=poll" || a == "cpuidle.off=1" {
			return true
		}
	}
	return false
}

// busyIdleWarning is what to say when such arguments are chosen in a virtual
// machine, where every virtual CPU spinning is a host CPU busy: a Telco/RAN
// profile held a 12-CPU guest at 95% while it did nothing. Nil when it does not
// apply. The choice stands; testing the profile in a VM is legitimate.
func (m model) busyIdleWarning(args string) []string {
	if !m.vm || !busyIdle(args) {
		return nil
	}
	what := "This is a virtual machine"
	if m.vmName != "" {
		what += " (" + m.vmName + ")"
	}
	return []string{
		what + ".",
		"idle=poll / cpuidle.off=1 keep every virtual CPU",
		"busy, so the host sees all of them near 100%",
		"even while Sunlight is idle.",
		"",
		"Fine for testing this profile. For everyday use",
		"in a VM, choose the VM Guest profile.",
	}
}

// profileNote is the note shown for a profile: for a CPU-isolation profile it
// reports what was auto-computed for this machine (or that there were too few
// cores to isolate), else the profile's static note.
func (m model) profileNote(p kernelProfile) string {
	n := m.profileNoteBase(p)
	if m.vm && busyIdle(p.args) {
		n = "In a VM this keeps every host CPU busy. " + n
	}
	return n
}

func (m model) profileNoteBase(p kernelProfile) string {
	if !p.isolate {
		return p.note
	}
	if !m.cpuIso.OK {
		return "Too few CPUs to isolate automatically; pin CPUs in Custom."
	}
	n := fmt.Sprintf("Auto-pinned for %d CPUs: isolated %s, housekeeping %s.",
		m.cpuIso.Total, m.cpuIso.Isolated, m.cpuIso.Housekeeping)
	if p.huge && m.hugeGiB > 0 {
		n += fmt.Sprintf(" %d x 1GiB hugepages.", m.hugeGiB)
	}
	if p.note != "" {
		n = p.note + " " + n
	}
	return n
}

// kernelLabel is how a kernel option reads in the dialog and the hub.
func kernelLabel(k string) string {
	switch k {
	case "":
		return "(default)"
	case "kernel-native":
		return "linux (current)"
	case "kernel-lts":
		return "linux-lts (long-term support)"
	default:
		return k
	}
}

// resyncProfileArgs rebuilds a predefined profile's arguments after a setting
// that feeds them (P-State mode, governor) changed. Custom arguments are the
// operator's own text and are left alone.
func (m *model) resyncProfileArgs() {
	if m.kernelProfile != "" && m.kernelProfile != "Custom" {
		m.kernelArgs = m.profileArgs(kernelProfileByName(m.kernelProfile))
	}
}
