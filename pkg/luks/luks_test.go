package luks

import "testing"

func TestArgs(t *testing.T) {
	if got := FormatArgs("/dev/sda3", false); got[len(got)-1] != "-" ||
		got[0] != "cryptsetup" || got[2] != "luksFormat" {
		t.Errorf("FormatArgs = %v", got)
	}
	if got := FormatArgs("/dev/sda3", true); !contains(got, "--pbkdf") || !contains(got, "pbkdf2") {
		t.Errorf("FormatArgs grubCompat should use pbkdf2: %v", got)
	}
	if got := OpenArgs("/dev/sda3", "cryptroot"); got[2] != "luksOpen" ||
		got[3] != "/dev/sda3" || got[4] != "cryptroot" || got[5] != "-" {
		t.Errorf("OpenArgs = %v", got)
	}
}

func contains(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}

func TestCrypttabLine(t *testing.T) {
	got := CrypttabLine("cryptroot", "1111-2222")
	if got != "cryptroot UUID=1111-2222 none luks\n" {
		t.Errorf("CrypttabLine = %q", got)
	}
}
