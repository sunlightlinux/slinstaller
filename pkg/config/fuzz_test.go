package config

import (
	"os"
	"path/filepath"
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
		if pl, err := c.Plan(); err == nil {
			_, _ = pl.MkfsCommands()
			_ = pl.TableCommands()
			_ = pl.FixedBytesByDisk()
		}
	})
}
