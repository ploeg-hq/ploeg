package main

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestMissingHarnessBinaryExitsBeforeClaiming(t *testing.T) {
	var requests atomic.Int32
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer api.Close()
	for key, value := range map[string]string{
		"LITELLM_KEY_BUDGET":           "1",
		"PLOEG_API_URL":                api.URL,
		"PLOEG_TEAM":                   "fixture",
		"FORGE_URL":                    "https://forge.example",
		"AGENT_BUILDER_TOKEN":          "fixture-token",
		"PLOEG_WORKER_BOOTSTRAP_TOKEN": "fixture-bootstrap",
		"PLOEG_WORKER_ID":              "fixture-pod",
		"PLOEG_HARNESS":                "openhands",
		"PLOEG_HARNESS_ENTRYPOINT":     "ploeg-fixture-harness-that-does-not-exist",
		"WORK_DIR":                     t.TempDir(),
	} {
		t.Setenv(key, value)
	}
	err := run(slog.New(slog.DiscardHandler))
	if err == nil || !strings.Contains(err.Error(), "ploeg-fixture-harness-that-does-not-exist") {
		t.Fatalf("missing harness binary not reported: %v", err)
	}
	if requests.Load() != 0 {
		t.Fatalf("worker contacted ploegd %d times before failing its harness check", requests.Load())
	}
}
