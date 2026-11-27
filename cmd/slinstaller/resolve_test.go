package main

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sunlightlinux/slinstaller/pkg/config"
	"github.com/sunlightlinux/slinstaller/pkg/controller"
)

func TestReachesBaseInstall(t *testing.T) {
	for _, c := range []struct {
		through, resume string
		want            bool
	}{
		{"", "", true},
		{"partition", "", false},
		{"meta-fs", "", false},
		{"base-install", "", true},
		{"bootloader", "", true},
		{"", "base-install", true},
		{"", "bootloader", false},
	} {
		if got := reachesBaseInstall(c.through, c.resume); got != c.want {
			t.Errorf("through %q resume %q: %v", c.through, c.resume, got)
		}
	}
}

// The package set is dry-run before the wipe: refused when xbps says it
// cannot be had, retried with the kernel fallback, and only warned about
// when the dry run fails for another reason.
func TestResolvePackages(t *testing.T) {
	bin := t.TempDir()
	os.WriteFile(filepath.Join(bin, "xbps-install"), []byte("#!/bin/sh\n"), 0o755)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	defer func(r func([][]string) (string, error)) { resolveRun = r }(resolveRun)

	opts := controller.Options{Repo: "https://repo", Packages: []string{"base", "linux"}}
	cfg := &config.Config{}
	var ran [][]string
	resolveRun = func(cmds [][]string) (string, error) {
		ran = cmds
		if slices.Contains(cmds[len(cmds)-1], "linux") {
			return "Package 'linux' not found in repository pool.\n", errors.New("exit status 2")
		}
		return "", nil
	}
	if err := resolvePackages(opts, cfg); err == nil || !strings.Contains(err.Error(), "'linux' not found") || !strings.Contains(err.Error(), "nothing was touched") {
		t.Errorf("unresolvable: %v", err)
	}
	if last, sync := strings.Join(ran[len(ran)-1], " "), strings.Join(ran[len(ran)-2], " "); !strings.Contains(last, "xbps-install -n ") || !strings.HasSuffix(sync, "xbps-install -S -R https://repo -r "+ran[len(ran)-2][len(ran[len(ran)-2])-1]) {
		t.Errorf("not a sync then a dry run: %s / %s", sync, last)
	}

	cfg.Kernel, cfg.KernelFallback = "linux", "linux-lts"
	if err := resolvePackages(opts, cfg); err != nil {
		t.Errorf("the fallback resolves: %v", err)
	}

	resolveRun = func([][]string) (string, error) {
		return "ERROR: [reposync] failed to fetch file\n", errors.New("exit status 1")
	}
	if err := resolvePackages(opts, &config.Config{}); err != nil {
		t.Errorf("a failure that is no verdict refused: %v", err)
	}

	t.Setenv("PATH", t.TempDir())
	resolveRun = func([][]string) (string, error) { t.Error("ran without xbps-install"); return "", nil }
	if err := resolvePackages(opts, cfg); err != nil {
		t.Error(err)
	}
}

// The real runner reports its commands' output and stops at the first failure.
func TestResolveRun(t *testing.T) {
	out, err := resolveRun([][]string{{"sh", "-c", "echo one"}, {"sh", "-c", "echo two; exit 3"}, {"sh", "-c", "echo three"}})
	if err == nil || out != "one\ntwo\n" {
		t.Errorf("%q %v", out, err)
	}
}
