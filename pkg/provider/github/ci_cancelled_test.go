package github

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/provider"
)

func TestCommitStatusClassifiesACancelledCheckRunAsCancelled(t *testing.T) {
	f, p := newFake(t)
	f.json("GET /repos/acme/widgets/commits/abc/status", http.StatusOK, `{"state":"pending","statuses":[]}`)
	f.json("GET /repos/acme/widgets/commits/abc/check-runs", http.StatusOK, `{"total_count":2,"check_runs":[
		{"name":"e2e","status":"completed","conclusion":"cancelled","details_url":"https://github.com/acme/widgets/actions/runs/9",
		 "completed_at":"2026-10-18T09:00:00Z","output":{"title":"The run was canceled"}},
		{"name":"test","status":"completed","conclusion":"failure"}]}`)
	st, ok, err := p.CommitStatus(context.Background(), "acme/widgets", "abc")
	if err != nil || !ok || len(st.Checks) != 2 {
		t.Fatalf("status = %+v, ok %v, err %v", st, ok, err)
	}
	cancelled, failed := st.Checks[0], st.Checks[1]
	if !cancelled.Cancelled || !cancelled.Failed() || cancelled.TargetURL == "" || cancelled.UpdatedAt.IsZero() {
		t.Errorf("cancelled check run = %+v; want a failed, cancelled check with its URL and time", cancelled)
	}
	if failed.Cancelled {
		t.Errorf("failed check run = %+v; a failure is not a cancellation", failed)
	}
}

func TestReRunCIReRunsTheFailedWorkflowRunsOfTheCommit(t *testing.T) {
	f, p := newFake(t)
	f.json("GET /repos/acme/widgets/actions/runs", http.StatusOK, `{"total_count":3,"workflow_runs":[
		{"id":1,"status":"completed","conclusion":"cancelled"},
		{"id":2,"status":"completed","conclusion":"success"},
		{"id":3,"status":"completed","conclusion":"failure"}]}`)
	f.json("POST /repos/acme/widgets/actions/runs/1/rerun-failed-jobs", http.StatusCreated, ``)
	f.json("POST /repos/acme/widgets/actions/runs/3/rerun-failed-jobs", http.StatusCreated, ``)
	if err := p.ReRunCI(context.Background(), "acme/widgets", "abc"); err != nil {
		t.Fatal(err)
	}
	var reRuns int
	for _, r := range f.seen {
		if r.Method == http.MethodPost {
			reRuns++
		}
		if r.Method == http.MethodGet && r.URL.Query().Get("head_sha") != "abc" {
			t.Errorf("workflow runs read without the head sha: %s", r.URL.RawQuery)
		}
	}
	if reRuns != 2 {
		t.Errorf("re-run requests = %d, want 2 (the cancelled and the failed run)", reRuns)
	}

	g, q := newFake(t)
	g.json("GET /repos/acme/widgets/actions/runs", http.StatusOK, `{"total_count":0,"workflow_runs":[]}`)
	if err := q.ReRunCI(context.Background(), "acme/widgets", "abc"); !errors.Is(err, provider.ErrNoReRun) {
		t.Errorf("err = %v for a commit with no workflow run, want ErrNoReRun", err)
	}
}
