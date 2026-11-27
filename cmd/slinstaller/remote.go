package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"gopkg.in/yaml.v3"

	"github.com/sunlightlinux/slinstaller/pkg/config"
	"github.com/sunlightlinux/slinstaller/pkg/storage"
)

// isURL reports whether a config argument names a file to fetch.
func isURL(s string) bool {
	return strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://")
}

// refuseDowngrade is an http.Client CheckRedirect that refuses a redirect to
// plain http unless plain http is allowed: refusing an http:// URL up front
// protects nothing if an https server can send the client on to one.
func refuseDowngrade(allowInsecure bool) func(*http.Request, []*http.Request) error {
	return func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return errors.New("stopped after 10 redirects")
		}
		if req.URL.Scheme == "http" && !allowInsecure {
			return fmt.Errorf("redirected to plain http %s; use https, or pass --allow-insecure-http", req.URL.Redacted())
		}
		return nil
	}
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
	client := &http.Client{Timeout: 30 * time.Second, CheckRedirect: refuseDowngrade(allowInsecure)}
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

func newReporter(url, hostname string, allowInsecure bool) *reporter {
	if url == "" {
		return nil
	}
	client := &http.Client{Timeout: 5 * time.Second, CheckRedirect: refuseDowngrade(allowInsecure)}
	return &reporter{url: url, host: hostname, client: client}
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
		fmt.Fprintf(os.Stderr, "warning: the install could not be reported to %s: %v\n", storage.MaskSecret(r.url), err)
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

// The installer messages, by clr-installer's file names.
const (
	preInstallMessage  = "pre-install-msg.yaml"
	postInstallMessage = "post-install-msg.yaml"
)

// installerMessage fetches a message for the operator from under base: a
// small YAML file, in clr-installer's shape, whose install-msg entries each
// carry a line of text under "default". It returns "" when there is no
// base, no such file, nothing readable in it, or no text — a message is a
// courtesy, and its absence is never an error.
func installerMessage(base, name string) string {
	if base == "" {
		return ""
	}
	client := &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		// Not from https down to http: what is shown sits right before the
		// confirmation of a destructive install.
		if req.URL.Scheme != "https" && len(via) > 0 && via[0].URL.Scheme == "https" {
			return fmt.Errorf("redirect from https to %s refused", req.URL.Scheme)
		}
		if len(via) >= 10 {
			return fmt.Errorf("too many redirects")
		}
		return nil
	}}
	resp, err := client.Get(strings.TrimSuffix(base, "/") + "/" + name)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ""
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return ""
	}
	var doc struct {
		Messages []struct {
			Default string `yaml:"default"`
		} `yaml:"install-msg"`
	}
	if yaml.Unmarshal(data, &doc) != nil {
		return ""
	}
	var lines []string
	text := false
	for _, m := range doc.Messages {
		line := strings.TrimSuffix(m.Default, "\n")
		text = text || strings.TrimSpace(line) != ""
		// What a server sends is shown on a terminal: nothing but text.
		lines = append(lines, strings.Map(func(r rune) rune {
			// C1 too (U+0080–U+009F): the console takes U+009B as CSI even
			// in UTF-8 mode, so a server could clear or redraw the screen
			// over the disk warning that follows the message.
			if r < 0x20 && r != '\t' || r >= 0x7f && r <= 0x9f || r == utf8.RuneError {
				return -1
			}
			return r
		}, line))
	}
	if !text {
		return ""
	}
	return strings.Join(lines, "\n")
}

// showMessage prints an installer message, if there is one.
func showMessage(base, name string) {
	if msg := installerMessage(base, name); msg != "" {
		fmt.Fprintf(os.Stderr, "\n%s\n", msg)
	}
}

// convertDescriptor writes the config an old JSON descriptor converts to
// beside it, under the same name with .yaml, as clr-installer's --json-yaml
// does. An existing file is not overwritten.
func convertDescriptor(path string) error {
	if filepath.Ext(path) != ".json" {
		return fmt.Errorf("--json-yaml: %s must end in .json", path)
	}
	out := strings.TrimSuffix(path, ".json") + ".yaml"
	if _, err := os.Stat(out); err == nil {
		return fmt.Errorf("%s exists; not overwriting it", out)
	}
	cfg, notes, err := config.FromJSONDescriptor(path)
	if err != nil {
		return err
	}
	if err := config.Save(out, cfg); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "Wrote %s.\n", out)
	for _, n := range notes {
		fmt.Fprintln(os.Stderr, "note:", n)
	}
	return nil
}
