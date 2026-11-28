package blockdev

import (
	"fmt"
	"os"
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

// An array across two disks is listed under both; torn down together it is
// stopped once, before either disk's signatures go.
func TestTeardownAllReleasesASharedArrayOnce(t *testing.T) {
	md := Device{Name: "md127", Path: "/dev/md127", Type: "raid1", Children: []Device{
		{Name: "vg-root", Path: "/dev/mapper/vg-root", Type: "lvm"}}}
	disk := func(n string) Device {
		return Device{Name: n, Path: "/dev/" + n, Type: "disk", Children: []Device{
			{Name: n + "1", Path: "/dev/" + n + "1", Type: "part", Children: []Device{md}}}}
	}
	var lines []string
	for _, c := range TeardownAll([]Device{disk("sda"), disk("sdb")}) {
		lines = append(lines, strings.Join(c, " "))
	}
	want := []string{
		"dmsetup remove vg-root", "mdadm --stop /dev/md127",
		"wipefs -a /dev/sda1", "wipefs -a /dev/sda", "wipefs -a /dev/sdb1", "wipefs -a /dev/sdb",
	}
	if strings.Join(lines, "\n") != strings.Join(want, "\n") {
		t.Errorf("TeardownAll:\n%s\nwant:\n%s", strings.Join(lines, "\n"), strings.Join(want, "\n"))
	}
}

// An Intel RST array: the volume, then the container beside it (whatever
// order lsblk lists them in), before any signature is erased.
func TestTeardownRSTContainer(t *testing.T) {
	disk := Device{Name: "sda", Path: "/dev/sda", Type: "disk", Size: 1 << 40, Children: []Device{
		{Name: "md127", Path: "/dev/md127", Type: "container"},
		{Name: "md126", Path: "/dev/md126", Type: "raid1"},
	}}
	got := fmt.Sprint(Teardown(disk))
	if got != "[[mdadm --stop /dev/md126] [mdadm --stop /dev/md127] [wipefs -a /dev/sda]]" {
		t.Errorf("teardown: %s", got)
	}
}

// Multipath: the kpartx partition, then the map, are removed.
func TestTeardownMultipath(t *testing.T) {
	mp := Device{Name: "mpatha", Path: "/dev/mapper/mpatha", Type: "mpath", Children: []Device{
		{Name: "mpatha1", Path: "/dev/mapper/mpatha1", Type: "part", FsType: "ext4"}}}
	sda := Device{Name: "sda", Path: "/dev/sda", Type: "disk", Size: 1 << 40, Children: []Device{mp}}
	sdb := Device{Name: "sdb", Path: "/dev/sdb", Type: "disk", Size: 1 << 40, Children: []Device{mp}}
	if got := fmt.Sprint(Teardown(sda)); got != "[[dmsetup remove mpatha1] [dmsetup remove mpatha] [wipefs -a /dev/sda]]" {
		t.Errorf("teardown: %s", got)
	}
	// sda and sdb are two paths to one LUN: not "shared" with itself.
	if got := Shared([]Device{sda, sdb}, "/dev/sda", []string{"/dev/sda"}, nil); len(got) != 0 {
		t.Errorf("one LUN refused as shared with itself: %v", got)
	}
}

func TestInstallable(t *testing.T) {
	for _, d := range []Device{{Name: "zram0", Size: 8 << 30}, {Name: "mmcblk0boot0", Size: 4 << 20}, {Name: "mmcblk0rpmb", Size: 4 << 20}, {Name: "sdc"}} {
		if Installable(d) {
			t.Errorf("%s offered", d.Name)
		}
	}
	if !Installable(Device{Name: "mmcblk0", Size: 64 << 30}) || !Installable(Device{Name: "nvme0n1", Size: 1 << 40}) {
		t.Error("a real disk refused")
	}
}

// Members on disk, assembled or not, of something with a member outside
// what is destroyed are reported; the whole of it inside is not.
func TestSharedMembers(t *testing.T) {
	devs := []Device{
		{Name: "sda", Path: "/dev/sda", Type: "disk", Size: 1 << 40, Children: []Device{
			{Name: "sda1", Path: "/dev/sda1", Type: "part", FsType: "linux_raid_member", UUID: "r1"},
			{Name: "sda2", Path: "/dev/sda2", Type: "part", FsType: "btrfs", UUID: "b1"},
			{Name: "sda3", Path: "/dev/sda3", Type: "part", FsType: "ext4", UUID: "e1"},
		}},
		{Name: "sdz", Path: "/dev/sdz", Type: "disk", Size: 1 << 40, Children: []Device{
			{Name: "sdz1", Path: "/dev/sdz1", Type: "part", FsType: "linux_raid_member", UUID: "r1"},
			{Name: "sdz2", Path: "/dev/sdz2", Type: "part", FsType: "btrfs", UUID: "b1", Mountpoint: "/data"},
		}},
	}
	got := SharedMembers(devs, []string{"/dev/sda"})
	if len(got) != 2 || !strings.Contains(got[0], "/dev/sda1 (linux_raid_member r1) has a fellow member on /dev/sdz1") || !strings.Contains(got[1], "/dev/sdz2") {
		t.Errorf("wiping sda: %q", got)
	}
	if got := SharedMembers(devs, []string{"/dev/sda", "/dev/sdz"}); len(got) != 0 {
		t.Errorf("both wiped: %q", got)
	}
	if got := SharedMembers(devs, []string{"/dev/sda3"}); len(got) != 0 {
		t.Errorf("a plain ext4: %q", got)
	}
	// A reuse install formatting one member of an array on the same disk.
	devs[1].Children[0].UUID = "other"
	devs[0].Children = append(devs[0].Children, Device{Name: "sda4", Path: "/dev/sda4", Type: "part", FsType: "linux_raid_member", UUID: "r1"})
	if got := SharedMembers(devs, []string{"/dev/sda1"}); len(got) != 1 || !strings.Contains(got[0], "/dev/sda4") {
		t.Errorf("formatting sda1: %q", got)
	}
}

// One LUN through two paths: its map's partitions, seen under both, are one
// device, not fellow members.
func TestSharedMembersMultipath(t *testing.T) {
	mp := Device{Name: "mpatha", Path: "/dev/mapper/mpatha", Type: "mpath", Children: []Device{
		{Name: "mpatha-part2", Path: "/dev/mapper/mpatha-part2", Type: "part", FsType: "linux_raid_member", UUID: "r"}}}
	devs := []Device{
		{Name: "sda", Path: "/dev/sda", Type: "disk", Size: 1 << 40, Children: []Device{mp}},
		{Name: "sdb", Path: "/dev/sdb", Type: "disk", Size: 1 << 40, Children: []Device{mp}},
	}
	if got := SharedMembers(devs, []string{"/dev/sda"}); len(got) != 0 {
		t.Errorf("one LUN shared with itself: %q", got)
	}
}

// A member signature with no UUID read yet is nobody's fellow, a UUID
// shared across different signature types is not one array, and a ZFS
// pool's members are members like any other.
func TestSharedMembersUUIDAndTypes(t *testing.T) {
	disk := func(n string, kids ...Device) Device {
		return Device{Name: n, Path: "/dev/" + n, Type: "disk", Size: 1 << 40, Children: kids}
	}
	part := func(n, fs, uuid string) Device {
		return Device{Name: n, Path: "/dev/" + n, Type: "part", FsType: fs, UUID: uuid}
	}
	devs := []Device{
		disk("sda", part("sda1", "btrfs", ""), part("sda2", "linux_raid_member", "u"), part("sda3", "zfs_member", "z")),
		disk("sdz", part("sdz1", "btrfs", ""), part("sdz2", "ext4", "u"), part("sdz3", "zfs_member", "z")),
	}
	got := SharedMembers(devs, []string{"/dev/sda"})
	if len(got) != 1 || !strings.Contains(got[0], "/dev/sda3 (zfs_member z) has a fellow member on /dev/sdz3") {
		t.Errorf("SharedMembers = %q, want only the ZFS pool", got)
	}
}

// One LUN through two paths, with a volume group on its partition: the
// group draws on the LUN alone, not on a second disk.
func TestSharedMultipathHolder(t *testing.T) {
	mp := Device{Name: "mpatha", Path: "/dev/mapper/mpatha", Type: "mpath", Children: []Device{
		{Name: "mpatha1", Path: "/dev/mapper/mpatha1", Type: "part", Children: []Device{
			{Name: "vg-root", Path: "/dev/mapper/vg-root", Type: "lvm"}}}}}
	sda := Device{Name: "sda", Path: "/dev/sda", Type: "disk", Size: 1 << 40, Children: []Device{mp}}
	sdb := Device{Name: "sdb", Path: "/dev/sdb", Type: "disk", Size: 1 << 40, Children: []Device{mp}}
	if got := Shared([]Device{sda, sdb}, "/dev/sda", []string{"/dev/sda"}, nil); len(got) != 0 {
		t.Errorf("a volume on one LUN refused as shared with itself: %q", got)
	}
	// Both paths torn down together: each map is removed once.
	got := fmt.Sprint(TeardownAll([]Device{sda, sdb}))
	if got != "[[dmsetup remove vg-root] [dmsetup remove mpatha1] [dmsetup remove mpatha] [wipefs -a /dev/sda] [wipefs -a /dev/sdb]]" {
		t.Errorf("TeardownAll over two paths: %s", got)
	}
}

// Each kind of holder is released with its own tool: an md array (linear
// too) is stopped, a device-mapper volume (dmraid's partitions as well)
// removed; lsblk's plain "raid" is not an array type and is left alone.
func TestTeardownHolderKinds(t *testing.T) {
	disk := Device{Name: "sda", Path: "/dev/sda", Type: "disk", Size: 1 << 40, Children: []Device{
		{Name: "sda1", Path: "/dev/sda1", Type: "part", Children: []Device{
			{Name: "md0", Path: "/dev/md0", Type: "linear"},
			{Name: "md1", Path: "/dev/md1", Type: "raid"},
			{Name: "thing", Path: "/dev/mapper/thing", Type: "dm"}}},
		{Name: "isw_x", Path: "/dev/mapper/isw_x", Type: "dmraid", Children: []Device{
			{Name: "isw_x1", Path: "/dev/mapper/isw_x1", Type: "part"}}},
	}}
	got := fmt.Sprint(Teardown(disk))
	want := "[[mdadm --stop /dev/md0] [dmsetup remove thing] [dmsetup remove isw_x1] [dmsetup remove isw_x] [wipefs -a /dev/sda1] [wipefs -a /dev/sda]]"
	if got != want {
		t.Errorf("Teardown =\n%s\nwant\n%s", got, want)
	}
}

// A pvs line with no physical volume names nothing.
func TestParseVolumeGroupsNoPV(t *testing.T) {
	if vgs := parseVolumeGroups("  :vg0\n  /dev/sda2:vg1\n"); len(vgs) != 1 || len(vgs["vg1"]) != 1 {
		t.Errorf("parseVolumeGroups = %v", vgs)
	}
}

// A device is found by its path, or by /dev/ and its name where lsblk gave
// no path (an older lsblk without -p); a device-mapper volume by its
// /dev/mapper path, which is not /dev/ and its name.
func TestFindByPathOrName(t *testing.T) {
	devs := []Device{
		{Name: "sda", Type: "disk", Children: []Device{{Name: "sda1", Type: "part"}}},
		{Name: "nvme0n1", Path: "/dev/nvme0n1", Type: "disk", Children: []Device{
			{Name: "vg-root", Path: "/dev/mapper/vg-root", Type: "lvm"}}},
	}
	for path, name := range map[string]string{"/dev/sda": "sda", "/dev/sda1": "sda1", "/dev/mapper/vg-root": "vg-root", "/dev/nvme0n1": "nvme0n1"} {
		if d := FindByPath(devs, path); d == nil || d.Name != name {
			t.Errorf("FindByPath(%s) = %+v, want %s", path, d, name)
		}
	}
	if d := FindByPath(devs, "/dev/sdb"); d != nil {
		t.Errorf("FindByPath(/dev/sdb) = %+v, want nil", d)
	}
}

// An array with two members on the disk is listed under each, and reported
// once: an operator reads each line as one more thing broken.
func TestSharedReportsAnArrayOnce(t *testing.T) {
	md := Device{Name: "md0", Path: "/dev/md0", Type: "raid5"}
	part := func(n string) Device { return Device{Name: n, Path: "/dev/" + n, Type: "part", Children: []Device{md}} }
	devs := []Device{
		{Name: "sda", Path: "/dev/sda", Type: "disk", Children: []Device{part("sda1"), part("sda2")}},
		{Name: "sdb", Path: "/dev/sdb", Type: "disk", Children: []Device{part("sdb1")}},
	}
	got := Shared(devs, "/dev/sda", []string{"/dev/sda"}, nil)
	if len(got) != 1 || got[0] != "/dev/md0 (raid5) also uses /dev/sdb" {
		t.Errorf("Shared = %q", got)
	}
}

// A member seen twice inside the targets (a multipath partition, under each
// path to its LUN, both wiped) with a fellow outside is reported once.
func TestSharedMembersReportsAMemberOnce(t *testing.T) {
	mp := Device{Name: "mpatha", Path: "/dev/mapper/mpatha", Type: "mpath", Children: []Device{
		{Name: "mpatha1", Path: "/dev/mapper/mpatha1", Type: "part", FsType: "linux_raid_member", UUID: "r"}}}
	devs := []Device{
		{Name: "sda", Path: "/dev/sda", Type: "disk", Children: []Device{mp}},
		{Name: "sdb", Path: "/dev/sdb", Type: "disk", Children: []Device{mp}},
		{Name: "sdz", Path: "/dev/sdz", Type: "disk", Children: []Device{
			{Name: "sdz1", Path: "/dev/sdz1", Type: "part", FsType: "linux_raid_member", UUID: "r"}}},
	}
	got := SharedMembers(devs, []string{"/dev/sda", "/dev/sdb"})
	if len(got) != 1 || !strings.Contains(got[0], "/dev/mapper/mpatha1 (linux_raid_member r) has a fellow member on /dev/sdz1 ") {
		t.Errorf("SharedMembers = %q", got)
	}
}

// A volume group the install creates under a name a group already has —
// on a disk it does not wipe — cannot be created: vgcreate failed after the
// target was wiped ("A volume group called vg0 already exists"), and two
// vg0s would have left rd.lvm.lv=vg0/root ambiguous anyway.
func TestVGNameClashes(t *testing.T) {
	devs := []Device{
		{Name: "sda", Path: "/dev/sda", Type: "disk", Children: []Device{
			{Name: "sda2", Path: "/dev/sda2", Type: "part", Children: []Device{
				{Name: "cryptold", Path: "/dev/mapper/cryptold", Type: "crypt"}}}}},
		{Name: "sdb", Path: "/dev/sdb", Type: "disk", Children: []Device{
			{Name: "sdb1", Path: "/dev/sdb1", Type: "part"}}},
	}
	vgs := parseVolumeGroups("  /dev/mapper/cryptold:old\n  /dev/sdb1:vg0\n  /dev/sda2:both\n  /dev/sdb1:both\n  /dev/sdq9:ghost\n")
	got := VGNameClashes(devs, vgs, []string{"old", "vg0", "both", "ghost", "fresh"}, []string{"/dev/sda"})
	want := []string{
		"volume group both already exists, on /dev/sdb1",
		"volume group ghost already exists, on /dev/sdq9",
		"volume group vg0 already exists, on /dev/sdb1",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("clashes:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	// Every disk it is on wiped: it goes with them, and the name is free.
	if got := VGNameClashes(devs, vgs, []string{"vg0", "old"}, []string{"/dev/sda", "/dev/sdb"}); len(got) != 0 {
		t.Errorf("all wiped: %q", got)
	}
}

// A group on a multipath LUN the install wipes goes with the wipe, though
// lsblk shows the LUN's map under each path to it: the other path is the
// same disk, not one the wipe leaves (Shared counts it the same way).
// Reported, an install over a former install's vg0 on such a LUN was
// refused.
func TestVGNameClashesMultipath(t *testing.T) {
	mp := Device{Name: "mpatha", Path: "/dev/mapper/mpatha", Type: "mpath", Children: []Device{
		{Name: "mpatha2", Path: "/dev/mapper/mpatha2", Type: "part"}}}
	devs := []Device{
		{Name: "sda", Path: "/dev/sda", Type: "disk", Children: []Device{mp}},
		{Name: "sdb", Path: "/dev/sdb", Type: "disk", Children: []Device{mp}},
		{Name: "sdc", Path: "/dev/sdc", Type: "disk", Children: []Device{
			{Name: "sdc1", Path: "/dev/sdc1", Type: "part"}}},
	}
	vgs := parseVolumeGroups("  /dev/mapper/mpatha2:vg0\n  /dev/sdc1:data\n")
	if got := VGNameClashes(devs, vgs, []string{"vg0"}, []string{"/dev/sda"}); len(got) != 0 {
		t.Errorf("the wiped LUN's own group: %q", got)
	}
	// The other way about: the LUN not wiped keeps its group.
	if got := VGNameClashes(devs, vgs, []string{"vg0", "data"}, []string{"/dev/sdc"}); len(got) != 1 || !strings.Contains(got[0], "vg0") {
		t.Errorf("the LUN kept: %q", got)
	}
}

// pvs names a physical volume as lvm.conf's preferred_names make it — a
// /dev/disk/by-id link, a /dev/dm-N — which need not be the path lsblk
// gives it. Not found on any disk, the group counted as kept, and a
// reinstall over the install's own vg0 was refused as a clash with itself,
// with no way past it. The PV's link is followed before it is given up.
func TestVGNameClashesFollowsPVLinks(t *testing.T) {
	saved := resolveDev
	defer func() { resolveDev = saved }()
	links := map[string]string{
		"/dev/disk/by-id/ata-X-part2": "/dev/sda2",
		"/dev/dm-3":                   "/dev/dm-3",
		"/dev/mapper/cryptold":        "/dev/dm-3",
		"/dev/sda2":                   "/dev/sda2",
	}
	resolveDev = func(p string) (string, error) {
		if r, ok := links[p]; ok {
			return r, nil
		}
		return "", os.ErrNotExist
	}
	devs := []Device{
		{Name: "sda", Path: "/dev/sda", Type: "disk", Children: []Device{
			{Name: "sda2", Path: "/dev/sda2", Type: "part", Children: []Device{
				{Name: "cryptold", Path: "/dev/mapper/cryptold", Type: "crypt"}}}}},
	}
	vgs := parseVolumeGroups("  /dev/disk/by-id/ata-X-part2:vg0\n  /dev/dm-3:old\n  /dev/sdq9:ghost\n")
	got := VGNameClashes(devs, vgs, []string{"vg0", "old", "ghost"}, []string{"/dev/sda"})
	if want := "volume group ghost already exists, on /dev/sdq9"; strings.Join(got, "\n") != want {
		t.Errorf("clashes %q, want only %q", got, want)
	}
}

// A firmware RAID member shows no UUID: another disk carrying the same
// kind of metadata is taken for a fellow member; mdadm's own members are
// judged by their UUID, as before.
func TestSharedMembersFirmwareRaid(t *testing.T) {
	devs := []Device{
		{Name: "sda", Path: "/dev/sda", Type: "disk", FsType: "isw_raid_member"},
		{Name: "sdb", Path: "/dev/sdb", Type: "disk", FsType: "isw_raid_member"},
		{Name: "sdc", Path: "/dev/sdc", Type: "disk", FsType: "linux_raid_member"},
	}
	if got := SharedMembers(devs, []string{"/dev/sda"}); len(got) != 1 || !strings.Contains(got[0], "firmware RAID array with /dev/sdb") {
		t.Errorf("isw member beside another: %q", got)
	}
	if got := SharedMembers(devs, []string{"/dev/sda", "/dev/sdb"}); len(got) != 0 {
		t.Errorf("both members wiped: %q", got)
	}
	if got := SharedMembers(devs, []string{"/dev/sdc"}); len(got) != 0 {
		t.Errorf("a linux_raid_member with no UUID: %q", got)
	}
}

// Without pvs, the PVs among what targets destroy are listed (and only
// those), with what to do about it: lvm2 is needed on the live system only
// for an LVM layout, so a reinstall over an old LVM disk meets this.
func TestUnknownGroups(t *testing.T) {
	devs := []Device{
		{Name: "sda", Path: "/dev/sda", Type: "disk", Children: []Device{
			{Name: "sda1", Path: "/dev/sda1", Type: "part", FsType: "vfat"},
			{Name: "sda2", Path: "/dev/sda2", Type: "part", FsType: "LVM2_member"}}},
		{Name: "sdb", Path: "/dev/sdb", Type: "disk", Children: []Device{
			{Name: "sdb1", Path: "/dev/sdb1", Type: "part", FsType: "LVM2_member"}}},
	}
	got := UnknownGroups(devs, []string{"/dev/sda"})
	if len(got) != 1 || !strings.HasPrefix(got[0], "/dev/sda2 ") || !strings.Contains(got[0], "install lvm2") {
		t.Errorf("wiping sda: %q", got)
	}
	if got := UnknownGroups(devs, []string{"/dev/sda1"}); len(got) != 0 {
		t.Errorf("formatting the ESP: %q", got)
	}
}
