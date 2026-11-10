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

// CPUIsolation computes the partitioning following common industry practice
// (tuned's cpu-partitioning, OpenShift's performance profile): reserve the
// first physical core — cpu0 and its SMT sibling — for housekeeping, isolate
// every other online CPU, and apply isolcpus (managed_irq,domain), nohz_full
// and rcu_nocbs to the isolated set with irqaffinity on the housekeeping set.
func ComputeCPUIsolation() CPUIsolation {
	online := onlineCPUs()
	if len(online) == 0 {
		for i := 0; i < runtime.NumCPU(); i++ {
			online = append(online, i)
		}
	}
	hk := cpu0Siblings(online)
	hkSet := map[int]bool{}
	for _, c := range hk {
		hkSet[c] = true
	}
	var iso []int
	for _, c := range online {
		if !hkSet[c] {
			iso = append(iso, c)
		}
	}
	ci := CPUIsolation{Total: len(online)}
	// Fewer than three online CPUs, or nothing left after housekeeping, is
	// too small to partition — the whole machine stays general-purpose.
	if ci.Total < 3 || len(iso) == 0 {
		return ci
	}
	ci.Housekeeping = formatRanges(hk)
	ci.Isolated = formatRanges(iso)
	ci.Args = fmt.Sprintf("isolcpus=managed_irq,domain,%s nohz_full=%s rcu_nocbs=%s irqaffinity=%s",
		ci.Isolated, ci.Isolated, ci.Isolated, ci.Housekeeping)
	ci.OK = true
	return ci
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

// cpu0Siblings returns cpu0's SMT thread siblings (a full physical core), or
// just cpu0 when the topology is unreadable or has no siblings.
func cpu0Siblings(online []int) []int {
	data, err := os.ReadFile(cpuSysDir + "/cpu0/topology/thread_siblings_list")
	if err != nil {
		return []int{0}
	}
	sib := parseCPUList(strings.TrimSpace(string(data)))
	// Keep only online siblings.
	onlineSet := map[int]bool{}
	for _, c := range online {
		onlineSet[c] = true
	}
	var out []int
	for _, c := range sib {
		if onlineSet[c] {
			out = append(out, c)
		}
	}
	if len(out) == 0 {
		return []int{0}
	}
	return out
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
