package storage

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"time"
)

// SinkRunner returns a Runner that echoes each command and streams its
// output line by line to sink, instead of to stderr. It is VerboseRunner
// for a front-end that renders the install itself — a TUI progress view —
// where the operator sees the same command trace inside the interface.
//
// Output is forwarded as it is produced (the base-system download in
// particular takes minutes), so a long step shows activity rather than a
// frozen bar. sink is called from the command's lifetime; it must be safe
// to call from the runner's goroutine.
// outputWait bounds how long a finished command's output pipe is waited
// on. A command (a hook, often) that leaves a process in the background
// with the pipe still open kept the install waiting for as long as that
// process lived; once the command itself has exited, its output gets this
// long to drain. A variable for tests.
var outputWait = 5 * time.Second

// outputWaited is a command's result once outputWait has passed: the
// command exited well, only something it left behind still held its
// output, which is not the command failing.
func outputWaited(err error) error {
	if errors.Is(err, exec.ErrWaitDelay) {
		return nil
	}
	return err
}

func SinkRunner(sink func(line string)) Runner {
	return func(args []string) error {
		if len(args) == 0 {
			return fmt.Errorf("empty command")
		}
		sink("+ " + ShowCommand(args))

		c := exec.Command(args[0], args[1:]...)
		c.WaitDelay = outputWait
		pr, pw := io.Pipe()
		c.Stdout, c.Stderr = pw, pw

		done := make(chan struct{})
		go func() {
			sc := bufio.NewScanner(pr)
			sc.Buffer(make([]byte, 64*1024), 1<<20)
			for sc.Scan() {
				sink(sc.Text())
			}
			// A line past the buffer stops the scanner. The child still
			// writes to the pipe, so keep reading until EOF or it blocks
			// and Run never returns.
			if err := sc.Err(); err != nil {
				sink("(output line dropped: " + err.Error() + ")")
			}
			io.Copy(io.Discard, pr)
			close(done)
		}()

		err := c.Run()
		pw.Close() // unblock the scanner at EOF
		<-done     // drain remaining lines before returning
		return outputWaited(err)
	}
}
