package main

import (
	"errors"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/sunlightlinux/slinstaller/pkg/blockdev"
	"github.com/sunlightlinux/slinstaller/pkg/controller"
)

// Ctrl+C (SIGINT) during an install stops it after the stage running,
// through the controller's cleanup, instead of killing the process.
func TestInstallStopsOnSIGINT(t *testing.T) {
	var ran []string
	signalled := false
	opts := controller.Options{
		Root: t.TempDir(), Through: "mount",
		Devices: func() ([]blockdev.Device, error) { return nil, nil },
		Runner: func(args []string) error {
			ran = append(ran, strings.Join(args, " "))
			if !signalled {
				signalled = true
				syscall.Kill(syscall.Getpid(), syscall.SIGINT)
				time.Sleep(200 * time.Millisecond) // let the handler see it
			}
			return nil
		},
	}
	err := installStoppable(plainConfig(), opts)
	if !errors.Is(err, controller.ErrStopped) || !strings.Contains(err.Error(), "before mount") {
		t.Fatalf("err = %v\nran:\n%s", err, strings.Join(ran, "\n"))
	}
}

// --help prints the usage and succeeds: a script asking for it is not told
// it failed.
func TestHelpIsNotAnError(t *testing.T) {
	for _, a := range []string{"-h", "--help"} {
		if err := run([]string{a}); err != nil {
			t.Errorf("%s: %v", a, err)
		}
	}
	if err := run([]string{"--no-such-flag"}); err == nil {
		t.Error("an unknown flag was not an error")
	}
}
