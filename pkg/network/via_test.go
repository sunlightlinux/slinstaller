package network

import (
	"net/http"
	"net/http/httptest"
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
