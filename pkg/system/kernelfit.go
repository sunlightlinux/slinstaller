package system

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// minFreeAfterHugepages is the least memory a hugepage reservation must leave
// for the rest of the system. Hugepages are taken at boot and never given
// back, so a reservation that leaves less than this can stop the installed
// system from coming up at all.
const minFreeAfterHugepages = 1 << 30

// CheckKernelArgs reports what in a kernel command line does not fit the
// machine it is about to be installed on: CPU lists that name CPUs the machine
// does not have or that isolate every CPU, and a hugepage reservation that
// leaves no memory. These arguments are computed for one machine — a saved
// config carries literal CPU lists and page counts — so a config replayed on
// other hardware is caught here, before the disk is touched, rather than at
// the first boot. It returns nothing when all is well or when the machine's
// CPUs or memory cannot be read.
func CheckKernelArgs(args []string) []string {
	var probs []string

	if online := onlineCPUs(); len(online) > 0 {
		have := map[int]bool{}
		for _, c := range online {
			have[c] = true
		}
		for _, a := range args {
			name, val, ok := strings.Cut(a, "=")
			if !ok {
				continue
			}
			switch name {
			case "isolcpus", "nohz_full", "rcu_nocbs", "irqaffinity":
			default:
				continue
			}
			// parseCPUList skips what is not a CPU number, so isolcpus'
			// leading flags (domain,nohz,managed_irq) and forms such as
			// rcu_nocbs=all fall away.
			list := parseCPUList(val)
			var absent []int
			for _, c := range list {
				if !have[c] {
					absent = append(absent, c)
				}
			}
			if len(absent) > 0 {
				probs = append(probs, fmt.Sprintf("%s names CPUs %s, but this machine has CPUs %s",
					name, formatRanges(absent), formatRanges(online)))
				continue
			}
			if name == "isolcpus" && len(list) == len(online) {
				probs = append(probs, fmt.Sprintf("isolcpus isolates every CPU (%s), leaving none for the system",
					formatRanges(online)))
			}
		}
	}

	if total := memTotalBytes(); total > 0 {
		if want := hugepageBytes(args); want > 0 && want+minFreeAfterHugepages > total {
			probs = append(probs, fmt.Sprintf("hugepages reserve %s of this machine's %s of memory, leaving less than %s for the system",
				gib(want), gib(total), gib(minFreeAfterHugepages)))
		}
	}
	return probs
}

// hugepageBytes sums the hugepage reservations on a command line. Each
// hugepages=N counts pages of the size the preceding hugepagesz= named, or of
// the default size (default_hugepagesz=, else 2M) when none did.
func hugepageBytes(args []string) uint64 {
	size := uint64(2 << 20)
	for _, a := range args {
		if v, ok := strings.CutPrefix(a, "default_hugepagesz="); ok {
			if n := parseMemSize(v); n > 0 {
				size = n
			}
		}
	}
	var total uint64
	for _, a := range args {
		if v, ok := strings.CutPrefix(a, "hugepagesz="); ok {
			if n := parseMemSize(v); n > 0 {
				size = n
			}
		} else if v, ok := strings.CutPrefix(a, "hugepages="); ok {
			if n, err := strconv.ParseUint(v, 10, 64); err == nil {
				total += n * size
			}
		}
	}
	return total
}

// parseMemSize parses a kernel memory size such as 2M or 1G (K, M and G
// suffixes, either case); 0 means it could not be read.
func parseMemSize(s string) uint64 {
	if s == "" {
		return 0
	}
	shift := 0
	switch s[len(s)-1] {
	case 'k', 'K':
		shift = 10
	case 'm', 'M':
		shift = 20
	case 'g', 'G':
		shift = 30
	}
	if shift > 0 {
		s = s[:len(s)-1]
	}
	n, err := strconv.ParseUint(s, 10, 64)
	if err != nil {
		return 0
	}
	return n << shift
}

// memTotalBytes is the machine's memory from /proc/meminfo, or 0 when it
// cannot be read.
func memTotalBytes() uint64 {
	data, err := os.ReadFile(meminfoFile)
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "MemTotal:") {
			if f := strings.Fields(line); len(f) >= 2 {
				kib, _ := strconv.ParseUint(f[1], 10, 64)
				return kib << 10
			}
		}
	}
	return 0
}

// gib renders a byte count in GiB with one decimal.
func gib(b uint64) string {
	return strconv.FormatFloat(float64(b)/(1<<30), 'f', 1, 64) + " GiB"
}

// HugepagesArgs is the kernel command line that reserves n 1 GiB hugepages,
// or "" for none.
func HugepagesArgs(n int) string {
	if n <= 0 {
		return ""
	}
	return fmt.Sprintf("default_hugepagesz=1G hugepagesz=1G hugepages=%d", n)
}

// isolationArg reports whether a kernel argument is one the isolation
// profiles compute per machine: a CPU list, or (when hugepages is set) part of
// the hugepage reservation.
func isolationArg(arg string, hugepages bool) bool {
	name, _, ok := strings.Cut(arg, "=")
	if !ok {
		return false
	}
	switch name {
	case "isolcpus", "nohz_full", "rcu_nocbs", "irqaffinity":
		return true
	case "default_hugepagesz", "hugepagesz", "hugepages":
		return hugepages
	}
	return false
}

// RefitIsolation recomputes, for the machine it runs on, the arguments an
// isolation profile derives from the hardware: the isolcpus=, nohz_full=,
// rcu_nocbs= and irqaffinity= CPU lists and, when hugepages is set, the
// 1 GiB hugepage reservation. A saved config holds these literally for the
// machine it was written on; this makes them right for the one being
// installed. Every other argument is kept, in place.
//
// It returns the refitted arguments, and the machine-specific ones as they
// were and as they are now, so the caller can say what changed. When the two
// are the same the arguments come back untouched. A machine too small to
// partition gets no CPU lists, and one with too little memory no hugepages —
// the same as the wizard would give it.
func RefitIsolation(args []string, hugepages bool) (out, was, now []string) {
	if ci := ComputeCPUIsolation(); ci.OK {
		now = strings.Fields(ci.Args)
	}
	if hugepages {
		now = append(now, strings.Fields(HugepagesArgs(HugepagesGiB()))...)
	}
	for _, a := range args {
		if isolationArg(a, hugepages) {
			was = append(was, a)
		} else {
			out = append(out, a)
		}
	}
	if strings.Join(was, " ") == strings.Join(now, " ") {
		return args, was, now
	}
	return append(out, now...), was, now
}
