package system

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func slinitTarget(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, SlinitServicesDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dir, "boot"), nil, 0o644)
	return root
}

// A new system gets a random seed for its first boot, where slinit-seedrng
// looks for one, private to root, and not credited as entropy.
func TestSeedRNG(t *testing.T) {
	root := slinitTarget(t)
	if err := SeedRNG(root); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "var", "lib", "seedrng")
	seed, err := os.ReadFile(filepath.Join(dir, "seed.no-credit"))
	if err != nil || len(seed) != 32 || bytes.Equal(seed, make([]byte, 32)) {
		t.Fatalf("seed: %v, %d bytes", err, len(seed))
	}
	if st, _ := os.Stat(filepath.Join(dir, "seed.no-credit")); st.Mode().Perm() != 0o400 {
		t.Errorf("seed mode %v, want 0400", st.Mode().Perm())
	}
	if st, _ := os.Stat(dir); st.Mode().Perm() != 0o700 {
		t.Errorf("seed directory mode %v, want 0700", st.Mode().Perm())
	}
	// Never as a credited seed: a disk copied before its first boot would
	// have every copy trust the same one.
	if _, err := os.Stat(filepath.Join(dir, "seed.credit")); err == nil {
		t.Error("the seed was written as credited")
	}
	// Two installs get two seeds.
	other := slinitTarget(t)
	if err := SeedRNG(other); err != nil {
		t.Fatal(err)
	}
	if second, _ := os.ReadFile(filepath.Join(other, "var", "lib", "seedrng", "seed.no-credit")); bytes.Equal(seed, second) {
		t.Error("two installs were given the same seed")
	}
	// A seed that is there is kept: run again, it is the same one.
	if err := SeedRNG(root); err != nil {
		t.Fatal(err)
	}
	if again, _ := os.ReadFile(filepath.Join(dir, "seed.no-credit")); !bytes.Equal(seed, again) {
		t.Error("an existing seed was replaced")
	}
}

func TestSeedRNGLeavesOtherTargetsAlone(t *testing.T) {
	// Not a slinit system: nothing is written.
	root := t.TempDir()
	if err := SeedRNG(root); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "var")); err == nil {
		t.Error("a seed was written for a system that does not boot slinit")
	}
	// A credited seed an image brought with it is kept, and none added.
	root = slinitTarget(t)
	dir := filepath.Join(root, "var", "lib", "seedrng")
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "seed.credit"), []byte("from the image"), 0o400)
	if err := SeedRNG(root); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "seed.no-credit")); err == nil {
		t.Error("a second seed was added beside the image's own")
	}
	// An empty file is not a seed: it is replaced, and the directory made
	// private.
	root = slinitTarget(t)
	dir = filepath.Join(root, "var", "lib", "seedrng")
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "seed.no-credit"), nil, 0o400)
	if err := SeedRNG(root); err != nil {
		t.Fatal(err)
	}
	if seed, _ := os.ReadFile(filepath.Join(dir, "seed.no-credit")); len(seed) != 32 {
		t.Errorf("an empty seed was kept: %d bytes", len(seed))
	}
	if st, _ := os.Stat(dir); st.Mode().Perm() != 0o700 {
		t.Errorf("seed directory mode %v, want 0700", st.Mode().Perm())
	}
	// What cannot be written is reported.
	root = slinitTarget(t)
	blockDir(t, root, "var", "lib")
	if err := SeedRNG(root); err == nil || !strings.Contains(err.Error(), "system:") {
		t.Errorf("an unwritable seed directory: %v", err)
	}
	root = slinitTarget(t)
	blockLink(t, root, "var", "lib", "seedrng", "seed.no-credit")
	if err := SeedRNG(root); err == nil || !strings.Contains(err.Error(), "write random seed") {
		t.Errorf("an unwritable seed: %v", err)
	}
}
