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

// Proxy credentials without a scheme, and registered secrets, are masked.
func TestMaskSecretBareAndRegistered(t *testing.T) {
	if got := ShowCommand([]string{"env", "https_proxy=user:pw@proxy:3128", "curl"}); strings.Contains(got, "pw@") || !strings.Contains(got, "user:***@proxy:3128") {
		t.Errorf("bare userinfo: %s", got)
	}
	if got := MaskSecret("10:30 and a@b"); got != "10:30 and a@b" {
		t.Errorf("no userinfo masked: %s", got)
	}
	AddSecret("s3cr3t-token", "x")
	got := ShowCommand([]string{"env", "API_TOKEN=s3cr3t-token", "bash", "-c", `curl -H "Authorization: s3cr3t-token" x`})
	if strings.Contains(got, "s3cr3t") {
		t.Errorf("registered secret shown: %s", got)
	}
	if MaskSecret("x marks") != "x marks" {
		t.Error("a one-character value was masked")
	}
	// Not credentials: an image digest, a GPT name, a schedule.
	for _, a := range []string{"alpine:3.19@sha256:abcd", "--change-name=1:EFI@1", "10:30@daily", "mailto:a@b.c"} {
		if got := MaskSecret(a); got != a {
			t.Errorf("%q masked as %q", a, got)
		}
	}
	if got := MaskSecret("HTTPS_PROXY=u:p@proxy:3128"); got != "HTTPS_PROXY=u:***@proxy:3128" {
		t.Errorf("upper-case proxy: %s", got)
	}
	// A registered secret a command prints is masked in its output too.
	var b strings.Builder
	if err := StreamRunner(&b)([]string{"sh", "-c", "printf 'tok=s3cr3t-'; printf 'token\\n'; echo done"}); err != nil {
		t.Fatal(err)
	}
	_, out, _ := strings.Cut(b.String(), "\n") // after the command line, which holds it in two halves
	if strings.Contains(out, "s3cr3t") || !strings.Contains(out, "tok=***") || !strings.Contains(out, "done") {
		t.Errorf("stream output: %q", b.String())
	}
	var lines []string
	SinkRunner(func(l string) { lines = append(lines, l) })([]string{"echo", "s3cr3t-token"})
	if strings.Contains(strings.Join(lines, "\n"), "s3cr3t") {
		t.Errorf("sink output: %q", lines)
	}
}

// With a secret registered, a \r-ended progress redraw is passed on at once.
func TestMaskWriterPassesProgress(t *testing.T) {
	var b strings.Builder
	m := &maskWriter{w: &b}
	m.Write([]byte("10%\r"))
	if b.String() != "10%\r" {
		t.Errorf("held back: %q", b.String())
	}
}

// A line longer than 64 KiB is masked and written as it stands.
func TestMaskWriterLongLine(t *testing.T) {
	AddSecret("long-secret-value")
	var b strings.Builder
	m := &maskWriter{w: &b}
	m.Write([]byte(strings.Repeat("x", 70<<10) + "long-secret-value"))
	m.flush()
	if strings.Contains(b.String(), "long-secret-value") || len(b.String()) < 70<<10 {
		t.Errorf("long line: %d bytes, secret shown %v", len(b.String()), strings.Contains(b.String(), "long-secret-value"))
	}
}

// A secret that holds a shorter one is masked whole, whichever was
// registered first; a yescrypt-GOST hash is a hash like the others.
func TestMaskSecretLongestFirstAndGOST(t *testing.T) {
	AddSecret("q7zk")
	AddSecret("q7zk-and-the-rest")
	if got := MaskSecret("T=q7zk-and-the-rest"); got != "T=***" {
		t.Errorf("a secret holding another: %q", got)
	}
	if got := MaskSecret("$gy$j9T$salt$hash"); got != "<password hash>" {
		t.Errorf("gost-yescrypt hash: %q", got)
	}
}

// A proxy without a scheme is masked to the last "@" before its host, as
// one with a scheme is: a password with an "@" in it was shown from there.
func TestMaskBareProxyAtInPassword(t *testing.T) {
	for in, want := range map[string]string{
		"https_proxy=user:@pw@proxy:3128": "https_proxy=user:***@proxy:3128",
		"https_proxy=user:p@w@proxy:3128": "https_proxy=user:***@proxy:3128",
		"http_proxy=user:@proxy:3128":     "http_proxy=user:***@proxy:3128",
	} {
		if got := MaskSecret(in); got != want {
			t.Errorf("%s masked as %s, want %s", in, got, want)
		}
	}
}

// A secret of four characters is registered, one of three is not (it would
// garble everything); a secret that holds another is masked whole, however
// they were registered.
func TestAddSecretLengthAndOrder(t *testing.T) {
	withSecrets(t, nil, func() {
		AddSecret("q7z", "k9w2")
		if got := maskRegistered("q7z k9w2"); got != "q7z ***" {
			t.Errorf("short secrets: %q", got)
		}
		AddSecret("tok-k9w2-tail", "k9w2")
		if got := maskRegistered("a tok-k9w2-tail b"); got != "a *** b" {
			t.Errorf("a secret holding another: %q", got)
		}
		if len(secrets) != 2 {
			t.Errorf("registered %q, want each once", secrets)
		}
	})
}
