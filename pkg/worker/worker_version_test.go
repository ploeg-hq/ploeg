package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestTheClaimCarriesTheWorkerVersion(t *testing.T) {
	var claim map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&claim)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	w := New(Config{APIURL: srv.URL, Team: "bronze", Version: "v0.4.1"}, nil, nil, slog.New(slog.DiscardHandler))
	if err := w.RunContext(context.Background()); err != nil {
		t.Fatalf("RunContext() = %v, want an empty-handed exit", err)
	}
	if claim["workerVersion"] != "v0.4.1" {
		t.Errorf("claim = %v, want workerVersion v0.4.1", claim)
	}
}

func TestAWorkerPloegdRefusesWith426ExitsAfterOneClaimNamingTheRange(t *testing.T) {
	var claims atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/claim" {
			t.Errorf("unexpected request %s after the refusal", r.URL.Path)
		}
		claims.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUpgradeRequired)
		_, _ = w.Write([]byte(`{"error":"worker_version_unsupported","ploegd":"v0.4.1","worker":"v0.2.7",` +
			`"oldestWorker":"0.3","newestWorker":"0.4","upgrade":"worker",` +
			`"message":"ploegd v0.4.1 serves ploeg-worker 0.3 to 0.4, and this worker is v0.2.7: upgrade the workers to ploegd's release"}`))
	}))
	defer srv.Close()
	var logs bytes.Buffer

	w := New(Config{APIURL: srv.URL, Team: "bronze", Version: "v0.2.7"}, nil, nil, slog.New(slog.NewTextHandler(&logs, nil)))
	err := w.RunContext(context.Background())

	var unsupported *UnsupportedWorkerVersion
	if !errors.As(err, &unsupported) {
		t.Fatalf("RunContext() = %v, want an UnsupportedWorkerVersion", err)
	}
	if unsupported.OldestWorker != "0.3" || unsupported.NewestWorker != "0.4" || unsupported.Upgrade != "worker" {
		t.Errorf("refusal = %+v, want range 0.3 to 0.4 with the worker to upgrade", unsupported)
	}
	if !strings.Contains(err.Error(), "0.3 to 0.4") {
		t.Errorf("error %q does not name the supported range", err)
	}
	if n := claims.Load(); n != 1 {
		t.Errorf("worker claimed %d times, want exactly one", n)
	}
	for _, part := range []string{"level=ERROR", "worker_version=v0.2.7", "ploegd_version=v0.4.1", "oldest_worker=0.3", "newest_worker=0.4", "upgrade_first=worker"} {
		if !strings.Contains(logs.String(), part) {
			t.Errorf("log %q does not carry %q", logs.String(), part)
		}
	}
}

func TestA426WithoutABodyIsStillAVersionRefusal(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUpgradeRequired)
	}))
	defer srv.Close()

	api := &APIClient{Base: srv.URL, HC: srv.Client(), WorkerVersion: "v0.2.7"}
	_, err := api.Claim("bronze", "")

	var unsupported *UnsupportedWorkerVersion
	if !errors.As(err, &unsupported) || !strings.Contains(err.Error(), "426") {
		t.Fatalf("Claim() = %v, want an UnsupportedWorkerVersion naming HTTP 426", err)
	}
}
