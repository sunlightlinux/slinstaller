package blockdev

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeTool puts a stand-in command first on PATH, so the wrappers around
// lsblk and pvs are tested without the tools or any device.
func fakeTool(t *testing.T, name, script string) string {
	t.Helper()
	dir := t.TempDir()
	body := "#!/bin/sh\necho \"$@\" >> \"" + dir + "/args\"\n" + script + "\n"
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return dir
}

func TestListRunsLsblk(t *testing.T) {
	dir := fakeTool(t, "lsblk", "cat <<'JSON'\n"+sampleNumber+"\nJSON")
	devs, err := List()
	if err != nil || len(devs) != 2 || devs[0].Path != "/dev/sda" || len(devs[0].Children) != 2 {
		t.Fatalf("List = %+v, %v", devs, err)
	}
	// JSON, sizes in bytes, and the columns the installer reads.
	args, _ := os.ReadFile(filepath.Join(dir, "args"))
	if got := strings.TrimSpace(string(args)); got != "-J -b -o NAME,PATH,SIZE,TYPE,MODEL,MOUNTPOINT,FSTYPE,LABEL" {
		t.Errorf("lsblk called with %q", got)
	}
}

func TestListFailures(t *testing.T) {
	// lsblk failing, or printing something that is not its JSON, is an error
	// — never an empty list, which would read as "no disks".
	fakeTool(t, "lsblk", "exit 1")
	if devs, err := List(); err == nil || devs != nil {
		t.Errorf("List with a failing lsblk = %v, %v", devs, err)
	}
	fakeTool(t, "lsblk", "echo 'not json'")
	if _, err := List(); err == nil || !strings.Contains(err.Error(), "parse lsblk") {
		t.Errorf("List with junk output: %v", err)
	}
}

func TestSizeFormsAndBadSizes(t *testing.T) {
	// A missing size reads as 0, whether null or an empty string.
	devs, err := parse([]byte(`{"blockdevices":[
	  {"name":"sr0","path":"/dev/sr0","size":null,"type":"rom"},
	  {"name":"sr1","path":"/dev/sr1","size":"","type":"rom"}]}`))
	if err != nil || len(devs) != 2 || devs[0].Size != 0 || devs[1].Size != 0 {
		t.Errorf("null/empty size: %+v, %v", devs, err)
	}
	// A size that is not a number is refused rather than read as 0, which
	// would let a layout through the disk-fit check.
	if _, err := parse([]byte(`{"blockdevices":[{"name":"sda","path":"/dev/sda","size":"10G","type":"disk"}]}`)); err == nil {
		t.Error("a non-numeric size was accepted")
	}
}

func TestSharedOnAnUnknownDisk(t *testing.T) {
	devs, _ := parse([]byte(sampleNumber))
	if got := Shared(devs, "/dev/nope", nil, nil); got != nil {
		t.Errorf("Shared for a disk that is not there = %v", got)
	}
}

func TestVolumeGroups(t *testing.T) {
	fakeTool(t, "pvs", `echo "  /dev/sda2:vg0"; echo "  /dev/sdb1:vg0"; echo "  /dev/sdc1:"; echo "  /dev/sdd1:data"`)
	vgs := VolumeGroups()
	if len(vgs) != 2 || strings.Join(vgs["vg0"], ",") != "/dev/sda2,/dev/sdb1" || strings.Join(vgs["data"], ",") != "/dev/sdd1" {
		t.Errorf("VolumeGroups = %v", vgs)
	}
	// No LVM tools, or pvs failing: no groups, so nothing a wipe could break.
	fakeTool(t, "pvs", "exit 5")
	if vgs := VolumeGroups(); vgs != nil {
		t.Errorf("VolumeGroups with a failing pvs = %v", vgs)
	}
}
