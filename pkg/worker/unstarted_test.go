package worker

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/harness"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

func TestFailUnstartedRunReportsAnInfraFailure(t *testing.T) {
	var claim map[string]string
	var outcome harness.OutcomeReport
	var outcomeAuth, workerHeader string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/claim":
			workerHeader = r.Header.Get("X-Ploeg-Worker-ID")
			_ = json.NewDecoder(r.Body).Decode(&claim)
			_ = json.NewEncoder(w).Encode(ClaimResponse{RunToken: "run-1", ControlToken: "control-1"})
		case "/api/v1/runs/run-1/outcome":
			outcomeAuth = r.Header.Get("Authorization")
			_ = json.NewDecoder(r.Body).Decode(&outcome)
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusTeapot)
		}
	}))
	defer srv.Close()

	claimed, err := FailUnstartedRun(srv.URL, "bootstrap", "launcher-uid", "bronze", "builder", "sandbox claim never became ready within 10m0s: ReconcilerError: pod is owned by Job/x")
	if err != nil || !claimed {
		t.Fatalf("FailUnstartedRun() = %v, %v; want true, nil", claimed, err)
	}
	if claim["team"] != "bronze" || claim["role"] != "builder" || workerHeader != "launcher-uid" {
		t.Fatalf("claim = %v from worker %q, want bronze/builder from launcher-uid", claim, workerHeader)
	}
	if outcome.Outcome != work.OutcomeFailed || outcome.FailureReason != string(work.FailureInfraNode) || !strings.Contains(outcome.Summary, "ReconcilerError") {
		t.Fatalf("outcome = %+v, want failed/infra_node naming the controller's reason", outcome)
	}
	if outcomeAuth != "Bearer control-1" {
		t.Fatalf("outcome sent with %q, want the Run's control token", outcomeAuth)
	}
}

func TestFailUnstartedRunClaimsNothingFromAnEmptyQueue(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/claim" {
			t.Errorf("unexpected request %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	claimed, err := FailUnstartedRun(srv.URL, "bootstrap", "launcher-uid", "bronze", "builder", "never ready")
	if err != nil || claimed {
		t.Fatalf("FailUnstartedRun() = %v, %v; want false, nil", claimed, err)
	}
}
