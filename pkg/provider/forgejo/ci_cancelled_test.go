package forgejo

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/provider"
)

func TestCommitStatus_ClassifiesACancelledJobAsCancelled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"state":"failure","sha":"abc","total_count":2,"statuses":[
			{"context":"ci / e2e (push)","status":"failure","description":"Has been cancelled",
			 "target_url":"https://forgejo/o/r/actions/runs/9/jobs/1","updated_at":"2026-10-18T09:00:00Z"},
			{"context":"ci / test (push)","status":"failure","description":"Failing after 2m"}]}`))
	}))
	defer srv.Close()
	status, ok, err := (&Provider{BaseURL: srv.URL}).CommitStatus(context.Background(), "o/r", "abc")
	if err != nil || !ok || len(status.Checks) != 2 {
		t.Fatalf("status = %+v, ok = %v, err = %v", status, ok, err)
	}
	cancelled, failed := status.Checks[0], status.Checks[1]
	if !cancelled.Cancelled || cancelled.State != provider.CommitFailure || cancelled.TargetURL != "https://forgejo/o/r/actions/runs/9/jobs/1" ||
		cancelled.UpdatedAt.IsZero() {
		t.Errorf("cancelled job = %+v; want failure, cancelled, with its target URL and time", cancelled)
	}
	if failed.Cancelled || failed.Description != "Failing after 2m" {
		t.Errorf("failed job = %+v; a failure is not a cancellation", failed)
	}
}

func TestPullRequestFacts_ReadsTheBaseCommit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"state":"open","merged":false,"head":{"sha":"abc"},"base":{"ref":"main","sha":"def"}}`))
	}))
	defer srv.Close()
	f, err := (&Provider{BaseURL: srv.URL}).PullRequestFacts(context.Background(), "o/r", 7)
	if err != nil || f.BaseSHA != "def" {
		t.Fatalf("base = %q, err = %v; want def", f.BaseSHA, err)
	}
}
