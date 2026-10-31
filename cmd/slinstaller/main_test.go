package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPresentRepos(t *testing.T) {
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "x86_64-repodata"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	notRepo := t.TempDir() // a directory with no index
	missing := filepath.Join(t.TempDir(), "absent")

	got := presentRepos([]string{missing, " " + repo + " ", "", notRepo})
	if len(got) != 1 || got[0] != repo {
		t.Errorf("presentRepos = %q, want only %q", got, repo)
	}
}

func TestCheckLocalPackages(t *testing.T) {
	withSlinit := []string{"sunlight-base", "grub-x86_64-efi"}
	runitOnly := []string{"base-system", "grub-x86_64-efi"}

	// The case this exists for: slinit asked for, no repository to take
	// it from. It must fail here, before the disk is touched.
	err := checkLocalPackages(withSlinit, nil, defaultLocalRepo)
	if err == nil {
		t.Fatal("sunlight-base with no local repository: got nil, want an error")
	}
	if !strings.Contains(err.Error(), defaultLocalRepo) || !strings.Contains(err.Error(), "--local-repo") {
		t.Errorf("error should name the path tried and the flag: %v", err)
	}

	if err := checkLocalPackages(withSlinit, []string{"/repo"}, defaultLocalRepo); err != nil {
		t.Errorf("with a local repository: %v", err)
	}
	if err := checkLocalPackages(runitOnly, nil, defaultLocalRepo); err != nil {
		t.Errorf("runit-only package set needs no local repository: %v", err)
	}
}

func TestDefaultPackagesBootSlinit(t *testing.T) {
	// The default install must be a slinit system. sunlight-base is what
	// brings slinit; base-system would bring runit-void, which conflicts
	// with it, so the two must never both be in the default set.
	set := "," + defaultPackages + ","
	if !strings.Contains(set, ",sunlight-base,") {
		t.Errorf("defaultPackages = %q, want sunlight-base in it", defaultPackages)
	}
	if strings.Contains(set, ",base-system,") {
		t.Errorf("defaultPackages = %q: base-system pulls runit-void, which conflicts with slinit", defaultPackages)
	}
}
