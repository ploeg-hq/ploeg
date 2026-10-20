package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"time"
)

type backgroundLoops struct {
	running sync.WaitGroup
}

func (l *backgroundLoops) Go(loop func()) {
	l.running.Add(1)
	go func() {
		defer l.running.Done()
		loop()
	}()
}

func (l *backgroundLoops) Wait() { l.running.Wait() }

type shutdownBudget struct {
	http  time.Duration
	total time.Duration
}

var defaultShutdownBudget = shutdownBudget{http: 10 * time.Second, total: 20 * time.Second}

type drain struct {
	name string
	wait func()
}

func serveUntilStopped(ctx context.Context, log *slog.Logger, srv *http.Server, serve func() error,
	budget shutdownBudget, drains ...drain) error {
	served := make(chan error, 1)
	go func() { served <- serve() }()
	select {
	case err := <-served:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-ctx.Done():
	}

	deadline := time.Now().Add(budget.total)
	httpCtx, cancelHTTP := context.WithTimeout(context.Background(), budget.http)
	defer cancelHTTP()
	if err := srv.Shutdown(httpCtx); err != nil {
		log.Warn("http requests still running at shutdown", "err", err)
	}
	if err := <-served; err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Warn("http server stopped with an error", "err", err)
	}

	drainCtx, cancelDrain := context.WithDeadline(context.Background(), deadline)
	defer cancelDrain()
	for _, d := range drains {
		if !waitWithin(drainCtx, d.wait) {
			log.Warn("background work still running at shutdown; the store closes under it", "work", d.name)
		}
	}
	return nil
}

func waitWithin(ctx context.Context, wait func()) bool {
	done := make(chan struct{})
	go func() {
		wait()
		close(done)
	}()
	select {
	case <-done:
		return true
	case <-ctx.Done():
		return false
	}
}
