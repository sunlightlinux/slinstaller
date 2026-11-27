package grub

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// FuzzCmdlineConfigShell checks the kernel command line reaches the
// kernel as written: /etc/default/grub is sourced by sh as root, so what
// the lines hold once sh has read them must be the arguments, word for
// word — no expansion, no command run, no line that ends the assignment
// early — whatever the arguments hold. The serial lines are checked the
// same way, since console= feeds them.
func FuzzCmdlineConfigShell(f *testing.F) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		f.Skip("no sh")
	}
	f.Add("quiet", "rd.luks.uuid=$(touch${IFS}pwned)", "console=ttyS1,9600o7")
	f.Add("a\"b`c`\\", "init=/sbin/slinit", "$HOME")
	f.Add("'", "root=UUID='x'", "console=ttyS0,115200n8\\")
	dir := f.TempDir()
	f.Fuzz(func(t *testing.T, a, b, c string) {
		args := []string{a, b, c}
		for _, s := range args {
			if s == "" || strings.ContainsAny(s, "\x00 \t\n") {
				t.Skip() // one argument is one word: the config splits at blanks
			}
		}
		o := Options{KernelArgs: args, SerialTerminal: true}
		file := filepath.Join(dir, "grub")
		if err := os.WriteFile(file, []byte(o.CmdlineConfig()+o.TerminalConfig()), 0o600); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(sh, "-c", `. "$1" && printf '%s\000%s\000%s' "$GRUB_CMDLINE_LINUX" "$GRUB_CMDLINE_LINUX_DEFAULT" "$GRUB_SERIAL_COMMAND"`, sh, file)
		cmd.Dir = dir
		cmd.Env = []string{"PATH=/nonexistent"} // a command run would fail, or be seen below
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("sourcing %q: %v", o.CmdlineConfig(), err)
		}
		got := strings.Split(string(out), "\x00")
		var always, normal []string
		for _, a := range args {
			if bootCritical(a) {
				always = append(always, a)
			} else {
				normal = append(normal, a)
			}
		}
		if got[0] != strings.Join(always, " ") || got[1] != strings.Join(normal, " ") {
			t.Errorf("args %q read back as %q", args, got[:2])
		}
		unit, speed, word, parity := serialPort(args)
		if want := "serial --unit=" + unit + " --speed=" + speed + " --word=" + word + " --parity=" + parity + " --stop=1"; got[2] != want {
			t.Errorf("serial line %q, want %q", got[2], want)
		}
		if strings.Trim(unit+speed, "0123456789") != "" {
			t.Errorf("serial unit/speed %q/%q from %q", unit, speed, args)
		}
		if entries, _ := os.ReadDir(dir); len(entries) != 1 {
			t.Errorf("sourcing made files: %v", entries)
		}
	})
}
