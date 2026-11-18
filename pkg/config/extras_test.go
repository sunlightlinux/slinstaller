package config

import (
	"os"
	"strings"
	"testing"
)

func TestProxyEnv(t *testing.T) {
	for _, tc := range []struct {
		c    Config
		want string
	}{
		{Config{}, ""},
		// As before there were two settings: one proxy serves both.
		{Config{HTTPSProxy: "http://p:3128"}, "https_proxy=http://p:3128 http_proxy=http://p:3128"},
		{Config{HTTPSProxy: "http://s:1", HTTPProxy: "http://h:2", NoProxy: "localhost,10.0.0.0/8"},
			"https_proxy=http://s:1 http_proxy=http://h:2 no_proxy=localhost,10.0.0.0/8"},
		{Config{HTTPProxy: "http://h:2"}, "http_proxy=http://h:2"},
		// Hosts to reach directly mean nothing without a proxy.
		{Config{NoProxy: "localhost"}, ""},
	} {
		if got := strings.Join(tc.c.ProxyEnv(), " "); got != tc.want {
			t.Errorf("ProxyEnv(%+v) = %q, want %q", tc.c, got, tc.want)
		}
	}
}

func TestPowerAction(t *testing.T) {
	for _, tc := range []struct {
		c    Config
		want string
	}{
		{Config{}, ""},
		{Config{PostReboot: true}, "reboot"},
		{Config{PowerState: "poweroff"}, "poweroff"},
		{Config{PowerState: "halt", PostReboot: true}, "halt"}, // the newer setting says more
	} {
		if got := tc.c.PowerAction(); got != tc.want {
			t.Errorf("PowerAction = %q, want %q", got, tc.want)
		}
	}
	c := btrfsConfig()
	c.PowerState = "suspend"
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), `power-state: "suspend" is not reboot, poweroff or halt`) {
		t.Errorf("unknown power state: %v", err)
	}
}

func TestWriteFilesValidation(t *testing.T) {
	for name, tc := range map[string]struct {
		files []*WriteFile
		want  string
	}{
		"ok":           {[]*WriteFile{nil, {Path: "/etc/motd", Content: "hi\n"}, {Path: "/etc/secret", Permissions: "0600"}}, ""},
		"relative":     {[]*WriteFile{{Path: "etc/motd"}}, "is not an absolute file path"},
		"the root":     {[]*WriteFile{{Path: "/"}}, "is not an absolute file path"},
		"twice":        {[]*WriteFile{{Path: "/etc/motd"}, {Path: "/etc/../etc/motd"}}, "/etc/motd is written twice"},
		"bad mode":     {[]*WriteFile{{Path: "/etc/motd", Permissions: "rw-r--r--"}}, "are not an octal mode"},
		"mode too big": {[]*WriteFile{{Path: "/etc/motd", Permissions: "17777"}}, "are not an octal mode"},
	} {
		c := btrfsConfig()
		c.WriteFiles = tc.files
		err := c.Validate()
		switch {
		case tc.want == "" && err != nil:
			t.Errorf("%s: %v", name, err)
		case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
			t.Errorf("%s: error %v, want %q", name, err, tc.want)
		}
	}
	for perm, want := range map[string]os.FileMode{
		"": 0o644, "600": 0o600, "0755": 0o755,
		"4755": 0o755 | os.ModeSetuid, "2750": 0o750 | os.ModeSetgid, "1777": 0o777 | os.ModeSticky,
	} {
		if got, err := (&WriteFile{Permissions: perm}).Mode(); err != nil || got != want {
			t.Errorf("Mode(%q) = %v, %v; want %v", perm, got, err, want)
		}
	}
}
