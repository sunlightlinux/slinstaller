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
	// What each partition holds is written down as it is — the install
	// checks a kept partition against it — with a note on what can be done
	// with the ones slinstaller cannot install to.
	if d.Children[1].FsType != "ntfs" || d.Children[3].FsType != "crypto_LUKS" {
		t.Errorf("filesystems: %q, %q", d.Children[1].FsType, d.Children[3].FsType)
	}
	for _, want := range []string{
		"/dev/sda1 is vfat: if it is the EFI System Partition",
		"/dev/sda2 holds ntfs: it can be kept, and shrunk with resize",
		"/dev/sda4 holds crypto_LUKS, which can only be kept as it is",
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
	// The Windows partition stays in, kept and untouched, and may be shrunk.
	d.Children = []*config.Media{d.Children[0], d.Children[1], d.Children[2]}
	d.Children[1].Resize = "60G"
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

// A layout prepared by hand and named for its roles in the partition table
// (clr-installer's convention) comes out of --discover ready to install: the
// names say what is mounted where and what is formatted.
func TestDiscoverHonoursRoleLabels(t *testing.T) {
	dir := t.TempDir()
	bin := onlyTools(t, dir)
	setListing(t, bin, `{"name":"sda","path":"/dev/sda","size":512000000000,"type":"disk","children":[
		{"name":"sda1","path":"/dev/sda1","size":536870912,"type":"part","fstype":"vfat","partlabel":"CLR_BOOT"},
		{"name":"sda2","path":"/dev/sda2","size":4294967296,"type":"part","partlabel":"CLR_SWAP"},
		{"name":"sda3","path":"/dev/sda3","size":53687091200,"type":"part","partlabel":"CLR_ROOT_F"},
		{"name":"sda4","path":"/dev/sda4","size":107374182400,"type":"part","fstype":"xfs","partlabel":"CLR_MNT_/srv/data"},
		{"name":"sda5","path":"/dev/sda5","size":107374182400,"type":"part","fstype":"ext4","partlabel":"CLR_MNT_/home_E_F"},
		{"name":"sda6","path":"/dev/sda6","size":1073741824,"type":"part","fstype":"ext4","partlabel":"CLR_SOMETHING"},
		{"name":"sda7","path":"/dev/sda7","size":1073741824,"type":"part","fstype":"ext4","partlabel":"Linux filesystem"}]}`)
	out, notes, err := runWith(t, "", "--discover", "/dev/sda")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "found.yaml")
	os.WriteFile(path, []byte(out), 0o600)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	ch := cfg.TargetMedia[0].Children
	type want struct {
		fs, mount string
		preserve  bool
	}
	for i, w := range []want{
		{"vfat", "/boot", true},    // the ESP, kept
		{"swap", "", true},         //
		{"ext4", "/", false},       // _F: formatted, and nothing was there to say as what
		{"xfs", "/srv/data", true}, // kept
		{"ext4", "/home", false},   // _F wins; _E is said to be unsupported here
		{"ext4", "", true},         // a name that is no role
		{"ext4", "", true},         // an ordinary name
	} {
		if ch[i].FsType != w.fs || ch[i].Mountpoint != w.mount || ch[i].Preserve != w.preserve {
			t.Errorf("%s = fstype %q mountpoint %q preserve %v, want %+v", ch[i].Name, ch[i].FsType, ch[i].Mountpoint, ch[i].Preserve, w)
		}
	}
	for _, n := range []string{
		"/dev/sda3 is named CLR_ROOT_F: the root filesystem, to be FORMATTED as ext4",
		"/dev/sda1 is named CLR_BOOT: the EFI System Partition, mounted at /boot, kept as it is",
		"/dev/sda5 is named CLR_MNT_/home_E_F: mounted at /home, to be FORMATTED; it asks for encryption",
		"/dev/sda6 is named CLR_SOMETHING: not a role slinstaller knows",
	} {
		if !strings.Contains(notes, n) {
			t.Errorf("notes missing %q:\n%s", n, notes)
		}
	}
	// Only what the names say is formatted — and with users added, the
	// config is an install.
	cfg.Users = []*config.User{{Login: "ion", Password: "$6$a$b", Admin: true}}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("the labelled layout does not validate: %v", err)
	}
	plan, _ := cfg.Plan()
	for _, p := range plan.Parts {
		formatted := p.Device == "/dev/sda3" || p.Device == "/dev/sda5"
		if p.Preserve == formatted {
			t.Errorf("%s: preserve = %v", p.Device, p.Preserve)
		}
	}
}
