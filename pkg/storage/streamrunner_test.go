package storage

import (
	"strings"
	"testing"
	"time"
)

func TestSinkRunnerStreams(t *testing.T) {
	var got []string
	run := SinkRunner(func(line string) { got = append(got, line) })

	if err := run([]string{"sh", "-c", "echo one; echo two"}); err != nil {
		t.Fatalf("run: %v", err)
	}

	all := strings.Join(got, "\n")
	// The command is echoed first, then each output line is forwarded.
	if !strings.HasPrefix(got[0], "+ sh -c ") {
		t.Errorf("first line should echo the command, got %q", got[0])
	}
	if !strings.Contains(all, "one") || !strings.Contains(all, "two") {
		t.Errorf("output lines not streamed: %q", all)
	}
}

func TestSinkRunnerError(t *testing.T) {
	run := SinkRunner(func(string) {})
	if err := run([]string{"sh", "-c", "exit 3"}); err == nil {
		t.Error("a failing command should return an error")
	}
	if err := run(nil); err == nil {
		t.Error("an empty command should return an error")
	}
}

// A line past the scanner's limit must not stop the pipe from draining:
// the child would block on write and the install would hang.
func TestSinkRunnerLongLine(t *testing.T) {
	var got []string
	run := SinkRunner(func(line string) { got = append(got, line) })

	errc := make(chan error, 1)
	go func() {
		errc <- run([]string{"sh", "-c", "head -c 2097152 /dev/zero | tr '\\0' x; echo; echo after"})
	}()
	select {
	case err := <-errc:
		if err != nil {
			t.Fatalf("run: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("SinkRunner hung on an output line longer than its buffer")
	}
}
