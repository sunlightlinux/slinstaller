package config

import (
	"strings"
	"testing"

	"github.com/sunlightlinux/slinstaller/pkg/lvm"
	"github.com/sunlightlinux/slinstaller/pkg/mdadm"
	"github.com/sunlightlinux/slinstaller/pkg/storage"
)

func flatCmds(cmds [][]string) string {
	var b strings.Builder
	for _, c := range cmds {
		b.WriteString(strings.Join(c, " ") + "\n")
	}
	return b.String()
}

// serverConfig is two disks, each with an ESP and a RAID member; the array
// is built by the caller.
func serverConfig(array *RaidArray) *Config {
	return &Config{
		TargetMedia: []*Media{
			{Name: "sda", Type: "disk", Children: []*Media{
				{Name: "sda1", Type: "part", FsType: "vfat", Mountpoint: "/boot/efi", Size: "512M"},
				{Name: "sda2", Type: "raid", Raid: "md0"}}},
			{Name: "sdb", Type: "disk", Children: []*Media{
				{Name: "sdb1", Type: "part", FsType: "vfat", Size: "512M", ESP: true},
				{Name: "sdb2", Type: "raid", Raid: "md0"}}},
		},
		Raid: []*RaidArray{array},
	}
}

func byDevice(pl *storage.Plan, dev string) storage.Partition {
	for _, p := range pl.Parts {
		if p.EffectiveDevice() == dev {
			return p
		}
	}
	return storage.Partition{}
}

func TestLVMOnRAID(t *testing.T) {
	c := serverConfig(&RaidArray{Name: "md0", Level: "1", VolumeGroup: "vg0", Children: []*Media{
		{Name: "root", FsType: "ext4", Mountpoint: "/", Size: "20G"},
		{Name: "var", FsType: "xfs", Mountpoint: "/var"},
	}})
	if err := c.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	plan, err := c.Plan()
	if err != nil {
		t.Fatal(err)
	}
	// The array is made from the two members, becomes the group's physical
	// volume, and the filesystems go on the volumes — never on the array.
	if got := flatCmds(mdadm.Commands(plan)); !strings.Contains(got, "mdadm --create /dev/md0") || !strings.Contains(got, "/dev/sda2 /dev/sdb2") {
		t.Errorf("mdadm:\n%s", got)
	}
	want := "pvcreate -ff -y /dev/md0\nvgcreate vg0 /dev/md0\nlvcreate -y -L 20G -n root vg0\nlvcreate -y -l 100%FREE -n var vg0\n"
	if got := flatCmds(lvm.Commands(plan)); got != want {
		t.Errorf("lvm:\n%s\nwant:\n%s", got, want)
	}
	mkfs, _ := plan.MkfsCommands()
	flat := flatCmds(mkfs)
	if !strings.Contains(flat, "/dev/vg0/root") || !strings.Contains(flat, "mkfs.xfs -f /dev/vg0/var") || strings.Contains(flat, "/dev/md0") {
		t.Errorf("mkfs:\n%s", flat)
	}
	if root := byDevice(plan, "/dev/vg0/root"); root.Mountpoint != "/" || !root.LV {
		t.Errorf("root = %+v", root)
	}
}

func TestLUKSOnRAID(t *testing.T) {
	c := serverConfig(&RaidArray{Name: "md0", Level: "1", FsType: "ext4", Mountpoint: "/", Encrypted: true})
	if err := c.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	plan, _ := c.Plan()
	arr := plan.Parts[len(plan.Parts)-1]
	// LUKS goes on the array device; the filesystem on the mapper.
	if !arr.Raid || !arr.Encrypted || arr.Device != "/dev/md0" || arr.MapName != "cryptmd0" || arr.EffectiveDevice() != "/dev/mapper/cryptmd0" {
		t.Errorf("array = %+v", arr)
	}
	mkfs, _ := plan.MkfsCommands()
	if flat := flatCmds(mkfs); !strings.Contains(flat, "/dev/mapper/cryptmd0") || strings.Contains(flat, "mkfs.ext4 -v -F -b 4096 /dev/md0") {
		t.Errorf("mkfs:\n%s", flat)
	}
	// Both together: the group's physical volume is the mapper on the array.
	c = serverConfig(&RaidArray{Name: "md0", Level: "1", Encrypted: true, VolumeGroup: "vg0",
		Children: []*Media{{Name: "root", FsType: "ext4", Mountpoint: "/"}}})
	if err := c.Validate(); err != nil {
		t.Fatalf("LVM on LUKS on RAID: %v", err)
	}
	plan, _ = c.Plan()
	if got := flatCmds(lvm.Commands(plan)); !strings.HasPrefix(got, "pvcreate -ff -y /dev/mapper/cryptmd0\nvgcreate vg0 /dev/mapper/cryptmd0\n") {
		t.Errorf("lvm:\n%s", got)
	}
}

func TestVolumeGroupAcrossPartitions(t *testing.T) {
	c := &Config{TargetMedia: []*Media{
		{Name: "sda", Type: "disk", Children: []*Media{
			{Name: "sda1", Type: "part", FsType: "vfat", Mountpoint: "/boot", Size: "512M"},
			{Name: "sda2", Type: "lvm", VolumeGroup: "vg0", Children: []*Media{
				{Name: "root", FsType: "ext4", Mountpoint: "/", Size: "20G"}}}}},
		{Name: "sdb", Type: "disk", Children: []*Media{
			{Name: "sdb1", Type: "lvm", VolumeGroup: "vg0", Children: []*Media{
				{Name: "data", FsType: "ext4", Mountpoint: "/data"}}},
			{Name: "sdb2", Type: "lvm", VolumeGroup: "vg1", Size: "10G", Children: []*Media{
				{Name: "scratch", FsType: "ext4", Mountpoint: "/scratch"}}}}},
	}}
	// sdb1 fills its disk but sdb2 follows it: give it a size instead.
	c.TargetMedia[1].Children[0].Size = "100G"
	if err := c.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	plan, err := c.Plan()
	if err != nil {
		t.Fatal(err)
	}
	// Each group is created once, on all of its physical volumes.
	want := "pvcreate -ff -y /dev/sda2\npvcreate -ff -y /dev/sdb1\npvcreate -ff -y /dev/sdb2\n" +
		"vgcreate vg0 /dev/sda2 /dev/sdb1\nvgcreate vg1 /dev/sdb2\n" +
		"lvcreate -y -L 20G -n root vg0\nlvcreate -y -l 100%FREE -n data vg0\nlvcreate -y -l 100%FREE -n scratch vg1\n"
	if got := flatCmds(lvm.Commands(plan)); got != want {
		t.Errorf("lvm:\n%s\nwant:\n%s", got, want)
	}

	// Across a group, a volume name is used once and one volume fills it.
	c.TargetMedia[1].Children[0].Children[0].Name = "root"
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), `logical volume "root" is declared twice in group vg0`) {
		t.Errorf("duplicate volume: %v", err)
	}
	c.TargetMedia[1].Children[0].Children[0].Name = "data"
	c.TargetMedia[0].Children[1].Children[0].Size = ""
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "more than one logical volume fills the group") {
		t.Errorf("two fills: %v", err)
	}
}

func TestSpareESP(t *testing.T) {
	c := serverConfig(&RaidArray{Name: "md0", Level: "1", FsType: "ext4", Mountpoint: "/"})
	if err := c.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	plan, _ := c.Plan()
	spares := plan.SpareESPs()
	if len(spares) != 1 || spares[0].Device != "/dev/sdb1" || spares[0].Disk != "/dev/sdb" || spares[0].Mountpoint != "" {
		t.Fatalf("spare ESPs = %+v", spares)
	}
	// It is flagged and formatted like the ESP, and is in neither the
	// mounts nor fstab.
	table := flatCmds(plan.TableCommands())
	if !strings.Contains(table, "/dev/sdb mkpart EFI fat32") || !strings.Contains(table, "parted -s /dev/sdb set 1 esp on") {
		t.Errorf("table:\n%s", table)
	}
	mkfs, _ := plan.MkfsCommands()
	if !strings.Contains(flatCmds(mkfs), "mkfs.vfat -F32 /dev/sdb1") {
		t.Errorf("mkfs:\n%s", flatCmds(mkfs))
	}
	for _, p := range plan.MountOrder() {
		if p.Device == "/dev/sdb1" {
			t.Error("the spare ESP is mounted")
		}
	}
	if strings.Contains(plan.Fstab(nil), "sdb1") {
		t.Errorf("the spare ESP is in fstab:\n%s", plan.Fstab(nil))
	}
	// The mounted ESP is still the one the bootloader goes to.
	if plan.ESPMountpoint() != "/boot/efi" || plan.Disk != "/dev/sda" {
		t.Errorf("ESP at %q on %s", plan.ESPMountpoint(), plan.Disk)
	}

	for name, tc := range map[string]struct {
		change func(c *Config)
		want   string
	}{
		// With the ESP at /boot the kernels are on it: a copy would go stale.
		"ESP at /boot": {func(c *Config) { c.TargetMedia[0].Children[0].Mountpoint = "/boot" }, "need the main ESP at /boot/efi"},
		"mounted":      {func(c *Config) { c.TargetMedia[1].Children[0].Mountpoint = "/efi2" }, "is a plain vfat partition with no mountpoint"},
		"not vfat":     {func(c *Config) { c.TargetMedia[1].Children[0].FsType = "ext4" }, "is a plain vfat partition with no mountpoint"},
		"too small":    {func(c *Config) { c.TargetMedia[1].Children[0].Size = "10M" }, "ESP size"},
	} {
		c := serverConfig(&RaidArray{Name: "md0", Level: "1", FsType: "ext4", Mountpoint: "/"})
		tc.change(c)
		if err := c.Validate(); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: error %v, want %q", name, err, tc.want)
		}
	}
}

func TestRaidAsPhysicalVolumeValidation(t *testing.T) {
	for name, tc := range map[string]struct {
		array *RaidArray
		want  string
	}{
		"filesystem and group": {&RaidArray{Name: "md0", Level: "1", FsType: "ext4", Mountpoint: "/", VolumeGroup: "vg0",
			Children: []*Media{{Name: "root", FsType: "ext4", Mountpoint: "/"}}}, "carries no filesystem of its own"},
		"volumes without a group": {&RaidArray{Name: "md0", Level: "1", FsType: "ext4", Mountpoint: "/",
			Children: []*Media{{Name: "x", FsType: "ext4"}}}, "children are logical volumes and need a volumeGroup"},
		"bad volume": {&RaidArray{Name: "md0", Level: "1", VolumeGroup: "vg0",
			Children: []*Media{{Name: "root", FsType: "ntfs", Mountpoint: "/"}}}, `filesystem "ntfs" is not supported`},
	} {
		if err := serverConfig(tc.array).Validate(); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: error %v, want %q", name, err, tc.want)
		}
	}
}

// An array may carry a partition table of its own: it is then cut like a
// disk, once it exists, and its partitions carry the filesystems.
func TestPartitionedRAID(t *testing.T) {
	c := serverConfig(&RaidArray{Name: "md0", Level: "1", Partitions: []*Media{
		nil,
		{FsType: "ext4", Mountpoint: "/", Size: "20G", UUID: "99999999-8888-7777-6666-555555555555"},
		{FsType: "xfs", Mountpoint: "/srv", MountOptions: "noatime"},
	}})
	if err := c.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	plan, err := c.Plan()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(plan.RaidDisks, ",") != "/dev/md0" || strings.Join(plan.Disks, ",") != "/dev/sda,/dev/sdb" {
		t.Errorf("array disks %v, disks %v", plan.RaidDisks, plan.Disks)
	}
	// The disks' own tables know nothing of the array's partitions …
	if table := flatCmds(plan.TableCommands()); strings.Contains(table, "/dev/md0") {
		t.Errorf("the array is partitioned with the disks:\n%s", table)
	}
	// … which are cut on the array, the last one filling it.
	want := "parted -s /dev/md0 mklabel gpt\n" +
		"parted -a optimal --script /dev/md0 mkpart / 0% 20480MiB\n" +
		"parted -a optimal --script /dev/md0 mkpart /srv 20480MiB 100%\n" +
		"partprobe /dev/md0\n"
	if got := flatCmds(plan.ArrayTableCommands()); got != want {
		t.Errorf("array table:\n%s\nwant:\n%s", got, want)
	}
	mkfs, _ := plan.MkfsCommands()
	flat := flatCmds(mkfs)
	if !strings.Contains(flat, "mkfs.ext4 -U 99999999-8888-7777-6666-555555555555 -v -F -b 4096 /dev/md0p1") || !strings.Contains(flat, "mkfs.xfs -f /dev/md0p2") {
		t.Errorf("mkfs:\n%s", flat)
	}
	if fstab := plan.Fstab(nil); !strings.Contains(fstab, "/dev/md0p2\t/srv\txfs\tnoatime") {
		t.Errorf("fstab:\n%s", fstab)
	}
	// A dry run shows the array's table after the disks' and before mkfs.
	all, _ := plan.Commands()
	shown := flatCmds(all)
	if i, j, k := strings.Index(shown, "/dev/sdb mklabel"), strings.Index(shown, "/dev/md0 mklabel"), strings.Index(shown, "mkfs."); i < 0 || j < i || k < j {
		t.Errorf("dry run order:\n%s", shown)
	}
	// A named array's partitions are named as udev names them.
	c = serverConfig(&RaidArray{Name: "data", Level: "1", Partitions: []*Media{{FsType: "ext4", Mountpoint: "/"}}})
	plan, _ = c.Plan()
	if got := plan.Parts[len(plan.Parts)-1].Device; got != "/dev/md/data1" {
		t.Errorf("partition of a named array = %s", got)
	}

	for name, tc := range map[string]struct {
		array *RaidArray
		want  string
	}{
		"and a filesystem": {&RaidArray{Name: "md0", Level: "1", FsType: "ext4", Partitions: []*Media{{FsType: "ext4", Mountpoint: "/"}}}, "carries nothing else itself"},
		"encrypted":        {&RaidArray{Name: "md0", Level: "1", Encrypted: true, Partitions: []*Media{{FsType: "ext4", Mountpoint: "/"}}}, "carries nothing else itself"},
		"ESP":              {&RaidArray{Name: "md0", Level: "1", Partitions: []*Media{{FsType: "vfat", Mountpoint: "/boot"}, {FsType: "ext4", Mountpoint: "/"}}}, "the EFI System Partition cannot be on RAID"},
		"crypt partition":  {&RaidArray{Name: "md0", Level: "1", Partitions: []*Media{{Type: "crypt", FsType: "ext4", Mountpoint: "/"}}}, "is a plain one"},
		"fill not last":    {&RaidArray{Name: "md0", Level: "1", Partitions: []*Media{{FsType: "ext4", Mountpoint: "/"}, {FsType: "ext4", Mountpoint: "/srv", Size: "1G"}}}, ""},
		"bad size":         {&RaidArray{Name: "md0", Level: "1", Partitions: []*Media{{FsType: "ext4", Mountpoint: "/", Size: "big"}}}, ""},
	} {
		c := serverConfig(tc.array)
		err := c.Validate()
		if tc.want != "" {
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("%s: error %v, want %q", name, err, tc.want)
			}
			continue
		}
		// Caught when planned, if not before.
		if _, perr := c.Plan(); err == nil && perr == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestKernelRemoveValidation(t *testing.T) {
	c := btrfsConfig()
	c.Kernel, c.KernelRemove = "linux-lts", []string{"linux6.18", "linux-mainline"}
	if err := c.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	for _, bad := range []string{"", "linux 6", "-f", "../x"} {
		c.KernelRemove = []string{bad}
		if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "is not a package name") {
			t.Errorf("kernel-remove %q: %v", bad, err)
		}
	}
	c.KernelRemove = []string{c.KernelPackage()}
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "is the kernel the config installs") {
		t.Errorf("removing the installed kernel: %v", err)
	}
}

// A package, kernel or fallback kernel the config names is handed to
// xbps-install as an argument: one that begins with "-" was taken as an
// option (packages: [-r, /] installed into the live system's root), and one
// with a space was one argument no repository has.
func TestPackageNamesValidation(t *testing.T) {
	c := btrfsConfig()
	c.Kernel, c.KernelFallback, c.Packages = "linux6.12", "linux-lts", []string{"vim", "git"}
	if err := c.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	for _, bad := range []string{"-r", "--repository=http://x", "vim git", "a/b"} {
		for field, set := range map[string]func(*Config){
			"packages":        func(c *Config) { c.Packages = []string{"vim", bad} },
			"kernel":          func(c *Config) { c.Kernel = bad },
			"kernel-fallback": func(c *Config) { c.KernelFallback = bad },
		} {
			c := btrfsConfig()
			c.Kernel = "linux6.12"
			set(c)
			if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "is not a package name") {
				t.Errorf("%s %q: %v", field, bad, err)
			}
		}
	}
}
