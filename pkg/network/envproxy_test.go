package network

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

// The environment's proxy is read at each check, not once per process:
// Go's ProxyFromEnvironment keeps what it saw at its first request, so a
// proxy the config set after the wizard's own test (or another one after
// "Back to menu") was not the one the reachability check went through,
// while xbps, a new process, used the new one.
func TestVerifyConnectivityReadsTheProxyEachTime(t *testing.T) {
	for _, k := range []string{"http_proxy", "HTTP_PROXY", "https_proxy", "HTTPS_PROXY", "no_proxy", "NO_PROXY"} {
		t.Setenv(k, "")
	}
	// A first request, with no proxy: the host does not resolve.
	if err := verify("http://mirror.invalid/current", 2e9); err == nil {
		t.Fatal("mirror.invalid answered with no proxy")
	}
	var asked string
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = r.URL.String()
	}))
	defer proxy.Close()
	t.Setenv("http_proxy", proxy.URL)
	if err := VerifyConnectivity("http://mirror.invalid/current"); err != nil {
		t.Fatalf("the proxy set after the first check was not used: %v", err)
	}
	if asked != "http://mirror.invalid/current" {
		t.Errorf("the proxy was asked for %q", asked)
	}
	// And taken away again, it is not used.
	t.Setenv("http_proxy", "")
	asked = ""
	if err := verify("http://mirror.invalid/current", 2e9); err == nil || asked != "" {
		t.Errorf("the proxy was kept after it was unset (err %v, asked %q)", err, asked)
	}
}

// EnvProxy reads the variables as Go's ProxyFromEnvironment does.
func TestEnvProxy(t *testing.T) {
	cases := []struct {
		env  map[string]string
		req  string
		want string
	}{
		{map[string]string{"https_proxy": "p:3128"}, "https://repo.example/x", "http://p:3128"},
		{map[string]string{"HTTPS_PROXY": "http://u:pw@p:1"}, "https://repo.example/x", "http://u:pw@p:1"},
		{map[string]string{"https_proxy": "p:3128"}, "http://repo.example/x", ""},
		{map[string]string{"http_proxy": "p:3128"}, "http://repo.example/x", "http://p:3128"},
		{map[string]string{"http_proxy": "p:3128"}, "http://localhost/x", ""},
		{map[string]string{"http_proxy": "p:3128"}, "http://127.0.0.1:8080/x", ""},
		{map[string]string{"http_proxy": "p:3128", "no_proxy": "*"}, "http://repo.example/x", ""},
		{map[string]string{"http_proxy": "p:3128", "no_proxy": "example"}, "http://repo.example/x", ""},
		{map[string]string{"http_proxy": "p:3128", "no_proxy": ".example"}, "http://example/x", ""},
		{map[string]string{"http_proxy": "p:3128", "no_proxy": "*.example"}, "http://a.b.example/x", ""},
		{map[string]string{"http_proxy": "p:3128", "no_proxy": "ample"}, "http://repo.example/x", "http://p:3128"},
		{map[string]string{"http_proxy": "p:3128", "NO_PROXY": "repo.example:80"}, "http://repo.example/x", ""},
		{map[string]string{"http_proxy": "p:3128", "no_proxy": "repo.example:8080"}, "http://repo.example/x", "http://p:3128"},
		{map[string]string{"http_proxy": "p:3128", "no_proxy": "10.0.0.0/8"}, "http://10.1.2.3/x", ""},
		{map[string]string{"http_proxy": "p:3128", "no_proxy": "10.1.2.3"}, "http://10.1.2.3/x", ""},
		{map[string]string{"http_proxy": "p:3128", "no_proxy": "10.0.0.0/8"}, "http://11.1.2.3/x", "http://p:3128"},
	}
	for _, c := range cases {
		for _, k := range []string{"http_proxy", "HTTP_PROXY", "https_proxy", "HTTPS_PROXY", "no_proxy", "NO_PROXY"} {
			t.Setenv(k, c.env[k])
		}
		u, _ := url.Parse(c.req)
		got, err := EnvProxy(&http.Request{URL: u})
		if err != nil {
			t.Errorf("%v %s: %v", c.env, c.req, err)
			continue
		}
		s := ""
		if got != nil {
			s = got.String()
		}
		if s != c.want {
			t.Errorf("%v %s: proxy %q, want %q", c.env, c.req, s, c.want)
		}
	}
}
