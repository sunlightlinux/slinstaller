package main

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/sunlightlinux/slinstaller/pkg/config"
)

// FuzzRedactedConfig checks the config in a diagnostics archive — handed
// to someone else — never holds a secret it carries: a password hash, the
// password of a proxy, mirror, report, image or disk-image URL, a secret
// env value or a written file's content, whatever the secret is.
func FuzzRedactedConfig(f *testing.F) {
	f.Add("s3cr3t-pw", "API_TOKEN")
	f.Add("p@ss#w?rd", "dbPassword")
	f.Add("a: b\n- c", "WIFI_PSK")
	f.Fuzz(func(t *testing.T, secret, envName string) {
		// A secret short or common enough to turn up in the YAML anyway
		// (a key, "true", a quote) tells nothing; nor one URL syntax
		// cannot carry in a password.
		if len(secret) < 6 || strings.ContainsAny(secret, "/ \t\r\n\"'\\") || !config.SecretName(envName) || nameHolds(envName, secret) {
			t.Skip()
		}
		for _, r := range secret {
			if r < 0x21 || r > 0x7e {
				t.Skip()
			}
		}
		cfg := &config.Config{
			Users:       []*config.User{{Login: "u", Password: "$6$salt$" + secret}},
			HTTPSProxy:  "http://user:" + secret + "@proxy:3128",
			HTTPProxy:   "user:" + secret + "@proxy:3128",
			SwupdMirror: "https://user:" + secret + "@mirror.example/current",
			ReportURL:   "https://user:" + secret + "@report.example/r",
			Image:       "https://user:" + secret + "@img.example/root.tar",
			DiskImage:   "https://user:" + secret + "@img.example/disk.raw",
			Environment: map[string]string{envName: secret, "MIRROR_URL": "https://user:" + secret + "@m.example/x"},
			WriteFiles:  []*config.WriteFile{{Path: "/etc/x", Content: "key=" + secret}},
		}
		if got := string(redactedConfig(cfg)); strings.Contains(got, secret) {
			t.Errorf("secret %q in:\n%s", secret, got)
		}
	})
}

// nameHolds reports whether name, as YAML writes it (escaped, or base64 as
// !!binary), holds secret: the name is not redacted, so the secret is then
// in the archive by the name, not by a leak.
func nameHolds(name, secret string) bool {
	data, _ := yaml.Marshal(map[string]string{name: ""})
	return strings.Contains(string(data), secret) || strings.Contains(name, secret)
}
