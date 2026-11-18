package main

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/sunlightlinux/slinstaller/pkg/config"
	"github.com/sunlightlinux/slinstaller/pkg/system"
)

// readArchive returns the diagnostics archive's files by name.
func readArchive(t *testing.T, path string) map[string]string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("diagnostics archive: %v", err)
	}
	defer f.Close()
	if st, _ := f.Stat(); st.Mode().Perm() != 0o600 {
		t.Errorf("diagnostics archive mode %v, want 0600", st.Mode().Perm())
	}
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return out
		}
		if err != nil {
			t.Fatal(err)
		}
		data, _ := io.ReadAll(tr)
		out[strings.TrimPrefix(h.Name, "slinstaller-diagnostics/")] = string(data)
	}
}

func keys(m map[string]string) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func TestInstallLock(t *testing.T) {
	defer func(p string) { lockPath = p }(lockPath)
	lockPath = filepath.Join(t.TempDir(), "install.lock")
	first, err := lockInstall()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lockInstall(); err == nil || !strings.Contains(err.Error(), "another slinstaller install is running") {
		t.Errorf("a second lock: %v", err)
	}
	// Released when its holder is done — a crashed install does not block
	// the next one.
	first.Close()
	second, err := lockInstall()
	if err != nil {
		t.Fatalf("lock after release: %v", err)
	}
	second.Close()

	lockPath = filepath.Join(t.TempDir(), "no-such-dir", "install.lock")
	if _, err := lockInstall(); err == nil || !strings.Contains(err.Error(), "install lock") {
		t.Errorf("a lock that cannot be made: %v", err)
	}
	// A lock path that cannot be locked (a directory) is an error too.
	lockPath = t.TempDir()
	if _, err := lockInstall(); err == nil {
		t.Error("a directory was taken as the lock")
	}
}

func TestDiagnosticsArchive(t *testing.T) {
	dir := t.TempDir()
	defer func(p string) { diagnosticsDir = p }(diagnosticsDir)
	diagnosticsDir = dir
	log := filepath.Join(dir, "install.log")
	os.WriteFile(log, []byte("==> partition\nparted: boom\n"), 0o600)
	cfg := plainConfig()
	cfg.Users = []*config.User{{Login: "ion", Password: "$6$salt$the-hash"}, {Login: "ana"}}
	cfg.PostInstall = []*config.Hook{{Cmd: "echo done"}}

	// No lsblk or efibootmgr here: their files say so instead of being
	// left out, and the rest is still written.
	t.Setenv("PATH", t.TempDir())
	path, err := writeDiagnostics("", cfg, log, errors.New("controller: partition: boom"))
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(path) != dir || !strings.HasPrefix(filepath.Base(path), "slinstaller-diagnostics-") {
		t.Errorf("default path = %s", path)
	}
	files := readArchive(t, path)
	if !strings.Contains(files["error.txt"], "controller: partition: boom") || !strings.Contains(files["error.txt"], "slinstaller "+version) {
		t.Errorf("error.txt = %q", files["error.txt"])
	}
	if files["install.log"] != "==> partition\nparted: boom\n" {
		t.Errorf("install.log = %q", files["install.log"])
	}
	c := files["config.yaml"]
	if strings.Contains(c, "the-hash") || !strings.Contains(c, "password: (removed)") || !strings.Contains(c, "login: ana") || !strings.Contains(c, "echo done") {
		t.Errorf("config.yaml:\n%s", c)
	}
	if !strings.Contains(files["lsblk.txt"], "executable file not found") {
		t.Errorf("lsblk.txt = %q", files["lsblk.txt"])
	}
	if _, err := os.Stat("/proc/cmdline"); err == nil && files["cmdline.txt"] == "" {
		t.Error("the kernel command line is missing")
	}

	// No log to include, and nowhere to write: the first is fine, the
	// second is an error with nothing left behind.
	path, err = writeDiagnostics(filepath.Join(dir, "x.tar.gz"), cfg, filepath.Join(dir, "absent.log"), errors.New("e"))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := readArchive(t, path)["install.log"]; ok {
		t.Error("a log that does not exist was included")
	}
	if _, err := writeDiagnostics(filepath.Join(dir, "no-such-dir", "x.tar.gz"), cfg, "", errors.New("e")); err == nil {
		t.Error("an unwritable archive was reported as written")
	}
	if _, err := os.Stat("/dev/full"); err == nil {
		if _, err := writeDiagnostics("/dev/full", cfg, log, errors.New("e")); err == nil {
			t.Error("a failed write was reported as written")
		}
	}
}

func TestSystemCheckNeedsNoRoot(t *testing.T) {
	_, _, err := runWith(t, "", "--system-check")
	wantRunErr(t, "no config", err, "--system-check needs the config")

	dir := t.TempDir()
	bin := onlyTools(t, dir, baseTools...)
	repo := filepath.Join(dir, "repo")
	os.MkdirAll(repo, 0o755)
	os.WriteFile(filepath.Join(repo, "x86_64-repodata"), nil, 0o644)
	args := []string{"--system-check", "--offline", "--local-repo", repo, writeConfig(t, dir, plainConfig())}
	_, errOut, err := runWith(t, "", args...)
	if !system.IsUEFI() {
		wantRunErr(t, "not UEFI", err, "not booted in UEFI mode")
		return
	}
	if err != nil || !strings.Contains(errOut, "System check passed") {
		t.Errorf("--system-check as a user: %v\n%s", err, errOut)
	}
	if _, err := os.Stat(filepath.Join(bin, "parted.args")); err == nil {
		t.Error("--system-check ran parted")
	}
	os.Remove(filepath.Join(bin, "parted"))
	_, _, err = runWith(t, "", args...)
	wantRunErr(t, "missing parted", err, "missing tools on the live system: parted")
}
