package system

import (
	"os"
	"strings"
)

// eppPrefPath and eppAvailPath are the sysfs EPP knobs for cpu0; variables so
// tests can point them elsewhere.
var (
	eppPrefPath  = "/sys/devices/system/cpu/cpu0/cpufreq/energy_performance_preference"
	eppAvailPath = "/sys/devices/system/cpu/cpu0/cpufreq/energy_performance_available_preferences"
)

// EPPCapable reports whether the CPU exposes the Energy Performance Preference
// knob — i.e. the scaling driver runs in active (HWP/CPPC) mode. In that mode
// only the performance and powersave governors apply; powersave is further
// tuned by the EPP preference.
func EPPCapable() bool {
	_, err := os.Stat(eppPrefPath)
	return err == nil
}

// EPPPreferences returns the settable Energy Performance Preference values from
// sysfs (e.g. default, performance, balance_performance, balance_power, power),
// dropping "custom" (which needs a raw numeric value instead of a name).
func EPPPreferences() []string {
	data, err := os.ReadFile(eppAvailPath)
	if err != nil {
		return nil
	}
	var out []string
	for _, f := range strings.Fields(string(data)) {
		if f == "custom" {
			continue
		}
		out = append(out, f)
	}
	return out
}
