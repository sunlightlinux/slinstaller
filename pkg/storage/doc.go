// Package storage turns a partition plan into the disk it describes.
//
// It is the first core subsystem ported from clr-installer, reduced to
// what Sunlight Linux installs: a single UEFI target disk, a GPT label,
// an EFI System Partition, and a straightforward list of filesystems.
// The mechanisms are clr-installer's — parted for the partition table
// and the ESP type, mkfs.* / mkswap for filesystems — but the input is a
// Plan that pkg/config builds from the install config, not
// clr-installer's BlockDevice model, and none of clr's RAID, LVM,
// encryption or advanced paths are carried over. They arrive if and
// when a layout needs them.
//
// The destructive work is expressed as data. Plan.Commands returns the
// exact, ordered list of commands that would partition the disk and
// make its filesystems, and executes nothing — it is the dry run. A
// caller that has confirmed the disk passes that list to RunCommands.
// Nothing in this package writes to a disk except through RunCommands,
// so there is one line to audit and one gate to hold: the list is built
// purely, shown to the operator, and only then run.
//
// This package partitions and formats. Mounting the target tree,
// bind-mounting the meta filesystems for chroot, and writing fstab are
// the next subsystem, not this one.
package storage
