package system

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// The detection helpers read /proc and /sys, which differ between kernels,
// containers and virtual machines. These tests feed them the odd shapes: a
// missing file, a line that is not there, an entry that is not what its name
// suggests.

func TestCPUVendorAndVMWithoutCpuinfo(t *testing.T) {
	saved := cpuinfoFile
	defer func() { cpuinfoFile = saved }()

	cpuinfoFile = filepath.Join(t.TempDir(), "none")
	if v := CPUVendor(); v != "" {
		t.Errorf("vendor without /proc/cpuinfo = %q", v)
	}
	if name, ok := VirtualMachine(); ok || name != "" {
		t.Errorf("VM without /proc/cpuinfo = %q, %v", name, ok)
	}

	// A cpuinfo with neither line (an architecture that prints neither).
	f := filepath.Join(t.TempDir(), "cpuinfo")
	os.WriteFile(f, []byte("processor\t: 0\nBogoMIPS\t: 50.00\n"), 0o644)
	cpuinfoFile = f
	if v := CPUVendor(); v != "" {
		t.Errorf("vendor without a vendor_id line = %q", v)
	}
	if _, ok := VirtualMachine(); ok {
		t.Error("VM reported without a flags line")
	}
}

func TestDetectConsolesWithNothingToRead(t *testing.T) {
	savedC, savedA := procCmdline, consoleActive
	defer func() { procCmdline, consoleActive = savedC, savedA }()
	procCmdline = filepath.Join(t.TempDir(), "none")
	consoleActive = filepath.Join(t.TempDir(), "none")
	if got := DetectConsoles(); got != nil {
		t.Errorf("consoles with nothing to read = %v", got)
	}
}

func TestIsolationWithoutSysfsFallsBackToTheRuntime(t *testing.T) {
	savedC, savedN := cpuSysDir, nodeSysDir
	defer func() { cpuSysDir, nodeSysDir = savedC, savedN }()
	cpuSysDir = t.TempDir() // no "online" file
	nodeSysDir = filepath.Join(t.TempDir(), "none")
	if ci := ComputeCPUIsolation(); ci.Total != runtime.NumCPU() {
		t.Errorf("Total = %d, want the runtime's %d", ci.Total, runtime.NumCPU())
	}
}

func TestNUMANodesIgnoresWhatIsNotANode(t *testing.T) {
	savedN := nodeSysDir
	defer func() { nodeSysDir = savedN }()
	online := []int{0, 1, 2, 3}
	same := func(got [][]int) bool {
		return len(got) == 1 && len(got[0]) == len(online)
	}

	// /sys/devices/system/node also holds has_cpu, possible, power, …; a
	// node directory may lack cpulist, and a memory-only node lists no CPU.
	// None of those is a group of CPUs.
	dir := t.TempDir()
	nodeSysDir = dir
	os.WriteFile(filepath.Join(dir, "possible"), []byte("0\n"), 0o644)
	os.MkdirAll(filepath.Join(dir, "power"), 0o755)
	os.MkdirAll(filepath.Join(dir, "nodeX"), 0o755)                           // not a number
	os.MkdirAll(filepath.Join(dir, "node7"), 0o755)                           // no cpulist
	os.MkdirAll(filepath.Join(dir, "node8"), 0o755)                           // memory only
	os.WriteFile(filepath.Join(dir, "node8", "cpulist"), []byte("\n"), 0o644) //
	os.MkdirAll(filepath.Join(dir, "node9"), 0o755)                           // only offline CPUs
	os.WriteFile(filepath.Join(dir, "node9", "cpulist"), []byte("64-127\n"), 0o644)
	if got := numaNodes(online); !same(got) {
		t.Errorf("no usable node should fall back to one pool, got %v", got)
	}

	// One real node beside them is used, and only it.
	os.MkdirAll(filepath.Join(dir, "node0"), 0o755)
	os.WriteFile(filepath.Join(dir, "node0", "cpulist"), []byte("0-1\n"), 0o644)
	if got := numaNodes(online); len(got) != 1 || len(got[0]) != 2 {
		t.Errorf("nodes = %v, want just node0's CPUs 0-1", got)
	}
}

func TestPhysicalCoresWhenSiblingsAreOffline(t *testing.T) {
	saved := cpuSysDir
	defer func() { cpuSysDir = saved }()
	dir := t.TempDir()
	cpuSysDir = dir
	// cpu2's sibling list names only CPUs that are not online (it was read
	// while its siblings were being hot-unplugged): it is still a core.
	d := filepath.Join(dir, "cpu2", "topology")
	os.MkdirAll(d, 0o755)
	os.WriteFile(filepath.Join(d, "thread_siblings_list"), []byte("8-9\n"), 0o644)
	cores := physicalCores([]int{2})
	if len(cores) != 1 || len(cores[0]) != 1 || cores[0][0] != 2 {
		t.Errorf("cores = %v, want cpu2 on its own", cores)
	}
}

func TestCPUListParsingAndFormatting(t *testing.T) {
	// Stray commas, spaces and unparsable ranges are skipped, not fatal.
	got := parseCPUList("0-1,, 3 ,a-b,5-x,7")
	if s := formatRanges(got); s != "0-1,3,7" {
		t.Errorf("parseCPUList = %v (%q)", got, s)
	}
	if s := formatRanges(nil); s != "" {
		t.Errorf("formatRanges(nil) = %q", s)
	}
}

func TestMemorySizesAndMeminfo(t *testing.T) {
	for in, want := range map[string]uint64{
		"2M": 2 << 20, "1G": 1 << 30, "1g": 1 << 30, "512k": 512 << 10, "64K": 64 << 10,
		"4096": 4096, // no suffix: bytes
		"":     0, "G": 0, "twoM": 0, "-1G": 0,
	} {
		if got := parseMemSize(in); got != want {
			t.Errorf("parseMemSize(%q) = %d, want %d", in, got, want)
		}
	}

	saved := meminfoFile
	defer func() { meminfoFile = saved }()
	f := filepath.Join(t.TempDir(), "meminfo")
	// A meminfo without MemTotal reads as unknown, so nothing is judged.
	os.WriteFile(f, []byte("MemFree:  100 kB\nBuffers:  1 kB\n"), 0o644)
	meminfoFile = f
	if got := memTotalBytes(); got != 0 {
		t.Errorf("memTotalBytes without MemTotal = %d", got)
	}
	if n := HugepagesGiB(); n != 0 {
		t.Errorf("hugepages with unknown memory = %d", n)
	}
	if probs := CheckKernelArgs(strings.Fields("hugepagesz=1G hugepages=999")); len(probs) != 0 {
		t.Errorf("unknown memory should not be judged: %v", probs)
	}
}
