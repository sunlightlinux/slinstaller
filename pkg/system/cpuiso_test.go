package system

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestParseAndFormatCPUList(t *testing.T) {
	// The ids themselves, sorted, once each; a range with a bound that is
	// not a number is passed over, not read as starting at 0.
	for list, want := range map[string]string{
		"0-3,8,10-11":   "[0 1 2 3 8 10 11]",
		"8, 1-2 ,1,,":   "[1 2 8]",
		"x-3,5,4-y,z,7": "[5 7]",
		"":              "[]",
	} {
		if got := fmt.Sprint(parseCPUList(list)); got != want {
			t.Errorf("parseCPUList(%q) = %s, want %s", list, got, want)
		}
	}
	got := parseCPUList("0-3,8,10-11")
	if s := formatRanges(got); s != "0-3,8,10-11" {
		t.Errorf("formatRanges = %q, want 0-3,8,10-11", s)
	}
	if s := formatRanges([]int{2, 3, 4, 5, 6, 7}); s != "2-7" {
		t.Errorf("formatRanges contiguous = %q, want 2-7", s)
	}
}

func TestComputeCPUIsolation(t *testing.T) {
	saved, savedN := cpuSysDir, nodeSysDir
	defer func() { cpuSysDir, nodeSysDir = saved, savedN }()
	dir := t.TempDir()
	cpuSysDir = dir
	nodeSysDir = filepath.Join(dir, "no-numa") // force single-pool fallback
	os.WriteFile(filepath.Join(dir, "online"), []byte("0-15\n"), 0o644)
	os.MkdirAll(filepath.Join(dir, "cpu0", "topology"), 0o755)
	os.WriteFile(filepath.Join(dir, "cpu0", "topology", "thread_siblings_list"), []byte("0,1\n"), 0o644)

	ci := ComputeCPUIsolation()
	if !ci.OK || ci.Total != 16 || ci.Housekeeping != "0-1" || ci.Isolated != "2-15" {
		t.Fatalf("isolation wrong: %+v", ci)
	}
	for _, want := range []string{"isolcpus=domain,nohz,managed_irq,2-15", "nohz_full=2-15", "rcu_nocbs=2-15", "irqaffinity=0-1"} {
		if !contains([]string{ci.Args}, want) && !containsSub(ci.Args, want) {
			t.Errorf("args missing %q: %s", want, ci.Args)
		}
	}

	// Three CPUs are enough: one kept for the system, two isolated.
	os.WriteFile(filepath.Join(dir, "online"), []byte("0-2\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "cpu0", "topology", "thread_siblings_list"), []byte("0\n"), 0o644)
	if ci := ComputeCPUIsolation(); !ci.OK || ci.Isolated != "1-2" {
		t.Errorf("3 CPUs: %+v", ci)
	}
	// Too few CPUs: not OK.
	os.WriteFile(filepath.Join(dir, "online"), []byte("0-1\n"), 0o644)
	if ComputeCPUIsolation().OK {
		t.Error("2 CPUs should not isolate")
	}
}

func TestHugepagesGiB(t *testing.T) {
	saved := meminfoFile
	defer func() { meminfoFile = saved }()
	f := filepath.Join(t.TempDir(), "meminfo")
	os.WriteFile(f, []byte("MemTotal:       32000000 kB\nMemFree: 1 kB\n"), 0o644)
	meminfoFile = f
	if n := HugepagesGiB(); n != 7 { // 30 GiB: a quarter
		t.Errorf("HugepagesGiB for 30G = %d, want 7", n)
	}
	// Where a quarter would leave the system under 4 GiB, what leaves it 4;
	// one page is still worth reserving.
	for kib, want := range map[string]int{"5242880": 1, "6291456": 1, "7340032": 1} {
		os.WriteFile(f, []byte("MemTotal:       "+kib+" kB\n"), 0o644)
		if n := HugepagesGiB(); n != want {
			t.Errorf("HugepagesGiB for %s kB = %d, want %d", kib, n, want)
		}
	}
	// Small machine: 0.
	os.WriteFile(f, []byte("MemTotal:       4000000 kB\n"), 0o644)
	if n := HugepagesGiB(); n != 0 {
		t.Errorf("HugepagesGiB for 4G = %d, want 0", n)
	}
}

func containsSub(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// writeTopology lays out a fake sysfs for n physical cores with the given SMT
// width, enumerated thread-major (0..n-1 = thread0, n..2n-1 = thread1, ...).
func writeTopology(t *testing.T, cores, smt int) {
	t.Helper()
	saved, savedN := cpuSysDir, nodeSysDir
	t.Cleanup(func() { cpuSysDir, nodeSysDir = saved, savedN })
	dir := t.TempDir()
	cpuSysDir = dir
	nodeSysDir = filepath.Join(dir, "no-numa") // single-pool unless a test sets nodes
	total := cores * smt
	os.WriteFile(filepath.Join(dir, "online"), []byte(fmt.Sprintf("0-%d\n", total-1)), 0o644)
	for c := 0; c < cores; c++ {
		var sib []string
		for s := 0; s < smt; s++ {
			sib = append(sib, strconv.Itoa(c+s*cores))
		}
		list := strings.Join(sib, ",")
		for s := 0; s < smt; s++ {
			cpu := c + s*cores
			d := filepath.Join(dir, fmt.Sprintf("cpu%d", cpu), "topology")
			os.MkdirAll(d, 0o755)
			os.WriteFile(filepath.Join(d, "thread_siblings_list"), []byte(list+"\n"), 0o644)
		}
	}
}

func TestCPUIsolationScales(t *testing.T) {
	// 64 physical cores, SMT2 = 128 logical: reserve 4 whole cores (8 logical),
	// a common telco housekeeping count, kept as sibling groups.
	writeTopology(t, 64, 2)
	ci := ComputeCPUIsolation()
	if !ci.OK || ci.Total != 128 {
		t.Fatalf("got %+v", ci)
	}
	if ci.Housekeeping != "0-3,64-67" {
		t.Errorf("housekeeping = %q, want 0-3,64-67", ci.Housekeeping)
	}
	if ci.Isolated != "4-63,68-127" {
		t.Errorf("isolated = %q, want 4-63,68-127", ci.Isolated)
	}

	// 4 cores, SMT2 = 8 logical: minimum one whole core reserved.
	writeTopology(t, 4, 2)
	ci = ComputeCPUIsolation()
	if ci.Housekeeping != "0,4" || ci.Isolated != "1-3,5-7" {
		t.Errorf("small machine hk=%q iso=%q, want 0,4 / 1-3,5-7", ci.Housekeeping, ci.Isolated)
	}
}

func TestCPUIsolationNUMAAware(t *testing.T) {
	saved, savedN := cpuSysDir, nodeSysDir
	defer func() { cpuSysDir, nodeSysDir = saved, savedN }()
	cdir, ndir := t.TempDir(), t.TempDir()
	cpuSysDir, nodeSysDir = cdir, ndir
	// 16 CPUs, no SMT, two NUMA nodes: 0-7 and 8-15.
	os.WriteFile(filepath.Join(cdir, "online"), []byte("0-15\n"), 0o644)
	os.MkdirAll(filepath.Join(ndir, "node0"), 0o755)
	os.MkdirAll(filepath.Join(ndir, "node1"), 0o755)
	os.WriteFile(filepath.Join(ndir, "node0", "cpulist"), []byte("0-7\n"), 0o644)
	os.WriteFile(filepath.Join(ndir, "node1", "cpulist"), []byte("8-15\n"), 0o644)

	ci := ComputeCPUIsolation()
	// One housekeeping core reserved on each node: 0 and 8.
	if ci.Housekeeping != "0,8" {
		t.Errorf("housekeeping = %q, want 0,8 (one per node)", ci.Housekeeping)
	}
	if ci.Isolated != "1-7,9-15" {
		t.Errorf("isolated = %q, want 1-7,9-15", ci.Isolated)
	}
}
