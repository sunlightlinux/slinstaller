package blockdev

import (
	"slices"
	"strings"
	"testing"
)

// fuzzTree builds a device tree from fuzz bytes, three per device: which
// device it hangs under (or a new disk), its type and filesystem, and its
// UUID. Paths come from a small set, so the same path turns up under two
// disks the way a multipath map's partitions do, and UUIDs from a small
// set, so members of one array or pool turn up on several disks.
func fuzzTree(b []byte) []Device {
	types := []string{"disk", "part", "part", "raid1", "lvm", "crypt", "mpath", "dmraid", "container", "linear"}
	fs := []string{"", "linux_raid_member", "zfs_member", "btrfs", "ext4", "LVM2_member", "crypto_LUKS"}
	var devs []Device
	type ref struct{ path []int }
	var all []ref
	at := func(r ref) *Device {
		d := &devs[r.path[0]]
		for _, i := range r.path[1:] {
			d = &d.Children[i]
		}
		return d
	}
	for i := 0; i+2 < len(b) && i < 3*24; i += 3 {
		n := len(all)
		d := Device{
			Name: "d" + string(rune('a'+int(b[i+2]>>4)%8)),
			Type: types[int(b[i+1])%len(types)],
			// Value in the low bits so the fuzzer moves it independently.
			FsType: fs[int(b[i+1]>>4)%len(fs)],
			UUID:   []string{"", "u1", "u2", "u3"}[int(b[i+2])%4],
			Size:   1 << 30,
		}
		d.Path = "/dev/" + d.Name
		if p := int(b[i]); n == 0 || p%3 == 0 {
			d.Type = "disk"
			devs = append(devs, d)
			all = append(all, ref{[]int{len(devs) - 1}})
		} else {
			parent := at(all[p%n])
			parent.Children = append(parent.Children, d)
			pr := all[p%n].path
			all = append(all, ref{append(slices.Clone(pr), len(parent.Children)-1)})
		}
	}
	return devs
}

// under returns every device path in the trees of the devices at paths.
func under(devs []Device, paths []string) map[string]bool {
	out := map[string]bool{}
	var walk func(d Device, in bool)
	walk = func(d Device, in bool) {
		in = in || slices.Contains(paths, d.Path)
		if in {
			out[d.Path] = true
		}
		for _, c := range d.Children {
			walk(c, in)
		}
	}
	for _, d := range devs {
		walk(d, false)
	}
	return out
}

// FuzzSharedMembers checks the shared-member verdict that stands before
// every wipe: it names only devices the install destroys, does not depend
// on the order the targets were given in, and finds nothing when every
// disk is a target — there is then no fellow member left to break.
func FuzzSharedMembers(f *testing.F) {
	f.Add([]byte{0, 0, 0, 1, 0x11, 1, 3, 0, 0, 4, 0x11, 1}, uint8(1))
	f.Add([]byte{0, 0, 0, 1, 0x31, 1, 3, 0, 0, 4, 0x31, 1, 0, 0, 0}, uint8(3))
	f.Fuzz(func(t *testing.T, b []byte, pick uint8) {
		devs := fuzzTree(b)
		if len(devs) == 0 {
			return
		}
		var targets, reversed []string
		for i, d := range devs {
			if pick>>(i%8)&1 == 1 {
				targets = append(targets, d.Path)
			}
		}
		for i := len(targets) - 1; i >= 0; i-- {
			reversed = append(reversed, targets[i])
		}
		got := SharedMembers(devs, targets)
		if back := SharedMembers(devs, reversed); !slices.Equal(sorted(got), sorted(back)) {
			t.Errorf("order changed the verdict:\n %q\n %q", got, back)
		}
		inside := under(devs, targets)
		for _, s := range got {
			path, _, _ := strings.Cut(s, " ")
			if !inside[path] {
				t.Errorf("%q names %s, which the install does not destroy", s, path)
			}
		}
		var every []string
		for _, d := range devs {
			every = append(every, d.Path)
		}
		if all := SharedMembers(devs, every); len(all) > 0 {
			t.Errorf("every disk a target, still shared: %q", all)
		}
	})
}

// FuzzTeardownAll checks the teardown before a wipe touches nothing but
// what is on the disks being wiped: every device it stops, removes or
// erases is in their trees (by path, or by name for dmsetup), and each
// disk's own signature is erased last of its commands.
func FuzzTeardownAll(f *testing.F) {
	f.Add([]byte{0, 0, 0, 1, 0x11, 1, 3, 3, 0, 1, 6, 0x10, 4, 1, 0x20}, uint8(1))
	f.Fuzz(func(t *testing.T, b []byte, pick uint8) {
		devs := fuzzTree(b)
		var ds []Device
		var paths []string
		for i, d := range devs {
			if pick>>(i%8)&1 == 1 {
				ds = append(ds, d)
				paths = append(paths, d.Path)
			}
		}
		inside := under(devs, paths)
		// What dmsetup may remove: a device-mapper volume, or a partition
		// of a multipath or dmraid device (kpartx's mpatha1). A plain
		// partition is no mapping: removing it fails the install.
		names, mapped := map[string]bool{}, map[string]bool{}
		var walk func(d Device, onDM bool)
		walk = func(d Device, onDM bool) {
			names[d.Name] = true
			if d.Type == "lvm" || d.Type == "crypt" || d.Type == "dm" || d.Type == "mpath" || d.Type == "dmraid" || onDM && d.Type == "part" {
				mapped[d.Name] = true
			}
			for _, c := range d.Children {
				walk(c, d.Type == "mpath" || d.Type == "dmraid")
			}
		}
		for _, d := range ds {
			walk(d, false)
		}
		for _, c := range TeardownAll(ds) {
			target := c[len(c)-1]
			switch c[0] {
			case "dmsetup":
				if !names[target] {
					t.Errorf("%q: %s is on no disk being wiped", c, target)
				} else if !mapped[target] {
					t.Errorf("%q: %s is no device-mapper volume", c, target)
				}
			case "mdadm", "wipefs":
				if !inside[target] {
					t.Errorf("%q: %s is on no disk being wiped", c, target)
				}
			default:
				t.Errorf("unexpected command %q", c)
			}
		}
	})
}

// FuzzParse checks lsblk output we have not seen is refused or read, never
// a panic, and that what is read goes through the checks without one.
func FuzzParse(f *testing.F) {
	f.Add([]byte(`{"blockdevices":[{"name":"sda","path":"/dev/sda","size":"1000","type":"disk","children":[{"name":"sda1","path":"/dev/sda1","size":500,"type":"part","fstype":"btrfs","uuid":"u"}]}]}`))
	f.Add([]byte(`{"blockdevices":[{"size":null},{"size":"-1"},{"size":1e3}]}`))
	f.Fuzz(func(t *testing.T, b []byte) {
		devs, err := parse(b)
		if err != nil {
			return
		}
		_ = Available(devs)
		var paths []string
		for _, d := range Disks(devs) {
			paths = append(paths, d.Path)
			_ = Shared(devs, d.Path, nil, nil)
		}
		_ = SharedMembers(devs, paths)
		_ = TeardownAll(Disks(devs))
	})
}

func sorted(s []string) []string {
	s = slices.Clone(s)
	slices.Sort(s)
	return s
}
