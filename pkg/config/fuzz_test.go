package config

import (
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// FuzzLoadValidatePlan feeds arbitrary files through what every install
// does first — load, validate, plan — which must refuse a bad config with
// an error, never a panic: a panic here is a crashed installer with the
// operator's answers lost, or worse, a check skipped.
func FuzzLoadValidatePlan(f *testing.F) {
	seeds, _ := filepath.Glob("testdata/*.yaml")
	more, _ := filepath.Glob("../../demo/vm/*.yaml")
	for _, p := range append(seeds, more...) {
		if data, err := os.ReadFile(p); err == nil {
			f.Add(data)
		}
	}
	// An array named out of /dev/md, with every check switched off.
	f.Add([]byte(`targetMedia:
- name: vda
  type: disk
  children:
  - { name: vda1, type: part, fstype: vfat, mountpoint: /boot/efi, size: 512M }
  - { name: vda2, type: raid, raid: ../sdz, size: rest }
- name: vdb
  type: disk
  children:
  - { name: vdb1, type: raid, raid: ../sdz, size: rest }
raid:
- { name: ../sdz, level: 1, fstype: ext4, mountpoint: / }
skipValidationAll: true
`))
	dir := f.TempDir()
	f.Fuzz(func(t *testing.T, data []byte) {
		path := filepath.Join(dir, "c.yaml")
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Skip()
		}
		c, err := Load(path)
		if err != nil {
			return
		}
		_ = c.Validate()
		checkPlan(t, c)
		// skipValidationAll turns Validate off; what the plan touches
		// must still stay on the disks it names.
		c.SkipValidationAll = true
		checkPlan(t, c)
	})
}

// checkPlan checks a plan points every command at a device by a clean
// path (no "..": /dev/md/../sdz is /dev/sdz) and partitions only the disks
// it lists — the disks the confirmation names.
func checkPlan(t *testing.T, c *Config) {
	t.Helper()
	pl, err := c.Plan()
	if err != nil {
		return
	}
	mkfs, _ := pl.MkfsCommands()
	table := pl.TableCommands()
	_ = pl.FixedBytesByDisk()
	disks := append(append([]string{}, pl.DiskList()...), pl.RaidDisks...)
	for _, cmd := range append(mkfs, table...) {
		for _, a := range cmd {
			if strings.HasPrefix(a, "/dev/") && path.Clean(a) != a {
				t.Errorf("%q: device path %s is not clean", cmd, a)
			}
		}
		if cmd[0] == "parted" || cmd[0] == "partprobe" {
			named := false
			for _, a := range cmd {
				named = named || slices.Contains(disks, a)
			}
			if !named {
				t.Errorf("%q: on no disk the plan lists (%q)", cmd, disks)
			}
		}
	}
}
