package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"

	embeddedpostgres "github.com/fergusstrange/embedded-postgres"
)

func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return addr
}

func get(addr, path string) (int, string, http.Header, error) {
	client := http.Client{Timeout: time.Second}
	resp, err := client.Get("http://" + addr + path)
	if err != nil {
		return 0, "", nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body), resp.Header, nil
}

func waitForStatus(t *testing.T, addr, path string, want int, within time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(within)
	var last string
	for time.Now().Before(deadline) {
		code, body, _, err := get(addr, path)
		if err == nil && code == want {
			return body
		}
		last = fmt.Sprintf("status %d body %q err %v", code, body, err)
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("GET %s did not answer %d within %s; last: %s", path, want, within, last)
	return ""
}

type runningDaemon struct {
	stopped chan struct{}
	err     error
}

func startRun(t *testing.T) *runningDaemon {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	d := &runningDaemon{stopped: make(chan struct{})}
	go func() {
		d.err = run(ctx, slog.New(slog.DiscardHandler))
		close(d.stopped)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-d.stopped:
		case <-time.After(15 * time.Second):
			t.Error("run did not stop after its context was cancelled")
		}
	})
	return d
}

func unreachableDatabase(t *testing.T) string {
	return "postgresql://ploeg:ploeg@" + freeAddr(t) + "/ploeg?sslmode=disable&connect_timeout=1"
}

func TestBootGateAnswersLivenessAndRefusesWorkUntilOpen(t *testing.T) {
	gate := newBootGate()
	gate.enter("waiting for the database")

	probe := func(method, path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		gate.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
		return rec
	}
	if rec := probe(http.MethodGet, "/healthz"); rec.Code != http.StatusOK {
		t.Errorf("/healthz while booting = %d, want 200", rec.Code)
	}
	rec := probe(http.MethodGet, "/readyz")
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "waiting for the database") {
		t.Errorf("/readyz while booting = %d %q, want 503 naming the phase", rec.Code, rec.Body.String())
	}
	rec = probe(http.MethodPost, "/api/v1/runs/tok/renew")
	if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Retry-After") == "" {
		t.Errorf("a store route while booting = %d Retry-After %q, want 503 with Retry-After", rec.Code, rec.Header().Get("Retry-After"))
	}

	gate.open(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTeapot) }))
	for _, path := range []string{"/healthz", "/readyz", "/api/v1/runs/tok/renew"} {
		if rec := probe(http.MethodGet, path); rec.Code != http.StatusTeapot {
			t.Errorf("%s after open = %d, want the application's answer", path, rec.Code)
		}
	}
}

func TestRunAnswersLivenessWhileWaitingForTheDatabase(t *testing.T) {
	addr := freeAddr(t)
	t.Setenv("PLOEG_LISTEN", addr)
	t.Setenv("PLOEG_DATABASE_URL", unreachableDatabase(t))
	startRun(t)

	waitForStatus(t, addr, "/healthz", http.StatusOK, 5*time.Second)
	code, body, _, err := get(addr, "/readyz")
	if err != nil || code != http.StatusServiceUnavailable || !strings.Contains(body, "waiting for the database") {
		t.Fatalf("/readyz while the database is unreachable = %d %q %v, want 503 naming the wait", code, body, err)
	}
	code, _, header, err := get(addr, "/api/v1/queue/default")
	if err != nil || code != http.StatusServiceUnavailable || header.Get("Retry-After") == "" {
		t.Fatalf("a store route while the database is unreachable = %d Retry-After %q %v", code, header.Get("Retry-After"), err)
	}
}

func TestRunFailsFastWhenTheListenAddressIsTaken(t *testing.T) {
	taken, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer taken.Close()
	t.Setenv("PLOEG_LISTEN", taken.Addr().String())
	t.Setenv("PLOEG_DATABASE_URL", unreachableDatabase(t))
	d := startRun(t)

	select {
	case <-d.stopped:
		if !errors.Is(d.err, syscall.EADDRINUSE) {
			t.Fatalf("run returned %v, want address already in use", d.err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("run kept booting against a database for 3s although its listen address was taken")
	}
}

func startEmbeddedPostgres(t *testing.T) string {
	t.Helper()
	_, portText, _ := net.SplitHostPort(freeAddr(t))
	var port uint32
	if _, err := fmt.Sscan(portText, &port); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	epg := embeddedpostgres.NewDatabase(embeddedpostgres.DefaultConfig().Encoding("UTF8").Port(port).RuntimePath(dir).Logger(io.Discard))
	if err := epg.Start(); err != nil {
		t.Fatalf("embedded postgres failed to start: %v", err)
	}
	t.Cleanup(func() { _ = epg.Stop() })
	return fmt.Sprintf("postgresql://postgres:postgres@localhost:%d/postgres?sslmode=disable", port)
}

func TestOrphanSweepDoesNotHoldBackReadiness(t *testing.T) {
	dbURL := startEmbeddedPostgres(t)
	listing := make(chan struct{}, 1)
	release := make(chan struct{})
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/key/list" {
			select {
			case listing <- struct{}{}:
			default:
			}
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"keys":[],"total_count":0,"current_page":1,"total_pages":1}`)
	}))
	defer gateway.Close()
	defer close(release)

	addr := freeAddr(t)
	t.Setenv("PLOEG_LISTEN", addr)
	t.Setenv("PLOEG_DATABASE_URL", dbURL)
	t.Setenv("LITELLM_ADMIN_URL", gateway.URL)
	t.Setenv("LITELLM_MASTER_KEY", "sk-test-master")
	t.Setenv("PLOEG_WORKER_AUTH_MODE", "legacy")
	for _, unset := range []string{"PLOEG_WORKER_SIGNING_KEY", "PLOEG_WORKER_BOOTSTRAPS", "PLOEG_CONFIG", "PLOEG_FORGEJO_URL", "PLOEG_GITLAB_URL", "PLOEG_GITHUB_URL", "PLOEG_VIKUNJA_URL", "PLOEG_CLICKUP_SECRET", "PLOEG_CLICKUP_TOKEN"} {
		t.Setenv(unset, "")
		_ = os.Unsetenv(unset)
	}
	d := startRun(t)

	select {
	case <-listing:
	case <-d.stopped:
		t.Fatalf("run stopped before the orphan sweep started: %v", d.err)
	case <-time.After(60 * time.Second):
		t.Fatal("the LiteLLM orphan sweep never started")
	}
	waitForStatus(t, addr, "/readyz", http.StatusOK, 5*time.Second)
}
