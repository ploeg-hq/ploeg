package forgejo

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/provider"
)

func TestPullRequestFacts_ReadsTheDiffSize(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"state":"open","merged":false,"head":{"sha":"abc"},
			"additions":214,"deletions":0,"changed_files":6}`))
	}))
	defer srv.Close()
	f, err := (&Provider{BaseURL: srv.URL}).PullRequestFacts(context.Background(), "webgrip/ploeg", 7)
	if err != nil {
		t.Fatal(err)
	}
	if f.Additions == nil || *f.Additions != 214 || f.Deletions == nil || *f.Deletions != 0 ||
		f.ChangedFiles == nil || *f.ChangedFiles != 6 {
		t.Errorf("diff = %v %v %v; want 214, a reported 0, 6", f.Additions, f.Deletions, f.ChangedFiles)
	}

	bare := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"state":"open","merged":false,"head":{"sha":"abc"}}`))
	}))
	defer bare.Close()
	f, err = (&Provider{BaseURL: bare.URL}).PullRequestFacts(context.Background(), "webgrip/ploeg", 7)
	if err != nil {
		t.Fatal(err)
	}
	if f.Additions != nil || f.Deletions != nil || f.ChangedFiles != nil {
		t.Errorf("an unreported diff became %v %v %v; want nil", f.Additions, f.Deletions, f.ChangedFiles)
	}
}

func TestCommitStatus_ReadsTheCombinedStatusAtTheSHA(t *testing.T) {
	var gotPath, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotAuth = r.URL.Path, r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"state":"failure","sha":"abc","total_count":2,"statuses":[
			{"context":"verify","status":"success"},{"context":"lint","status":"failure"}]}`))
	}))
	defer srv.Close()
	status, ok, err := (&Provider{BaseURL: srv.URL, Token: "tok"}).CommitStatus(context.Background(), "webgrip/ploeg", "abc")
	if err != nil || !ok {
		t.Fatalf("ok = %v, err = %v", ok, err)
	}
	if gotPath != "/api/v1/repos/webgrip/ploeg/commits/abc/status" || gotAuth != "token tok" {
		t.Errorf("request = %s (%s)", gotPath, gotAuth)
	}
	if status.State != provider.CommitFailure || status.SHA != "abc" || len(status.Checks) != 2 ||
		status.Checks[0] != (provider.CommitCheck{Context: "verify", State: provider.CommitSuccess}) {
		t.Errorf("status = %+v", status)
	}
}

func TestCommitStatus_NoChecksIsUnknown(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"state":"","sha":"abc","total_count":0,"statuses":[]}`))
	}))
	defer srv.Close()
	_, ok, err := (&Provider{BaseURL: srv.URL}).CommitStatus(context.Background(), "webgrip/ploeg", "abc")
	if err != nil || ok {
		t.Fatalf("ok = %v, err = %v; a commit without checks has no CI", ok, err)
	}
}

func TestCommitStatus_DropsAWarningAndRecombines(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"state":"warning","sha":"abc","total_count":2,"statuses":[
			{"context":"verify","status":"success"},{"context":"advice","status":"warning"}]}`))
	}))
	defer srv.Close()
	status, ok, err := (&Provider{BaseURL: srv.URL}).CommitStatus(context.Background(), "webgrip/ploeg", "abc")
	if err != nil || !ok {
		t.Fatalf("ok = %v, err = %v", ok, err)
	}
	if status.State != provider.CommitSuccess || len(status.Checks) != 1 {
		t.Errorf("status = %+v; want success from the one check in the vocabulary", status)
	}
}

func TestCommitStatus_SurfacesTheFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	_, ok, err := (&Provider{BaseURL: srv.URL, Token: "s3cret"}).CommitStatus(context.Background(), "webgrip/ploeg", "abc")
	if err == nil || ok || strings.Contains(err.Error(), "s3cret") {
		t.Fatalf("ok = %v, err = %v", ok, err)
	}
}

func TestParseWebhook_OpenedAndSynchronizedCarryTheHeadAndDiff(t *testing.T) {
	p := &Provider{Secret: "shh"}
	for action, kind := range map[string]provider.ForgeEventKind{
		"opened": provider.ForgePROpened, "reopened": provider.ForgePROpened, "synchronized": provider.ForgePRSynchronized,
	} {
		events, err := post(t, p, "pull_request", map[string]any{
			"action":     action,
			"repository": map[string]any{"full_name": "webgrip/ploeg"},
			"sender":     map[string]any{"login": "ploeg-bot"},
			"pull_request": map[string]any{"number": 57, "head": map[string]any{"ref": "agent/vik-1", "sha": "h1"},
				"additions": 10, "deletions": 2, "changed_files": 3},
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(events) != 1 {
			t.Fatalf("%s: events = %+v", action, events)
		}
		ev := events[0]
		if ev.Kind != kind || ev.PR != 57 || ev.Branch != "agent/vik-1" || ev.PullRequest.State != provider.PullRequestOpen ||
			ev.PullRequest.HeadSHA != "h1" || ev.PullRequest.Additions == nil || *ev.PullRequest.Additions != 10 ||
			*ev.PullRequest.ChangedFiles != 3 {
			t.Errorf("%s: event = %+v", action, ev)
		}
	}
}
