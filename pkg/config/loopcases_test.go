package config

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The CI loop job (tests/loop/run.sh) installs real configs as root, and
// only there: a validation rule its configs break turned the job red with
// nothing in the unit suite saying so (a created root held to the 2 GB
// minimum, against the 1000M one case 3a cut). Every config it writes is
// one it expects to pass validation - a refusal it tests comes later, from
// the disk itself.
func TestLoopHarnessConfigsValidate(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "tests", "loop", "run.sh"))
	if err != nil {
		t.Fatal(err)
	}
	blocks := regexp.MustCompile(`(?ms)cat > "\$WORK/([^"]+)" <<YAML\n(.*?)\nYAML$`).FindAllStringSubmatch(string(data), -1)
	if len(blocks) < 5 {
		t.Fatalf("found %d configs in run.sh; the heredoc form changed?", len(blocks))
	}
	for _, b := range blocks {
		// As the script expands them: the disks' names, and the loop's $n
		// (create2/create3), for which 3 is the case that installs.
		body := strings.NewReplacer("${name2}", "loop1", "$name2", "loop1", "${name}", "loop0", "$name", "loop0").Replace(b[2])
		body = strings.ReplaceAll(body, "$n", "3")
		path := filepath.Join(t.TempDir(), "c.yaml")
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		c, err := Load(path)
		if err != nil {
			t.Errorf("%s: %v", b[1], err)
			continue
		}
		if err := c.Validate(); err != nil {
			t.Errorf("%s does not validate: %v", b[1], err)
		}
	}
}
