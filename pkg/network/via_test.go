package network

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestVerifyConnectivityVia goes through the proxy it is given: the
// request reaches the proxy, for a host that does not resolve.
func TestVerifyConnectivityVia(t *testing.T) {
	var asked string
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = r.URL.String()
	}))
	defer proxy.Close()
	if err := VerifyConnectivityVia("http://mirror.invalid/current", proxy.URL); err != nil {
		t.Fatalf("through the proxy: %v", err)
	}
	if asked != "http://mirror.invalid/current" {
		t.Errorf("the proxy was asked for %q", asked)
	}
	if err := VerifyConnectivityVia("http://mirror.invalid/current", "http://[bad"); err == nil {
		t.Error("an unparseable proxy was taken")
	}
}

// A proxy written without a scheme - host:port, an address, user:pw@host -
// is what the install's https_proxy takes (Go and xbps both read it as
// http://), so the wizard's test has to take it too: refused or misread,
// the test failed where the install went through, and the live network
// was not carried over.
func TestVerifyConnectivityViaSchemelessProxy(t *testing.T) {
	var asked string
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = r.URL.String()
	}))
	defer proxy.Close()
	host := strings.TrimPrefix(proxy.URL, "http://")
	for _, p := range []string{host, "user:secret@" + host} {
		asked = ""
		if err := VerifyConnectivityVia("http://mirror.invalid/current", p); err != nil {
			t.Errorf("through %q: %v", p, err)
		}
		if asked != "http://mirror.invalid/current" {
			t.Errorf("through %q the proxy was asked for %q", p, asked)
		}
	}
}
