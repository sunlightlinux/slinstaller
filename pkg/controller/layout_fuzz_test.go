package controller

import (
	"fmt"
	"path"
	"slices"
	"strconv"
	"strings"
	"testing"
	"unicode"

	"github.com/sunlightlinux/slinstaller/pkg/config"
	"github.com/sunlightlinux/slinstaller/pkg/storage"
)

// layoutGen turns fuzz bytes into a storage layout: each choice takes one
// byte and picks from a short list of values, good and bad, or — one past
// the end of the list — the fuzzed string s. Out of bytes, every choice is
// the list's first value, which is a plain one.
type layoutGen struct {
	b []byte
	s string
}

func (g *layoutGen) next() int {
	if len(g.b) == 0 {
		return 0
	}
	c := g.b[0]
	g.b = g.b[1:]
	return int(c)
}

func (g *layoutGen) pick(pool ...string) string {
	if i := g.next() % (len(pool) + 1); i < len(pool) {
		return pool[i]
	}
	return g.s
}

// pickAt is pick turned by k places, so the k-th partition out of
// bytes gets the k-th value: an ESP, then the root.
func (g *layoutGen) pickAt(k int, pool ...string) string {
	if i := (g.next() + k) % (len(pool) + 1); i < len(pool) {
		return pool[i]
	}
	return g.s
}

func (g *layoutGen) upTo(n int) int { return g.next() % (n + 1) }

var (
	genDisks    = []string{"sda", "sdb", "vda", "nvme0n1", "md0", "", "..", "../sdz", "-x", "sd a"}
	genParts    = []string{"sda1", "sdb2", "sda", "../sdz1", "-x1", "md0p1"}
	genTypes    = []string{"part", "crypt", "lvm", "raid", "", "disk"}
	genSizes    = []string{"512M", "rest", "20G", "", "0", "100M", "1.5G", "4K", "-1G"}
	genFS       = []string{"vfat", "ext4", "swap", "xfs", "btrfs", "", "ext4 x", "ntfs"}
	genMounts   = []string{"/boot/efi", "/", "/home", "/boot", "/var", "", "/var/lib", "/var-lib", "/home x", "-x", "home", "/home/"}
	genMountOpt = []string{"", "noatime", "defaults,nofail", "a b"}
	genCryptOpt = []string{"", "discard", "a, b"}
	genArrays   = []string{"md0", "data", "md1", "../sdz", "-x", "md", "root"}
	genLevels   = []string{"1", "0", "5", "10", "raid1", "linear"}
	genVGs      = []string{"vg0", "vg1", "", "../x", "-x", "root"}
	genLVs      = []string{"root", "home", "swap", "", "-x", ".."}
	genLVSizes  = []string{"10G", "rest", "", "1.5G", "1073741824", "-1"}
	genWipes    = []string{"", "superblock", "zero", "discard", "random"}
	genStarts   = []string{"", "1M", "1000M", "x"}
	genResizes  = []string{"", "10G", "0"}
	genSubvols  = [][]*config.Subvolume{nil,
		{{Name: "@", Mountpoint: "/"}, {Name: "@home", Mountpoint: "/home"}},
		{{Name: "@log", Mountpoint: "/var/log"}},
		{{Name: "@", Mountpoint: "/"}, {Name: "@", Mountpoint: "/srv"}},
	}
)

// genConfig builds a config from fuzz input: disks of every partition type,
// encrypted or not, RAID arrays — plain, partitioned or carrying LVM —,
// logical volumes, fill sizes, a reuse (no wipe) install.
func genConfig(data []byte, s string) *config.Config {
	g := &layoutGen{b: data, s: s}
	c := &config.Config{ReusePartitions: g.next()%4 == 3}
	lvs := func() []*config.Media {
		var out []*config.Media
		for range g.upTo(2) {
			out = append(out, &config.Media{Name: g.pick(genLVs...), Size: g.pick(genLVSizes...),
				FsType: g.pick(genFS...), Mountpoint: g.pick(genMounts...), MountOptions: g.pick(genMountOpt...)})
		}
		return out
	}
	k := 0 // partitions so far, on every disk
	for range 1 + g.upTo(2) {
		d := &config.Media{Name: g.pick(genDisks...), Type: "disk", Wipe: g.pick(genWipes...)}
		for i := range 1 + (g.next()+1)%4 {
			ch := &config.Media{Type: g.pick(genTypes...), Size: g.pickAt(k, genSizes...),
				FsType: g.pickAt(k, genFS...), Mountpoint: g.pickAt(k, genMounts...), MountOptions: g.pick(genMountOpt...)}
			k++
			switch g.upTo(2) {
			case 1:
				if d.Name != "" {
					ch.Name = strings.TrimPrefix(storage.PartitionName("/dev/"+d.Name, i+1), "/dev/")
				}
			case 2:
				ch.Name = g.pick(genParts...)
			}
			flags := g.next()
			ch.ESP = flags&1 != 0
			ch.Subvolumes = genSubvols[g.upTo(len(genSubvols)-1)]
			switch ch.Type {
			case "crypt":
				ch.CryptOptions = g.pick(genCryptOpt...)
			case "raid":
				ch.Raid, ch.Spare = g.pick(genArrays...), flags&2 != 0
			case "lvm":
				ch.VolumeGroup, ch.Encrypted = g.pick(genVGs...), flags&4 != 0
				if ch.Encrypted {
					ch.CryptOptions = g.pick(genCryptOpt...)
				}
				ch.Children = lvs()
			}
			if c.ReusePartitions {
				// A created partition keeps nothing: never both.
				ch.Create = flags&16 != 0
				ch.Preserve = flags&8 != 0 && !ch.Create
				ch.Start, ch.Resize = g.pick(genStarts...), g.pick(genResizes...)
			}
			d.Children = append(d.Children, ch)
		}
		c.TargetMedia = append(c.TargetMedia, d)
	}
	for range g.upTo(2) {
		a := &config.RaidArray{Name: g.pick(genArrays...), Level: g.pick(genLevels...),
			FsType: g.pick(genFS...), Mountpoint: g.pick(genMounts...), MountOptions: g.pick(genMountOpt...)}
		if a.Encrypted = g.next()&1 != 0; a.Encrypted {
			a.CryptOptions = g.pick(genCryptOpt...)
		}
		switch g.upTo(2) {
		case 1:
			a.FsType, a.Mountpoint = "", ""
			for range 1 + g.upTo(2) {
				a.Partitions = append(a.Partitions, &config.Media{Size: g.pick(genSizes...),
					FsType: g.pick(genFS...), Mountpoint: g.pick(genMounts...)})
			}
		case 2:
			a.FsType, a.Mountpoint = "", ""
			a.VolumeGroup, a.Children = g.pick(genVGs...), lvs()
		}
		c.Raid = append(c.Raid, a)
	}
	return c
}

// laidOut is a plan, and whether its config passed Validate with the
// checks on (validated) or only with skipValidationAll.
type laidOut struct {
	pl        *storage.Plan
	validated bool
}

// genPlans is every plan the fuzz input makes: with the checks on, when
// the config passes Validate, and with skipValidationAll, when it passes
// what that leaves of Validate. Plan has to refuse the rest.
func genPlans(data []byte, s string) (plans []laidOut, validated bool) {
	for _, skip := range []bool{false, true} {
		c := genConfig(data, s)
		c.SkipValidationAll = skip
		if c.Validate() != nil {
			continue
		}
		pl, err := c.Plan()
		if err != nil {
			continue
		}
		validated = validated || !skip
		plans = append(plans, laidOut{pl, !skip})
	}
	return plans, validated
}

// layoutSeeds are inputs for genConfig that plan with the checks on
// (TestLayoutSeedsPlan holds them to it), one per kind of stack.
var layoutSeeds = []struct {
	name string
	data []byte
}{
	// sda: an ESP and a raid member; sdb: a raid member; md0 RAID 1 at /.
	{"raid1", []byte{0, 1, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 3, 0, 4, 4, 0, 0, 0, 0, 0, 1, 0, 3, 3, 9, 3, 3, 0, 0, 0, 0, 0, 1, 0, 0, 1, 1, 0, 0, 0}},
	// The same, md0 encrypted.
	{"raid1-luks", []byte{0, 1, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 3, 0, 4, 4, 0, 0, 0, 0, 0, 1, 0, 3, 3, 9, 3, 3, 0, 0, 0, 0, 0, 1, 0, 0, 1, 1, 0, 1, 0, 0}},
	// md0 with a table of its own: md0p1 at /.
	{"raid1-partitioned", []byte{0, 1, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 3, 0, 4, 4, 0, 0, 0, 0, 0, 1, 0, 3, 3, 9, 3, 3, 0, 0, 0, 0, 0, 1, 0, 0, 1, 1, 0, 0, 1, 0, 1, 1, 1}},
	// md0 a physical volume of vg0, whose root fills it.
	{"raid1-lvm", []byte{0, 1, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 3, 0, 4, 4, 0, 0, 0, 0, 0, 1, 0, 3, 3, 9, 3, 3, 0, 0, 0, 0, 0, 1, 0, 0, 1, 1, 0, 0, 2, 0, 1, 0, 1, 1, 1, 0}},
	// md0 encrypted, a physical volume of vg0.
	{"raid1-luks-lvm", []byte{0, 1, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 3, 0, 4, 4, 0, 0, 0, 0, 0, 1, 0, 3, 3, 9, 3, 3, 0, 0, 0, 0, 0, 1, 0, 0, 1, 1, 0, 1, 0, 2, 0, 1, 0, 1, 1, 1, 0}},
	// sda: an ESP, a swap and a root, nothing asked of their mounts.
	{"swap", []byte{0, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0, 0, 0, 0, 9, 1, 4, 0, 0, 0, 0, 0, 9, 8, 12, 0, 0, 0, 0, 0}},
	// sda: an ESP, a root and an encrypted /home with crypt-options.
	{"crypt-home-options", []byte{0, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0, 1, 9, 8, 0, 0, 0, 0, 0, 1, 0}},
	// sda: an ESP and an encrypted root.
	{"crypt-root", []byte{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0, 0, 0, 0}},
	// Found by search: a btrfs root with subvolumes.
	{"subvol", []byte{0x0, 0x0, 0x0, 0x0, 0x0, 0x0, 0x0, 0x0, 0x0, 0x0, 0x0, 0x78, 0x0, 0x0, 0x0, 0x3, 0x0, 0x0, 0x0, 0x0, 0x2d, 0x0, 0x0, 0x0, 0x0, 0x0, 0x0, 0x2, 0x35, 0x0, 0x0, 0x2, 0x1, 0x0, 0x0, 0x0, 0x0, 0x0, 0x0, 0x0, 0x3, 0x3, 0x0, 0x1, 0x0, 0x0, 0xc9, 0x0, 0x1}},
	// A whole-disk wipe first.
	{"diskwipe", []byte{0x1, 0x3, 0x0, 0x3, 0x0, 0x0, 0x0, 0x0, 0x0, 0x0}},
	// Reuse: the partitions kept, one cut in free space.
	{"reuse-create", []byte{0x3, 0x0, 0x0, 0x0, 0x0, 0x0, 0x0, 0x0, 0x0, 0x0, 0x0, 0x1c, 0x0, 0x0, 0x0, 0x0}},
	{"reuse-multidisk", []byte{0x3, 0x1, 0x3, 0x0, 0x3, 0x0, 0x0, 0x0, 0x0, 0x0, 0x0, 0x0, 0x0, 0x0, 0x0, 0x2, 0x1, 0x3, 0x0, 0x48, 0x0, 0x0, 0x0, 0x0, 0x3, 0x0, 0x0, 0x0, 0x0, 0x0, 0xcd, 0x2, 0x1, 0x1, 0x2, 0x0, 0x0, 0x3b, 0x4e, 0x0, 0x0, 0x0, 0x3, 0x0, 0x0, 0x0, 0x0, 0x0, 0x0, 0x0, 0x2, 0x0, 0x1, 0x18, 0xab, 0x0, 0x3, 0x0, 0x2, 0x0, 0x0, 0x3, 0x3, 0x0, 0x3, 0x0, 0x0, 0x0, 0x1}},
	// An encrypted partition that is not the root, on a second disk.
	{"crypt-nonroot", []byte{0x1, 0x1, 0x3, 0x0, 0x0, 0x90, 0x0, 0x0, 0x0, 0x0, 0x0, 0x0, 0x0, 0x0, 0x1, 0x0, 0x0, 0x2, 0x0, 0x0, 0x0, 0x0, 0x0, 0x3, 0x1, 0x0, 0x0, 0x3, 0x2, 0x2, 0x0, 0x2, 0x0, 0x0, 0x3, 0x0, 0x0, 0x0, 0x0, 0x0, 0x0, 0x0, 0x0, 0x0, 0x3, 0x2, 0x0, 0x0, 0x0}},
	// LVM on LUKS on a wiped disk.
	{"lvm-luks", []byte{0x0, 0xde, 0x2, 0x2, 0x1, 0x0, 0x0, 0x0, 0x0, 0x0, 0x3, 0x0, 0x0, 0x1, 0x1, 0x0, 0x0, 0x0, 0x1, 0x0, 0x0, 0x0, 0x2, 0x0, 0x47, 0x0, 0x3, 0x3, 0x9d, 0x0, 0x0, 0x0, 0x0, 0x0}},
}

// TestLayoutSeedsPlan keeps the fuzz seeds what they are for: a seed the
// filter drops tests nothing.
func TestLayoutSeedsPlan(t *testing.T) {
	for _, sd := range layoutSeeds {
		if _, ok := genPlans(sd.data, ""); !ok {
			c := genConfig(sd.data, "")
			t.Errorf("seed %s: Validate %v", sd.name, c.Validate())
		}
	}
}

// scope is what a plan may touch, and what exists as its commands run.
type scope struct {
	pl      *storage.Plan
	strict  bool              // validated: every device exists when used, none written twice
	allowed map[string]string // device -> what it is
	vgs     map[string]bool
	lvs     map[string]bool // vg/lv
	exists  map[string]bool
	written map[string]string // device -> the command that wrote it
	luks    map[string]bool
	pvs     map[string]bool
	groups  map[string]bool
	partsOf map[string][]string // disk -> its plan partitions
	disks   map[string]bool
	arrays  map[string]bool
}

func newScope(t *testing.T, pl *storage.Plan) *scope {
	sc := &scope{pl: pl, allowed: map[string]string{}, vgs: map[string]bool{}, lvs: map[string]bool{},
		exists: map[string]bool{}, written: map[string]string{}, luks: map[string]bool{}, pvs: map[string]bool{},
		groups: map[string]bool{}, partsOf: map[string][]string{}, disks: map[string]bool{},
		arrays: map[string]bool{}}
	for _, d := range pl.DiskList() {
		sc.allowed[d], sc.disks[d], sc.exists[d] = "disk", true, true
	}
	for _, d := range pl.RaidDisks {
		sc.allowed[d], sc.disks[d] = "partitioned array", true
	}
	for _, p := range pl.Parts {
		switch {
		case p.LV:
			sc.lvs[p.VGName+"/"+p.LVName] = true
			sc.allowed["/dev/"+p.VGName+"/"+p.LVName] = "logical volume"
		case p.Raid:
			sc.allowed[storage.RaidDevice(p.RaidName)] = "array"
			sc.arrays[storage.RaidDevice(p.RaidName)] = true
		default:
			disk := p.Disk
			if disk == "" {
				disk = pl.Disk
			}
			if !sc.disks[disk] {
				t.Errorf("partition %s on %s, not a disk of the plan (%q, %q)", p.Device, disk, pl.DiskList(), pl.RaidDisks)
				continue
			}
			dev := storage.PartitionName(disk, p.Number)
			sc.allowed[dev] = "partition"
			sc.partsOf[disk] = append(sc.partsOf[disk], dev)
			if !pl.Wipe && !p.Create && !slices.Contains(pl.RaidDisks, disk) {
				sc.exists[dev] = true // kept: there before the install
			}
		}
		if p.IsPV {
			sc.vgs[p.VGName] = true
		}
		if p.Encrypted {
			sc.allowed["/dev/mapper/"+p.MapName] = "mapper"
		}
	}
	return sc
}

// device checks one device argument: a clean /dev path of the plan's,
// which exists by the time cmd runs.
func (sc *scope) device(t *testing.T, cmd []string, dev string) bool {
	t.Helper()
	switch {
	case !strings.HasPrefix(dev, "/dev/") || slices.Contains(strings.Split(dev, "/"), "..") || path.Clean(dev) != dev:
		t.Errorf("%q: device %q is not a clean /dev path", cmd, dev)
	case sc.allowed[dev] == "":
		t.Errorf("%q: %s is no device of the plan (disks %q)", cmd, dev, sc.pl.DiskList())
	case sc.strict && !sc.exists[dev]:
		t.Errorf("%q: %s does not exist yet", cmd, dev)
	default:
		return true
	}
	return false
}

// write records that cmd puts something on dev: a table, a filesystem, a
// LUKS header, a PV label, an array superblock. Two of them on one device
// and the first is lost.
func (sc *scope) write(t *testing.T, cmd []string, dev string) {
	t.Helper()
	if prev, ok := sc.written[dev]; ok && sc.strict {
		t.Errorf("%q: %s already written by %s", cmd, dev, prev)
	}
	sc.written[dev] = strings.Join(cmd, " ")
}

// checkCommands checks the commands the partition stage runs for a plan
// stay on the plan's devices and run in an order that works. Past
// skipValidationAll (strict false) a layout may not work — an array of
// no members, a disk named md0 beside an array md0 — but it still may
// not reach past the plan's devices.
func checkCommands(t *testing.T, pl *storage.Plan, cmds [][]string, strict bool) {
	t.Helper()
	sc := newScope(t, pl)
	sc.strict = strict
	for _, cmd := range cmds {
		if len(cmd) == 0 {
			t.Fatalf("an empty command")
		}
		for _, a := range cmd {
			if a == "" {
				t.Errorf("%q: an empty argument", cmd)
			}
		}
		// A shrink runs under setsid -w (a session of its own, out of the
		// terminal's Ctrl+C): the command is what it runs.
		if len(cmd) > 2 && cmd[0] == "setsid" && cmd[1] == "-w" {
			cmd = cmd[2:]
		}
		last := cmd[len(cmd)-1]
		switch tool := cmd[0]; {
		case tool == "parted":
			i := slices.IndexFunc(cmd, func(a string) bool { return a == "-s" || a == "--script" })
			if i < 0 || i+2 >= len(cmd) {
				t.Errorf("%q: no device", cmd)
				continue
			}
			dev := cmd[i+1]
			if sc.device(t, cmd, dev) && !sc.disks[dev] {
				t.Errorf("%q: %s partitioned, but it is not a disk the plan cuts", cmd, dev)
			}
			if cmd[i+2] == "mklabel" {
				if !pl.Wipe {
					t.Errorf("%q: a new table on a disk that is kept", cmd)
				}
				sc.write(t, cmd, dev)
				for _, p := range sc.partsOf[dev] {
					delete(sc.exists, p)
				}
			}
		case tool == "partprobe":
			if len(cmd) != 2 {
				t.Errorf("%q: not one device", cmd)
				continue
			}
			if sc.device(t, cmd, cmd[1]) {
				for _, p := range sc.partsOf[cmd[1]] {
					sc.exists[p] = true
				}
			}
		case tool == "sfdisk":
			if len(cmd) != 5 {
				t.Errorf("%q: not an entry change", cmd)
				continue
			}
			sc.device(t, cmd, cmd[2])
		case tool == "sh":
			// A resize: sfdisk or ntfsresize, the device the last word.
			w := strings.Fields(cmd[len(cmd)-1])
			if len(cmd) != 3 || cmd[1] != "-c" || len(w) == 0 {
				t.Errorf("%q: not a resize", cmd)
				continue
			}
			sc.device(t, cmd, w[len(w)-1])
		case tool == "blkdiscard", tool == "shred", tool == "e2fsck", tool == "ntfsresize", tool == "wipefs":
			sc.device(t, cmd, last)
		case tool == "resize2fs":
			sc.device(t, cmd, cmd[1])
		case tool == "btrfs":
			// [device] or [device]/subvolume: done on it mounted.
			if !strings.HasPrefix(last, "[") || !strings.Contains(last, "]") {
				t.Errorf("%q: no device", cmd)
				continue
			}
			sc.device(t, cmd, last[1:strings.Index(last, "]")])
		case tool == "mkswap", strings.HasPrefix(tool, "mkfs."):
			if sc.device(t, cmd, last) {
				if sc.strict && sc.disks[last] {
					t.Errorf("%q: a filesystem over the partition table of %s", cmd, last)
				}
				sc.write(t, cmd, last)
			}
		case tool == "cryptsetup":
			sc.cryptsetup(t, cmd)
		case tool == "mdadm":
			sc.mdadm(t, cmd)
		case tool == "pvcreate":
			if sc.device(t, cmd, last) {
				sc.write(t, cmd, last)
				sc.pvs[last] = true
			}
		case tool == "vgcreate":
			if len(cmd) < 3 || !sc.vgs[cmd[1]] || sc.groups[cmd[1]] {
				t.Errorf("%q: not a new group of the plan (%v)", cmd, sc.vgs)
				continue
			}
			for _, d := range cmd[2:] {
				if sc.device(t, cmd, d) && !sc.pvs[d] {
					t.Errorf("%q: %s is no physical volume yet", cmd, d)
				}
			}
			sc.groups[cmd[1]] = true
		case tool == "lvcreate":
			n := slices.Index(cmd, "-n")
			if n < 0 || n+3 != len(cmd) || !sc.groups[last] || !sc.lvs[last+"/"+cmd[n+1]] {
				t.Errorf("%q: not a volume of a group made before it (%v)", cmd, sc.lvs)
				continue
			}
			sc.exists["/dev/"+last+"/"+cmd[n+1]] = true
		default:
			t.Errorf("%q: a tool this check does not know: teach it the tool's device arguments", cmd)
		}
	}
}

func (sc *scope) cryptsetup(t *testing.T, cmd []string) {
	t.Helper()
	switch {
	case slices.Contains(cmd, "luksFormat"):
		dev := cmd[len(cmd)-2]
		if cmd[len(cmd)-1] != "-" {
			t.Errorf("%q: the passphrase not from stdin", cmd)
		}
		if sc.device(t, cmd, dev) {
			sc.write(t, cmd, dev)
			sc.luks[dev] = true
		}
	case slices.Contains(cmd, "luksOpen"):
		i := slices.Index(cmd, "luksOpen")
		if len(cmd) != i+4 || cmd[len(cmd)-1] != "-" {
			t.Errorf("%q: not luksOpen <device> <name> -", cmd)
			return
		}
		dev, name := cmd[i+1], cmd[i+2]
		if sc.device(t, cmd, dev) && !sc.luks[dev] {
			t.Errorf("%q: %s opened before it was formatted", cmd, dev)
		}
		mapper := "/dev/mapper/" + name
		if sc.allowed[mapper] != "mapper" || strings.ContainsAny(name, "/") {
			t.Errorf("%q: %s is no mapper of the plan", cmd, mapper)
		}
		if sc.strict && sc.exists[mapper] {
			t.Errorf("%q: %s opened twice", cmd, mapper)
		}
		sc.exists[mapper] = true
	default:
		t.Errorf("%q: a cryptsetup action this check does not know", cmd)
	}
}

func (sc *scope) mdadm(t *testing.T, cmd []string) {
	t.Helper()
	if len(cmd) < 4 || cmd[1] != "--create" {
		t.Errorf("%q: an mdadm action this check does not know", cmd)
		return
	}
	array := cmd[2]
	if !sc.arrays[array] {
		t.Errorf("%q: %s is no array of the plan", cmd, array)
	}
	if sc.strict && sc.exists[array] {
		t.Errorf("%q: %s created twice", cmd, array)
	}
	var devs int
	want := -1
	for _, a := range cmd[3:] {
		if strings.HasPrefix(a, "--") {
			if v, ok := strings.CutPrefix(a, "--raid-devices="); ok {
				want, _ = strconv.Atoi(v)
			}
			if v, ok := strings.CutPrefix(a, "--spare-devices="); ok {
				n, _ := strconv.Atoi(v)
				want += n
			}
			continue
		}
		devs++
		if sc.device(t, cmd, a) {
			if sc.allowed[a] == "array" {
				t.Errorf("%q: array %s a member of another", cmd, a)
			}
			sc.write(t, cmd, a)
		}
	}
	if devs != want || sc.strict && devs == 0 {
		t.Errorf("%q: %d devices for %d asked", cmd, devs, want)
	}
	sc.exists[array] = true
}

// FuzzPartitionCommands checks the commands the partition stage runs —
// every destructive one: parted, wipes, resizes, cryptsetup, mdadm, LVM,
// mkfs — for any layout a config that passes Validate (with
// skipValidationAll or without) plans: each names only a device of the
// plan — its disks, their partitions, its arrays and theirs, its mappers
// and logical volumes — by a clean path, never another disk the
// confirmation did not name; each runs on a device that exists by then
// (LUKS formatted before it is opened, an array created before it is used,
// PVs before their group, the group before its volumes, a filesystem only
// after what it is made on); and no device is written twice.
func FuzzPartitionCommands(f *testing.F) {
	for _, sd := range layoutSeeds {
		f.Add(sd.data, "")
	}
	f.Add([]byte{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 255}, "sdz")
	f.Fuzz(func(t *testing.T, data []byte, s string) {
		plans, _ := genPlans(data, s)
		for _, lo := range plans {
			cmds, err := PartitionCommands(lo.pl)
			if err != nil {
				continue
			}
			checkCommands(t, lo.pl, cmds, lo.validated)
		}
	})
}

// FuzzFstabCrypttab checks /etc/fstab and /etc/crypttab, as the install
// renders them for any layout that plans, are in shape: fstab six fields
// on every line, none empty, each mountpoint once, every mount of the
// plan listed; crypttab two to four fields, a mapper of the plan's that the
// initramfs does not open, each once.
func FuzzFstabCrypttab(f *testing.F) {
	for _, sd := range layoutSeeds {
		f.Add(sd.data, "", true)
	}
	f.Fuzz(func(t *testing.T, data []byte, s string, uuids bool) {
		plans, _ := genPlans(data, s)
		for _, lo := range plans {
			pl := lo.pl
			ids := map[string]string{}
			for i, p := range pl.Parts {
				if uuids {
					ids[p.EffectiveDevice()] = fmt.Sprintf("%08x-0000-4000-8000-%012x", i, i)
				}
			}
			if mountable(pl) {
				checkFstab(t, pl, pl.Fstab(ids), lo.validated)
			}

			in := &installer{plan: pl}
			_, nonRoot, _ := in.cryptVolumes()
			for _, key := range []string{"", keyfilePath} {
				tab, err := crypttabText(nonRoot, key, func(dev string) (string, error) {
					return "uuid-" + strings.TrimPrefix(dev, "/dev/"), nil
				})
				if err != nil {
					t.Fatal(err)
				}
				checkCrypttab(t, pl, tab, len(nonRoot))
			}
		}
	})
}

// checkFstab checks an fstab's shape; for a validated layout (strict) also
// that every mountpoint is an absolute path and mounted once, which
// skipValidationAll leaves to the operator.
// mountable reports whether an install of pl gets as far as writing its
// fstab: every filesystem made (MkfsCommands takes only a type it knows)
// and mounted — the type of one kept, or of an array that makes none, has
// to be one mount(2) can take, which "" or "ext4 x", let past
// skipValidationAll, are not.
func mountable(pl *storage.Plan) bool {
	if _, err := pl.MkfsCommands(); err != nil {
		return false
	}
	for _, p := range pl.Parts {
		if (p.Mountpoint != "" || p.Filesystem == "swap") &&
			(p.Filesystem == "" || strings.IndexFunc(p.Filesystem, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0) {
			return false
		}
	}
	return true
}

func checkFstab(t *testing.T, pl *storage.Plan, fstab string, strict bool) {
	t.Helper()
	seen := map[string]bool{}
	lines := 0
	for _, line := range strings.Split(strings.TrimSuffix(fstab, "\n"), "\n") {
		if strings.HasPrefix(line, "#") {
			continue
		}
		lines++
		f := strings.Fields(line)
		cols := strings.Split(line, "\t")
		if len(f) != 6 || len(cols) != 5 || slices.Contains(cols, "") {
			t.Errorf("fstab line %q: not six fields", line)
			continue
		}
		for _, n := range f[4:] {
			if _, err := strconv.Atoi(n); err != nil {
				t.Errorf("fstab line %q: %q is not a number", line, n)
			}
		}
		if f[1] == "none" {
			if f[2] != "swap" {
				t.Errorf("fstab line %q: no mountpoint, and not swap", line)
			}
			continue
		}
		if strict && !strings.HasPrefix(f[1], "/") {
			t.Errorf("fstab line %q: mountpoint not absolute", line)
		}
		if strict && seen[f[1]] {
			t.Errorf("fstab: %s mounted twice:\n%s", f[1], fstab)
		}
		seen[f[1]] = true
	}
	want := len(pl.MountOrder())
	for _, p := range pl.Parts {
		if p.Filesystem == "swap" {
			want++
		}
	}
	if lines != want {
		t.Errorf("fstab: %d entries for %d mounts and swaps:\n%s", lines, want, fstab)
	}
}

func checkCrypttab(t *testing.T, pl *storage.Plan, tab string, volumes int) {
	t.Helper()
	names, rootPath := map[string]bool{}, map[string]bool{}
	rootVG := ""
	for _, p := range pl.Parts {
		if p.LV && p.Mountpoint == "/" {
			rootVG = p.VGName
		}
	}
	for _, p := range pl.Parts {
		if p.Encrypted {
			names[p.MapName] = true
			rootPath[p.MapName] = p.Mountpoint == "/" || p.IsPV && rootVG != "" && p.VGName == rootVG
		}
	}
	seen := map[string]bool{}
	lines := 0
	for _, line := range strings.Split(strings.TrimSuffix(tab, "\n"), "\n") {
		if line == "" && tab == "" {
			break
		}
		lines++
		f := strings.Fields(line)
		if len(f) < 2 || len(f) > 4 || strings.Join(f, " ") != line {
			t.Errorf("crypttab line %q: not 2-4 fields", line)
			continue
		}
		if !names[f[0]] || seen[f[0]] {
			t.Errorf("crypttab line %q: %s is no mapper of the plan, or listed twice", line, f[0])
		}
		seen[f[0]] = true
		if rootPath[f[0]] {
			t.Errorf("crypttab lists %s, on the root's path, which the initramfs opens: %q", f[0], line)
		}
	}
	if lines != volumes {
		t.Errorf("crypttab: %d lines for %d volumes:\n%s", lines, volumes, tab)
	}
}
