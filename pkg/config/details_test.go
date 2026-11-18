package config

import (
	"strings"
	"testing"

	"github.com/sunlightlinux/slinstaller/pkg/mdadm"
)

func detailedConfig() *Config {
	zero := 0
	return &Config{TargetMedia: []*Media{{Name: "sda", Type: "disk", Children: []*Media{
		{Name: "sda1", Type: "part", FsType: "vfat", Mountpoint: "/boot", Size: "512M", UUID: "1A2B-3C4D",
			PartitionName: "Sunlight ESP", PartitionUUID: "11111111-2222-3333-4444-555555555555"},
		{Name: "sda2", Type: "part", FsType: "swap", Size: "2G", UUID: "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"},
		{Name: "sda3", Type: "crypt", FsType: "ext4", Mountpoint: "/", Size: "20G", CryptOptions: "discard",
			UUID: "99999999-8888-7777-6666-555555555555", PartitionType: "4F68BCE3-E8CD-4DB1-96E7-FBCAF984B709"},
		{Name: "sda4", Type: "crypt", FsType: "xfs", Mountpoint: "/data", CryptOptions: "discard,no-read-workqueue",
			UUID: "12345678-1234-1234-1234-123456789abc", Passno: &zero, Freq: 1},
	}}}}
}

func TestPartitionDetailsReachTheCommands(t *testing.T) {
	c := detailedConfig()
	if err := c.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	plan, err := c.Plan()
	if err != nil {
		t.Fatal(err)
	}
	table := flatCmds(plan.TableCommands())
	// The GPT entries are set once the partitions exist, before the kernel
	// re-reads the table.
	for _, want := range []string{
		"sfdisk --part-uuid /dev/sda 1 11111111-2222-3333-4444-555555555555",
		"sfdisk --part-label /dev/sda 1 Sunlight ESP",
		"sfdisk --part-type /dev/sda 3 4F68BCE3-E8CD-4DB1-96E7-FBCAF984B709",
	} {
		if !strings.Contains(table, want+"\n") {
			t.Errorf("table commands missing %q:\n%s", want, table)
		}
	}
	if strings.Index(table, "sfdisk --part-type") > strings.Index(table, "partprobe") || strings.Index(table, "sfdisk") < strings.LastIndex(table, "mkpart") {
		t.Errorf("order:\n%s", table)
	}
	if tools := " " + strings.Join(plan.RequiredTools(), " ") + " "; !strings.Contains(tools, " sfdisk ") {
		t.Errorf("sfdisk is not among the required tools: %s", tools)
	}
	mkfs, _ := plan.MkfsCommands()
	flat := flatCmds(mkfs)
	// Each mkfs is given the UUID in its own way.
	for _, want := range []string{
		"mkfs.vfat -i 1A2B3C4D -F32 /dev/sda1",
		"mkswap -U aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee /dev/sda2",
		"mkfs.ext4 -U 99999999-8888-7777-6666-555555555555 ",
		"mkfs.xfs -m uuid=12345678-1234-1234-1234-123456789abc -f ",
	} {
		if !strings.Contains(flat, want) {
			t.Errorf("mkfs missing %q:\n%s", want, flat)
		}
	}
	fstab := plan.Fstab(nil)
	if !strings.Contains(fstab, "\t/data\txfs\tdefaults\t1 0\n") || !strings.Contains(fstab, "\t/\text4\tdefaults\t0 1\n") {
		t.Errorf("fstab:\n%s", fstab)
	}
	if plan.Parts[2].CryptOptions != "discard" || plan.Parts[3].CryptOptions != "discard,no-read-workqueue" {
		t.Errorf("crypt options: %q, %q", plan.Parts[2].CryptOptions, plan.Parts[3].CryptOptions)
	}
}

func TestPartitionDetailsValidation(t *testing.T) {
	three := 3
	for name, tc := range map[string]struct {
		change func(ch []*Media)
		want   string
	}{
		"type not a GUID":   {func(ch []*Media) { ch[2].PartitionType = "linux" }, `partition-type "linux" is not a GUID`},
		"uuid not a GUID":   {func(ch []*Media) { ch[0].PartitionUUID = "1234" }, `partition-uuid "1234" is not a GUID`},
		"name with a quote": {func(ch []*Media) { ch[0].PartitionName = `my "esp"` }, "is not a name a GPT entry can carry"},
		"vfat id":           {func(ch []*Media) { ch[0].UUID = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee" }, "is not a vfat volume id"},
		"fs uuid":           {func(ch []*Media) { ch[2].UUID = "1A2B-3C4D" }, `uuid "1A2B-3C4D" is not a UUID`},
		"passno":            {func(ch []*Media) { ch[3].Passno = &three }, "passno is 0, 1 or 2"},
		"freq":              {func(ch []*Media) { ch[3].Freq = 12 }, "passno is 0, 1 or 2 and freq 0 to 9"},
		"crypt spaces":      {func(ch []*Media) { ch[3].CryptOptions = "discard, x" }, "crypt-options"},
		"not encrypted":     {func(ch []*Media) { ch[1].CryptOptions = "discard" }, "crypt-options on a volume that is not encrypted"},
	} {
		c := detailedConfig()
		tc.change(c.TargetMedia[0].Children)
		if err := c.Validate(); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: error %v, want %q", name, err, tc.want)
		}
	}
	// A logical volume is not a partition.
	lv := &Config{TargetMedia: []*Media{{Name: "sda", Type: "disk", Children: []*Media{
		{Name: "sda1", Type: "part", FsType: "vfat", Mountpoint: "/boot", Size: "512M"},
		{Name: "sda2", Type: "lvm", VolumeGroup: "vg0", Encrypted: true, CryptOptions: "discard", Children: []*Media{
			{Name: "root", FsType: "ext4", Mountpoint: "/", PartitionName: "x"}}}}}}}
	if err := lv.Validate(); err == nil || !strings.Contains(err.Error(), "are for partitions, not logical volumes") {
		t.Errorf("partition-name on a volume: %v", err)
	}
	lv.TargetMedia[0].Children[1].Children[0].PartitionName = ""
	plan, err := lv.Plan()
	if err != nil || plan.Parts[1].CryptOptions != "discard" {
		t.Errorf("encrypted physical volume: %v, %+v", err, plan.Parts[1])
	}
}

func TestRaidMetadataAndDetails(t *testing.T) {
	two := 2
	c := serverConfig(&RaidArray{Name: "md0", Level: "1", FsType: "ext4", Mountpoint: "/", Metadata: "1.0",
		Encrypted: true, CryptOptions: "discard", UUID: "99999999-8888-7777-6666-555555555555", Passno: &two})
	if err := c.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	plan, _ := c.Plan()
	if got := flatCmds(mdadm.Commands(plan)); !strings.Contains(got, "--metadata=1.0") {
		t.Errorf("mdadm:\n%s", got)
	}
	arr := plan.Parts[len(plan.Parts)-1]
	if arr.CryptOptions != "discard" || arr.FsUUID == "" || *arr.Passno != 2 {
		t.Errorf("array = %+v", arr)
	}
	c.Raid[0].Metadata = "2.0"
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), `metadata "2.0" is not 1.2, 1.1, 1.0 or 0.90`) {
		t.Errorf("bad metadata: %v", err)
	}
	// Unset, it is what it always was.
	if got := strings.Join(mdadm.Create("/dev/md0", "1", []string{"a", "b"}, nil), " "); !strings.Contains(got, "--metadata=1.2") {
		t.Errorf("default metadata: %s", got)
	}
}
