package main

import (
	"net"
	"net/http"
	"sync/atomic"
)

const bootRetryAfterSeconds = "5"

type bootGate struct {
	phase atomic.Pointer[string]
	app   atomic.Pointer[http.Handler]
}

func newBootGate() *bootGate {
	g := &bootGate{}
	g.enter("starting")
	return g
}

func (g *bootGate) enter(phase string) { g.phase.Store(&phase) }

func (g *bootGate) open(app http.Handler) { g.app.Store(&app) }

func (g *bootGate) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if app := g.app.Load(); app != nil {
		(*app).ServeHTTP(w, r)
		return
	}
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/healthz":
		w.WriteHeader(http.StatusOK)
	case r.Method == http.MethodGet && r.URL.Path == "/readyz":
		http.Error(w, "not ready: "+*g.phase.Load(), http.StatusServiceUnavailable)
	default:
		w.Header().Set("Retry-After", bootRetryAfterSeconds)
		http.Error(w, "ploegd is starting", http.StatusServiceUnavailable)
	}
}

type earlyServer struct {
	http   *http.Server
	served chan error
}

func listenEarly(addr string, gate *bootGate) (*earlyServer, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	s := &earlyServer{http: newHTTPServer(addr, gate, defaultHTTPTimeouts), served: make(chan error, 1)}
	go func() { s.served <- s.http.Serve(ln) }()
	return s, nil
}

func (s *earlyServer) abort() { _ = s.http.Close() }

func (s *earlyServer) wait() error { return <-s.served }
