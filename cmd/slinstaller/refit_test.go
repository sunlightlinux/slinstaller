package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/sunlightlinux/slinstaller/pkg/config"
	"github.com/sunlightlinux/slinstaller/pkg/system"
)

// foreignArgs is what a Telco/RAN config saved on a large two-socket server
// carries: CPU lists and a hugepage count no ordinary machine matches.
const foreignArgs = "preempt=full idle=poll isolcpus=domain,nohz,managed_irq,4-4000,4068-8127 " +
	"nohz_full=4-4000,4068-8127 rcu_nocbs=4-4000,4068-8127 irqaffinity=0-3,4064-4067 " +
	"default_hugepagesz=1G hugepagesz=1G hugepages=100000 console=ttyS0,115200"

func cfgWith(profile, args string) *config.Config {
	return &config.Config{
		KernelProfile:   profile,
		KernelArguments: &config.KernelArguments{Add: strings.Fields(args)},
	}
}

func TestRefitLeavesTheOperatorsOwnArgumentsAlone(t *testing.T) {
	// Custom, no profile, an unknown name, and a profile that computes
	// nothing from the hardware: the arguments are installed as written.
	for _, profile := range []string{"Custom", "", "No Such Profile", "Server"} {
		cfg := cfgWith(profile, foreignArgs)
		var log bytes.Buffer
		refitKernelArgs(cfg, &log)
		if got := strings.Join(cfg.KernelArguments.Add, " "); got != foreignArgs {
			t.Errorf("kernel-profile %q: arguments changed to %q", profile, got)
		}
		if log.Len() != 0 {
			t.Errorf("kernel-profile %q: reported a change: %s", profile, log.String())
		}
	}
}

func TestRefitRecomputesAnIsolationProfile(t *testing.T) {
	cfg := cfgWith("Telco/RAN", foreignArgs)
	var log bytes.Buffer
	refitKernelArgs(cfg, &log)
	add := cfg.KernelArguments.Add
	got := strings.Join(add, " ")

	// The other machine's lists and reservation are gone …
	for _, gone := range []string{"4-4000", "4064-4067", "hugepages=100000"} {
		if strings.Contains(got, gone) {
			t.Errorf("foreign value %q survived: %s", gone, got)
		}
	}
	// … everything else is kept …
	for _, kept := range []string{"preempt=full", "idle=poll", "console=ttyS0,115200"} {
		if !strings.Contains(got, kept) {
			t.Errorf("%q was dropped: %s", kept, got)
		}
	}
	// … and what is installed fits the machine the test runs on, whatever
	// it is.
	if probs := system.CheckKernelArgs(add); len(probs) != 0 {
		t.Errorf("refitted arguments do not fit this machine: %v", probs)
	}
	if out := log.String(); !strings.Contains(out, "kernel-profile Telco/RAN: kernel arguments adapted to this machine") ||
		!strings.Contains(out, "was: isolcpus=domain,nohz,managed_irq,4-4000,4068-8127") {
		t.Errorf("the change was not reported:\n%s", out)
	}

	// Already right for this machine: nothing changes and nothing is said.
	before := got
	log.Reset()
	refitKernelArgs(cfg, &log)
	if got := strings.Join(cfg.KernelArguments.Add, " "); got != before || log.Len() != 0 {
		t.Errorf("second refit changed %q to %q, or reported: %s", before, got, log.String())
	}
}

func TestRefitWithoutKernelArguments(t *testing.T) {
	// An isolation profile named with no arguments at all still gets this
	// machine's isolation, when it is large enough to have one.
	cfg := &config.Config{KernelProfile: "Realtime"}
	var log bytes.Buffer
	refitKernelArgs(cfg, &log)
	if cfg.KernelArguments != nil {
		if probs := system.CheckKernelArgs(cfg.KernelArguments.Add); len(probs) != 0 {
			t.Errorf("computed arguments do not fit: %v", probs)
		}
		for _, a := range cfg.KernelArguments.Add {
			if strings.HasPrefix(a, "hugepages") {
				t.Errorf("Realtime does not reserve hugepages, got %q", a)
			}
		}
	}
}
