package tui

import (
	"bufio"
	"io"
	"strings"
	"testing"
)

func TestReadSecretReadsLine(t *testing.T) {
	// ReadSecret reads from the given bufio reader, not os.Stdin, so a
	// non-terminal input is returned verbatim (trimmed). The echo-off path
	// needs a real TTY and is exercised interactively.
	in := bufio.NewReader(strings.NewReader("hunter2\nnext\n"))
	if got := ReadSecret(in, io.Discard, "Password"); got != "hunter2" {
		t.Errorf("ReadSecret = %q, want hunter2", got)
	}
	// The next line is still available: ReadSecret consumed only its line.
	line, _ := in.ReadString('\n')
	if strings.TrimSpace(line) != "next" {
		t.Errorf("leftover = %q, want next", line)
	}
}
