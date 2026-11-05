package tui

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/sys/unix"
)

// ReadSecret reads one line from in with terminal echo turned off, so a
// typed password or passphrase is not shown on screen. When stdin is not a
// terminal (a pipe or a file, e.g. an unattended run), it reads normally —
// there is nothing to hide and no terminal to switch. label is printed as
// the prompt. Used by both front-ends and the LUKS passphrase prompt so a
// secret is never echoed in the clear.
func ReadSecret(in *bufio.Reader, w io.Writer, label string) string {
	fmt.Fprintf(w, "%s: ", label)
	restore := disableEcho()
	line, _ := in.ReadString('\n')
	if restore != nil {
		restore()
		// The Enter that ended the line was not echoed; move to the next
		// line so the following output does not run into the prompt.
		fmt.Fprintln(w)
	}
	return strings.TrimSpace(line)
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
	return func() { _ = unix.IoctlSetTermios(fd, unix.TCSETS, &orig) }
}
