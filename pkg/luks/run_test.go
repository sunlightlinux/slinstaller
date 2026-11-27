package luks

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeCryptsetup puts a stand-in cryptsetup first on PATH. It records its
// arguments and stdin under dir, prints a UUID for luksUUID, and fails when
// FAKE_CRYPTSETUP_FAIL is set — so the wrappers are tested without touching
// a device or needing the real tool.
func fakeCryptsetup(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	script := `#!/bin/sh
echo "$@" >> "` + dir + `/args"
if [ "$1" = luksUUID ]; then
	echo "  1111-2222-3333  "
	exit 0
fi
cat > "` + dir + `/stdin"
echo "cryptsetup says hello"
[ -z "$FAKE_CRYPTSETUP_FAIL" ]
`
	if err := os.WriteFile(filepath.Join(dir, "cryptsetup"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return dir
}

func read(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestPassphraseGoesOnStdinNeverOnTheCommandLine(t *testing.T) {
	dir := fakeCryptsetup(t)
	const pass = "correct horse battery staple"
	var out bytes.Buffer

	if err := Format("/dev/sda3", pass, &out, false); err != nil {
		t.Fatalf("Format: %v", err)
	}
	if err := Open("/dev/sda3", "cryptroot", pass, &out); err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := AddKey("/dev/sda3", "/mnt/etc/cryptkeys/k", pass, &out); err != nil {
		t.Fatalf("AddKey: %v", err)
	}

	args := read(t, filepath.Join(dir, "args"))
	for _, want := range []string{
		"--batch-mode luksFormat /dev/sda3 -",
		"--batch-mode luksOpen /dev/sda3 cryptroot -",
		"--batch-mode --key-file=- --pbkdf=pbkdf2 --pbkdf-force-iterations=1000 luksAddKey /dev/sda3 /mnt/etc/cryptkeys/k",
	} {
		if !strings.Contains(args, want) {
			t.Errorf("cryptsetup not called with %q:\n%s", want, args)
		}
	}
	// The passphrase reaches cryptsetup on stdin, and is neither an argument
	// (visible in the process list) nor echoed into the install log.
	if got := read(t, filepath.Join(dir, "stdin")); got != pass {
		t.Errorf("stdin = %q, want the passphrase", got)
	}
	if strings.Contains(args, pass) || strings.Contains(out.String(), pass) {
		t.Errorf("passphrase leaked into the arguments or the log:\nargs: %s\nlog: %s", args, out.String())
	}
	// The command is echoed and cryptsetup's output is captured by out.
	if log := out.String(); !strings.Contains(log, "+ cryptsetup --batch-mode luksFormat") || !strings.Contains(log, "cryptsetup says hello") {
		t.Errorf("log:\n%s", log)
	}
}

func TestFailureIsReportedWithTheSubcommand(t *testing.T) {
	fakeCryptsetup(t)
	t.Setenv("FAKE_CRYPTSETUP_FAIL", "1")
	var out bytes.Buffer
	// The error names the step that failed, not one of its flags.
	for step, run := range map[string]func() error{
		"luksFormat": func() error { return Format("/dev/sda3", "pw", &out, true) },
		"luksOpen":   func() error { return Open("/dev/sda3", "cryptroot", "pw", &out) },
		"luksAddKey": func() error { return AddKey("/dev/sda3", "/k", "pw", &out) },
	} {
		if err := run(); err == nil || !strings.Contains(err.Error(), "luks: "+step+":") {
			t.Errorf("%s failing: %v", step, err)
		}
	}
	if got := subcommand([]string{"cryptsetup", "--version"}); got != "cryptsetup" {
		t.Errorf("subcommand with only options = %q", got)
	}
}

func TestUUIDIsTrimmed(t *testing.T) {
	fakeCryptsetup(t)
	got, err := UUID("/dev/sda3")
	if err != nil || got != "1111-2222-3333" {
		t.Errorf("UUID = %q, %v", got, err)
	}
}

func TestOutputDefaultsToStderrAndUUIDFailureNamesTheDevice(t *testing.T) {
	fakeCryptsetup(t)
	// No writer given: the command still runs (its output goes to stderr).
	if err := Open("/dev/sda3", "cryptroot", "pw", nil); err != nil {
		t.Errorf("Open with no writer: %v", err)
	}

	// cryptsetup failing on luksUUID is an error naming the device, not an
	// empty UUID for the kernel command line.
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "cryptsetup"), []byte("#!/bin/sh\nexit 1\n"), 0o755)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	if got, err := UUID("/dev/sda9"); err == nil || got != "" || !strings.Contains(err.Error(), "/dev/sda9") {
		t.Errorf("UUID on failure = %q, %v", got, err)
	}
}

// The tool's own message reaches the error, not just "exit status 1".
func TestUUIDFailureCarriesStderr(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "cryptsetup"), []byte("#!/bin/sh\necho 'Device /dev/sda9 is not a valid LUKS device.' >&2\nexit 1\n"), 0o755)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	if _, err := UUID("/dev/sda9"); err == nil || !strings.Contains(err.Error(), "not a valid LUKS device") {
		t.Errorf("UUID error should carry cryptsetup's stderr, got %v", err)
	}
}
