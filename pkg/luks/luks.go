// Package luks encrypts partitions with LUKS, via cryptsetup.
//
// clr-installer does the same thing with cryptsetup; the parameters here
// are cryptsetup's modern defaults (LUKS2, aes-xts-plain64) rather than
// clr's pinned older ones. The passphrase is always fed on stdin, so it
// never appears in a process's argument list.
//
// The mechanism: luksFormat the raw partition, luksOpen it to a mapper
// device, and from then on the filesystem is made and mounted on that
// mapper (storage.Partition.EffectiveDevice). The installed system
// unlocks it from /etc/crypttab (CrypttabLine), and for the root device
// also from a rd.luks.uuid kernel argument.
package luks

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

// FormatArgs and OpenArgs build the cryptsetup commands. The trailing
// "-" tells cryptsetup to read the passphrase from stdin.
//
// grubCompat formats with pbkdf2 instead of the LUKS2 argon2id default:
// GRUB can only unlock a container whose key derivation is pbkdf2, so the
// volume GRUB itself must open to read the kernel (an encrypted /boot) is
// formatted that way. Other volumes keep the stronger argon2id default.
func FormatArgs(device string, grubCompat bool) []string {
	args := []string{"cryptsetup", "--batch-mode", "luksFormat"}
	if grubCompat {
		args = append(args, "--pbkdf", "pbkdf2")
	}
	return append(args, device, "-")
}

func OpenArgs(device, name string) []string {
	return []string{"cryptsetup", "--batch-mode", "luksOpen", device, name, "-"}
}

// Format creates a LUKS volume on device with the given passphrase. out
// receives the command echo and cryptsetup's output (nil means stderr); a
// front-end that renders the install itself passes its own writer so the
// output does not leak onto the terminal. grubCompat selects a GRUB-
// readable key derivation (see FormatArgs).
func Format(device, passphrase string, out io.Writer, grubCompat bool) error {
	return run(FormatArgs(device, grubCompat), passphrase, out)
}

// Open unlocks device to /dev/mapper/<name> with the passphrase. See
// Format for out.
func Open(device, name, passphrase string, out io.Writer) error {
	return run(OpenArgs(device, name), passphrase, out)
}

func run(args []string, passphrase string, out io.Writer) error {
	if out == nil {
		out = os.Stderr
	}
	fmt.Fprintf(out, "    + %s\n", strings.Join(args, " "))
	c := exec.Command(args[0], args[1:]...)
	c.Stdin = strings.NewReader(passphrase)
	c.Stdout = out
	c.Stderr = out
	if err := c.Run(); err != nil {
		return fmt.Errorf("luks: %s: %w", args[1], err)
	}
	return nil
}

// UUID reads the LUKS UUID of a volume — the partition's UUID, which is
// what crypttab and the kernel rd.luks.uuid argument reference.
func UUID(device string) (string, error) {
	out, err := exec.Command("cryptsetup", "luksUUID", device).Output()
	if err != nil {
		return "", fmt.Errorf("luks: luksUUID %s: %w", device, err)
	}
	return strings.TrimSpace(string(out)), nil
}

// CrypttabLine renders an /etc/crypttab entry that unlocks the volume at
// boot by its LUKS UUID.
func CrypttabLine(name, uuid string) string {
	return fmt.Sprintf("%s UUID=%s none luks\n", name, uuid)
}
