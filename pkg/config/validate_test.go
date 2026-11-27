package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// sdaLayout is a one-disk config with the given partitions on sda.
func sdaLayout(children ...*Media) *Config {
	return &Config{TargetMedia: []*Media{{Name: "sda", Type: "disk", Children: children}}}
}

// validateWants checks that Validate refuses c with a problem containing
// want, or accepts it when want is "".
func validateWants(t *testing.T, name string, c *Config, want string) {
	t.Helper()
	err := c.Validate()
	switch {
	case want == "" && err != nil:
		t.Errorf("%s: rejected: %v", name, err)
	case want != "" && err == nil:
		t.Errorf("%s: validated, want %q", name, want)
	case want != "" && !strings.Contains(err.Error(), want):
		t.Errorf("%s: error does not say %q:\n%v", name, want, err)
	}
}

// Only a real filesystem counts as the root, and the firmware gets one ESP.
func TestValidateRootAndESPShape(t *testing.T) {
	esp := func(n, mnt string) *Media {
		return &Media{Name: n, Type: "part", FsType: "vfat", Mountpoint: mnt, Size: "512M"}
	}
	root := func(n string) *Media {
		return &Media{Name: n, Type: "part", FsType: "ext4", Mountpoint: "/", Size: "rest"}
	}
	for _, tc := range []struct {
		name string
		c    *Config
		want string
	}{
		{"swap at /", sdaLayout(esp("sda1", "/boot"),
			&Media{Name: "sda2", Type: "part", FsType: "swap", Mountpoint: "/", Size: "rest"}),
			"sda2: swap is not mounted"},
		{"swap at /swap beside a root", sdaLayout(esp("sda1", "/boot"),
			&Media{Name: "sda2", Type: "part", FsType: "swap", Mountpoint: "/swap", Size: "1G"}, root("sda3")),
			"sda2: swap is not mounted"},
		{"vfat root", sdaLayout(esp("sda1", "/boot/efi"),
			&Media{Name: "sda2", Type: "part", FsType: "vfat", Mountpoint: "/", Size: "rest"}),
			"sda2: vfat cannot hold the root"},
		// A vfat /boot beside the ESP at /boot/efi is a layout that has
		// always installed.
		{"vfat /boot beside the ESP", sdaLayout(esp("sda1", "/boot/efi"), esp("sda2", "/boot"), root("sda3")), ""},
		{"one ESP", sdaLayout(esp("sda1", "/boot"), root("sda2")), ""},
		{"spare ESP for RAID", raidWithSpareESP(), ""},
	} {
		validateWants(t, tc.name, tc.c, tc.want)
	}
}

// raidWithSpareESP is raidConfig with a spare ESP on the second disk.
func raidWithSpareESP() *Config {
	c := raidConfig()
	vdb := c.TargetMedia[1]
	vdb.Children = []*Media{
		{Name: "vdb1", Type: "part", FsType: "vfat", ESP: true, Size: "512M"},
		{Name: "vdb2", Type: "raid", Raid: "root", Size: "rest"},
	}
	return c
}

// A mountpoint is compared and written to fstab as given: it has to be
// one clean path, and not one of the system's own mounts. Every entry
// that carries one is checked.
func TestValidateMountpointShape(t *testing.T) {
	part := func(mnt string) *Config {
		return sdaLayout(
			&Media{Name: "sda1", Type: "part", FsType: "vfat", Mountpoint: "/boot", Size: "512M"},
			&Media{Name: "sda2", Type: "part", FsType: "ext4", Mountpoint: "/home", Size: "1G"},
			&Media{Name: "sda3", Type: "part", FsType: "ext4", Mountpoint: mnt, Size: "1G"},
			&Media{Name: "sda4", Type: "part", FsType: "ext4", Mountpoint: "/", Size: "rest"},
		)
	}
	for mnt, want := range map[string]string{
		"/srv":         "",
		"/home/":       `"/home/" is not a clean path (write "/home")`,
		"/boot/../tmp": "not a clean path",
		"//srv":        "not a clean path",
		"/a b":         "space or control character",
		"/a\tb":        "space or control character",
		"/a\x01":       "space or control character",
		"/proc":        "under /proc",
		"/sys/fs":      "under /sys",
		"/dev":         "under /dev",
		"/run/media":   "under /run",
		"/devices":     "",
		"/running":     "",
	} {
		validateWants(t, "partition at "+mnt, part(mnt), want)
	}

	// Logical volumes, RAID arrays, RAID partitions and btrfs subvolumes
	// carry mountpoints too.
	lv := part("/srv")
	lv.TargetMedia[0].Children[2] = &Media{Name: "sda3", Type: "lvm", VolumeGroup: "vg", Size: "2G",
		Children: []*Media{{Name: "data", FsType: "ext4", Mountpoint: "/data/", Size: "1G"}}}
	validateWants(t, "logical volume", lv, "sda3/data: mountpoint \"/data/\" is not a clean path")

	arr := raidConfig()
	arr.Raid = append(arr.Raid, &RaidArray{Name: "srv", Level: "1", FsType: "ext4", Mountpoint: "/run/srv"})
	arr.TargetMedia[0].Children[1].Size = "1G"
	arr.TargetMedia[0].Children = append(arr.TargetMedia[0].Children, &Media{Name: "vda3", Type: "raid", Raid: "srv", Size: "rest"})
	arr.TargetMedia[1].Children[0].Size = "1G"
	arr.TargetMedia[1].Children = append(arr.TargetMedia[1].Children, &Media{Name: "vdb2", Type: "raid", Raid: "srv", Size: "rest"})
	validateWants(t, "raid array", arr, "raid srv: mountpoint \"/run/srv\" is under /run")

	rp := raidConfig()
	rp.Raid[0] = &RaidArray{Name: "root", Level: "1", Partitions: []*Media{
		{FsType: "ext4", Mountpoint: "/", Size: "10G"},
		{FsType: "ext4", Mountpoint: "/var/./log", Size: "rest"},
	}}
	validateWants(t, "raid partition", rp, "raid root partition 2: mountpoint \"/var/./log\" is not a clean path")

	sv := part("/srv")
	sv.TargetMedia[0].Children[3] = &Media{Name: "sda4", Type: "part", FsType: "btrfs", Mountpoint: "/", Size: "rest",
		Subvolumes: []*Subvolume{{Name: "@", Mountpoint: "/"}, {Name: "@log", Mountpoint: "/var/log "}}}
	validateWants(t, "subvolume", sv, "subvolume \"@log\": mountpoint \"/var/log \" has a space")
}

// Each LUKS container gets a mapper name of its own: where two roles give
// the same name, the later one has its device's name added.
func TestPlanMapperNamesUnique(t *testing.T) {
	esp := &Media{Name: "sda1", Type: "part", FsType: "vfat", Mountpoint: "/boot", Size: "512M"}
	crypt := func(n, fs, mnt string) *Media {
		return &Media{Name: n, Type: "crypt", FsType: fs, Mountpoint: mnt, Size: "1G"}
	}
	names := func(c *Config) []string {
		t.Helper()
		pl, err := c.Plan()
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, p := range pl.Parts {
			if p.Encrypted {
				out = append(out, p.MapName)
			}
		}
		return out
	}
	root := &Media{Name: "sda4", Type: "crypt", FsType: "ext4", Mountpoint: "/", Size: "rest"}
	if got := strings.Join(names(sdaLayout(esp, crypt("sda2", "swap", ""), crypt("sda3", "swap", ""), root)), " "); got != "cryptswap cryptswap-sda3 cryptroot" {
		t.Errorf("two encrypted swaps: %s", got)
	}
	if got := strings.Join(names(sdaLayout(esp, crypt("sda2", "ext4", "/var/lib"), crypt("sda3", "ext4", "/var-lib"), root)), " "); got != "cryptvar-lib cryptvar-lib-sda3 cryptroot" {
		t.Errorf("/var/lib and /var-lib: %s", got)
	}
	// An encrypted array named root beside an encrypted root partition.
	arr := raidConfig()
	arr.Raid[0].Mountpoint = "/srv"
	arr.Raid[0].Encrypted = true
	arr.TargetMedia[0].Children[1].Size = "1G"
	arr.TargetMedia[0].Children = append(arr.TargetMedia[0].Children,
		&Media{Name: "vda3", Type: "crypt", FsType: "ext4", Mountpoint: "/", Size: "rest"})
	if got := strings.Join(names(arr), " "); got != "cryptroot cryptroot-root" {
		t.Errorf("array named root: %s", got)
	}
}

// A key no field reads loads (a clr-installer config must keep working)
// but is listed, with its path, so a typo is not silently dropped. The
// clr-installer keys slinstaller knows and ignores are not listed.
func TestUnknownKeysAreListed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c.yaml")
	if err := os.WriteFile(path, []byte(`
telemetry: true
hostnme: box
stage-hooks:
  partition: [{cmd: "true", chrot: true}]
targetMedia:
- name: sda
  type: disk
  model: 'Ultra USB 3.0'
  majMin: "8:32"
  ro: "false"
  children:
  - {name: sda1, type: part, fstype: vfat, mountpoint: /boot, size: 512M, rm: "false"}
  - &root {name: sda2, type: crypt, fstype: ext4, mountpoint: /, size: 0, encryptd: true}
`), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	got := strings.Join(c.UnknownKeys(), " ")
	if want := "hostnme stage-hooks.partition[0].chrot targetMedia[0].children[1].encryptd"; got != want {
		t.Errorf("UnknownKeys = %q, want %q", got, want)
	}
	if err := c.Validate(); err != nil {
		t.Errorf("Validate: %v", err)
	}

	clean, err := Load("testdata/real-example.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if k := clean.UnknownKeys(); len(k) != 0 {
		t.Errorf("a clr-installer config's ignored keys were listed: %v", k)
	}
}

// The keys clr-installer documents (InstallerYAMLSyntax.md) or writes into
// a config it saves, for Clear's bundles, swupd and image making, were each
// warned about as a likely typo, so a clr config came with a list of
// warnings that hid a real one. legacyBios is still named: it asks for a
// BIOS boot, which this installer does not do.
func TestClrOnlyKeysAreNotTypos(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c.yaml")
	if err := os.WriteFile(path, []byte(`
targetBundles: [vim]
userBundles: [editors]
swupdFormat: staging
swupdSkipOptional: true
preCheckDone: true
hypervisor: kvm
legacyBios: true
post-image: [{cmd: "true"}]
block-devices: [{name: d, file: /dev/sda, devicefile: true}]
targetMedia: [{name: sda, type: disk}]
`), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(c.UnknownKeys(), " "); got != "legacyBios" {
		t.Errorf("UnknownKeys = %q, want legacyBios", got)
	}
}

// A key reached through a YAML alias is checked where the alias is used:
// the anchored node is the same, and its typo is the second disk's too.
func TestUnknownKeysThroughAnAlias(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c.yaml")
	os.WriteFile(path, []byte(`
targetMedia:
- &disk
  name: sda
  type: disk
  modle: x
  children:
  - {name: sda1, type: part, fstype: ext4, mountpoint: /, size: 0, mountpiont: /}
- *disk
`), 0o644)
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(c.UnknownKeys(), " ")
	want := "targetMedia[0].modle targetMedia[0].children[0].mountpiont targetMedia[1].modle targetMedia[1].children[0].mountpiont"
	if got != want {
		t.Errorf("UnknownKeys = %q, want %q", got, want)
	}
}

// clr-installer keys slinstaller knows and ignores are not reported.
func TestClrImageKeysAreQuiet(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c.yaml")
	os.WriteFile(path, []byte("iso: true\nisoPublisher: x\nkeepImage: true\ncopySwupd: false\npostArchive: false\n"), 0o644)
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.UnknownKeys()) != 0 {
		t.Errorf("reported: %v", c.UnknownKeys())
	}
	if c.PostArchive == nil || *c.PostArchive {
		t.Error("postArchive: false not read")
	}
}

// The env's names are substituted into the hooks as $name and passed to
// env(1) as name=value: an empty name replaced every "$" in every hook with
// its value, one with "=" split elsewhere, and one beginning with "-" was
// an option to env.
func TestEnvNamesValidation(t *testing.T) {
	c := btrfsConfig()
	// clr-installer's names are any text: these are fine.
	c.Environment = map[string]string{"MIRROR": "x", "_a1": "y", "build-dir": "z", "my.var": "w", "1A": "v"}
	if err := c.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	for _, bad := range []string{"", "A=B", "-i", "A B", "A\tB", "A\nB"} {
		c.Environment = map[string]string{bad: "v"}
		if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "env:") {
			t.Errorf("env name %q: %v", bad, err)
		}
	}
}

// Each limit Validate holds a value to lets the value at the limit through
// and refuses the one just past it: an off-by-one either way refuses a
// config that is fine, or writes what the kernel, GPT or dhcpcd cannot take.
func TestValidationLimits(t *testing.T) {
	// A domain of 253 characters: three labels of 63 and one of 61.
	label := strings.Repeat("a", 63)
	domain253 := strings.Join([]string{label, label, label, strings.Repeat("b", 61)}, ".")
	intp := func(n int) *int { return &n }
	for _, tc := range []struct {
		name   string
		change func(c *Config)
		want   string
	}{
		{"VLAN id 1", func(c *Config) { c.NetworkInterfaces[2].VLAN.ID = 1 }, ""},
		{"VLAN id 4094", func(c *Config) { c.NetworkInterfaces[2].VLAN.ID = 4094 }, ""},
		{"VLAN id 0", func(c *Config) { c.NetworkInterfaces[2].VLAN.ID = 0 }, "VLAN id 0 is not between 1 and 4094"},
		{"two domains", func(c *Config) { c.NetworkInterfaces[2].Domain = "example.com corp.example" }, ""},
		{"domain of 253", func(c *Config) { c.NetworkInterfaces[2].Domain = domain253 }, ""},
		{"domain of 254", func(c *Config) { c.NetworkInterfaces[2].Domain = domain253 + "b" }, "is not a domain name"},
		{"domain after a line break", func(c *Config) { c.NetworkInterfaces[2].Domain = "\nscript /tmp/x" }, "domain is one line of names"},
		{"console log level 0", func(c *Config) { c.ConsoleLogLevel = intp(0) }, ""},
		{"console log level 7", func(c *Config) { c.ConsoleLogLevel = intp(7) }, ""},
		{"console log level 8", func(c *Config) { c.ConsoleLogLevel = intp(8) }, "console-log-level: 8"},
		{"console log level -1", func(c *Config) { c.ConsoleLogLevel = intp(-1) }, "console-log-level: -1"},
		{"power-state delay 3600", func(c *Config) { c.PowerStateDelay = 3600 }, ""},
		{"power-state delay 3601", func(c *Config) { c.PowerStateDelay = 3601 }, "power-state-delay: 3601"},
		{"partition name of 36", func(c *Config) { c.TargetMedia[0].Children[2].PartitionName = strings.Repeat("n", 36) }, ""},
		{"partition name of 37", func(c *Config) { c.TargetMedia[0].Children[2].PartitionName = strings.Repeat("n", 37) }, "partition-name"},
		{"passno 2, freq 9", func(c *Config) { c.TargetMedia[0].Children[2].Passno, c.TargetMedia[0].Children[2].Freq = intp(2), 9 }, ""},
		{"passno 3", func(c *Config) { c.TargetMedia[0].Children[2].Passno = intp(3) }, "passno is 0, 1 or 2"},
		{"freq 10", func(c *Config) { c.TargetMedia[0].Children[2].Freq = 10 }, "passno is 0, 1 or 2"},
	} {
		c := linkedConfig()
		tc.change(c)
		validateWants(t, tc.name, c, tc.want)
	}

	if err := ValidHostname(strings.Repeat("h", 63)); err != nil {
		t.Errorf("a hostname of 63: %v", err)
	}
	if err := ValidHostname(strings.Repeat("h", 64)); err == nil {
		t.Error("a hostname of 64 accepted")
	}
	// Printable ASCII is space to tilde, both ends included.
	if err := ValidPassphrase("~~~~ ~~~~"); err != nil {
		t.Errorf("a passphrase of space and tilde: %v", err)
	}
	if err := ValidPassphrase("~~~~\x7f~~~~"); err == nil {
		t.Error("a passphrase with DEL accepted")
	}
	if m, err := (&WriteFile{Permissions: "7777"}).Mode(); err != nil || m.Perm() != 0o777 {
		t.Errorf("permissions 7777: %v, %v", m, err)
	}
	if _, err := (&WriteFile{Permissions: "10000"}).Mode(); err == nil {
		t.Error("permissions 10000 accepted")
	}
	// A crypt(3) hash starts with $ and has three of them; a plaintext
	// password with dollars in it is not one.
	for s, want := range map[string]bool{"$6$salt$hash": true, "$y$j9T$a$b": true, "pa$$w$rd": false, "$plain": false, "$a$b": false} {
		if looksHashed(s) != want {
			t.Errorf("looksHashed(%q) = %v", s, !want)
		}
	}
}

// dhcp is read as clr-installer reads it, with strconv.ParseBool: "1", "t"
// and "True" are DHCP there, and an interface carrying addresses with one
// of them was configured statically here. A value ParseBool refuses
// ("yes") stops clr-installer's load; here it was taken as static.
func TestDHCPIsAClrBool(t *testing.T) {
	ni := func(dhcp string) *Interface {
		return &Interface{Name: "eth0", DHCP: dhcp, Addrs: []*Address{{IP: "10.0.0.2", Netmask: "24"}}}
	}
	for _, v := range []string{"true", "True", "TRUE", "1", "t", "T"} {
		if ni(v).Static() {
			t.Errorf("dhcp %q: static, want DHCP", v)
		}
	}
	for _, v := range []string{"", "false", "0", "f", "F", "False"} {
		if !ni(v).Static() {
			t.Errorf("dhcp %q: DHCP, want static", v)
		}
	}
	c := btrfsConfig()
	c.NetworkInterfaces = []*Interface{ni("yes")}
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), `dhcp "yes" is not true or false`) {
		t.Errorf("dhcp yes: %v", err)
	}
	c.NetworkInterfaces = []*Interface{ni("1")}
	if err := c.Validate(); err != nil {
		t.Errorf("dhcp 1: %v", err)
	}
}
