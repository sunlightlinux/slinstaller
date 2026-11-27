package storage

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
)

// mountsFile lists what is mounted (a variable so tests can point elsewhere).
var mountsFile = "/proc/self/mounts"

// mountsUnder picks, from the contents of /proc/self/mounts, the mount
// points at or below root, in the order they have to be unmounted: the
// bound kernel filesystems first, each as a whole, then the rest deepest
// first. What lies inside a bound kernel filesystem is left out — it goes
// with its parent, and unmounting it by itself would reach the live
// system's own mount.
func mountsUnder(root, mounts string) []string {
	root = filepath.Clean(root)
	meta := map[string]bool{}
	for _, name := range metaMounts {
		meta[filepath.Join(root, name)] = true
	}
	var metas, rest []string
	seen := map[string]bool{}
next:
	for _, line := range strings.Split(mounts, "\n") {
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		// The kernel writes a space in a path as \040.
		target := strings.ReplaceAll(f[1], `\040`, " ")
		if target != root && !strings.HasPrefix(target, root+"/") || seen[target] {
			continue
		}
		seen[target] = true
		if meta[target] {
			metas = append(metas, target)
			continue
		}
		for m := range meta {
			if strings.HasPrefix(target, m+"/") {
				continue next
			}
		}
		rest = append(rest, target)
	}
	sort.Strings(metas)
	sort.Slice(rest, func(i, j int) bool {
		di, dj := strings.Count(rest[i], "/"), strings.Count(rest[j], "/")
		if di != dj {
			return di > dj
		}
		return rest[i] > rest[j]
	})
	return append(metas, rest...)
}

// UnmountTree unmounts everything mounted at or below root — what a staged
// install (--through) leaves behind. It returns what it unmounted.
func UnmountTree(root string) ([]string, error) {
	data, err := os.ReadFile(mountsFile)
	if err != nil {
		return nil, fmt.Errorf("storage: %w", err)
	}
	var done, failed []string
	targets := mountsUnder(root, string(data))
	if len(targets) > 0 {
		syscall.Sync()
	}
	for _, target := range targets {
		// Unmounted properly where it can be, so the data is on the disk;
		// detached only when something holds it (or its submounts, as a
		// recursive bind of /dev has).
		err := syscall.Unmount(target, 0)
		if err == syscall.EBUSY {
			err = syscall.Unmount(target, syscall.MNT_DETACH)
		}
		if err != nil {
			failed = append(failed, fmt.Sprintf("%s (%v)", target, err))
			continue
		}
		done = append(done, target)
	}
	if len(failed) > 0 {
		return done, fmt.Errorf("storage: failed to unmount: %s", strings.Join(failed, ", "))
	}
	return done, nil
}
