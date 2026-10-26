package storage

import (
	"strings"
	"testing"
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
