package forgejo

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/provider"
)

func ts(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t.UTC()
}

func tsp(s string) *time.Time {
	t := ts(s)
	return &t
}

func TestPullRequestActivity_ReadsTimelineReviewsAndCommitsWithoutText(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "token tok" {
			t.Errorf("read without the token: %s", r.URL)
		}
		page := r.URL.Query().Get("page")
		switch r.URL.Path {
		case "/api/v1/repos/webgrip/ploeg/issues/7/timeline":
			if page != "1" {
				fmt.Fprint(w, `[]`)
				return
			}
			fmt.Fprint(w, `[
				{"type":"pull_push","user":{"login":"ploeg-bot"},"created_at":"2026-10-01T09:00:02Z","body":"{\"is_force_push\":false,\"commit_ids\":[\"aaa\",\"bbb\"]}"},
				{"type":"change_title","user":{"login":"ploeg-bot"},"created_at":"2026-10-01T09:10:00Z","old_title":"WIP: add billing","new_title":"add billing"},
				{"type":"change_title","user":{"login":"anna"},"created_at":"2026-10-01T09:11:00Z","old_title":"add billing","new_title":"add billing export"},
				{"type":"comment","user":{"login":"anna"},"created_at":"2026-10-01T09:30:00Z","body":"secret review text"},
				{"type":"code","user":{"login":"bob"},"created_at":"2026-10-01T09:40:00Z","body":"nit"},
				{"type":"review","user":{"login":"anna"},"created_at":"2026-10-01T09:45:00Z","body":"looks fine"},
				{"type":"pull_push","user":{"login":"ploeg-bot"},"created_at":"2026-10-01T10:00:00Z","body":"{\"is_force_push\":true,\"commit_ids\":[\"bbb\",\"ccc\"]}"},
				{"type":"pull_push","user":{"login":"ploeg-bot"},"created_at":"2026-10-01T10:01:00Z","body":"not json"},
				{"type":"merge_pull","user":{"login":"anna"},"created_at":"2026-10-01T11:00:00Z"},
				{"type":"comment","user":null,"created_at":""}
			]`)
		case "/api/v1/repos/webgrip/ploeg/pulls/7/reviews":
			fmt.Fprint(w, `[{"state":"REQUEST_CHANGES","submitted_at":"2026-10-01T09:45:00Z","commit_id":"bbb","user":{"login":"anna"}},
				{"state":"PENDING","submitted_at":"2026-10-01T09:50:00Z","commit_id":"bbb","user":{"login":"bob"}},
				{"state":"APPROVED","submitted_at":"2026-10-01T10:30:00Z","commit_id":"ccc","user":{"login":"anna"}}]`)
		case "/api/v1/repos/webgrip/ploeg/pulls/7/commits":
			if r.URL.Query().Get("files") != "false" || r.URL.Query().Get("verification") != "false" {
				t.Errorf("commits read with files or verification: %s", r.URL.RawQuery)
			}
			fmt.Fprint(w, `[{"commit":{"author":{"date":"2026-10-01T08:30:00+02:00"}}},{"commit":{"author":{"date":"2026-10-01T05:00:00Z"}}}]`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	got, err := (&Provider{BaseURL: srv.URL, Token: "tok"}).PullRequestActivity(context.Background(), "webgrip/ploeg", 7)
	if err != nil {
		t.Fatal(err)
	}
	want := []provider.ActivityEvent{
		{Kind: provider.ActivityPush, Actor: "ploeg-bot", At: ts("2026-10-01T09:00:02Z"), HeadSHA: "bbb"},
		{Kind: provider.ActivityReady, Actor: "ploeg-bot", At: ts("2026-10-01T09:10:00Z")},
		{Kind: provider.ActivityComment, Actor: "anna", At: ts("2026-10-01T09:30:00Z")},
		{Kind: provider.ActivityReviewComment, Actor: "bob", At: ts("2026-10-01T09:40:00Z")},
		{Kind: provider.ActivityReview, Actor: "anna", At: ts("2026-10-01T09:45:00Z"), State: provider.ForgeReviewChangesRequested, HeadSHA: "bbb"},
		{Kind: provider.ActivityForcePush, Actor: "ploeg-bot", At: ts("2026-10-01T10:00:00Z"), HeadSHA: "ccc"},
		{Kind: provider.ActivityReview, Actor: "anna", At: ts("2026-10-01T10:30:00Z"), State: provider.ForgeReviewApproved, HeadSHA: "ccc"},
	}
	if !reflect.DeepEqual(got.Events, want) {
		t.Fatalf("events = %+v\nwant %+v", got.Events, want)
	}
	if got.Commits != 2 || got.FirstCommitAt == nil || !got.FirstCommitAt.Equal(ts("2026-10-01T05:00:00Z")) || !got.ForcePushesKnown ||
		got.EventsTruncated || got.CommitsTruncated {
		t.Errorf("activity = %+v", got)
	}
}

func TestPullRequestActivity_StopsAtItsBound(t *testing.T) {
	var timelinePages int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/timeline"):
			timelinePages++
			items := make([]string, activityPageSize)
			for i := range items {
				items[i] = `{"type":"comment","user":{"login":"anna"},"created_at":"2026-10-01T09:00:00Z"}`
			}
			fmt.Fprint(w, "["+strings.Join(items, ",")+"]")
		case strings.HasSuffix(r.URL.Path, "/commits"):
			items := make([]string, activityPageSize)
			for i := range items {
				items[i] = `{"commit":{"author":{"date":"2026-10-01T09:00:00Z"}}}`
			}
			fmt.Fprint(w, "["+strings.Join(items, ",")+"]")
		default:
			t.Errorf("unexpected read %s once events are truncated", r.URL)
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	got, err := (&Provider{BaseURL: srv.URL}).PullRequestActivity(context.Background(), "webgrip/ploeg", 7)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Events) != provider.MaxActivityEvents || !got.EventsTruncated || timelinePages != activityPages {
		t.Errorf("events = %d, truncated = %v, pages = %d", len(got.Events), got.EventsTruncated, timelinePages)
	}
	if got.Commits != provider.MaxActivityCommits || !got.CommitsTruncated {
		t.Errorf("commits = %d, truncated = %v", got.Commits, got.CommitsTruncated)
	}
}

const statusHistory107 = `[
	{"id":10,"status":"success","context":"On Pull Request / warnings (pull_request)","description":"Successful in 0s","created_at":"2026-10-01T17:49:07Z"},
	{"id":9,"status":"pending","context":"On Pull Request / warnings (pull_request)","description":"Has started running","created_at":"2026-10-01T17:49:07Z"},
	{"id":8,"status":"pending","context":"On Pull Request / warnings (pull_request)","description":"Waiting to run","created_at":"2026-10-01T17:39:29Z"},
	{"id":7,"status":"failure","context":"On Pull Request / checks (pull_request)","description":"Failing after 3m45s","created_at":"2026-10-01T17:39:28Z"},
	{"id":6,"status":"success","context":"On Pull Request / release-policy (pull_request)","description":"Successful in 25s","created_at":"2026-10-01T17:36:16Z"},
	{"id":5,"status":"pending","context":"On Pull Request / release-policy (pull_request)","description":"Has started running","created_at":"2026-10-01T17:35:52Z"},
	{"id":4,"status":"pending","context":"On Pull Request / checks (pull_request)","description":"Has started running","created_at":"2026-10-01T17:35:43Z"},
	{"id":3,"status":"pending","context":"On Pull Request / release-policy (pull_request)","description":"Waiting to run","created_at":"2026-10-01T17:24:24Z"},
	{"id":1,"status":"pending","context":"On Pull Request / checks (pull_request)","description":"Waiting to run","created_at":"2026-10-01T17:24:23Z"},
	{"id":2,"status":"pending","context":"On Pull Request / warnings (pull_request)","description":"Blocked by required conditions","created_at":"2026-10-01T17:24:23Z"}
]`

func TestPullRequestCI_ReadsActionsRunsAndJobTimingsFromTheStatusHistory(t *testing.T) {
	var refs []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/repos/webgrip/ploeg/actions/runs":
			ref := r.URL.Query().Get("ref")
			refs = append(refs, ref)
			switch ref {
			case "refs/pull/107/head":
				fmt.Fprint(w, `{"total_count":2,"workflow_runs":[
					{"id":19480,"commit_sha":"bbb","workflow_id":"on_pull_request.yml","status":"cancelled","created":"2026-10-01T17:50:00Z","started":"1970-01-01T00:00:00Z","stopped":"2026-10-01T17:51:00Z"},
					{"id":19477,"commit_sha":"aaa","workflow_id":"on_pull_request.yml","status":"failure","created":"2026-10-01T17:24:22Z","started":"2026-10-01T17:35:43Z","stopped":"2026-10-01T17:39:27Z","repository":{"name":"ignored"}}]}`)
			case "refs/heads/agent/vik-7":
				fmt.Fprint(w, `{"workflow_runs":[
					{"id":19478,"commit_sha":"aaa","workflow_id":"on_push.yml","status":"success","created":"2026-10-01T17:24:21Z","started":"2026-10-01T17:24:30Z","stopped":"2026-10-01T17:25:30Z"},
					{"id":19000,"commit_sha":"zzz","workflow_id":"on_push.yml","status":"success","created":"2026-09-30T10:00:00Z","started":"2026-09-30T10:00:10Z","stopped":"2026-09-30T10:01:00Z"}]}`)
			default:
				t.Errorf("unexpected ref %q", ref)
			}
		case "/api/v1/repos/webgrip/ploeg/commits/aaa/statuses":
			fmt.Fprint(w, statusHistory107)
		case "/api/v1/repos/webgrip/ploeg/commits/bbb/statuses":
			fmt.Fprint(w, `[]`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	got, err := (&Provider{BaseURL: srv.URL}).PullRequestCI(context.Background(), "webgrip/ploeg", 107, "agent/vik-7", []string{"aaa", "bbb"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Source != provider.CISourceActions || got.Truncated || len(got.Runs) != 3 || !reflect.DeepEqual(refs, []string{"refs/pull/107/head", "refs/heads/agent/vik-7"}) {
		t.Fatalf("ci = %+v, refs %v; the branch run on an unknown commit is left out", got, refs)
	}
	cancelled, push, failed := got.Runs[0], got.Runs[1], got.Runs[2]
	if cancelled.ID != "19480" || cancelled.Status != provider.CICancelled || cancelled.StartedAt != nil || len(cancelled.Jobs) != 0 {
		t.Errorf("cancelled run = %+v; Forgejo's zero start time is unknown", cancelled)
	}
	if push.ID != "19478" || push.Workflow != "on_push.yml" || len(push.Jobs) != 0 {
		t.Errorf("push run = %+v; jobs belong to the run that was created before them", push)
	}
	if failed.Status != provider.CIFailure || !failed.StartedAt.Equal(ts("2026-10-01T17:35:43Z")) || !failed.CompletedAt.Equal(ts("2026-10-01T17:39:27Z")) {
		t.Errorf("failed run = %+v", failed)
	}
	q := func(n int64) *int64 { return &n }
	wantJobs := []provider.CIJob{
		{Name: "On Pull Request / checks (pull_request)", Status: provider.CIFailure, StartedAt: tsp("2026-10-01T17:35:43Z"),
			CompletedAt: tsp("2026-10-01T17:39:28Z"), QueuedSeconds: q(680), Attempt: 1},
		{Name: "On Pull Request / warnings (pull_request)", Status: provider.CISuccess, StartedAt: tsp("2026-10-01T17:49:07Z"),
			CompletedAt: tsp("2026-10-01T17:49:07Z"), QueuedSeconds: q(578), Attempt: 1},
		{Name: "On Pull Request / release-policy (pull_request)", Status: provider.CISuccess, StartedAt: tsp("2026-10-01T17:35:52Z"),
			CompletedAt: tsp("2026-10-01T17:36:16Z"), QueuedSeconds: q(688), Attempt: 1},
	}
	if !reflect.DeepEqual(failed.Jobs, wantJobs) {
		t.Errorf("jobs = %+v\nwant %+v", failed.Jobs, wantJobs)
	}
}

func TestPullRequestCI_WithoutTheRunsEndpointReadsStatusesPerHead(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/repos/webgrip/ploeg/commits/aaa/statuses":
			fmt.Fprint(w, `[
				{"id":1,"status":"pending","context":"ci/woodpecker","description":"Pipeline is pending","created_at":"2026-10-01T09:00:00Z"},
				{"id":2,"status":"failure","context":"ci/woodpecker","description":"Pipeline failed","created_at":"2026-10-01T09:05:00Z"},
				{"id":3,"status":"pending","context":"ci/woodpecker","description":"Pipeline is pending","created_at":"2026-10-01T09:10:00Z"},
				{"id":4,"status":"success","context":"ci/woodpecker","description":"Pipeline was successful","created_at":"2026-10-01T09:14:00Z"},
				{"id":5,"status":"warning","context":"lint","description":"","created_at":"2026-10-01T09:01:00Z"}]`)
		case "/api/v1/repos/webgrip/ploeg/commits/bbb/statuses":
			fmt.Fprint(w, `[{"id":6,"status":"pending","context":"ci/woodpecker","description":"","created_at":"2026-10-01T10:00:00Z"}]`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	got, err := (&Provider{BaseURL: srv.URL}).PullRequestCI(context.Background(), "webgrip/ploeg", 7, "", []string{"aaa", "bbb"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Source != provider.CISourceStatuses || len(got.Runs) != 2 {
		t.Fatalf("ci = %+v", got)
	}
	pending, done := got.Runs[0], got.Runs[1]
	if pending.SHA != "bbb" || pending.Status != provider.CIPending || pending.CompletedAt != nil {
		t.Errorf("pending run = %+v", pending)
	}
	if done.ID != "statuses:aaa" || done.Status != provider.CISuccess || !done.CreatedAt.Equal(ts("2026-10-01T09:00:00Z")) ||
		!done.CompletedAt.Equal(ts("2026-10-01T09:14:00Z")) || len(done.Jobs) != 3 {
		t.Fatalf("run = %+v", done)
	}
	first, lint, second := done.Jobs[0], done.Jobs[1], done.Jobs[2]
	if first.Attempt != 1 || first.Status != provider.CIFailure || second.Attempt != 2 || second.Status != provider.CISuccess ||
		!second.StartedAt.Equal(ts("2026-10-01T09:10:00Z")) || second.QueuedSeconds != nil {
		t.Errorf("attempts = %+v, %+v; a pending after a final status is a rerun, and a generic check has no queue", first, second)
	}
	if lint.Name != "lint" || lint.Status != provider.CISuccess || lint.StartedAt != nil {
		t.Errorf("lint = %+v; a lone warning is a finished check without a start", lint)
	}
}

func TestPullRequestCI_WithoutActionsRunsAnExternalCIStillCounts(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/repos/webgrip/ploeg/actions/runs":
			fmt.Fprint(w, `{"total_count":0,"workflow_runs":[]}`)
		case "/api/v1/repos/webgrip/ploeg/commits/aaa/statuses":
			fmt.Fprint(w, `[{"id":1,"status":"pending","context":"ci/drone","created_at":"2026-10-01T09:00:00Z"},
				{"id":2,"status":"success","context":"ci/drone","created_at":"2026-10-01T09:03:00Z"}]`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	got, err := (&Provider{BaseURL: srv.URL}).PullRequestCI(context.Background(), "webgrip/ploeg", 7, "agent/x", []string{"aaa"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Source != provider.CISourceStatuses || len(got.Runs) != 1 || got.Runs[0].Status != provider.CISuccess || len(got.Runs[0].Jobs) != 1 {
		t.Fatalf("ci = %+v", got)
	}
}

func TestPullRequestCI_ForgejoRerunInPlaceIsASecondAttempt(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/repos/webgrip/ploeg/actions/runs":
			fmt.Fprint(w, `{"workflow_runs":[{"id":5,"commit_sha":"aaa","workflow_id":"ci.yml","status":"success","created":"2026-10-01T09:00:00Z","started":"2026-10-01T09:20:00Z","stopped":"2026-10-01T09:25:00Z"}]}`)
		case "/api/v1/repos/webgrip/ploeg/commits/aaa/statuses":
			fmt.Fprint(w, `[
				{"id":1,"status":"pending","context":"CI / test (pull_request)","description":"Waiting to run","created_at":"2026-10-01T09:00:01Z"},
				{"id":2,"status":"pending","context":"CI / test (pull_request)","description":"Has started running","created_at":"2026-10-01T09:01:00Z"},
				{"id":3,"status":"failure","context":"CI / test (pull_request)","description":"Failing after 2m","created_at":"2026-10-01T09:03:00Z"},
				{"id":4,"status":"pending","context":"CI / test (pull_request)","description":"Waiting to run","created_at":"2026-10-01T09:19:00Z"},
				{"id":5,"status":"pending","context":"CI / test (pull_request)","description":"Has started running","created_at":"2026-10-01T09:20:00Z"},
				{"id":6,"status":"success","context":"CI / test (pull_request)","description":"Successful in 5m","created_at":"2026-10-01T09:25:00Z"}]`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	got, err := (&Provider{BaseURL: srv.URL}).PullRequestCI(context.Background(), "webgrip/ploeg", 7, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	jobs := got.Runs[0].Jobs
	if len(jobs) != 2 || jobs[0].Attempt != 1 || jobs[1].Attempt != 2 || *jobs[0].QueuedSeconds != 59 || *jobs[1].QueuedSeconds != 60 {
		t.Fatalf("jobs = %+v", jobs)
	}
}

func TestPullRequestDiff_ReadsAndCutsAtALine(t *testing.T) {
	diff := "diff --git a/a.go b/a.go\n--- a/a.go\n+++ b/a.go\n@@ -1 +1,2 @@\n+\tone\n+\ttwo\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/repos/webgrip/ploeg/pulls/7.diff" {
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, diff)
	}))
	defer srv.Close()
	p := &Provider{BaseURL: srv.URL}
	got, truncated, err := p.PullRequestDiff(context.Background(), "webgrip/ploeg", 7, provider.MaxDiffBytes)
	if err != nil || truncated || string(got) != diff {
		t.Fatalf("diff = %q, %v, %v", got, truncated, err)
	}
	got, truncated, err = p.PullRequestDiff(context.Background(), "webgrip/ploeg", 7, len(diff)-3)
	if err != nil || !truncated || string(got) != strings.TrimSuffix(diff, "+\ttwo\n") {
		t.Fatalf("cut diff = %q, %v, %v", got, truncated, err)
	}
	if _, _, err := p.PullRequestDiff(context.Background(), "webgrip/ploeg", 8, 100); err == nil {
		t.Error("a missing pull request read as an empty diff")
	}
}

func TestPullRequestFacts_ReadsOpeningAuthorAndDraft(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"state":"open","head":{"sha":"h"},"created_at":"2026-10-01T17:24:16Z","draft":true,"user":{"login":"ploeg-bot"}}`)
	}))
	defer srv.Close()
	got, err := (&Provider{BaseURL: srv.URL}).PullRequestFacts(context.Background(), "webgrip/ploeg", 7)
	if err != nil {
		t.Fatal(err)
	}
	if got.OpenedAt == nil || !got.OpenedAt.Equal(ts("2026-10-01T17:24:16Z")) || got.Author != "ploeg-bot" || got.Draft == nil || !*got.Draft {
		t.Fatalf("facts = %+v", got)
	}
}
