package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/sunlightlinux/slinstaller/pkg/storage"
)

// isURL reports whether a config argument names a file to fetch.
func isURL(s string) bool {
	return strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://")
}

// maxConfigBytes bounds a fetched config: a config is a few kilobytes, and
// an endless response must not fill the live system's memory.
const maxConfigBytes = 4 << 20

// fetchConfig downloads a config to a file of its own and returns its path;
// the caller removes its directory. Plain http is refused unless allowed: a
// config says which disk to erase and what to run as root, so one that can
// be swapped on the way is not a config to act on.
func fetchConfig(url string, allowInsecure bool) (string, error) {
	if strings.HasPrefix(url, "http://") && !allowInsecure {
		return "", fmt.Errorf("config %s is plain http; use https, or pass --allow-insecure-http", url)
	}
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return "", fmt.Errorf("fetching the config: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("fetching the config: %s answered %s", url, resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxConfigBytes+1))
	if err != nil {
		return "", fmt.Errorf("fetching the config: %w", err)
	}
	if len(data) > maxConfigBytes {
		return "", fmt.Errorf("fetching the config: %s is larger than %d MiB; not a config", url, maxConfigBytes>>20)
	}
	dir, err := os.MkdirTemp("", "slinstaller-config-")
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		os.RemoveAll(dir)
		return "", err
	}
	return path, nil
}

// reporter posts the install's progress to a URL, for whoever is watching
// many installs at once. Reporting never decides the install: a report that
// cannot be delivered is a warning, once, and the install goes on.
type reporter struct {
	url    string
	host   string
	warned bool
	client *http.Client
}

func newReporter(url, hostname string) *reporter {
	if url == "" {
		return nil
	}
	return &reporter{url: url, host: hostname, client: &http.Client{Timeout: 5 * time.Second}}
}

// event is one report. Fields that do not apply are left out.
type event struct {
	Event    string `json:"event"` // "stage" or "finish"
	Hostname string `json:"hostname,omitempty"`
	Stage    string `json:"stage,omitempty"`
	Done     int    `json:"done,omitempty"`
	Total    int    `json:"total,omitempty"`
	Result   string `json:"result,omitempty"` // "success" or "failure"
	Error    string `json:"error,omitempty"`
	Time     string `json:"time"`
}

func (r *reporter) post(e event) {
	if r == nil {
		return
	}
	e.Hostname, e.Time = r.host, time.Now().UTC().Format(time.RFC3339)
	body, _ := json.Marshal(e)
	resp, err := r.client.Post(r.url, "application/json", bytes.NewReader(body))
	if err == nil {
		resp.Body.Close()
		if resp.StatusCode < 300 {
			return
		}
		err = fmt.Errorf("%s", resp.Status)
	}
	if !r.warned {
		r.warned = true
		fmt.Fprintf(os.Stderr, "warning: the install could not be reported to %s: %v\n", r.url, err)
	}
}

// stage reports a stage beginning; it has controller.Options.Progress's shape.
func (r *reporter) stage(done, total int, name string) {
	if name == "complete" {
		return // finish says it, with the result
	}
	r.post(event{Event: "stage", Stage: name, Done: done, Total: total})
}

// finish reports how the install ended.
func (r *reporter) finish(err error) {
	if err != nil {
		r.post(event{Event: "finish", Result: "failure", Error: err.Error()})
		return
	}
	r.post(event{Event: "finish", Result: "success"})
}

// unmountTarget unmounts what a staged install left mounted under root.
func unmountTarget(root string) error {
	done, err := storage.UnmountTree(root)
	for _, d := range done {
		fmt.Fprintln(os.Stderr, "unmounted", d)
	}
	if err == nil && len(done) == 0 {
		fmt.Fprintf(os.Stderr, "nothing is mounted under %s\n", root)
	}
	return err
}

// inTarget runs a shell command inside the mounted target, as the install's
// own chrooted steps run. It is for looking at, or fixing, what a staged
// install left: the target and its /proc, /sys and /dev are expected to be
// mounted already (--through meta-fs or later).
func inTarget(root, command string) error {
	if os.Geteuid() != 0 {
		return fmt.Errorf("--in-target must run as root")
	}
	if fi, err := os.Stat(filepath.Join(root, "proc", "self")); err != nil || !fi.IsDir() {
		return fmt.Errorf("%s does not look like a mounted target (no %s/proc): run a staged install first (--through meta-fs or later)", root, root)
	}
	cmd := exec.Command("chroot", root, "/bin/sh", "-c", command)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("in the target: %w", err)
	}
	return nil
}
