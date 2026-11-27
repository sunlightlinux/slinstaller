package config

import (
	"math"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
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

// FuzzJSONDescriptor checks --json-yaml on any descriptor: it converts or
// refuses, never panics, and what it writes is a config that loads, and
// loads back to itself — saved again it is the same file and plans the
// same, its aliases resolved — so the YAML an operator is handed is the config converted.
func FuzzJSONDescriptor(f *testing.F) {
	f.Add([]byte(descriptor))
	f.Add([]byte(`{"DestinationType":"physical","PartitionLayout":[{"disk":"../sdz","partition":1,"size":"rest"}],"Hostname":"a\nb: c"}`))
	dir := f.TempDir()
	f.Fuzz(func(t *testing.T, data []byte) {
		in := filepath.Join(dir, "d.json")
		if err := os.WriteFile(in, data, 0o600); err != nil {
			t.Skip()
		}
		c, _, err := FromJSONDescriptor(in)
		if err != nil {
			return
		}
		first, second := filepath.Join(dir, "1.yaml"), filepath.Join(dir, "2.yaml")
		if err := Save(first, c); err != nil {
			return
		}
		c1, err := Load(first)
		if err != nil {
			t.Fatalf("the converted config does not load: %v\n%s", err, read(first))
		}
		if err := Save(second, c1); err != nil {
			t.Fatal(err)
		}
		c2, err := Load(second)
		if err != nil {
			t.Fatalf("saved again, it does not load: %v", err)
		}
		third := filepath.Join(dir, "3.yaml")
		if err := Save(third, c2); err != nil {
			t.Fatal(err)
		}
		// The first file names disks by alias (${sda}); loading resolves
		// them, so it is the second and third that must agree.
		if a, b := read(second), read(third); a != b {
			t.Errorf("not the same after a round trip:\n%s\n---\n%s", a, b)
		}
		p1, e1 := c1.Plan()
		p2, e2 := c2.Plan()
		if (e1 == nil) != (e2 == nil) || e1 == nil && !slices.Equal(flat(p1.TableCommands()), flat(p2.TableCommands())) {
			t.Errorf("plans differ: %v / %v", e1, e2)
		}
	})
}

func read(path string) string {
	data, _ := os.ReadFile(path)
	return string(data)
}

func flat(cmds [][]string) []string {
	var out []string
	for _, c := range cmds {
		out = append(out, strings.Join(c, " "))
	}
	return out
}

// FuzzParseSize checks clr-installer's size grammar is read without a
// panic ("B" alone once sliced past its start) and that a size taken is
// the number times its unit: K, M, G, T, P with an optional B or iB, any
// case, all powers of 1024, or bytes.
func FuzzParseSize(f *testing.F) {
	for _, s := range []string{"512M", "150MiB", "2.6GiB", "10GB", "1P", "157286400", "512B", "B", "iB", "kib", "0", "1e3", "-1G", ""} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		got, err := parseSize(s)
		if err != nil {
			return
		}
		num, mult := strings.ToLower(strings.TrimSpace(s)), 1.0
		num = strings.TrimSuffix(strings.TrimSuffix(num, "ib"), "b")
		if num != "" {
			if i := strings.IndexByte("kmgtp", num[len(num)-1]); i >= 0 {
				num, mult = num[:len(num)-1], math.Pow(1024, float64(i+1))
			}
		}
		v, perr := strconv.ParseFloat(num, 64)
		if perr != nil {
			t.Fatalf("%q taken as %d, but %q is not a number", s, got, num)
		}
		if want := math.Round(v * mult); math.Abs(float64(got)-want) > want/1e12+1 {
			t.Errorf("%q = %d, want %.0f", s, got, want)
		}
	})
}
