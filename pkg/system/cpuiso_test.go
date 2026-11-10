package system

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseAndFormatCPUList(t *testing.T) {
	got := parseCPUList("0-3,8,10-11")
	want := []int{0, 1, 2, 3, 8, 10, 11}
	if len(got) != len(want) {
		t.Fatalf("parseCPUList = %v, want %v", got, want)
	}
	if s := formatRanges(got); s != "0-3,8,10-11" {
		t.Errorf("formatRanges = %q, want 0-3,8,10-11", s)
	}
	if s := formatRanges([]int{2, 3, 4, 5, 6, 7}); s != "2-7" {
		t.Errorf("formatRanges contiguous = %q, want 2-7", s)
	}
}

func TestComputeCPUIsolation(t *testing.T) {
	saved := cpuSysDir
	defer func() { cpuSysDir = saved }()
	dir := t.TempDir()
	cpuSysDir = dir
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
	if n := HugepagesGiB(); n < 1 {
		t.Errorf("HugepagesGiB for 32G = %d, want >=1", n)
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
