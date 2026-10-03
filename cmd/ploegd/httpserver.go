package main

import (
	"net/http"
	"time"
)

type httpTimeouts struct {
	readHeader time.Duration
	read       time.Duration
	idle       time.Duration
}

var defaultHTTPTimeouts = httpTimeouts{
	readHeader: 5 * time.Second,
	read:       30 * time.Second,
	idle:       120 * time.Second,
}

func newHTTPServer(addr string, handler http.Handler, timeouts httpTimeouts) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: timeouts.readHeader,
		ReadTimeout:       timeouts.read,
		IdleTimeout:       timeouts.idle,
	}
}
