package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/ploeg-hq/ploeg/pkg/work"
)

func versionedAPIServer(ploegdVersion string) http.Handler {
	return (&Server{
		Store:          testStore,
		LeaseTTL:       time.Minute,
		Log:            slog.New(slog.DiscardHandler),
		WorkerSecurity: &WorkerSecurity{AllowLegacy: true},
		Version:        ploegdVersion,
	}).Handler()
}

func queueOneItem(t *testing.T, externalID string) {
	t.Helper()
	if _, _, err := testStore.IngestAssigned(context.Background(), work.WorkItem{
		Provider: "vikunja", ExternalID: externalID, Team: "silver", Title: "versioned",
	}); err != nil {
		t.Fatal(err)
	}
}

func claimAs(t *testing.T, h http.Handler, workerVersion string) *httptest.ResponseRecorder {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"team": "silver", "workerVersion": workerVersion})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/claim", bytes.NewReader(body)))
	return rec
}

func TestClaim_AWorkerOlderThanThePreviousMinorGets426NamingTheRange(t *testing.T) {
	reset(t)
	queueOneItem(t, "900")

	rec := claimAs(t, versionedAPIServer("v0.4.1"), "v0.2.7")

	if rec.Code != http.StatusUpgradeRequired {
		t.Fatalf("claim from a worker two minors behind returned %d, want 426", rec.Code)
	}
	var refusal workerVersionRefusal
	if err := json.NewDecoder(rec.Body).Decode(&refusal); err != nil {
		t.Fatalf("decode refusal: %v", err)
	}
	want := workerVersionRefusal{Error: workerVersionUnsupported, Ploegd: "v0.4.1", Worker: "v0.2.7",
		OldestWorker: "0.3", NewestWorker: "0.4", Upgrade: "worker"}
	got := refusal
	got.Message = ""
	if got != want {
		t.Errorf("refusal = %+v, want %+v", got, want)
	}
	for _, part := range []string{"v0.4.1", "0.3 to 0.4", "v0.2.7", "upgrade the workers"} {
		if !strings.Contains(refusal.Message, part) {
			t.Errorf("message %q does not name %q", refusal.Message, part)
		}
	}
	if got := rec.Header().Get("Upgrade"); got != "ploeg-worker/0.3, ploeg-worker/0.4" {
		t.Errorf("Upgrade = %q, want the two served worker lines", got)
	}
	if rec := claimAs(t, versionedAPIServer("v0.4.1"), "v0.4.1"); rec.Code != http.StatusOK {
		t.Errorf("a served worker after the refusal got %d, want 200: the refused claim took the Work Item", rec.Code)
	}
}

func TestClaim_AWorkerNewerThanPloegdIsToldToUpgradePloegdFirst(t *testing.T) {
	reset(t)
	queueOneItem(t, "901")

	rec := claimAs(t, versionedAPIServer("v0.4.1"), "v0.5.0-rc.2")

	if rec.Code != http.StatusUpgradeRequired {
		t.Fatalf("claim from a worker newer than ploegd returned %d, want 426", rec.Code)
	}
	var refusal workerVersionRefusal
	if err := json.NewDecoder(rec.Body).Decode(&refusal); err != nil {
		t.Fatalf("decode refusal: %v", err)
	}
	if refusal.Upgrade != "ploegd" || !strings.Contains(refusal.Message, "upgrade ploegd first") {
		t.Errorf("refusal = %+v, want it to name ploegd as the side to upgrade first", refusal)
	}
}

func TestClaim_ServedWorkersClaimNormally(t *testing.T) {
	for _, tc := range []struct{ name, ploegd, worker string }{
		{"same release", "v0.4.1", "v0.4.1"},
		{"previous minor", "v0.4.1", "0.3.9"},
		{"newer patch of the same minor", "v0.4.0-rc.3", "v0.4.2"},
		{"no version sent", "v0.4.1", ""},
		{"development worker", "v0.4.1", "0.0.0-dev"},
		{"development ploegd", "0.0.0-dev", "v0.1.0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reset(t)
			queueOneItem(t, "902")
			rec := claimAs(t, versionedAPIServer(tc.ploegd), tc.worker)
			if rec.Code != http.StatusOK {
				t.Fatalf("worker %q against ploegd %q: claim returned %d %s, want 200", tc.worker, tc.ploegd, rec.Code, rec.Body)
			}
			var resp claimResponse
			if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil || resp.RunToken == "" {
				t.Fatalf("claim body = %v (%v), want a Run", resp, err)
			}
		})
	}
}

func TestWorkerVersionBodiesMatchTheRunAPIContract(t *testing.T) {
	path, err := filepath.Abs("../../docs/contracts/run-api.v1.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	refusal, _ := workerVersionRefusalFor("v0.4.1", "v0.2.7")
	for def, body := range map[string]any{
		"claimRequest":         claimRequest{Team: "silver", WorkerVersion: "v0.4.1"},
		"workerVersionRefusal": refusal,
	} {
		schema, err := jsonschema.NewCompiler().Compile(path + "#/$defs/" + def)
		if err != nil {
			t.Fatalf("%s: %v", def, err)
		}
		b, _ := json.Marshal(body)
		instance, err := jsonschema.UnmarshalJSON(bytes.NewReader(b))
		if err != nil {
			t.Fatal(err)
		}
		if err := schema.Validate(instance); err != nil {
			t.Errorf("%s violates the contract: %v", def, err)
		}
	}
}
