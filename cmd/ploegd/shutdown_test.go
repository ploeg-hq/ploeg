package main

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"sync/atomic"
	"testing"
	"time"
)

func listenLocal(t *testing.T) (*http.Server, func() error, string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := newHTTPServer("", http.NotFoundHandler(), defaultHTTPTimeouts)
	return srv, func() error { return srv.Serve(ln) }, ln.Addr().String()
}

func TestShutdownWaitsForLoopsBeforeTheStoreCloses(t *testing.T) {
	ctx, stop := context.WithCancel(context.Background())
	srv, serve, _ := listenLocal(t)
	var loops backgroundLoops
	var storeClosed, usedAfterClose atomic.Bool
	loops.Go(func() {
		<-ctx.Done()
		time.Sleep(200 * time.Millisecond)
		if storeClosed.Load() {
			usedAfterClose.Store(true)
		}
	})
	var pipelineFinished atomic.Bool
	pipelines := make(chan struct{})
	go func() {
		time.Sleep(300 * time.Millisecond)
		pipelineFinished.Store(true)
		close(pipelines)
	}()

	done := make(chan error, 1)
	go func() {
		err := serveUntilStopped(ctx, slog.New(slog.DiscardHandler), srv, serve, defaultShutdownBudget,
			drain{name: "loops", wait: loops.Wait},
			drain{name: "pull request reads", wait: func() { <-pipelines }})
		storeClosed.Store(true)
		done <- err
	}()
	time.Sleep(50 * time.Millisecond)
	stop()

	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown did not return")
	}
	loops.Wait()
	if usedAfterClose.Load() {
		t.Error("a background loop ran after the store closed")
	}
	if !pipelineFinished.Load() {
		t.Error("the store closed while a pull request read was still running")
	}
}

func TestShutdownGivesUpOnAStuckLoopWithinItsBudget(t *testing.T) {
	ctx, stop := context.WithCancel(context.Background())
	srv, serve, _ := listenLocal(t)
	var loops backgroundLoops
	release := make(chan struct{})
	defer close(release)
	loops.Go(func() { <-release })

	stop()
	start := time.Now()
	budget := shutdownBudget{http: 100 * time.Millisecond, total: 300 * time.Millisecond}
	if err := serveUntilStopped(ctx, slog.New(slog.DiscardHandler), srv, serve, budget, drain{name: "loops", wait: loops.Wait}); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed > budget.total+time.Second {
		t.Errorf("shutdown took %s with a %s budget", elapsed, budget.total)
	}
}

func TestShutdownFinishesInFlightRequestsBeforeDraining(t *testing.T) {
	ctx, stop := context.WithCancel(context.Background())
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	var handlerDone atomic.Bool
	srv := newHTTPServer("", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(entered)
		time.Sleep(200 * time.Millisecond)
		handlerDone.Store(true)
		w.WriteHeader(http.StatusNoContent)
	}), defaultHTTPTimeouts)

	var drainedAfterHandler atomic.Bool
	done := make(chan error, 1)
	go func() {
		done <- serveUntilStopped(ctx, slog.New(slog.DiscardHandler), srv, func() error { return srv.Serve(ln) },
			defaultShutdownBudget, drain{name: "loops", wait: func() { drainedAfterHandler.Store(handlerDone.Load()) }})
	}()

	respCh := make(chan *http.Response, 1)
	go func() {
		resp, err := http.Post("http://"+ln.Addr().String()+"/", "application/json", nil)
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
		}
		respCh <- resp
	}()
	<-entered
	stop()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if resp := <-respCh; resp == nil || resp.StatusCode != http.StatusNoContent {
		t.Errorf("the in-flight request did not complete: %v", resp)
	}
	if !drainedAfterHandler.Load() {
		t.Error("background work was drained before the in-flight request finished")
	}
}
