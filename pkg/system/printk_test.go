package system

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPrintkSysctl(t *testing.T) {
	root := t.TempDir()
	if err := PrintkSysctl(root, 3); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(root, "etc", "sysctl.d", "10-printk.conf"))
	if err != nil {
		t.Fatal(err)
	}
	want := "# console log verbosity set by slinstaller\nkernel.printk = 3 4 1 7\n"
	if string(got) != want {
		t.Errorf("printk.conf = %q, want %q", got, want)
	}
}
