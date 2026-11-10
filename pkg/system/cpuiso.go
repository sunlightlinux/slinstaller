package system

import (
	"fmt"
	"os"
	"runtime"
	"sort"
	"strconv"
	"strings"
)

// cpuSysDir and meminfoFile are the live sources for CPU topology and memory;
// variables so tests can point them elsewhere.
var (
	cpuSysDir   = "/sys/devices/system/cpu"
	meminfoFile = "/proc/meminfo"
)

// CPUIsolation is a computed CPU-partitioning layout for a real-time / DPDK
// workload: a housekeeping set (the first physical core, kept for the OS and
// IRQs) and an isolated set (the rest, dedicated to the workload), plus the
// kernel command-line arguments that pin them. OK is false when the machine
// has too few cores to partition.
type CPUIsolation struct {
	Total        int
	Housekeeping string // e.g. "0,8"
	Isolated     string // e.g. "1-7,9-15"
	Args         string
	OK           bool
}

// hkRatio scales the housekeeping reservation with machine size: roughly one
// housekeeping logical CPU per this many online CPUs, matching SUSE's telco
// examples (8 of 128, 4 of 64). Always at least one whole physical core.
const hkRatio = 16

// ComputeCPUIsolation computes the partitioning following common industry
// practice (tuned's cpu-partitioning, OpenShift's performance profile, SUSE
// Telco Cloud): reserve the first N physical cores for housekeeping — scaled
// with machine size and kept as whole SMT-sibling groups so no core is split
// between housekeeping and isolated — isolate every other online CPU, and
// apply isolcpus (domain,nohz,managed_irq), nohz_full and rcu_nocbs to the
// isolated set with irqaffinity on the housekeeping set.
func ComputeCPUIsolation() CPUIsolation {
	online := onlineCPUs()
	if len(online) == 0 {
		for i := 0; i < runtime.NumCPU(); i++ {
			online = append(online, i)
		}
	}
	cores := physicalCores(online)
	ci := CPUIsolation{Total: len(online)}

	// Reserve whole physical cores for housekeeping: at least one, and enough
	// to reach about Total/hkRatio logical CPUs.
	target := ci.Total / hkRatio
	var hk, iso []int
	hkLogical := 0
	for i, core := range cores {
		if i >= 1 && hkLogical >= target {
			iso = append(iso, core...)
			continue
		}
		hk = append(hk, core...)
		hkLogical += len(core)
	}
	sort.Ints(hk)
	sort.Ints(iso)

	// Fewer than three online CPUs, or nothing left to isolate, is too small
	// to partition — the whole machine stays general-purpose.
	if ci.Total < 3 || len(iso) == 0 {
		return ci
	}
	ci.Housekeeping = formatRanges(hk)
	ci.Isolated = formatRanges(iso)
	ci.Args = fmt.Sprintf("isolcpus=domain,nohz,managed_irq,%s nohz_full=%s rcu_nocbs=%s irqaffinity=%s",
		ci.Isolated, ci.Isolated, ci.Isolated, ci.Housekeeping)
	ci.OK = true
	return ci
}

// physicalCores groups the online CPUs into physical cores by their SMT thread
// siblings, each core's threads sorted, cores ordered by lowest thread id. A
// CPU whose topology is unreadable becomes a core of its own.
func physicalCores(online []int) [][]int {
	onlineSet := map[int]bool{}
	for _, c := range online {
		onlineSet[c] = true
	}
	seen := map[int]bool{}
	var cores [][]int
	for _, c := range online {
		if seen[c] {
			continue
		}
		sibs := threadSiblings(c)
		var core []int
		for _, s := range sibs {
			if onlineSet[s] {
				core = append(core, s)
				seen[s] = true
			}
		}
		if len(core) == 0 {
			core = []int{c}
			seen[c] = true
		}
		sort.Ints(core)
		cores = append(cores, core)
	}
	sort.Slice(cores, func(i, j int) bool { return cores[i][0] < cores[j][0] })
	return cores
}

// threadSiblings reads cpuN's SMT thread-sibling list, or just the CPU itself
// when the topology is unreadable.
func threadSiblings(cpu int) []int {
	data, err := os.ReadFile(fmt.Sprintf("%s/cpu%d/topology/thread_siblings_list", cpuSysDir, cpu))
	if err != nil {
		return []int{cpu}
	}
	return parseCPUList(strings.TrimSpace(string(data)))
}

// HugepagesGiB is a conservative number of 1 GiB hugepages to reserve for a
// DPDK/telco workload: about a quarter of RAM, always leaving at least 4 GiB
// for the OS, and 0 on a small machine.
func HugepagesGiB() int {
	data, err := os.ReadFile(meminfoFile)
	if err != nil {
		return 0
	}
	ramKiB := 0
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "MemTotal:") {
			f := strings.Fields(line)
			if len(f) >= 2 {
				ramKiB, _ = strconv.Atoi(f[1])
			}
			break
		}
	}
	ramGiB := ramKiB / (1024 * 1024)
	quarter := ramGiB / 4
	leaveOS := ramGiB - 4
	n := quarter
	if leaveOS < n {
		n = leaveOS
	}
	if n < 1 {
		return 0
	}
	return n
}

// onlineCPUs returns the online CPU ids from sysfs (e.g. "0-15"), sorted.
func onlineCPUs() []int {
	data, err := os.ReadFile(cpuSysDir + "/online")
	if err != nil {
		return nil
	}
	return parseCPUList(strings.TrimSpace(string(data)))
}

// parseCPUList parses a kernel CPU list such as "0-3,8,10-11" into sorted ids.
func parseCPUList(s string) []int {
	seen := map[int]bool{}
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if lo, hi, ok := strings.Cut(part, "-"); ok {
			a, e1 := strconv.Atoi(strings.TrimSpace(lo))
			b, e2 := strconv.Atoi(strings.TrimSpace(hi))
			if e1 != nil || e2 != nil {
				continue
			}
			for c := a; c <= b; c++ {
				seen[c] = true
			}
		} else if a, err := strconv.Atoi(part); err == nil {
			seen[a] = true
		}
	}
	out := make([]int, 0, len(seen))
	for c := range seen {
		out = append(out, c)
	}
	sort.Ints(out)
	return out
}

// formatRanges turns a sorted CPU id list into the compact "0-3,8,10-11"
// form the kernel command line uses.
func formatRanges(ids []int) string {
	if len(ids) == 0 {
		return ""
	}
	var parts []string
	start, prev := ids[0], ids[0]
	flush := func() {
		if start == prev {
			parts = append(parts, strconv.Itoa(start))
		} else {
			parts = append(parts, fmt.Sprintf("%d-%d", start, prev))
		}
	}
	for _, c := range ids[1:] {
		if c == prev+1 {
			prev = c
			continue
		}
		flush()
		start, prev = c, c
	}
	flush()
	return strings.Join(parts, ",")
}
