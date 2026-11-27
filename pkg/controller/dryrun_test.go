package controller

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sunlightlinux/slinstaller/pkg/config"
)

// The dry run promises that --apply runs what it printed. Each layout is
// partitioned here with cryptsetup as a stand-in that writes to the same
// log as the Runner, and the commands the stage ran, in their order, must
// be PartitionCommands — LUKS, RAID and LVM included, which the dry run
// used to leave out.
func TestPartitionCommandsAreWhatThePartitionStageRuns(t *testing.T) {
	cases := []struct {
		name string
		cfg  *config.Config
		want []string // what this layout must show, so the case checks something
	}{
		{"plain", twoPartConfig(), []string{"mkfs.ext4"}},
		{"luks", cryptConfig("/boot/efi"), []string{"luksFormat --pbkdf pbkdf2 /dev/sda2 -", "luksOpen /dev/sda2"}},
		{"encrypted /boot", cryptConfig("/boot/efi", &config.Media{Name: "sda3", Type: "crypt", FsType: "ext4", Mountpoint: "/boot", Size: "1G"}), []string{"--pbkdf pbkdf2 /dev/sda3"}},
		{"lvm on luks", lvmConfig("/boot/efi", true), []string{"luksOpen /dev/sda2", "pvcreate", "lvcreate"}},
		{"raid", raidConfig(), []string{"mdadm --create"}},
		{"encrypted raid", serverConfig(&config.RaidArray{Name: "md0", Level: "1", Encrypted: true, FsType: "ext4", Mountpoint: "/"}),
			[]string{"mdadm --create", "/dev/md0 -", "mkfs.ext4 -v -F -b 4096 /dev/mapper/cryptmd0"}},
		{"partitioned raid", serverConfig(&config.RaidArray{Name: "md0", Level: "1",
			Partitions: []*config.Media{{Name: "md0p1", FsType: "ext4", Mountpoint: "/"}}}), []string{"mdadm --create", "parted -s /dev/md0 mklabel gpt"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := fakeCryptsetup(t)
			log := filepath.Join(dir, "args")
			script := "#!/bin/sh\necho cryptsetup \"$@\" >> \"" + log + "\"\ncat > /dev/null\n"
			if err := os.WriteFile(filepath.Join(dir, "cryptsetup"), []byte(script), 0o755); err != nil {
				t.Fatal(err)
			}
			var r recorder
			in, _ := cryptInstaller(t, c.cfg, &r)
			in.opts.Runner = func(args []string) error {
				if args[0] == "udevadm" {
					return nil // settling, between the steps
				}
				f, err := os.OpenFile(log, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
				if err != nil {
					return err
				}
				defer f.Close()
				_, err = f.WriteString(strings.Join(args, " ") + "\n")
				return err
			}
			if err := in.partition(); err != nil {
				t.Fatalf("partition: %v", err)
			}
			ran, _ := os.ReadFile(log)

			cmds, err := PartitionCommands(in.plan)
			if err != nil {
				t.Fatal(err)
			}
			var shown strings.Builder
			for _, cmd := range cmds {
				if cmd[0] == "btrfs" {
					continue // subvolumes are made at the mount stage
				}
				shown.WriteString(strings.Join(cmd, " ") + "\n")
			}
			if string(ran) != shown.String() {
				t.Errorf("the stage ran:\n%s\nthe dry run shows:\n%s", ran, shown.String())
			}
			for _, w := range c.want {
				if !strings.Contains(shown.String(), w) {
					t.Errorf("dry run lacks %q:\n%s", w, shown.String())
				}
			}
		})
	}
}
