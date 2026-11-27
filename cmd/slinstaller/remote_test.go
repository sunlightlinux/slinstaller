package main

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"gopkg.in/yaml.v3"
)

// eventSink is a local server that records what is reported to it.
type eventSink struct {
	mu     sync.Mutex
	events []event
	status int
	*httptest.Server
}

func newEventSink(t *testing.T) *eventSink {
	s := &eventSink{status: http.StatusOK}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var e event
		body, _ := io.ReadAll(r.Body)
		if r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/json" || json.Unmarshal(body, &e) != nil {
			t.Errorf("not a JSON POST: %s %q %s", r.Method, r.Header.Get("Content-Type"), body)
		}
		s.mu.Lock()
		s.events = append(s.events, e)
		status := s.status
		s.mu.Unlock()
		w.WriteHeader(status)
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *eventSink) summary() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, e := range s.events {
		out = append(out, strings.TrimSpace(e.Event+" "+e.Stage+e.Result))
	}
	return strings.Join(out, ", ")
}

func TestReporter(t *testing.T) {
	sink := newEventSink(t)
	r := newReporter(sink.URL, "box", true)
	r.stage(0, 3, "pre-install")
	r.stage(1, 3, "partition")
	r.stage(3, 3, "complete") // the end is reported by finish, with the result
	r.finish(nil)
	if got := sink.summary(); got != "stage pre-install, stage partition, finish success" {
		t.Errorf("events = %s", got)
	}
	e := sink.events[1]
	if e.Hostname != "box" || e.Done != 1 || e.Total != 3 || e.Time == "" {
		t.Errorf("stage event = %+v", e)
	}
	r.finish(errors.New("controller: mount: no such device"))
	if last := sink.events[len(sink.events)-1]; last.Result != "failure" || last.Error != "controller: mount: no such device" {
		t.Errorf("failure event = %+v", last)
	}

	// No URL: nothing is sent and nothing breaks.
	var none *reporter = newReporter("", "box", false)
	none.stage(0, 1, "x")
	none.finish(nil)
}

// A report that cannot be delivered never decides the install: it is a
// warning, said once.
func TestReporterFailuresAreWarnedOnce(t *testing.T) {
	sink := newEventSink(t)
	sink.status = http.StatusInternalServerError
	dir := t.TempDir()
	errFile, _ := os.Create(filepath.Join(dir, "err"))
	saved := os.Stderr
	os.Stderr = errFile
	r := newReporter(sink.URL, "box", true)
	r.stage(0, 2, "a")
	r.stage(1, 2, "b")
	gone := newReporter("http://127.0.0.1:1/nothing-listens-here", "box", true)
	gone.finish(nil)
	os.Stderr = saved
	errFile.Close()
	out, _ := os.ReadFile(filepath.Join(dir, "err"))
	if n := strings.Count(string(out), "could not be reported to "+sink.URL); n != 1 {
		t.Errorf("warned %d times for the failing server:\n%s", n, out)
	}
	if !strings.Contains(string(out), "nothing-listens-here") {
		t.Errorf("no warning for the unreachable server:\n%s", out)
	}
}

func TestConfigFromAURL(t *testing.T) {
	onlyTools(t, t.TempDir())
	data, _ := yaml.Marshal(plainConfig())
	mux := http.NewServeMux()
	mux.HandleFunc("/config.yaml", func(w http.ResponseWriter, r *http.Request) { w.Write(data) })
	mux.HandleFunc("/huge.yaml", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(strings.Repeat("#", maxConfigBytes+10)))
	})
	plain := httptest.NewServer(mux)
	defer plain.Close()

	// Over plain http a config is refused unless that is asked for: it says
	// which disk to erase.
	_, _, err := runWith(t, "", plain.URL+"/config.yaml")
	wantRunErr(t, "plain http", err, "is plain http")
	out, _, err := runWith(t, "", "--allow-insecure-http", plain.URL+"/config.yaml")
	if err != nil || !strings.Contains(out, testDisk+" mklabel gpt") {
		t.Errorf("dry run from a URL: %v\n%s", err, out)
	}
	_, _, err = runWith(t, "", "--allow-insecure-http", plain.URL+"/absent.yaml")
	wantRunErr(t, "404", err, "answered 404")
	_, _, err = runWith(t, "", "--allow-insecure-http", plain.URL+"/huge.yaml")
	wantRunErr(t, "too large", err, "not a config")
	_, _, err = runWith(t, "", "--allow-insecure-http", "http://127.0.0.1:1/config.yaml")
	wantRunErr(t, "unreachable", err, "fetching the config")

	// The fetched copy is private and does not stay behind.
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	path, err := fetchConfig(plain.URL+"/config.yaml", true)
	if err != nil {
		t.Fatal(err)
	}
	if st, _ := os.Stat(path); st.Mode().Perm() != 0o600 {
		t.Errorf("fetched config mode %v", st.Mode().Perm())
	}
	os.RemoveAll(filepath.Dir(path))
	if _, _, err = runWith(t, "", "--allow-insecure-http", plain.URL+"/config.yaml"); err != nil {
		t.Fatal(err)
	}
	if left, _ := filepath.Glob(filepath.Join(tmp, "slinstaller-config-*")); len(left) != 0 {
		t.Errorf("the fetched config was left behind: %v", left)
	}
	t.Setenv("TMPDIR", filepath.Join(tmp, "no-such-dir"))
	if _, err := fetchConfig(plain.URL+"/config.yaml", true); err == nil {
		t.Error("nowhere to keep the fetched config, but no error")
	}
}

// An https config URL that redirects to plain http is refused unless plain
// http was allowed: the up-front check on the URL is otherwise for nothing.
func TestConfigRedirectToPlainHTTP(t *testing.T) {
	data, _ := yaml.Marshal(plainConfig())
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(data) }))
	defer plain.Close()
	secure := httptest.NewTLSServer(http.RedirectHandler(plain.URL+"/config.yaml", http.StatusFound))
	defer secure.Close()
	// fetchConfig uses the default transport: have it trust the test server.
	tr := http.DefaultTransport.(*http.Transport)
	saved := tr.TLSClientConfig
	tr.TLSClientConfig = secure.Client().Transport.(*http.Transport).TLSClientConfig
	t.Cleanup(func() { tr.TLSClientConfig = saved; tr.CloseIdleConnections() })

	t.Setenv("TMPDIR", t.TempDir())
	if _, err := fetchConfig(secure.URL+"/config.yaml", false); err == nil || !strings.Contains(err.Error(), "redirected to plain http") {
		t.Errorf("https -> http redirect: %v", err)
	}
	path, err := fetchConfig(secure.URL+"/config.yaml", true)
	if err != nil {
		t.Fatalf("https -> http redirect with plain http allowed: %v", err)
	}
	os.RemoveAll(filepath.Dir(path))

	// The reporter is held to the same rule.
	var hits atomic.Int32
	plainSink := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1) }))
	defer plainSink.Close()
	toPlain := httptest.NewTLSServer(http.RedirectHandler(plainSink.URL, http.StatusTemporaryRedirect))
	defer toPlain.Close()
	saved2 := os.Stderr
	os.Stderr, _ = os.Open(os.DevNull)
	newReporter(toPlain.URL, "box", false).finish(nil)
	os.Stderr.Close()
	os.Stderr = saved2
	if hits.Load() != 0 {
		t.Error("the report followed a redirect to plain http")
	}
}

func TestUnmountAndInTargetFlags(t *testing.T) {
	dir := t.TempDir()
	_, errOut, err := runWith(t, "", "--unmount", "--root", dir)
	if err != nil || !strings.Contains(errOut, "nothing is mounted under "+dir) {
		t.Errorf("--unmount on an unmounted root: %v\n%s", err, errOut)
	}
	if os.Geteuid() != 0 {
		_, _, err = runWith(t, "", "--in-target", "true", "--root", dir)
		wantRunErr(t, "--in-target as a user", err, "--in-target must run as root")
	}
	// A report URL that is not one is refused with the rest of the config.
	onlyTools(t, dir)
	_, _, err = runWith(t, "", "--report-url", "ftp://example.org/x", writeConfig(t, dir, plainConfig()))
	wantRunErr(t, "bad report URL", err, "is not an http or https URL")
	// Plain http needs the same allowance as a config or a repository.
	_, _, err = runWith(t, "", "--report-url", "http://example.org/x", writeConfig(t, dir, plainConfig()))
	wantRunErr(t, "plain http report URL", err, "report-url: http://example.org/x is plain http")
}

func TestJSONYAMLFlag(t *testing.T) {
	dir := t.TempDir()
	onlyTools(t, dir)
	src := filepath.Join(dir, "old.json")
	os.WriteFile(src, []byte(`{"DestinationType": "physical", "LegacyBios": true,
		"PartitionLayout": [{"disk": "slitest0", "partition": 1, "size": "512M"}, {"disk": "slitest0", "partition": 2, "size": "rest"}],
		"FilesystemTypes": [{"disk": "slitest0", "partition": 1, "type": "vfat"}, {"disk": "slitest0", "partition": 2, "type": "ext4"}],
		"PartitionMountPoints": [{"disk": "slitest0", "partition": 1, "mount": "/boot"}, {"disk": "slitest0", "partition": 2, "mount": "/"}]}`), 0o644)
	_, errOut, err := runWith(t, "", "--json-yaml", src)
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "old.yaml")
	if !strings.Contains(errOut, "Wrote "+out) || !strings.Contains(errOut, "note: LegacyBios is set") {
		t.Errorf("output:\n%s", errOut)
	}
	// What it wrote is a config slinstaller plans.
	plan, _, err := runWith(t, "", out)
	if err != nil || !strings.Contains(plan, "mkfs.ext4") || !strings.Contains(plan, testDisk+"p2") {
		t.Errorf("dry run of the converted config: %v\n%s", err, plan)
	}
	// It does not write over a file, takes only .json, and not with --template.
	_, _, err = runWith(t, "", "-j", src)
	wantRunErr(t, "existing output", err, "exists; not overwriting it")
	_, _, err = runWith(t, "", "--json-yaml", out)
	wantRunErr(t, "not .json", err, "must end in .json")
	_, _, err = runWith(t, "", "--json-yaml", src, "--template", filepath.Join(dir, "t.yaml"))
	wantRunErr(t, "with --template", err, "mutually exclusive")
	os.WriteFile(filepath.Join(dir, "bad.json"), []byte("{"), 0o644)
	_, _, err = runWith(t, "", "--json-yaml", filepath.Join(dir, "bad.json"))
	wantRunErr(t, "bad JSON", err, "parsing")
}

func TestInstallerMessage(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/msg/pre-install-msg.yaml", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "install-msg:\n- default: \"Maintenance on Friday.\\n\"\n- default: \"\"\n- default: \"Ask \\x1b[31mops\\x07 first\\u009b2J.\"\n  en_US: ignored\n")
	})
	mux.HandleFunc("/msg/blank-msg.yaml", func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "install-msg:\n- default: \"  \"\n") })
	mux.HandleFunc("/msg/broken-msg.yaml", func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "install-msg: [") })
	srv := httptest.NewServer(mux)
	defer srv.Close()

	// The lines as written, a blank one kept between them; what a terminal
	// would act on is taken out.
	if got := installerMessage(srv.URL+"/msg/", preInstallMessage); got != "Maintenance on Friday.\n\nAsk [31mops first2J." { // ESC, BEL and the C1 CSI (U+009B) removed
		t.Errorf("message = %q", got)
	}
	// No base, no such file, no text, not YAML, nobody listening: no
	// message, and never an error.
	for _, tc := range [][2]string{{"", preInstallMessage}, {srv.URL + "/msg", postInstallMessage},
		{srv.URL + "/msg", "blank-msg.yaml"}, {srv.URL + "/msg", "broken-msg.yaml"}, {"http://127.0.0.1:1", preInstallMessage}} {
		if got := installerMessage(tc[0], tc[1]); got != "" {
			t.Errorf("installerMessage(%q, %q) = %q", tc[0], tc[1], got)
		}
	}
	dir := t.TempDir()
	onlyTools(t, dir)
	_, _, err := runWith(t, "", "--message-url", "ftp://example.org", writeConfig(t, dir, plainConfig()))
	wantRunErr(t, "bad message URL", err, "message-url: ftp://example.org is not an http or https URL")
}
