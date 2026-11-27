package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"gopkg.in/yaml.v3"

	"github.com/sunlightlinux/slinstaller/pkg/config"
	"github.com/sunlightlinux/slinstaller/pkg/storage"
)

// lockPath is the lock an install holds from the moment it may touch a disk
// until it ends (a variable so tests can move it).
var lockPath = "/run/slinstaller.lock"

// lockInstall takes the install lock, or says another install holds it. Two
// installs at once would partition and write the same disks. The lock goes
// with the returned file: closing it, or the process ending, releases it.
func lockInstall() (*os.File, error) {
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("install lock: %w", err)
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		f.Close()
		if err == unix.EWOULDBLOCK {
			return nil, fmt.Errorf("another slinstaller install is running (it holds %s)", lockPath)
		}
		return nil, fmt.Errorf("install lock %s: %w", lockPath, err)
	}
	return f, nil
}

// diagnosticsDir is where a failed install leaves its diagnostics when
// --diagnostics does not say (a variable so tests can move it).
var diagnosticsDir = "/tmp"

// diagnosticCommands describe the machine the install failed on.
var diagnosticCommands = map[string][]string{
	"lsblk.txt":      {"lsblk", "-o", "NAME,SIZE,TYPE,FSTYPE,MOUNTPOINT,MODEL,SERIAL"},
	"efibootmgr.txt": {"efibootmgr", "-v"},
	// What still holds a disk when releasing it fails.
	"dmsetup.txt":   {"dmsetup", "ls", "--tree"},
	"lsblk-all.txt": {"lsblk", "-a", "-o", "NAME,SIZE,TYPE,FSTYPE,MOUNTPOINT"},
}

// diagnosticFiles are read as they are.
var diagnosticFiles = map[string]string{
	"cmdline.txt": "/proc/cmdline",
	"version.txt": "/proc/version",
	"meminfo.txt": "/proc/meminfo",
	// Arrays and mounts: what a re-run would find busy.
	"mdstat.txt": "/proc/mdstat",
	"mounts.txt": "/proc/self/mounts",
}

// writeDiagnostics gathers what is needed to understand a failed install
// into one gzipped tar: the error, the config (password hashes removed),
// the install log, and a description of the machine. It returns the file's
// path. path "" means a dated file in diagnosticsDir.
func writeDiagnostics(path string, cfg *config.Config, logPath string, failure error) (string, error) {
	// The default is in /tmp: never through a link planted there, and never
	// into a file someone else made first.
	flags := os.O_CREATE | os.O_TRUNC | os.O_WRONLY | unix.O_NOFOLLOW
	if path == "" {
		path = filepath.Join(diagnosticsDir, "slinstaller-diagnostics-"+time.Now().Format("20060102-150405")+".tar.gz")
		flags = os.O_CREATE | os.O_EXCL | os.O_WRONLY | unix.O_NOFOLLOW
	}
	f, err := os.OpenFile(path, flags, 0o600)
	if err != nil {
		return "", err
	}
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	add := func(name string, data []byte) {
		if err == nil {
			err = tw.WriteHeader(&tar.Header{Name: "slinstaller-diagnostics/" + name, Mode: 0o600, Size: int64(len(data)), ModTime: time.Now()})
		}
		if err == nil {
			_, err = tw.Write(data)
		}
	}

	add("error.txt", []byte(fmt.Sprintf("slinstaller %s\n%s\n\n%v\n", version, time.Now().Format(time.RFC1123), failure)))
	add("config.yaml", redactedConfig(cfg))
	if logPath != "" {
		if data, rerr := os.ReadFile(logPath); rerr == nil {
			add("install.log", data)
		}
	}
	for name, src := range diagnosticFiles {
		if data, rerr := os.ReadFile(src); rerr == nil {
			add(name, data)
		}
	}
	for name, argv := range diagnosticCommands {
		// A tool that is missing or fails says so in its own file.
		out, rerr := exec.Command(argv[0], argv[1:]...).CombinedOutput()
		if rerr != nil {
			out = append(out, []byte(fmt.Sprintf("\n(%v)\n", rerr))...)
		}
		add(name, out)
	}

	for _, close := range []func() error{tw.Close, gz.Close, f.Close} {
		if cerr := close(); err == nil {
			err = cerr
		}
	}
	if err != nil {
		os.Remove(path)
		return "", err
	}
	return path, nil
}

// redactedConfig is the config as YAML with every account's password hash
// taken out: the file is meant to be handed to someone else.
func redactedConfig(cfg *config.Config) []byte {
	// Redacted on the config itself, before it is YAML: on the YAML, an
	// env name YAML writes as !!binary (not UTF-8) made env a map of
	// other keys than strings, and every value in it was left in.
	c := *cfg
	c.Users = nil
	for _, u := range cfg.Users {
		if u != nil {
			cp := *u
			if cp.Password != "" {
				cp.Password = "(removed)"
			}
			u = &cp
		}
		c.Users = append(c.Users, u)
	}
	if cfg.Environment != nil {
		c.Environment = map[string]string{}
		for k, v := range cfg.Environment {
			// Secrets by name: the archive is handed to others.
			if config.SecretName(k) {
				v = "(removed)"
			}
			c.Environment[k] = v
		}
	}
	c.WriteFiles = nil
	for _, f := range cfg.WriteFiles {
		// write-files' contents: Wi-Fi keys, private keys, sudoers rules.
		if f != nil {
			cp := *f
			cp.Content = fmt.Sprintf("(%d bytes removed)", len(f.Content))
			f = &cp
		}
		c.WriteFiles = append(c.WriteFiles, f)
	}
	data, err := yaml.Marshal(&c)
	if err != nil {
		return []byte(fmt.Sprintf("(config could not be written: %v)\n", err))
	}
	var doc map[string]any
	if yaml.Unmarshal(data, &doc) == nil {
		maskURLPasswords(doc)
		var out bytes.Buffer
		out.WriteString("# password hashes, URL passwords, secret env values and write-files contents removed; hook commands are as written\n")
		if enc, err := yaml.Marshal(doc); err == nil {
			out.Write(enc)
			return out.Bytes()
		}
	}
	return []byte("(config could not be redacted, so it is left out)\n")
}

// maskURLPasswords replaces, in place, the password of every URL in the
// document (a proxy's, a mirror's) — the archive is handed to others.
func maskURLPasswords(v any) {
	switch t := v.(type) {
	case map[string]any:
		for k, e := range t {
			if s, ok := e.(string); ok {
				// A proxy (httpsProxy: user:pw@proxy:3128) is masked as
				// the …_proxy= setting it becomes.
				if strings.Contains(strings.ToLower(k), "proxy") {
					t[k] = strings.TrimPrefix(storage.MaskSecret("x_proxy="+s), "x_proxy=")
					continue
				}
				t[k] = storage.MaskSecret(s)
			} else {
				maskURLPasswords(e)
			}
		}
	case map[any]any:
		// A map with a key YAML does not read back as a string (an env
		// name written as !!binary): its values are masked all the same.
		m := map[string]any{}
		for k, e := range t {
			m[fmt.Sprint(k)] = e
		}
		maskURLPasswords(m)
		for k := range t {
			t[k] = m[fmt.Sprint(k)]
		}
	case []any:
		for i, e := range t {
			if s, ok := e.(string); ok {
				t[i] = storage.MaskSecret(s)
			} else {
				maskURLPasswords(e)
			}
		}
	}
}

// reportFailure writes the diagnostics for a failed install and tells the
// operator where they are. The install's own error is what is returned to
// them either way.
func reportFailure(path string, cfg *config.Config, logPath string, failure error) {
	file, err := writeDiagnostics(path, cfg, logPath, failure)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: the diagnostics could not be saved: %v\n", err)
		return
	}
	fmt.Fprintf(os.Stderr, "\nDiagnostics for this failure are in %s (config without password hashes, log, disks).\n", file)
}
