package main

import (
	"net/http"
	"time"
)

type httpTimeouts struct {
	readHeader time.Duration
	read       time.Duration
	write      time.Duration
	idle       time.Duration
}

var defaultHTTPTimeouts = httpTimeouts{
	readHeader: 5 * time.Second,
	read:       30 * time.Second,
	write:      2 * time.Minute,
	idle:       120 * time.Second,
}

const maxRequestHeaderBytes = 64 << 10

func newHTTPServer(addr string, handler http.Handler, timeouts httpTimeouts) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: timeouts.readHeader,
		ReadTimeout:       timeouts.read,
		WriteTimeout:      timeouts.write,
		IdleTimeout:       timeouts.idle,
		MaxHeaderBytes:    maxRequestHeaderBytes,
	}
}
