package system

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fitMachine stubs a machine with the given online CPUs and memory.
func fitMachine(t *testing.T, online string, memKiB string) {
	t.Helper()
	savedC, savedM := cpuSysDir, meminfoFile
	t.Cleanup(func() { cpuSysDir, meminfoFile = savedC, savedM })
	dir := t.TempDir()
	cpuSysDir = dir
	meminfoFile = filepath.Join(dir, "meminfo")
	os.WriteFile(filepath.Join(dir, "online"), []byte(online+"\n"), 0o644)
	os.WriteFile(meminfoFile, []byte("MemTotal:       "+memKiB+" kB\n"), 0o644)
}

func TestCheckKernelArgsAcceptsWhatFits(t *testing.T) {
	fitMachine(t, "0-15", "33554432") // 16 CPUs, 32 GiB
	args := strings.Fields("quiet isolcpus=domain,nohz,managed_irq,2-15 nohz_full=2-15 " +
		"rcu_nocbs=2-15 irqaffinity=0-1 default_hugepagesz=1G hugepagesz=1G hugepages=7 console=ttyS0,115200")
	if probs := CheckKernelArgs(args); len(probs) != 0 {
		t.Errorf("fitting arguments reported: %v", probs)
	}
	// Forms that are not CPU numbers are not judged.
	if probs := CheckKernelArgs([]string{"rcu_nocbs=all", "quiet"}); len(probs) != 0 {
		t.Errorf("rcu_nocbs=all reported: %v", probs)
	}
}

func TestCheckKernelArgsCatchesAnotherMachinesConfig(t *testing.T) {
	// A config saved on a 128-CPU, large-memory server, replayed on a
	// 16-CPU machine with 8 GiB.
	fitMachine(t, "0-15", "8388608")
	args := strings.Fields("isolcpus=domain,nohz,managed_irq,4-63,68-127 nohz_full=4-63,68-127 " +
		"irqaffinity=0-3,64-67 default_hugepagesz=1G hugepagesz=1G hugepages=40")
	probs := strings.Join(CheckKernelArgs(args), "\n")
	for _, want := range []string{
		"isolcpus names CPUs 16-63,68-127, but this machine has CPUs 0-15",
		"nohz_full names CPUs 16-63,68-127",
		"irqaffinity names CPUs 64-67",
		"hugepages reserve 40.0 GiB of this machine's 8.0 GiB",
	} {
		if !strings.Contains(probs, want) {
			t.Errorf("missing %q in:\n%s", want, probs)
		}
	}
}

func TestCheckKernelArgsIsolatingEveryCPU(t *testing.T) {
	fitMachine(t, "0-3", "8388608")
	probs := CheckKernelArgs([]string{"isolcpus=0-3"})
	if len(probs) != 1 || !strings.Contains(probs[0], "isolates every CPU") {
		t.Errorf("isolcpus=0-3 on 4 CPUs: %v", probs)
	}
}

func TestHugepageBytes(t *testing.T) {
	for args, want := range map[string]uint64{
		"hugepages=512":                                          512 * (2 << 20), // default size 2M
		"default_hugepagesz=1G hugepages=4":                      4 << 30,
		"hugepagesz=1G hugepages=10 hugepagesz=2M hugepages=0":   10 << 30,
		"hugepagesz=2M hugepages=1024 hugepagesz=1G hugepages=2": 1024*(2<<20) + 2<<30,
		"quiet": 0,
	} {
		if got := hugepageBytes(strings.Fields(args)); got != want {
			t.Errorf("hugepageBytes(%q) = %d, want %d", args, got, want)
		}
	}
	// The reservation has to leave memory for the system.
	fitMachine(t, "0-3", "8388608") // 8 GiB
	if probs := CheckKernelArgs(strings.Fields("hugepagesz=1G hugepages=7")); len(probs) != 0 {
		t.Errorf("7 of 8 GiB leaves 1 GiB and should pass: %v", probs)
	}
	if probs := CheckKernelArgs(strings.Fields("hugepagesz=1G hugepages=8")); len(probs) != 1 {
		t.Errorf("8 of 8 GiB should be refused: %v", probs)
	}
}

func TestCheckKernelArgsSilentWhenTheMachineCannotBeRead(t *testing.T) {
	savedC, savedM := cpuSysDir, meminfoFile
	defer func() { cpuSysDir, meminfoFile = savedC, savedM }()
	cpuSysDir, meminfoFile = t.TempDir(), filepath.Join(t.TempDir(), "none")
	if probs := CheckKernelArgs(strings.Fields("isolcpus=1-999 hugepagesz=1G hugepages=999")); len(probs) != 0 {
		t.Errorf("unreadable machine should not be judged: %v", probs)
	}
}

// refitMachine is fitMachine with no NUMA topology, so the isolation is the
// flat one: without sibling files every CPU is its own core.
func refitMachine(t *testing.T, online, memKiB string) {
	t.Helper()
	fitMachine(t, online, memKiB)
	saved := nodeSysDir
	t.Cleanup(func() { nodeSysDir = saved })
	nodeSysDir = filepath.Join(t.TempDir(), "no-numa")
}

func TestRefitIsolationForAnotherMachinesConfig(t *testing.T) {
	refitMachine(t, "0-15", "33554432") // 16 CPUs, 32 GiB
	args := strings.Fields("preempt=full idle=poll isolcpus=domain,nohz,managed_irq,4-63,68-127 " +
		"nohz_full=4-63,68-127 rcu_nocbs=4-63,68-127 irqaffinity=0-3,64-67 amd_pstate=active " +
		"default_hugepagesz=1G hugepagesz=1G hugepages=40 console=ttyS0,115200")

	out, was, now := RefitIsolation(args, true)
	if len(was) != 7 || was[0] != "isolcpus=domain,nohz,managed_irq,4-63,68-127" {
		t.Errorf("was = %v", was)
	}
	want := "isolcpus=domain,nohz,managed_irq,1-15 nohz_full=1-15 rcu_nocbs=1-15 irqaffinity=0 " +
		"default_hugepagesz=1G hugepagesz=1G hugepages=8"
	if got := strings.Join(now, " "); got != want {
		t.Errorf("now = %q\nwant  %q", got, want)
	}
	// Everything that is not machine-specific is kept, in order, ahead of
	// the recomputed arguments.
	if got := strings.Join(out, " "); got != "preempt=full idle=poll amd_pstate=active console=ttyS0,115200 "+want {
		t.Errorf("out = %q", got)
	}
	// What comes out fits the machine it was computed for.
	if probs := CheckKernelArgs(out); len(probs) != 0 {
		t.Errorf("refitted arguments do not fit: %v", probs)
	}

	// Refitting again changes nothing and hands the same arguments back.
	again, was2, now2 := RefitIsolation(out, true)
	if strings.Join(was2, " ") != strings.Join(now2, " ") || strings.Join(again, " ") != strings.Join(out, " ") {
		t.Errorf("refit is not stable: %v -> %v", out, again)
	}
}

func TestRefitIsolationLeavesHugepagesToProfilesWithout(t *testing.T) {
	refitMachine(t, "0-15", "33554432")
	// A profile that does not reserve hugepages: an operator's own
	// reservation is theirs, only the CPU lists are recomputed.
	out, _, _ := RefitIsolation(strings.Fields("isolcpus=1-99 hugepagesz=2M hugepages=512"), false)
	got := strings.Join(out, " ")
	if !strings.Contains(got, "hugepagesz=2M hugepages=512") || !strings.Contains(got, "isolcpus=domain,nohz,managed_irq,1-15") ||
		strings.Contains(got, "1-99") {
		t.Errorf("out = %q", got)
	}
}

func TestRefitIsolationOnAMachineTooSmall(t *testing.T) {
	// Two CPUs and 4 GiB: nothing to isolate and no room for hugepages, so
	// the foreign lists are dropped and nothing replaces them.
	refitMachine(t, "0-1", "4194304")
	out, was, now := RefitIsolation(strings.Fields(
		"quiet isolcpus=2-15 nohz_full=2-15 rcu_nocbs=2-15 irqaffinity=0-1 hugepagesz=1G hugepages=7"), true)
	if len(was) != 6 || len(now) != 0 {
		t.Errorf("was = %v, now = %v", was, now)
	}
	if got := strings.Join(out, " "); got != "quiet" {
		t.Errorf("out = %q, want only the machine-independent argument", got)
	}
}
