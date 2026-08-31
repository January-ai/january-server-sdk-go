// Compiled only into temporary test executables alongside the unchanged quickstart.
package main

import (
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
)

type offlineTransport struct {
	target *url.URL
	local  http.Transport
}

func (t *offlineTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.URL.Scheme != "https" || request.URL.Host != "partners.january.ai" {
		return nil, fmt.Errorf("expected the SDK production default")
	}
	local := request.Clone(request.Context())
	local.URL.Scheme = t.target.Scheme
	local.URL.Host = t.target.Host
	local.Host = t.target.Host
	// The zero-value transport has no environment proxy or production fallback.
	return t.local.RoundTrip(local)
}

func init() {
	if len(os.Args) != 2 {
		panic("offline executable requires one loopback fixture argument")
	}
	target, err := url.Parse(os.Args[1])
	if err != nil || target.Scheme != "http" || !net.ParseIP(target.Hostname()).IsLoopback() ||
		target.Port() == "" || target.User != nil || target.Path != "" || target.RawQuery != "" || target.Fragment != "" {
		panic("offline executable requires an HTTP loopback fixture")
	}
	http.DefaultTransport = &offlineTransport{target: target}
}
