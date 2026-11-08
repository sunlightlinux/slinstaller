package storage

import (
	"strings"
	"testing"
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
