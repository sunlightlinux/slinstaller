package storage

import (
	"bufio"
	"fmt"
	"io"
	"os/exec"
	"strings"
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
func SinkRunner(sink func(line string)) Runner {
	return func(args []string) error {
		if len(args) == 0 {
			return fmt.Errorf("empty command")
		}
		sink("+ " + strings.Join(args, " "))

		c := exec.Command(args[0], args[1:]...)
		pr, pw := io.Pipe()
		c.Stdout, c.Stderr = pw, pw

		done := make(chan struct{})
		go func() {
			sc := bufio.NewScanner(pr)
			sc.Buffer(make([]byte, 64*1024), 1<<20)
			for sc.Scan() {
				sink(sc.Text())
			}
			close(done)
		}()

		err := c.Run()
		pw.Close() // unblock the scanner at EOF
		<-done     // drain remaining lines before returning
		return err
	}
}
