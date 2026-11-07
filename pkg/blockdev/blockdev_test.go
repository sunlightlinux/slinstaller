package blockdev

import (
	"fmt"
	"strings"
	"testing"
)

// Two shapes of lsblk -J -b output: size as a number (newer lsblk) and
// as a quoted string (older). Both must parse.
const sampleNumber = `{"blockdevices":[
  {"name":"sda","path":"/dev/sda","size":10737418240,"type":"disk","model":"QEMU HARDDISK","children":[
    {"name":"sda1","path":"/dev/sda1","size":536870912,"type":"part","model":null},
    {"name":"sda2","path":"/dev/sda2","size":10200547328,"type":"part","model":null}
  ]},
  {"name":"sr0","path":"/dev/sr0","size":0,"type":"rom","model":"QEMU DVD-ROM"}
]}`

const sampleString = `{"blockdevices":[
  {"name":"vda","path":"/dev/vda","size":"8589934592","type":"disk","model":null}
]}`

func TestParseNumber(t *testing.T) {
	devs, err := parse([]byte(sampleNumber))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(devs) != 2 {
		t.Fatalf("got %d top devices, want 2", len(devs))
	}

	disks := Disks(devs)
	if len(disks) != 1 || disks[0].Name != "sda" {
		t.Fatalf("Disks = %v, want just sda", disks)
	}
	if disks[0].Size != 10737418240 {
		t.Errorf("sda size = %d", disks[0].Size)
	}
	if len(disks[0].Children) != 2 {
		t.Errorf("sda has %d partitions, want 2", len(disks[0].Children))
	}
}

func TestParseString(t *testing.T) {
	devs, err := parse([]byte(sampleString))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if devs[0].Size != 8589934592 {
		t.Errorf("vda size = %d, want 8589934592", devs[0].Size)
	}
}

func TestFindByPath(t *testing.T) {
	devs, _ := parse([]byte(sampleNumber))

	if d := FindByPath(devs, "/dev/sda"); d == nil || d.Type != "disk" {
		t.Errorf("FindByPath(/dev/sda) = %v", d)
	}
	// A partition is found, and is not a whole disk.
	if d := FindByPath(devs, "/dev/sda1"); d == nil || d.Type != "part" {
		t.Errorf("FindByPath(/dev/sda1) = %v", d)
	}
	if d := FindByPath(devs, "/dev/nope"); d != nil {
		t.Errorf("FindByPath(/dev/nope) = %v, want nil", d)
	}
}

// A machine after an earlier RAID1 + LVM-on-LUKS install, booted from a
// live USB: vda and vdb mirror /boot (md127), vda2 holds a LUKS volume
// carrying the vg0 physical volume, and sda is the live medium.
const sampleInUse = `{"blockdevices":[
  {"name":"sda","path":"/dev/sda","size":4000000000,"type":"disk","model":"USB","fstype":"iso9660","label":"SUNLIGHT_LIVE","mountpoint":null,"children":[
    {"name":"sda1","path":"/dev/sda1","size":3000000000,"type":"part","fstype":"iso9660","mountpoint":"/run/initramfs/live"}
  ]},
  {"name":"vda","path":"/dev/vda","size":10737418240,"type":"disk","children":[
    {"name":"vda1","path":"/dev/vda1","size":536870912,"type":"part","fstype":"linux_raid_member","children":[
      {"name":"md127","path":"/dev/md127","size":535822336,"type":"raid1","fstype":"vfat"}
    ]},
    {"name":"vda2","path":"/dev/vda2","size":10200547328,"type":"part","fstype":"crypto_LUKS","children":[
      {"name":"cryptroot","path":"/dev/mapper/cryptroot","size":10183770112,"type":"crypt","fstype":"LVM2_member","children":[
        {"name":"vg0-root","path":"/dev/mapper/vg0-root","size":9000000000,"type":"lvm","fstype":"ext4"}
      ]}
    ]}
  ]},
  {"name":"vdb","path":"/dev/vdb","size":10737418240,"type":"disk","children":[
    {"name":"vdb1","path":"/dev/vdb1","size":536870912,"type":"part","fstype":"linux_raid_member","children":[
      {"name":"md127","path":"/dev/md127","size":535822336,"type":"raid1","fstype":"vfat"}
    ]},
    {"name":"vdb2","path":"/dev/vdb2","size":10200547328,"type":"part","fstype":"LVM2_member"}
  ]},
  {"name":"vdc","path":"/dev/vdc","size":10737418240,"type":"disk","children":[
    {"name":"vdc1","path":"/dev/vdc1","size":2147483648,"type":"part","fstype":"swap","mountpoint":"[SWAP]"}
  ]}
]}`

func inUseDevs(t *testing.T) []Device {
	t.Helper()
	devs, err := parse([]byte(sampleInUse))
	if err != nil {
		t.Fatal(err)
	}
	return devs
}

// The live medium and a disk with active swap are not offered; the old
// install's disks are (they are not mounted), to be released by Teardown.
func TestInUse(t *testing.T) {
	devs := inUseDevs(t)
	want := map[string]string{
		"/dev/sda": "holds the live medium",
		"/dev/vda": "",
		"/dev/vdb": "",
		"/dev/vdc": "in use as swap",
	}
	for _, d := range Disks(devs) {
		if got := d.InUse(); (want[d.Path] == "") != (got == "") || !strings.Contains(got, want[d.Path]) {
			t.Errorf("%s: InUse = %q, want %q", d.Path, got, want[d.Path])
		}
	}
	var avail []string
	for _, d := range Available(devs) {
		avail = append(avail, d.Path)
	}
	if strings.Join(avail, " ") != "/dev/vda /dev/vdb" {
		t.Errorf("Available = %v, want vda and vdb", avail)
	}
	// A mounted partition alone is enough.
	d := Device{Path: "/dev/x", Type: "disk", Children: []Device{{Path: "/dev/x1", Type: "part", Mountpoint: "/home"}}}
	if got := d.InUse(); !strings.Contains(got, "/dev/x1 is mounted at /home") {
		t.Errorf("mounted partition: %q", got)
	}
}

// Teardown releases the deepest holders first, then erases signatures.
func TestTeardown(t *testing.T) {
	devs := inUseDevs(t)
	got := Teardown(*FindByPath(devs, "/dev/vda"))
	want := [][]string{
		{"mdadm", "--stop", "/dev/md127"},
		{"dmsetup", "remove", "vg0-root"},
		{"dmsetup", "remove", "cryptroot"},
		{"wipefs", "-a", "/dev/vda1"},
		{"wipefs", "-a", "/dev/vda2"},
		{"wipefs", "-a", "/dev/vda"},
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("Teardown(vda) =\n%v\nwant\n%v", got, want)
	}
	// A blank disk: only its own signatures.
	if got := Teardown(Device{Path: "/dev/vde", Type: "disk"}); fmt.Sprint(got) != "[[wipefs -a /dev/vde]]" {
		t.Errorf("blank disk: %v", got)
	}
}

// Wiping vda alone would break the mirror on vdb and the volume group with
// a physical volume on vdb; wiping both shares nothing outside the install.
func TestShared(t *testing.T) {
	devs := inUseDevs(t)
	vgs := parseVolumeGroups("  /dev/mapper/cryptroot:vg0\n  /dev/vdb2:vg0\n  /dev/sdz1:other\n")
	got := Shared(devs, "/dev/vda", []string{"/dev/vda"}, vgs)
	if len(got) != 2 || !strings.Contains(got[0], "/dev/md127 (raid1) also uses /dev/vdb") ||
		!strings.Contains(got[1], "volume group vg0 also has physical volumes on /dev/vdb") {
		t.Errorf("Shared(vda) = %q", got)
	}
	if got := Shared(devs, "/dev/vda", []string{"/dev/vda", "/dev/vdb"}, vgs); len(got) != 0 {
		t.Errorf("wiping both disks: Shared = %q, want nothing", got)
	}
	if got := Shared(devs, "/dev/vdc", []string{"/dev/vdc"}, vgs); len(got) != 0 {
		t.Errorf("a disk with no arrays or volumes: %q", got)
	}
}
