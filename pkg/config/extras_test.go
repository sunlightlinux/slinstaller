package config

import (
	"os"
	"strings"
	"testing"
)

func TestProxyEnv(t *testing.T) {
	for _, tc := range []struct {
		c    Config
		want string
	}{
		{Config{}, ""},
		// As before there were two settings: one proxy serves both.
		{Config{HTTPSProxy: "http://p:3128"}, "https_proxy=http://p:3128 http_proxy=http://p:3128"},
		{Config{HTTPSProxy: "http://s:1", HTTPProxy: "http://h:2", NoProxy: "localhost,10.0.0.0/8"},
			"https_proxy=http://s:1 http_proxy=http://h:2 no_proxy=localhost,10.0.0.0/8"},
		{Config{HTTPProxy: "http://h:2"}, "http_proxy=http://h:2"},
		// Hosts to reach directly mean nothing without a proxy.
		{Config{NoProxy: "localhost"}, ""},
	} {
		if got := strings.Join(tc.c.ProxyEnv(), " "); got != tc.want {
			t.Errorf("ProxyEnv(%+v) = %q, want %q", tc.c, got, tc.want)
		}
	}
}

func TestPowerAction(t *testing.T) {
	for _, tc := range []struct {
		c    Config
		want string
	}{
		{Config{}, ""},
		{Config{PostReboot: true}, "reboot"},
		{Config{PowerState: "poweroff"}, "poweroff"},
		{Config{PowerState: "halt", PostReboot: true}, "halt"}, // the newer setting says more
	} {
		if got := tc.c.PowerAction(); got != tc.want {
			t.Errorf("PowerAction = %q, want %q", got, tc.want)
		}
	}
	c := btrfsConfig()
	c.PowerState = "suspend"
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), `power-state: "suspend" is not reboot, poweroff, halt or kexec`) {
		t.Errorf("unknown power state: %v", err)
	}
}

func TestWriteFilesValidation(t *testing.T) {
	for name, tc := range map[string]struct {
		files []*WriteFile
		want  string
	}{
		"ok":           {[]*WriteFile{nil, {Path: "/etc/motd", Content: "hi\n"}, {Path: "/etc/secret", Permissions: "0600"}}, ""},
		"relative":     {[]*WriteFile{{Path: "etc/motd"}}, "is not an absolute file path"},
		"the root":     {[]*WriteFile{{Path: "/"}}, "is not an absolute file path"},
		"twice":        {[]*WriteFile{{Path: "/etc/motd"}, {Path: "/etc/../etc/motd"}}, "/etc/motd is written twice"},
		"bad mode":     {[]*WriteFile{{Path: "/etc/motd", Permissions: "rw-r--r--"}}, "are not an octal mode"},
		"mode too big": {[]*WriteFile{{Path: "/etc/motd", Permissions: "17777"}}, "are not an octal mode"},
	} {
		c := btrfsConfig()
		c.WriteFiles = tc.files
		err := c.Validate()
		switch {
		case tc.want == "" && err != nil:
			t.Errorf("%s: %v", name, err)
		case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
			t.Errorf("%s: error %v, want %q", name, err, tc.want)
		}
	}
	for perm, want := range map[string]os.FileMode{
		"": 0o644, "600": 0o600, "0755": 0o755,
		"4755": 0o755 | os.ModeSetuid, "2750": 0o750 | os.ModeSetgid, "1777": 0o777 | os.ModeSticky,
	} {
		if got, err := (&WriteFile{Permissions: perm}).Mode(); err != nil || got != want {
			t.Errorf("Mode(%q) = %v, %v; want %v", perm, got, err, want)
		}
	}
}

func TestSuggestedSwap(t *testing.T) {
	const gib = uint64(1) << 30
	for _, tc := range []struct{ mem, want uint64 }{
		{0, gib},                           // memory unknown
		{256 << 20, gib},                   // twice a tiny memory is still under the floor
		{768 << 20, 1536 << 20},            // twice the memory
		{gib + gib/2, 2 * gib},             // under 2 GiB
		{3 * gib, 3 * gib},                 // as much as the memory
		{8 * gib, 4 * gib},                 // a fixed 4 GiB
		{63 * gib, 4 * gib},                //
		{64 * gib, 8 * gib},                // large machines
		{1024 * gib, 8 * gib},              // never more
		{3*gib + 12345, 3*gib + (1 << 20)}, // whole mebibytes
	} {
		if got := SuggestedSwap(tc.mem); got != tc.want {
			t.Errorf("SuggestedSwap(%d MiB) = %d MiB, want %d MiB", tc.mem>>20, got>>20, tc.want>>20)
		}
	}
}

func TestAutoSwapIsResolvedForTheMachine(t *testing.T) {
	c := btrfsConfig()
	c.TargetMedia[0].Children[1].Size = "Auto"
	c.SwapFileSize = "auto"
	// Until it is resolved the config cannot be planned, and says why.
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), `swap size of "auto"`) {
		t.Errorf("unresolved auto: %v", err)
	}
	c.ResolveAutoSwap(16 << 30)
	if got := c.TargetMedia[0].Children[1].Size; got != "4096M" || c.SwapFileSize != "4096M" {
		t.Errorf("resolved to %q and %q, want 4096M", got, c.SwapFileSize)
	}
	if err := c.Validate(); err != nil {
		t.Errorf("resolved config: %v", err)
	}
	// A size that was given is left alone; so is everything that is not swap.
	c = btrfsConfig()
	c.ResolveAutoSwap(16 << 30)
	if c.TargetMedia[0].Children[1].Size != "2G" || c.SwapFileSize != "" || c.TargetMedia[0].Children[0].Size != "512M" {
		t.Error("a given size was changed")
	}
	// A swap logical volume, likewise.
	lv := &Config{TargetMedia: []*Media{nil, {Name: "sda", Type: "disk", Children: []*Media{nil,
		{Name: "sda2", Type: "lvm", VolumeGroup: "vg0", Children: []*Media{nil, {Name: "swap", FsType: "swap", Size: "auto"}}}}}}}
	lv.ResolveAutoSwap(2 << 30)
	if got := lv.TargetMedia[1].Children[1].Children[1].Size; got != "2048M" {
		t.Errorf("swap LV = %q", got)
	}
}

func raidWithSpare(level string, active int) *Config {
	c := &Config{Raid: []*RaidArray{{Name: "md0", Level: level, FsType: "ext4", Mountpoint: "/"}}}
	for i := 0; i < active+1; i++ {
		name := "sd" + string(rune('a'+i))
		d := &Media{Name: name, Type: "disk"}
		if i == 0 {
			d.Children = append(d.Children, &Media{Name: name + "1", Type: "part", FsType: "vfat", Mountpoint: "/boot", Size: "512M"})
		}
		// The member is the disk's second partition after the ESP, its
		// first on the other disks: parted numbers what it cuts 1, 2, ….
		member := name + "1"
		if i == 0 {
			member = name + "2"
		}
		d.Children = append(d.Children, &Media{Name: member, Type: "raid", Raid: "md0", Spare: i == active})
		c.TargetMedia = append(c.TargetMedia, d)
	}
	return c
}

func TestRaidSpares(t *testing.T) {
	c := raidWithSpare("1", 2)
	if err := c.Validate(); err != nil {
		t.Fatalf("RAID 1 with a spare: %v", err)
	}
	plan, err := c.Plan()
	if err != nil {
		t.Fatal(err)
	}
	arr := plan.Parts[len(plan.Parts)-1]
	if strings.Join(arr.RaidMembers, ",") != "/dev/sda2,/dev/sdb1" || strings.Join(arr.RaidSpares, ",") != "/dev/sdc1" {
		t.Errorf("members %v spares %v", arr.RaidMembers, arr.RaidSpares)
	}
	// A spare does not count towards what the level needs …
	if err := raidWithSpare("5", 2).Validate(); err == nil || !strings.Contains(err.Error(), "level 5 needs at least 3 members, has 2") {
		t.Errorf("RAID 5 with two members and a spare: %v", err)
	}
	// … and a level with nothing to rebuild from cannot use one.
	for _, level := range []string{"0", "raid0"} {
		if err := raidWithSpare(level, 2).Validate(); err == nil || !strings.Contains(err.Error(), "no redundancy to rebuild onto a spare") {
			t.Errorf("level %s with a spare: %v", level, err)
		}
	}
}

func TestDiskWipeModes(t *testing.T) {
	for mode, want := range map[string]string{"": "", "superblock": "", "zero": "zero", "random": "random", "discard": "discard"} {
		c := btrfsConfig()
		c.TargetMedia[0].Wipe = mode
		if err := c.Validate(); err != nil {
			t.Errorf("wipe %q: %v", mode, err)
		}
		plan, _ := c.Plan()
		if plan.DiskWipe["/dev/sda"] != want {
			t.Errorf("wipe %q planned as %q", mode, plan.DiskWipe["/dev/sda"])
		}
	}
	c := btrfsConfig()
	c.TargetMedia[0].Wipe = "everything"
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), `wipe "everything" is not superblock, zero, random or discard`) {
		t.Errorf("unknown wipe: %v", err)
	}
	// Reusing partitions and erasing the disk contradict each other.
	c = btrfsConfig()
	c.TargetMedia[0].Children[2].Subvolumes = nil
	c.ReusePartitions, c.TargetMedia[0].Wipe = true, "zero"
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "would erase the partitions reusePartitions keeps") {
		t.Errorf("wipe with reuse: %v", err)
	}
}

func TestSystemUpgradeAndKexecValidation(t *testing.T) {
	c := btrfsConfig()
	c.SystemUpgrade, c.PowerState = true, "kexec"
	if err := c.Validate(); err != nil || c.PowerAction() != "kexec" {
		t.Errorf("system-upgrade with kexec: %v, action %q", err, c.PowerAction())
	}
	c.Offline = true
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "cannot be done offline") {
		t.Errorf("an offline system upgrade: %v", err)
	}
}
