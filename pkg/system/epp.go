package system

import "os"

// eppPrefPath is the sysfs EPP knob for cpu0; a variable so tests can point it
// elsewhere.
var eppPrefPath = "/sys/devices/system/cpu/cpu0/cpufreq/energy_performance_preference"

// EPPCapable reports whether the CPU exposes the Energy Performance Preference
// knob — i.e. the scaling driver runs in active (HWP/CPPC) mode. In that mode
// only the performance and powersave governors apply.
func EPPCapable() bool {
	_, err := os.Stat(eppPrefPath)
	return err == nil
}
