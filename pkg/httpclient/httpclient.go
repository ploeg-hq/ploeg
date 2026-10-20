// Package httpclient holds the one connection pool Ploeg's outbound API
// clients share: forges, trackers, the model gateway and the notes graph.
package httpclient

import (
	"net/http"
	"time"
)

const (
	maxIdleConns        = 64
	maxIdleConnsPerHost = 16
	idleConnTimeout     = 90 * time.Second
	tlsHandshakeTimeout = 10 * time.Second
)

var shared = newTransport()

func newTransport() *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.MaxIdleConns = maxIdleConns
	t.MaxIdleConnsPerHost = maxIdleConnsPerHost
	t.IdleConnTimeout = idleConnTimeout
	t.TLSHandshakeTimeout = tlsHandshakeTimeout
	return t
}

// Transport returns the shared transport.
func Transport() http.RoundTripper { return shared }

// New returns a client on the shared transport with the given overall
// request timeout.
func New(timeout time.Duration) *http.Client {
	return &http.Client{Transport: shared, Timeout: timeout}
}
