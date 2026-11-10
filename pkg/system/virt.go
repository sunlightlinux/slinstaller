package system

import (
	"os"
	"strings"
)

// dmiDir is where the firmware names the machine; a variable for the tests.
var dmiDir = "/sys/class/dmi/id"

// VirtualMachine reports whether this runs under a hypervisor, and its name
// when the firmware gives one ("QEMU", "VMware, Inc.", ...). The CPU's own
// hypervisor flag decides; the name only labels it, since bare metal fills the
// same DMI fields with its maker.
func VirtualMachine() (name string, ok bool) {
	data, err := os.ReadFile(cpuinfoFile)
	if err != nil {
		return "", false
	}
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(line, "flags") {
			continue
		}
		_, flags, _ := strings.Cut(line, ":")
		for _, f := range strings.Fields(flags) {
			if f == "hypervisor" {
				return dmiName(), true
			}
		}
		return "", false
	}
	return "", false
}

func dmiName() string {
	for _, f := range []string{"sys_vendor", "product_name"} {
		b, err := os.ReadFile(dmiDir + "/" + f)
		if err != nil {
			continue
		}
		if v := strings.TrimSpace(string(b)); v != "" {
			return v
		}
	}
	return ""
}
