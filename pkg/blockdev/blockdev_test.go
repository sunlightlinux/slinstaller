package blockdev

import "testing"

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
