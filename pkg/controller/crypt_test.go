package controller

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sunlightlinux/slinstaller/pkg/config"
)

// The encrypted paths call cryptsetup directly (it is fed the passphrase on
// stdin, so it cannot go through the Runner). These tests put a stand-in
// cryptsetup first on PATH: it records its arguments and stdin and answers
// luksUUID with a UUID derived from the device, so each volume is
// distinguishable. No device is touched.

const testPassphrase = "correct horse battery staple"

func fakeCryptsetup(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	script := `#!/bin/sh
echo "$@" >> "` + dir + `/args"
if [ "$1" = luksUUID ]; then
	echo "uuid-$(basename "$2")"
	exit 0
fi
cat > "` + dir + `/stdin"
`
	if err := os.WriteFile(filepath.Join(dir, "cryptsetup"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return dir
}

// cryptConfig is a UEFI layout with an encrypted root; extra partitions are
// appended after it. espAt is the ESP's mountpoint: "/boot" leaves the kernel
// on the unencrypted ESP, "/boot/efi" puts /boot on the encrypted root.
func cryptConfig(espAt string, extra ...*config.Media) *config.Config {
	children := []*config.Media{
		{Name: "sda1", Type: "part", FsType: "vfat", Mountpoint: espAt, Size: "512M"},
		{Name: "sda2", Type: "crypt", FsType: "ext4", Mountpoint: "/", Size: "20G"},
	}
	return &config.Config{TargetMedia: []*config.Media{{
		Name: "sda", Type: "disk", Children: append(children, extra...),
	}}}
}

// cryptInstaller is stagedInstaller with the passphrase set and cryptsetup's
// output captured instead of going to stderr.
func cryptInstaller(t *testing.T, cfg *config.Config, r *recorder) (*installer, *bytes.Buffer) {
	t.Helper()
	in := stagedInstaller(t, cfg, r)
	var out bytes.Buffer
	in.opts.Passphrase = testPassphrase
	in.opts.Output = &out
	mkTarget(t, in, "etc", "default")
	return in, &out
}

func cmdlineArgs(t *testing.T, in *installer) []string {
	t.Helper()
	return strings.Fields(grubCmdline(t, in))
}

func count(args []string, prefix string) (n int, last string) {
	for _, a := range args {
		if strings.HasPrefix(a, prefix) {
			n++
			last = a
		}
	}
	return n, last
}

func TestCryptRootOnUnencryptedBoot(t *testing.T) {
	dir := fakeCryptsetup(t)
	var r recorder
	in, _ := cryptInstaller(t, cryptConfig("/boot"), &r)

	// One volume, opened by the initramfs, which asks for the passphrase:
	// no keyfile and no crypttab.
	if err := in.crypttab(); err != nil {
		t.Fatalf("crypttab: %v", err)
	}
	if in.keyfile != "" {
		t.Errorf("keyfile set for a lone crypt root: %q", in.keyfile)
	}
	for _, rel := range []string{"etc/crypttab", "etc/cryptkeys"} {
		if _, err := os.Stat(filepath.Join(in.opts.Root, rel)); err == nil {
			t.Errorf("%s written for a lone crypt root", rel)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "args")); err == nil {
		t.Errorf("cryptsetup was called: %s", readFile(t, filepath.Join(dir, "args")))
	}

	if err := in.bootloader(); err != nil {
		t.Fatalf("bootloader: %v", err)
	}
	args := cmdlineArgs(t, in)
	if n, got := count(args, "rd.luks.uuid="); n != 1 || got != "rd.luks.uuid=uuid-sda2" {
		t.Errorf("rd.luks.uuid: %d of them, last %q; want the root's once\n%v", n, got, args)
	}
	if n, _ := count(args, "rd.luks.crypttab=0"); n != 1 {
		t.Errorf("rd.luks.crypttab=0 missing: %v", args)
	}
	// The passphrase is typed at boot: no key on the command line, and GRUB
	// does not have to unlock anything.
	if n, _ := count(args, "rd.luks.key="); n != 0 {
		t.Errorf("rd.luks.key set though /boot is not encrypted: %v", args)
	}
	if grub := readTarget(t, in, "etc", "default", "grub"); strings.Contains(grub, "GRUB_ENABLE_CRYPTODISK") {
		t.Errorf("GRUB cryptodisk enabled though /boot is not encrypted:\n%s", grub)
	}
	// The initramfs is rebuilt so it carries the crypt module.
	if flat := r.flat(); !strings.Contains(flat, "chroot "+in.opts.Root+" xbps-reconfigure -fa") {
		t.Errorf("initramfs not rebuilt for an encrypted root:\n%s", flat)
	}
}

func TestCryptRootAndHomeShareOnePassphrase(t *testing.T) {
	dir := fakeCryptsetup(t)
	var r recorder
	home := &config.Media{Name: "sda3", Type: "crypt", FsType: "ext4", Mountpoint: "/home"}
	in, out := cryptInstaller(t, cryptConfig("/boot", home), &r)

	if err := in.crypttab(); err != nil {
		t.Fatalf("crypttab: %v", err)
	}

	// A keyfile on the (encrypted) root opens the non-root volume after boot.
	if in.keyfile != keyfilePath {
		t.Fatalf("keyfile = %q, want %q", in.keyfile, keyfilePath)
	}
	full := filepath.Join(in.opts.Root, keyfilePath)
	st, err := os.Stat(full)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o400 || st.Size() != 2048 {
		t.Errorf("keyfile mode %v size %d, want 0400 and 2048", st.Mode().Perm(), st.Size())
	}
	if dst, err := os.Stat(filepath.Dir(full)); err != nil || dst.Mode().Perm() != 0o700 {
		t.Errorf("keyfile directory mode: %v, %v", dst.Mode().Perm(), err)
	}

	// It is added as a key slot to every encrypted volume, authenticated
	// with the passphrase on stdin.
	args := readFile(t, filepath.Join(dir, "args"))
	for _, dev := range []string{"/dev/sda2", "/dev/sda3"} {
		if !strings.Contains(args, "--key-file=- luksAddKey "+dev+" "+full) {
			t.Errorf("keyfile not added to %s:\n%s", dev, args)
		}
	}
	if got := readFile(t, filepath.Join(dir, "stdin")); got != testPassphrase {
		t.Errorf("cryptsetup stdin = %q, want the passphrase", got)
	}
	if strings.Contains(args, testPassphrase) || strings.Contains(out.String(), testPassphrase) {
		t.Error("passphrase leaked into cryptsetup's arguments or the install log")
	}

	// crypttab lists only the non-root volume — root is the initramfs's —
	// and is private, since it names the keyfile.
	tab := readTarget(t, in, "etc", "crypttab")
	if !strings.Contains(tab, "UUID=uuid-sda3 "+keyfilePath+" luks") || strings.Contains(tab, "uuid-sda2") {
		t.Errorf("crypttab:\n%s", tab)
	}
	if st, _ := os.Stat(filepath.Join(in.opts.Root, "etc", "crypttab")); st.Mode().Perm() != 0o600 {
		t.Errorf("crypttab mode = %v, want 0600", st.Mode().Perm())
	}
	// /boot is not encrypted, so the keyfile must not ride in the initramfs.
	if _, err := os.Stat(filepath.Join(in.opts.Root, "etc", "dracut.conf.d", "10-slinstaller-keyfile.conf")); err == nil {
		t.Error("keyfile embedded in the initramfs though /boot is unencrypted")
	}

	if err := in.bootloader(); err != nil {
		t.Fatalf("bootloader: %v", err)
	}
	cmd := cmdlineArgs(t, in)
	if n, got := count(cmd, "rd.luks.uuid="); n != 1 || got != "rd.luks.uuid=uuid-sda2" {
		t.Errorf("initramfs should unlock only root: %d rd.luks.uuid, last %q\n%v", n, got, cmd)
	}
	if n, _ := count(cmd, "rd.luks.key="); n != 0 {
		t.Errorf("rd.luks.key on the command line with an unencrypted /boot: %v", cmd)
	}
}

func TestEncryptedBootUnlocksThroughGrubAndAKeyfile(t *testing.T) {
	fakeCryptsetup(t)
	var r recorder
	// The ESP at /boot/efi leaves /boot itself on the encrypted root.
	in, _ := cryptInstaller(t, cryptConfig("/boot/efi"), &r)

	if err := in.crypttab(); err != nil {
		t.Fatalf("crypttab: %v", err)
	}
	// GRUB asks for the passphrase; a keyfile inside the (encrypted)
	// initramfs then opens root without asking again.
	if in.keyfile != keyfilePath {
		t.Fatalf("keyfile = %q, want one for an encrypted /boot", in.keyfile)
	}
	conf := readTarget(t, in, "etc", "dracut.conf.d", "10-slinstaller-keyfile.conf")
	if !strings.Contains(conf, keyfilePath) {
		t.Errorf("dracut keyfile conf:\n%s", conf)
	}
	// Root is the only volume, so there is still nothing for crypttab.
	if _, err := os.Stat(filepath.Join(in.opts.Root, "etc", "crypttab")); err == nil {
		t.Error("crypttab written with no non-root volume")
	}

	if err := in.bootloader(); err != nil {
		t.Fatalf("bootloader: %v", err)
	}
	if grub := readTarget(t, in, "etc", "default", "grub"); !strings.Contains(grub, "GRUB_ENABLE_CRYPTODISK=y") {
		t.Errorf("GRUB cannot unlock /boot without GRUB_ENABLE_CRYPTODISK=y:\n%s", grub)
	}
	cmd := cmdlineArgs(t, in)
	if n, got := count(cmd, "rd.luks.key="); n != 1 || got != "rd.luks.key="+keyfilePath {
		t.Errorf("rd.luks.key: %d, last %q\n%v", n, got, cmd)
	}
	if n, _ := count(cmd, "rd.luks.uuid=uuid-sda2"); n != 1 {
		t.Errorf("rd.luks.uuid for root missing: %v", cmd)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
