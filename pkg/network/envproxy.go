package network

import (
	"net"
	"net/http"
	neturl "net/url"
	"os"
	"strings"
)

// NewTransport is http.DefaultTransport with the environment's proxy read
// at each request (EnvProxy). Go's own ProxyFromEnvironment keeps what it
// saw at the process's first request: a proxy the config sets after the
// wizard's network test, or another one after "Back to menu", was not
// the one the reachability check, the report or a message went through,
// while xbps, a new process, used the new one.
func NewTransport() *http.Transport {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.Proxy = EnvProxy
	return tr
}

// EnvProxy is http.ProxyFromEnvironment, reading the environment each
// time: HTTPS_PROXY/https_proxy for an https request, HTTP_PROXY/
// http_proxy for http (a value without a scheme is http://), none for
// localhost or a loopback address, nor for a host NO_PROXY/no_proxy names
// ("*", a domain and its subdomains, with an optional leading "." or
// "*.", an address or CIDR block, each with an optional port). One
// difference from Go: ".example" also matches the bare "example" (Go
// takes only its subdomains), so that host goes direct.
func EnvProxy(req *http.Request) (*neturl.URL, error) {
	var proxy string
	switch req.URL.Scheme {
	case "https":
		proxy = getenvEither("HTTPS_PROXY", "https_proxy")
	case "http":
		proxy = getenvEither("HTTP_PROXY", "http_proxy")
	}
	if proxy == "" || !useProxy(req.URL, getenvEither("NO_PROXY", "no_proxy")) {
		return nil, nil
	}
	return parseProxy(proxy)
}

func getenvEither(upper, lower string) string {
	if v := os.Getenv(upper); v != "" {
		return v
	}
	return os.Getenv(lower)
}

// useProxy reports whether a request to u goes through the proxy, given
// the no_proxy list.
func useProxy(u *neturl.URL, noProxy string) bool {
	host := strings.ToLower(u.Hostname())
	port := u.Port()
	if port == "" {
		port = map[string]string{"http": "80", "https": "443"}[u.Scheme]
	}
	if host == "localhost" {
		return false
	}
	ip := net.ParseIP(host)
	if ip != nil && ip.IsLoopback() {
		return false
	}
	for _, e := range strings.Split(noProxy, ",") {
		e = strings.ToLower(strings.TrimSpace(e))
		if e == "" {
			continue
		}
		if e == "*" {
			return false
		}
		if _, block, err := net.ParseCIDR(e); err == nil {
			if ip != nil && block.Contains(ip) {
				return false
			}
			continue
		}
		ehost, eport := e, ""
		if h, p, err := net.SplitHostPort(e); err == nil {
			ehost, eport = h, p
		}
		if eport != "" && eport != port {
			continue
		}
		if eip := net.ParseIP(ehost); eip != nil {
			if ip != nil && eip.Equal(ip) {
				return false
			}
			continue
		}
		ehost = strings.TrimPrefix(strings.TrimPrefix(ehost, "*"), ".")
		if ehost != "" && (host == ehost || strings.HasSuffix(host, "."+ehost)) {
			return false
		}
	}
	return true
}
