package tui

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

// ReadSecret reads one line from in with terminal echo turned off, so a
// typed password or passphrase is not shown on screen. When stdin is not a
// terminal (a pipe or a file, e.g. an unattended run), it reads normally —
// there is nothing to hide and no terminal to switch. label is printed as
// the prompt. Used by both front-ends and the LUKS passphrase prompt so a
// secret is never echoed in the clear.
func ReadSecret(in *bufio.Reader, w io.Writer, label string) string {
	s, _ := readSecret(in, w, label)
	return s
}

// ErrNoInput is returned by a prompt that has to be answered when the input
// ends before an answer arrives — a closed pipe, or a serial line that
// dropped. Without it a prompt that re-asks on an invalid answer would re-ask
// forever, since an ended input reads as an empty answer every time.
var ErrNoInput = errors.New("input ended before an answer was given")

// readSecret is ReadSecret that also reports ErrNoInput when the input ended
// with nothing read. A last line without its newline is still an answer.
func readSecret(in *bufio.Reader, w io.Writer, label string) (string, error) {
	fmt.Fprintf(w, "%s: ", label)
	restore := disableEcho()
	line, err := in.ReadString('\n')
	if restore != nil {
		restore()
		// The Enter that ended the line was not echoed; move to the next
		// line so the following output does not run into the prompt.
		fmt.Fprintln(w)
	}
	// Only the line ending is taken off: spaces are part of a password or
	// passphrase (ValidPassphrase allows them; the full-screen front-end
	// and --crypt-file keep them). Trimmed, "abcdefgh " became a password
	// other than the one typed, found at the first login or unlock.
	s := strings.TrimRight(line, "\r\n")
	if err != nil && s == "" {
		return "", ErrNoInput
	}
	return s, nil
}

// disableEcho turns off echo on the controlling terminal (stdin) and
// returns a function that restores the previous settings. It returns nil
// when stdin is not a terminal, so the caller reads normally. Only the ECHO
// flag is cleared; canonical mode stays on, so line editing and a
// newline-terminated read still work.
func disableEcho() func() {
	fd := int(os.Stdin.Fd())
	termios, err := unix.IoctlGetTermios(fd, unix.TCGETS)
	if err != nil {
		return nil // not a terminal (pipe/file): nothing to mask
	}
	orig := *termios
	termios.Lflag &^= unix.ECHO
	if err := unix.IoctlSetTermios(fd, unix.TCSETS, termios); err != nil {
		return nil
	}
	// A signal while echo is off (Ctrl+C at the prompt, a hangup) killed
	// the process with the terminal left not echoing: on a serial console
	// the operator typed blind afterwards. Echo is put back, and the
	// process ends as interrupted — nothing has been touched at a prompt.
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	done := make(chan struct{})
	go func() {
		select {
		case <-sigs:
			_ = unix.IoctlSetTermios(fd, unix.TCSETS, &orig)
			fmt.Fprintln(os.Stderr)
			os.Exit(130)
		case <-done:
		}
	}()
	return func() {
		signal.Stop(sigs)
		close(done)
		_ = unix.IoctlSetTermios(fd, unix.TCSETS, &orig)
	}
}
