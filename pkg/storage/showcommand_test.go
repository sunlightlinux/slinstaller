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
