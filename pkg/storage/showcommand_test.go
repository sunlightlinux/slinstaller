package storage

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// A password hash in a command is masked where the command is shown.
func TestShowCommandMasksHashes(t *testing.T) {
	got := ShowCommand([]string{"chroot", "/mnt", "useradd", "-m", "-p", "$6$salt$abc", "me"})
	if got != "chroot /mnt useradd -m -p <password hash> me" {
		t.Errorf("ShowCommand = %q", got)
	}
	if got := ShowCommand([]string{"echo", "$HOME", "$6"}); got != "echo $HOME $6" {
		t.Errorf("non-hash arguments changed: %q", got)
	}
	var lines []string
	if err := SinkRunner(func(l string) { lines = append(lines, l) })([]string{"true", "$y$j9T$x$y"}); err != nil {
		t.Fatal(err)
	}
	if len(lines) == 0 || strings.Contains(lines[0], "$y$") {
		t.Errorf("SinkRunner logged a hash: %q", lines)
	}
}

// StreamRunner sends the command, masked, and its output to the writer.
func TestStreamRunner(t *testing.T) {
	var b strings.Builder
	if err := StreamRunner(&b)([]string{"/bin/sh", "-c", "echo out; echo err >&2", "$6$x$y"}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"    + /bin/sh -c", "<password hash>", "out\n", "err\n"} {
		if !strings.Contains(b.String(), want) {
			t.Errorf("missing %q in %q", want, b.String())
		}
	}
	if strings.Contains(b.String(), "$6$") {
		t.Errorf("hash shown: %q", b.String())
	}
	if err := StreamRunner(&b)([]string{"/bin/false"}); err == nil {
		t.Error("a failing command returned no error")
	}
}

// Proxy and mirror credentials are masked too, keeping user and host.
func TestShowCommandMasksURLPasswords(t *testing.T) {
	got := ShowCommand([]string{"env", "https_proxy=http://bob:s3cret@proxy:3128", "no_proxy=a,b", "xbps-install", "-R", "https://u:p@m.example/x"})
	want := "env https_proxy=http://bob:***@proxy:3128 no_proxy=a,b xbps-install -R https://u:***@m.example/x"
	if got != want {
		t.Errorf("ShowCommand = %q\nwant          %q", got, want)
	}
	// A raw @, # or ? in the password is masked whole.
	for in, want := range map[string]string{
		"http://u:p@ss@proxy:3128":  "http://u:***@proxy:3128",
		"http://u:p#ss@proxy:3128/": "http://u:***@proxy:3128/",
		"http://u:p?ss@proxy":       "http://u:***@proxy",
	} {
		if got := MaskSecret(in); got != want {
			t.Errorf("MaskSecret(%q) = %q, want %q", in, got, want)
		}
	}
	for _, keep := range []string{"http://proxy:3128", "http://bob@proxy", "user@host:path", "https://h/a:b@c"} {
		if MaskSecret(keep) != keep {
			t.Errorf("MaskSecret(%q) = %q, want it unchanged", keep, MaskSecret(keep))
		}
	}
}

// A failed command's error is shown, logged and reported: it is masked.
func TestRunCommandsErrorIsMasked(t *testing.T) {
	err := RunCommands([][]string{{"useradd", "-p", "$6$salt$hash", "me"}}, func([]string) error { return errors.New("boom") })
	if err == nil || strings.Contains(err.Error(), "$6$") || !strings.Contains(err.Error(), "<password hash>") {
		t.Errorf("RunCommands error = %v", err)
	}
	if got := FormatCommands([][]string{{"env", "https_proxy=http://a:b@p"}}); strings.Contains(got, ":b@") {
		t.Errorf("FormatCommands = %q", got)
	}
}

// A command that leaves something in the background holding its output
// does not keep the install waiting for it, and is not taken as failed.
func TestRunnersDoNotWaitForBackgroundOutput(t *testing.T) {
	saved := outputWait
	defer func() { outputWait = saved }()
	outputWait = 200 * time.Millisecond
	var lines []string
	start := time.Now()
	err := SinkRunner(func(l string) { lines = append(lines, l) })([]string{"sh", "-c", "sleep 3 & echo hi"})
	if err != nil || time.Since(start) > 2*time.Second {
		t.Errorf("SinkRunner: %v after %v", err, time.Since(start))
	}
	var b strings.Builder
	start = time.Now()
	if err := StreamRunner(&b)([]string{"sh", "-c", "sleep 3 & echo hi"}); err != nil || time.Since(start) > 2*time.Second {
		t.Errorf("StreamRunner: %v after %v", err, time.Since(start))
	}
	// A real failure is still one.
	if err := SinkRunner(func(string) {})([]string{"sh", "-c", "exit 3"}); err == nil {
		t.Error("a failing command was taken as fine")
	}
}

// A sink slower than outputWait loses none of a command's own output: the
// pipe is drained into a queue whatever the sink does. The output (~79 KB)
// fits the kernel pipe plus the one 32 KB chunk exec's copier holds, so the
// command exits at once; the sink then stalls on its first line for five
// times outputWait. Read at the sink's own pace, the kernel pipe was closed
// under the ~46 KB still in it when outputWait ran out. (The margins are
// wide: draining it takes far less than 100ms even under -race on a loaded
// runner — 10ms, the earlier setting, did not always.)
func TestSlowSinkLosesNothing(t *testing.T) {
	saved := outputWait
	defer func() { outputWait = saved }()
	outputWait = 100 * time.Millisecond
	n := 0
	err := SinkRunner(func(string) {
		if n++; n == 2 { // the first line of output (the first is the command line)
			time.Sleep(5 * outputWait)
		}
	})([]string{"seq", "1", "15000"})
	if err != nil || n != 15001 { // the command line, then 15000 lines
		t.Errorf("err %v, %d lines, want 15001", err, n)
	}
}
