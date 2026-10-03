package gitlab

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

const mrBase = "/api/v4/projects/group%2Fapp/merge_requests/4"

func TestPullRequestActivity_ReadsNotesVersionsAndCommits(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("PRIVATE-TOKEN") != "tok" {
			t.Errorf("read without the token: %s", r.URL)
		}
		switch r.URL.EscapedPath() {
		case mrBase + "/notes":
			if r.URL.Query().Get("sort") != "asc" {
				t.Errorf("notes not read oldest first: %s", r.URL.RawQuery)
			}
			fmt.Fprint(w, `[
				{"body":"marked this merge request as **ready**","created_at":"2026-10-01T09:10:00.000Z","system":true,"author":{"username":"bot"}},
				{"body":"added 1 commit\n\n* abc - x","created_at":"2026-10-01T09:11:00.000Z","system":true,"author":{"username":"bot"}},
				{"body":"please rename","created_at":"2026-10-01T09:20:00.000Z","system":false,"type":"DiffNote","author":{"username":"anna"}},
				{"body":"looks good overall","created_at":"2026-10-01T09:25:00.000Z","system":false,"type":null,"author":{"username":"bob"}},
				{"body":"requested changes","created_at":"2026-10-01T09:26:00.000Z","system":true,"author":{"username":"anna"}},
				{"body":"approved this merge request","created_at":"2026-10-01T10:00:00.000Z","system":true,"author":{"username":"anna"}},
				{"body":"marked this merge request as **draft**","created_at":"2026-10-01T10:05:00.000Z","system":true,"author":{"username":"bot"}},
				{"body":"changed the description","created_at":"2026-10-01T10:06:00.000Z","system":true,"author":{"username":"bot"}}
			]`)
		case mrBase + "/versions":
			fmt.Fprint(w, `[{"head_commit_sha":"bbb","created_at":"2026-10-01T09:40:00.000Z"},{"head_commit_sha":"aaa","created_at":"2026-10-01T09:00:00.000Z"}]`)
		case mrBase + "/commits":
			fmt.Fprint(w, `[{"authored_date":"2026-10-01T08:00:00.000+02:00"},{"authored_date":"2026-10-01T07:00:00.000Z"}]`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	got, err := (&Provider{BaseURL: srv.URL, Token: "tok"}).PullRequestActivity(context.Background(), "group/app", 4)
	if err != nil {
		t.Fatal(err)
	}
	want := []provider.ActivityEvent{
		{Kind: provider.ActivityPush, At: ts("2026-10-01T09:00:00Z"), HeadSHA: "aaa"},
		{Kind: provider.ActivityReady, Actor: "bot", At: ts("2026-10-01T09:10:00Z")},
		{Kind: provider.ActivityReviewComment, Actor: "anna", At: ts("2026-10-01T09:20:00Z")},
		{Kind: provider.ActivityComment, Actor: "bob", At: ts("2026-10-01T09:25:00Z")},
		{Kind: provider.ActivityReview, Actor: "anna", At: ts("2026-10-01T09:26:00Z"), State: provider.ForgeReviewChangesRequested},
		{Kind: provider.ActivityPush, At: ts("2026-10-01T09:40:00Z"), HeadSHA: "bbb"},
		{Kind: provider.ActivityReview, Actor: "anna", At: ts("2026-10-01T10:00:00Z"), State: provider.ForgeReviewApproved},
		{Kind: provider.ActivityDraft, Actor: "bot", At: ts("2026-10-01T10:05:00Z")},
	}
	if !reflect.DeepEqual(got.Events, want) {
		t.Fatalf("events = %+v\nwant %+v", got.Events, want)
	}
	if got.Commits != 2 || !got.FirstCommitAt.Equal(ts("2026-10-01T06:00:00Z")) || got.ForcePushesKnown || got.EventsTruncated {
		t.Errorf("activity = %+v; GitLab reports no force pushes", got)
	}
}

func TestPullRequestCI_ReadsPipelinesAndRetriedJobs(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.EscapedPath() {
		case mrBase + "/pipelines":
			fmt.Fprint(w, `[{"id":11,"sha":"bbb","status":"success","created_at":"2026-10-01T10:00:00.000Z"},
				{"id":10,"sha":"aaa","status":"failed","created_at":"2026-10-01T09:00:00.000Z"}]`)
		case "/api/v4/projects/group%2Fapp/pipelines/11/jobs":
			if r.URL.Query().Get("include_retried") != "true" {
				t.Errorf("jobs read without retried attempts: %s", r.URL.RawQuery)
			}
			fmt.Fprint(w, `[
				{"id":103,"name":"test","status":"success","started_at":"2026-10-01T10:06:00.000Z","finished_at":"2026-10-01T10:10:00.000Z","queued_duration":12.7},
				{"id":101,"name":"test","status":"failed","started_at":"2026-10-01T10:01:00.000Z","finished_at":"2026-10-01T10:03:00.000Z","queued_duration":3},
				{"id":102,"name":"build","status":"success","started_at":"2026-10-01T10:01:30.000Z","finished_at":"2026-10-01T10:05:00.000Z"},
				{"id":104,"name":"deploy","status":"manual"}]`)
		case "/api/v4/projects/group%2Fapp/pipelines/10/jobs":
			fmt.Fprint(w, `[{"id":90,"name":"test","status":"failed","started_at":"2026-10-01T09:01:00.000Z","finished_at":"2026-10-01T09:02:00.000Z"}]`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	got, err := (&Provider{BaseURL: srv.URL}).PullRequestCI(context.Background(), "group/app", 4, "ignored", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.Source != provider.CISourcePipelines || got.Truncated || len(got.Runs) != 2 {
		t.Fatalf("ci = %+v", got)
	}
	green := got.Runs[0]
	if green.ID != "11" || green.Status != provider.CISuccess || !green.StartedAt.Equal(ts("2026-10-01T10:01:30Z")) ||
		!green.CompletedAt.Equal(ts("2026-10-01T10:10:00Z")) || len(green.Jobs) != 4 {
		t.Fatalf("run = %+v; the latest attempts time the run", green)
	}
	q := func(n int64) *int64 { return &n }
	want := []provider.CIJob{
		{Name: "test", Status: provider.CIFailure, StartedAt: tsp("2026-10-01T10:01:00Z"), CompletedAt: tsp("2026-10-01T10:03:00Z"), QueuedSeconds: q(3), Attempt: 1},
		{Name: "build", Status: provider.CISuccess, StartedAt: tsp("2026-10-01T10:01:30Z"), CompletedAt: tsp("2026-10-01T10:05:00Z"), Attempt: 1},
		{Name: "test", Status: provider.CISuccess, StartedAt: tsp("2026-10-01T10:06:00Z"), CompletedAt: tsp("2026-10-01T10:10:00Z"), QueuedSeconds: q(12), Attempt: 2},
		{Name: "deploy", Status: provider.CISkipped, Attempt: 1},
	}
	if !reflect.DeepEqual(green.Jobs, want) {
		t.Errorf("jobs = %+v\nwant %+v", green.Jobs, want)
	}
	if red := got.Runs[1]; red.Status != provider.CIFailure || red.SHA != "aaa" || len(red.Jobs) != 1 {
		t.Errorf("failed pipeline = %+v", red)
	}
}

func TestPullRequestCI_ReadsJobsOfTheNewestPipelinesOnly(t *testing.T) {
	var jobReads int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/jobs") {
			jobReads++
			fmt.Fprint(w, `[]`)
			return
		}
		items := make([]string, 12)
		for i := range items {
			items[i] = fmt.Sprintf(`{"id":%d,"sha":"s%d","status":"success","created_at":"2026-10-01T09:00:00Z"}`, 100-i, i)
		}
		fmt.Fprint(w, "["+strings.Join(items, ",")+"]")
	}))
	defer srv.Close()
	got, err := (&Provider{BaseURL: srv.URL}).PullRequestCI(context.Background(), "group/app", 4, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if jobReads != provider.MaxCIRunsWithJobs || len(got.Runs) != 12 || !got.Truncated {
		t.Fatalf("job reads = %d, runs = %d, truncated = %v", jobReads, len(got.Runs), got.Truncated)
	}
}

func TestPullRequestDiff_AssemblesFileDiffsAndMarksSkippedFiles(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.EscapedPath() != mrBase+"/diffs" {
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, `[{"new_path":"a.go","old_path":"a.go","diff":"@@ -1 +1,2 @@\n+\tone\n+--- not a header"},
			{"new_path":"big.sql","old_path":"big.sql","diff":"","too_large":true}]`)
	}))
	defer srv.Close()
	p := &Provider{BaseURL: srv.URL}
	got, truncated, err := p.PullRequestDiff(context.Background(), "group/app", 4, provider.MaxDiffBytes)
	want := "diff --git a/a.go b/a.go\n--- a/a.go\n+++ b/a.go\n@@ -1 +1,2 @@\n+\tone\n+--- not a header\n"
	if err != nil || !truncated || string(got) != want {
		t.Fatalf("diff = %q, %v, %v; a too-large file makes the diff truncated", got, truncated, err)
	}
	cut, truncated, err := p.PullRequestDiff(context.Background(), "group/app", 4, len(want)-5)
	if err != nil || !truncated || !strings.HasSuffix(string(cut), "+\tone\n") {
		t.Fatalf("cut diff = %q, %v, %v", cut, truncated, err)
	}
}

func TestPullRequestFacts_ReadsOpeningAuthorAndDraft(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"state":"opened","sha":"h","created_at":"2026-10-01T09:00:00.000Z","work_in_progress":true,"author":{"username":"bot"}}`)
	}))
	defer srv.Close()
	got, err := (&Provider{BaseURL: srv.URL}).PullRequestFacts(context.Background(), "group/app", 4)
	if err != nil {
		t.Fatal(err)
	}
	if got.OpenedAt == nil || !got.OpenedAt.Equal(ts("2026-10-01T09:00:00Z")) || got.Author != "bot" || got.Draft == nil || !*got.Draft {
		t.Fatalf("facts = %+v; work_in_progress stands in for draft on older GitLab", got)
	}
}
