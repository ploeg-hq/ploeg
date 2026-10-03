package gitlab

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/provider"
)

func TestPullRequestFacts_ChangesCountIsKeptOnlyWhenExact(t *testing.T) {
	for raw, want := range map[string]*int{`"6"`: ptr(6), `"1000+"`: nil, `null`: nil} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"state":"opened","sha":"h1","changes_count":` + raw + `}`))
		}))
		f, err := (&Provider{BaseURL: srv.URL}).PullRequestFacts(context.Background(), "g/p", 3)
		srv.Close()
		if err != nil {
			t.Fatal(err)
		}
		if (want == nil) != (f.ChangedFiles == nil) || (want != nil && *f.ChangedFiles != *want) {
			t.Errorf("changes_count %s: changed files = %v, want %v", raw, f.ChangedFiles, want)
		}
		if f.Additions != nil || f.Deletions != nil {
			t.Errorf("GitLab reports no line counts here; got %v %v", f.Additions, f.Deletions)
		}
	}
}

func TestCommitStatus_CombinesTheLatestStatuses(t *testing.T) {
	var gotPath, gotToken string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotToken = r.URL.EscapedPath(), r.Header.Get("PRIVATE-TOKEN")
		_, _ = w.Write([]byte(`[{"name":"verify","status":"success"},{"name":"deploy","status":"manual"},
			{"name":"lint","status":"running"}]`))
	}))
	defer srv.Close()
	status, ok, err := (&Provider{BaseURL: srv.URL, Token: "tok"}).CommitStatus(context.Background(), "g/p", "abc")
	if err != nil || !ok {
		t.Fatalf("ok = %v, err = %v", ok, err)
	}
	if gotPath != "/api/v4/projects/g%2Fp/repository/commits/abc/statuses" || gotToken != "tok" {
		t.Errorf("request = %s (%s)", gotPath, gotToken)
	}
	if status.State != provider.CommitPending || len(status.Checks) != 2 ||
		status.Checks[1] != (provider.CommitCheck{Context: "lint", State: provider.CommitPending}) {
		t.Errorf("status = %+v; a running job is pending and a manual one is left out", status)
	}
}

func TestCommitStatus_NoStatusesIsUnknown(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[]`))
	}))
	defer srv.Close()
	_, ok, err := (&Provider{BaseURL: srv.URL}).CommitStatus(context.Background(), "g/p", "abc")
	if err != nil || ok {
		t.Fatalf("ok = %v, err = %v", ok, err)
	}
}

func TestCommitStatus_SurfacesTheFailureWithoutTheToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()
	_, ok, err := (&Provider{BaseURL: srv.URL, Token: "s3cret"}).CommitStatus(context.Background(), "g/p", "abc")
	if err == nil || ok || strings.Contains(err.Error(), "s3cret") {
		t.Fatalf("ok = %v, err = %v", ok, err)
	}
}

func TestParseWebhookOpenAndPushAreRecordedFacts(t *testing.T) {
	p := &Provider{Secret: "s3cret"}
	for _, tc := range []struct {
		attrs map[string]any
		kind  provider.ForgeEventKind
	}{
		{map[string]any{"iid": 9, "action": "open", "source_branch": "agent/vik-1", "last_commit": map[string]any{"id": "h1"}}, provider.ForgePROpened},
		{map[string]any{"iid": 9, "action": "reopen", "source_branch": "agent/vik-1", "last_commit": map[string]any{"id": "h1"}}, provider.ForgePROpened},
		{map[string]any{"iid": 9, "action": "update", "oldrev": "h0", "source_branch": "agent/vik-1", "last_commit": map[string]any{"id": "h1"}}, provider.ForgePRSynchronized},
	} {
		evs, err := post(t, p, "", map[string]any{
			"object_kind": "merge_request", "project": map[string]any{"path_with_namespace": "g/p"},
			"user": map[string]any{"username": "bot"}, "object_attributes": tc.attrs,
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(evs) != 1 || evs[0].Kind != tc.kind || evs[0].PR != 9 || evs[0].PullRequest.HeadSHA != "h1" ||
			evs[0].PullRequest.State != provider.PullRequestOpen {
			t.Errorf("%v: events = %+v", tc.attrs["action"], evs)
		}
	}
}

func ptr(n int) *int { return &n }
