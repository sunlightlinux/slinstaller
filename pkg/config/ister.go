package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// isterConfig is the JSON descriptor of the installer clr-installer
// replaced ("ister"), which clr-installer's --json-yaml converts; the same
// conversion is offered here, so a descriptor kept from then can be turned
// into a config.
type isterConfig struct {
	DestinationType string `json:"DestinationType"`
	PartitionLayout []struct {
		Disk      string `json:"disk"`
		Partition int    `json:"partition"`
		Size      string `json:"size"`
	} `json:"PartitionLayout"`
	FilesystemTypes []struct {
		Disk      string `json:"disk"`
		Partition int    `json:"partition"`
		Type      string `json:"type"`
		Options   string `json:"options"`
	} `json:"FilesystemTypes"`
	PartitionMountPoints []struct {
		Disk      string `json:"disk"`
		Partition int    `json:"partition"`
		Mount     string `json:"mount"`
	} `json:"PartitionMountPoints"`
	// A number, or "" / "latest" (clr-installer's own fixtures use the
	// latter; its UintString takes both as 0): raw, read by isterVersion.
	Version json.RawMessage `json:"Version"`
	Bundles []string        `json:"Bundles"`
	Users   []struct {
		Username string `json:"username"`
		Key      string `json:"key"`
		Sudo     bool   `json:"sudo"`
		Password string `json:"password"`
	} `json:"Users"`
	Hostname string `json:"Hostname"`
	StaticIP *struct {
		Iface   string `json:"iface"`
		Address string `json:"address"`
		Gateway string `json:"gateway"`
		DNS     string `json:"dns"`
	} `json:"Static_IP"`
	PostNonChroot      []string `json:"PostNonChroot"`
	PostNonChrootShell []string `json:"PostNonChrootShell"`
	LegacyBios         bool     `json:"LegacyBios"`
	HTTPSProxy         string   `json:"HTTPSProxy"`
	HTTPProxy          string   `json:"HTTPProxy"`
	MirrorURL          string   `json:"MirrorURL"`
	Cmdline            string   `json:"cmdline"`
	VersionURL         string   `json:"VersionURL"`
}

// FromJSONDescriptor converts an "ister" JSON descriptor into a config, as
// clr-installer's --json-yaml does: each disk becomes a block-devices alias
// and a targetMedia entry written against it, with its partitions; users,
// hostname, a static address, post-install commands, the mirror, the
// proxies and the kernel command line are carried over. notes say what the
// descriptor asks for that the config cannot express here.
func FromJSONDescriptor(path string) (*Config, []string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	var ic isterConfig
	if err := json.Unmarshal(data, &ic); err != nil {
		return nil, nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	c := &Config{Keyboard: "us", Language: "en_US.UTF-8", Hostname: ic.Hostname,
		SwupdMirror: ic.MirrorURL, HTTPSProxy: ic.HTTPSProxy, HTTPProxy: ic.HTTPProxy}
	var notes []string

	alias := func(disk string) string { return strings.TrimSuffix(disk, filepath.Ext(disk)) }
	disks := map[string]*Media{}
	parts := map[string]map[int]*Media{}
	for _, pl := range ic.PartitionLayout {
		d := disks[pl.Disk]
		if d == nil {
			file := "/dev/" + pl.Disk
			switch ic.DestinationType {
			case "physical":
			case "virtual":
				// An image file: kept as the descriptor names it. Validate
				// then says, in its own words, that this is not supported.
				file = pl.Disk
				notes = append(notes, fmt.Sprintf("%s is an image file (DestinationType virtual); slinstaller installs to disks, so point the %s alias at one", pl.Disk, alias(pl.Disk)))
			default:
				return nil, nil, fmt.Errorf("%s: DestinationType %q is neither physical nor virtual", path, ic.DestinationType)
			}
			d = &Media{Name: "${" + alias(pl.Disk) + "}", Type: "disk"}
			disks[pl.Disk], parts[pl.Disk] = d, map[int]*Media{}
			c.TargetMedia = append(c.TargetMedia, d)
			c.BlockDevices = append(c.BlockDevices, &StorageAlias{Name: alias(pl.Disk), File: file})
		}
		if parts[pl.Disk][pl.Partition] != nil {
			return nil, nil, fmt.Errorf("%s: partition %d of %s is defined twice", path, pl.Partition, pl.Disk)
		}
		size := pl.Size
		if size == "rest" {
			size = "0"
		}
		parts[pl.Disk][pl.Partition] = &Media{
			Name: fmt.Sprintf("${%s}%d", alias(pl.Disk), pl.Partition), Type: "part", Size: size}
	}
	find := func(what, disk string, n int) (*Media, error) {
		if p := parts[disk][n]; p != nil {
			return p, nil
		}
		return nil, fmt.Errorf("%s: %s names partition %d of %s, which PartitionLayout does not define", path, what, n, disk)
	}
	for _, ft := range ic.FilesystemTypes {
		p, err := find("FilesystemTypes", ft.Disk, ft.Partition)
		if err != nil {
			return nil, nil, err
		}
		p.FsType, p.Options = ft.Type, ft.Options
	}
	for _, mp := range ic.PartitionMountPoints {
		p, err := find("PartitionMountPoints", mp.Disk, mp.Partition)
		if err != nil {
			return nil, nil, err
		}
		p.Mountpoint = mp.Mount
	}
	for disk, d := range disks {
		var nums []int
		for n := range parts[disk] {
			nums = append(nums, n)
		}
		sort.Ints(nums)
		for _, n := range nums {
			d.Children = append(d.Children, parts[disk][n])
		}
	}

	if v := isterVersion(ic.Version); v > 0 {
		c.Version = v
	}
	// The descriptor lists its kernel among the bundles; a config names it
	// apart. The rest are carried as they are (and, being another
	// distribution's, name nothing slinstaller installs).
	for _, b := range ic.Bundles {
		if strings.HasPrefix(b, "kernel-") && c.Kernel == "" {
			c.Kernel = b
			continue
		}
		c.Bundles = append(c.Bundles, b)
	}
	for _, u := range ic.Users {
		usr := &User{Login: u.Username, Admin: u.Sudo, Password: u.Password}
		if u.Key != "" {
			// ister's key is the path of a public-key file, beside the
			// descriptor when relative: taken as the key itself, the file
			// name went into authorized_keys.
			// (A value that is itself a public key line is taken as one.)
			kp := u.Key
			if looksLikeSSHKey(kp) {
				usr.SSHKeys = []string{kp}
				kp = ""
			} else if !filepath.IsAbs(kp) {
				kp = filepath.Join(filepath.Dir(path), kp)
			}
			if kp == "" {
				// taken above
			} else if data, err := os.ReadFile(kp); err == nil && strings.TrimSpace(string(data)) != "" {
				for _, l := range strings.Split(strings.TrimSpace(string(data)), "\n") {
					if l = strings.TrimSpace(l); l != "" && !strings.HasPrefix(l, "#") {
						usr.SSHKeys = append(usr.SSHKeys, l)
					}
				}
			} else {
				notes = append(notes, fmt.Sprintf("user %s: key file %s could not be read, so no SSH key is set - add it under ssh-keys", u.Username, kp))
			}
		}
		if u.Sudo && u.Password == "" {
			notes = append(notes, fmt.Sprintf("user %s is an administrator without a password: give it one (--genpass), or the config is refused", u.Username))
		}
		c.Users = append(c.Users, usr)
	}
	if ip := ic.StaticIP; ip != nil {
		addr, mask, ok := strings.Cut(ip.Address, "/")
		if !ok {
			return nil, nil, fmt.Errorf("%s: Static_IP address %q is not in address/prefix form", path, ip.Address)
		}
		c.NetworkInterfaces = []*Interface{{Name: ip.Iface, DHCP: "false", Gateway: ip.Gateway, DNS: ip.DNS,
			Addrs: []*Address{{IP: addr, Netmask: mask}}}}
	}
	for _, cmd := range ic.PostNonChroot {
		c.PostInstall = append(c.PostInstall, &Hook{Cmd: cmd})
	}
	for _, cmd := range ic.PostNonChrootShell {
		c.PostInstall = append(c.PostInstall, &Hook{Cmd: "/bin/bash -c " + cmd})
	}
	if f := strings.Fields(ic.Cmdline); len(f) > 0 {
		c.KernelArguments = &KernelArguments{Add: f}
	}
	if ic.LegacyBios {
		notes = append(notes, "LegacyBios is set; slinstaller installs for UEFI only, so the layout needs an EFI System Partition (vfat at /boot)")
	}
	if ic.VersionURL != "" {
		notes = append(notes, "VersionURL has no counterpart in a config and is left out")
	}
	return c, notes, nil
}

// isterVersion reads ister's Version: a number, a numeric string, or ""
// and "latest" (0, as clr-installer reads them).
func isterVersion(raw json.RawMessage) uint {
	var n json.Number
	if json.Unmarshal(raw, &n) == nil {
		if v, err := n.Int64(); err == nil && v > 0 {
			return uint(v)
		}
		return 0
	}
	var str string
	if json.Unmarshal(raw, &str) == nil {
		if v, err := strconv.ParseUint(str, 10, 32); err == nil {
			return uint(v)
		}
	}
	return 0
}

// looksLikeSSHKey reports whether s is an OpenSSH public key line rather than
// a file name.
func looksLikeSSHKey(s string) bool {
	for _, p := range []string{"ssh-", "ecdsa-sha2-", "sk-ssh-", "sk-ecdsa-"} {
		if strings.HasPrefix(s, p) && strings.Contains(s, " ") {
			return true
		}
	}
	return false
}
