package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sunlightlinux/slinstaller/pkg/config"
)

const existingLayout = `{"name":"sda","path":"/dev/sda","size":512000000000,"type":"disk","serial":" S5GX123 ","children":[
	{"name":"sda1","path":"/dev/sda1","size":536870912,"type":"part","fstype":"vfat","label":"EFI"},
	{"name":"sda2","path":"/dev/sda2","size":107374182400,"type":"part","fstype":"ntfs","label":"Windows"},
	{"name":"sda3","path":"/dev/sda3","size":53687091200,"type":"part","fstype":"ext4","label":"data"},
	{"name":"sda4","path":"/dev/sda4","size":10737418240,"type":"part","fstype":"crypto_LUKS","children":[
		{"name":"secret","path":"/dev/mapper/secret","size":1,"type":"crypt"}]},
	{"name":"sda5","path":"/dev/sda5","size":1048576,"type":"part"}]},
	{"name":"sdb","path":"/dev/sdb","size":1000,"type":"disk"},
	{"name":"sr0","path":"/dev/sr0","size":1000,"type":"rom"}`

// --discover writes down what is on a disk as a config that keeps all of
// it: a starting point the operator edits, which destroys nothing as it is.
func TestDiscoverDescribesTheExistingLayout(t *testing.T) {
	dir := t.TempDir()
	bin := onlyTools(t, dir)
	setListing(t, bin, existingLayout)

	out, notes, err := runWith(t, "", "--discover", "/dev/sda")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "found.yaml")
	os.WriteFile(path, []byte(out), 0o600)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("the discovered config does not load: %v\n%s", err, out)
	}
	if !cfg.ReusePartitions {
		t.Error("a discovered config must not repartition the disk")
	}
	d := cfg.TargetMedia[0]
	if d.Name != "sda" || d.Match == nil || d.Match.Serial != "S5GX123" || len(d.Children) != 5 {
		t.Fatalf("disk = %+v with %d partitions", d, len(d.Children))
	}
	for _, ch := range d.Children {
		if !ch.Preserve || ch.Mountpoint != "" {
			t.Errorf("%s: preserve %v mountpoint %q — nothing may be formatted or mounted until the operator says so", ch.Name, ch.Preserve, ch.Mountpoint)
		}
	}
	if d.Children[0].FsType != "vfat" || d.Children[0].Size != "512M" || d.Children[0].Label != "EFI" {
		t.Errorf("ESP = %+v", d.Children[0])
	}
	if d.Children[2].FsType != "ext4" || d.Children[2].Size != "51200M" {
		t.Errorf("data = %+v", d.Children[2])
	}
	// What cannot be used as it is has no filesystem named, and a note.
	if d.Children[1].FsType != "" || d.Children[3].FsType != "" {
		t.Errorf("unusable filesystems were carried over: %q, %q", d.Children[1].FsType, d.Children[3].FsType)
	}
	for _, want := range []string{
		"/dev/sda1 is vfat: if it is the EFI System Partition",
		"/dev/sda2 holds ntfs",
		"/dev/sda4 holds crypto_LUKS",
		"/dev/sda4 carries other devices",
		"/dev/sda5 has no filesystem slinstaller recognises",
	} {
		if !strings.Contains(notes, want) {
			t.Errorf("notes missing %q:\n%s", want, notes)
		}
	}
	// It is not installable until edited: there is no root yet.
	if err := cfg.Validate(); err == nil {
		t.Error("the untouched discovered config validates as an install")
	}
	// Edited as its header says, it plans an install that formats only the
	// partition given to it.
	d.Children[0].Mountpoint = "/boot/efi"
	d.Children[2].Mountpoint, d.Children[2].Preserve = "/", false
	d.Children = []*config.Media{d.Children[0], d.Children[1], d.Children[2]}
	d.Children[1].FsType = "ext4" // stands in for a partition kept and not mounted
	cfg.Users = []*config.User{{Login: "ion", Password: "$6$a$b", Admin: true}}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("edited config: %v", err)
	}
	plan, err := cfg.Plan()
	if err != nil || plan.Wipe {
		t.Fatalf("plan: %v wipe %v", err, plan.Wipe)
	}
	for _, p := range plan.Parts {
		if p.Preserve != (p.Device != "/dev/sda3") {
			t.Errorf("%s: preserve = %v", p.Device, p.Preserve)
		}
	}

	// An empty disk, something that is not a disk, and a disk that is absent.
	_, notes, err = runWith(t, "", "--discover", "/dev/sdb")
	if err != nil || !strings.Contains(notes, "/dev/sdb has no partitions") {
		t.Errorf("empty disk: %v\n%s", err, notes)
	}
	_, _, err = runWith(t, "", "--discover", "/dev/sda1")
	wantRunErr(t, "a partition", err, "is a part, not a whole disk")
	_, _, err = runWith(t, "", "--discover", "/dev/sdz")
	wantRunErr(t, "absent", err, "is not among the machine's block devices")
	os.Remove(filepath.Join(bin, "lsblk.json"))
	_, _, err = runWith(t, "", "--discover", "/dev/sda")
	wantRunErr(t, "no listing", err, "lsblk")
}
