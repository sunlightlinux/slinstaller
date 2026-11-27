package tui

import (
	"errors"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// abandonChildren ends what the installer started and leaves the installer
// running. Run in a child that leads its own process group, as the
// installer does when started from a shell or its service.
func TestAbandonChildren(t *testing.T) {
	if os.Getenv("SLINSTALLER_ABANDON_CHILD") == "1" {
		sleep := exec.Command("sleep", "30")
		if err := sleep.Start(); err != nil {
			t.Fatal(err)
		}
		abandonChildren()
		done := make(chan error, 1)
		go func() { done <- sleep.Wait() }()
		select {
		case err := <-done:
			if err == nil {
				t.Fatal("the command finished on its own")
			}
			os.Stdout.WriteString("ABANDONED\n")
		case <-time.After(5 * time.Second):
			sleep.Process.Kill()
			t.Fatal("the command was left running")
		}
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestAbandonChildren$", "-test.v")
	cmd.Env = append(os.Environ(), "SLINSTALLER_ABANDON_CHILD=1")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "ABANDONED") {
		t.Fatalf("child: %v\n%s", err, out)
	}
}

// Leaving mid-step waits for the install to wind down, taking the messages
// it still sends, and reports it as a failure (not a cancelled
// confirmation, which writes no diagnostics).
func TestAwaitAbandoned(t *testing.T) {
	ch := make(chan tea.Msg, 2)
	result := make(chan error, 1)
	go func() {
		for i := 0; i < 10; i++ { // more than the buffer: blocks unless taken
			ch <- logMsg("still writing")
		}
		result <- errors.New("controller: base-install: signal: terminated")
	}()
	err := awaitAbandoned(ch, result)
	if err == nil || err.Error() == "aborted" || !strings.Contains(err.Error(), "base-install") {
		t.Errorf("awaitAbandoned = %v", err)
	}
}

// What the installer starts after leaving mid-step still stops on SIGTERM:
// the disposition it set to spare itself is not passed on.
func TestAbandonLeavesSIGTERMToChildren(t *testing.T) {
	if os.Getenv("SLINSTALLER_ABANDON_CHILD2") == "1" {
		abandonChildren()
		later := exec.Command("sleep", "30")
		if err := later.Start(); err != nil {
			t.Fatal(err)
		}
		later.Process.Signal(syscall.SIGTERM)
		done := make(chan error, 1)
		go func() { done <- later.Wait() }()
		select {
		case err := <-done:
			if err == nil {
				t.Fatal("finished on its own")
			}
			os.Stdout.WriteString("STOPPED\n")
		case <-time.After(5 * time.Second):
			later.Process.Kill()
			t.Fatal("a command started afterwards ignored SIGTERM")
		}
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestAbandonLeavesSIGTERMToChildren$", "-test.v")
	cmd.Env = append(os.Environ(), "SLINSTALLER_ABANDON_CHILD2=1")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "STOPPED") {
		t.Fatalf("child: %v\n%s", err, out)
	}
}

func TestAwaitAbandonedOutcomes(t *testing.T) {
	saved := abandonWait
	defer func() { abandonWait = saved }()
	abandonWait = 50 * time.Millisecond
	// It never winds down: said so, after the wait.
	if err := awaitAbandoned(make(chan tea.Msg), make(chan error)); err == nil || !strings.Contains(err.Error(), "did not wind down") {
		t.Errorf("stuck: %v", err)
	}
	// It had already finished well: no failure.
	ok := make(chan error, 1)
	ok <- nil
	if err := awaitAbandoned(make(chan tea.Msg), ok); err != nil {
		t.Errorf("finished: %v", err)
	}
	// The exported wrapper calls the same.
	savedAb := abandonChildren
	defer func() { abandonChildren = savedAb }()
	called := false
	abandonChildren = func() { called = true }
	AbandonChildren()
	if !called {
		t.Error("AbandonChildren did nothing")
	}
	if layoutEncrypted([]partEntry{{Type: "lvm", Encrypted: true}}) != true || layoutEncrypted(autoLayout(false)) {
		t.Error("layoutEncrypted")
	}
}
