package system

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// TargetPath is rel, a path in the installed system, as a path from here:
// every link on the way is followed as the installed system will follow
// it — an absolute one from the target's root, ".." no higher than that
// root — so the path returned is root or under it, whatever links an
// image or a reused root has. Joined plainly, an absolute link
// (/home -> /data/home) named the live system's directory from here, and
// what was written into the target landed on the live system instead.
// The last component is resolved too, when it is a link; what does not
// exist yet is taken as it is written, and rel's own ".." lexically.
func TargetPath(root, rel string) (string, error) {
	var done []string // resolved components under root
	todo := strings.Split(filepath.Clean("/"+rel), "/")
	links := 0
	for len(todo) > 0 {
		p := todo[0]
		todo = todo[1:]
		switch p {
		case "", ".":
			continue
		case "..":
			if len(done) > 0 {
				done = done[:len(done)-1]
			}
			continue
		}
		next := filepath.Join(append([]string{root}, append(done, p)...)...)
		fi, err := os.Lstat(next)
		if err != nil || fi.Mode()&os.ModeSymlink == 0 {
			done = append(done, p) // a directory, a file, or not there yet
			continue
		}
		if links++; links > 40 {
			return "", fmt.Errorf("system: %s: too many links", rel)
		}
		link, err := os.Readlink(next)
		if err != nil {
			return "", fmt.Errorf("system: %w", err)
		}
		if filepath.IsAbs(link) {
			done = nil
		}
		todo = append(strings.Split(link, "/"), todo...)
	}
	return filepath.Join(append([]string{root}, done...)...), nil
}

// targetDir is TargetPath for a directory the caller is about to write
// into, made when it is not there.
func targetDir(root, rel string, perm os.FileMode) (string, error) {
	dir, err := TargetPath(root, rel)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, perm); err != nil {
		return "", fmt.Errorf("system: %w", err)
	}
	return dir, nil
}

// statInTarget is os.Stat of rel as the installed system sees it.
func statInTarget(root, rel string) (os.FileInfo, error) {
	path, err := TargetPath(root, rel)
	if err != nil {
		return nil, err
	}
	return os.Stat(path)
}
