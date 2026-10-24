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
	"os"
	"os/exec"
	"strings"
)

// FormatArgs and OpenArgs build the cryptsetup commands. The trailing
// "-" tells cryptsetup to read the passphrase from stdin.
func FormatArgs(device string) []string {
	return []string{"cryptsetup", "--batch-mode", "luksFormat", device, "-"}
}

func OpenArgs(device, name string) []string {
	return []string{"cryptsetup", "--batch-mode", "luksOpen", device, name, "-"}
}

// Format creates a LUKS volume on device with the given passphrase.
func Format(device, passphrase string) error {
	return run(FormatArgs(device), passphrase)
}

// Open unlocks device to /dev/mapper/<name> with the passphrase.
func Open(device, name, passphrase string) error {
	return run(OpenArgs(device, name), passphrase)
}

func run(args []string, passphrase string) error {
	fmt.Fprintf(os.Stderr, "    + %s\n", strings.Join(args, " "))
	c := exec.Command(args[0], args[1:]...)
	c.Stdin = strings.NewReader(passphrase)
	c.Stdout = os.Stderr
	c.Stderr = os.Stderr
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
