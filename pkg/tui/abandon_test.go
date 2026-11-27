package tui

import (
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"
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
