package main

import (
	"bytes"
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The completion scripts are made from the flags themselves: every flag is
// in them, the bash one is valid and completes flags and stage names.
func TestCompletion(t *testing.T) {
	var out bytes.Buffer
	if err := run([]string{"--completion", "bash"}); err != nil {
		t.Fatal(err)
	}
	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	fs.Bool("apply", false, "carry it out")
	fs.String("through", "", "stop after a stage")
	fs.Bool("b", false, "short")
	if err := printCompletion(&out, fs, "bash"); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(t.TempDir(), "c.bash")
	os.WriteFile(script, out.Bytes(), 0o644)
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("no bash")
	}
	complete := func(words ...string) string {
		cmd := exec.Command("bash", "-c", `source "$1"; shift; COMP_WORDS=("$@"); COMP_CWORD=$(( $# - 1 )); _slinstaller; echo "${COMPREPLY[*]}"`, "bash", script)
		cmd.Args = append(cmd.Args, words...)
		got, err := cmd.Output()
		if err != nil {
			t.Fatalf("bash: %v", err)
		}
		return strings.TrimSpace(string(got))
	}
	if got := complete("slinstaller", "--thr"); got != "--through" {
		t.Errorf("--thr completes to %q", got)
	}
	if got := complete("slinstaller", "--through", "boot"); got != "bootloader" {
		t.Errorf("a stage completes to %q", got)
	}

	out.Reset()
	if err := printCompletion(&out, fs, "fish"); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"-l apply -d 'carry it out'", "-l through -x -a '", "-s b -d 'short'"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("fish script missing %q:\n%s", want, out.String())
		}
	}
	if printCompletion(&out, fs, "tcsh") == nil {
		t.Error("an unknown shell was accepted")
	}
}

// The scripts in completions/ are the generator's output for the real
// flags: a new flag without a regenerated script fails here.
func TestShippedCompletionsAreCurrent(t *testing.T) {
	for _, shell := range []string{"bash", "fish"} {
		r, w, _ := os.Pipe()
		saved := os.Stdout
		os.Stdout = w
		err := run([]string{"--completion", shell})
		w.Close()
		os.Stdout = saved
		if err != nil {
			t.Fatal(err)
		}
		var got bytes.Buffer
		got.ReadFrom(r)
		want, _ := os.ReadFile(filepath.Join("..", "..", "completions", "slinstaller."+shell))
		if got.String() != string(want) {
			t.Errorf("completions/slinstaller.%s is out of date: regenerate with `slinstaller --completion %s > completions/slinstaller.%s`", shell, shell, shell)
		}
	}
}
