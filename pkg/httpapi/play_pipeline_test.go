package httpapi

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/provider"
	"github.com/ploeg-hq/ploeg/pkg/provider/forgejo"
)

type pipelineForge struct {
	mu            sync.Mutex
	merged        bool
	failActivity  bool
	timelineReads int
	diffReads     int
}

func (f *pipelineForge) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	base := "/api/v1/repos/webgrip/ploeg"
	switch r.URL.Path {
	case base + "/pulls/18":
		if f.merged {
			fmt.Fprint(w, `{"state":"closed","merged":true,"head":{"sha":"bbb"},"merge_commit_sha":"m18","merged_at":"2026-10-01T12:00:00Z",
				"closed_at":"2026-10-01T12:00:00Z","merged_by":{"login":"anna"},"additions":130,"deletions":20,"changed_files":4,
				"created_at":"2026-10-01T09:00:00Z","draft":false,"user":{"login":"ploeg-bot"},"title":"add the report","labels":[]}`)
			return
		}
		fmt.Fprint(w, `{"state":"open","merged":false,"head":{"sha":"bbb"},"additions":130,"deletions":20,"changed_files":4,
			"created_at":"2026-10-01T09:00:00Z","draft":false,"user":{"login":"ploeg-bot"},"title":"add the report","labels":[]}`)
	case base + "/commits/bbb/status":
		fmt.Fprint(w, `{"state":"success","sha":"bbb","total_count":1,"statuses":[{"context":"CI / test (pull_request)","status":"success"}]}`)
	case base + "/pulls/18/files":
		fmt.Fprint(w, `[{"filename":"pkg/report.go","additions":90,"deletions":10},{"filename":"pkg/report_test.go","additions":30,"deletions":0},
			{"filename":"docs/report.md","additions":5,"deletions":0},{"filename":"go.sum","additions":5,"deletions":10}]`)
	case base + "/pulls/18/commits":
		fmt.Fprint(w, `[{"commit":{"message":"add the report","author":{"date":"2026-10-01T08:00:00Z"}}},
			{"commit":{"message":"fix review","author":{"date":"2026-10-01T10:30:00Z"}}}]`)
	case base + "/issues/18/timeline":
		f.timelineReads++
		if f.failActivity {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		fmt.Fprint(w, `[
			{"type":"pull_push","user":{"login":"ploeg-bot"},"created_at":"2026-10-01T09:00:01Z","body":"{\"is_force_push\":false,\"commit_ids\":[\"aaa\"]}"},
			{"type":"comment","user":{"login":"ploeg-bot"},"created_at":"2026-10-01T09:05:00Z","body":"usage report"},
			{"type":"code","user":{"login":"anna"},"created_at":"2026-10-01T10:00:00Z","body":"rename this"},
			{"type":"pull_push","user":{"login":"ploeg-bot"},"created_at":"2026-10-01T10:30:00Z","body":"{\"is_force_push\":false,\"commit_ids\":[\"bbb\"]}"}
		]`)
	case base + "/pulls/18/reviews":
		fmt.Fprint(w, `[{"state":"REQUEST_CHANGES","submitted_at":"2026-10-01T10:00:00Z","commit_id":"aaa","user":{"login":"anna"}},
			{"state":"APPROVED","submitted_at":"2026-10-01T11:00:00Z","commit_id":"bbb","user":{"login":"anna"}}]`)
	case base + "/actions/runs":
		if r.URL.Query().Get("ref") != "refs/pull/18/head" {
			fmt.Fprint(w, `{"workflow_runs":[]}`)
			return
		}
		fmt.Fprint(w, `{"workflow_runs":[
			{"id":2,"commit_sha":"bbb","workflow_id":"ci.yml","status":"success","created":"2026-10-01T10:30:05Z","started":"2026-10-01T10:31:00Z","stopped":"2026-10-01T10:36:00Z"},
			{"id":1,"commit_sha":"aaa","workflow_id":"ci.yml","status":"failure","created":"2026-10-01T09:00:05Z","started":"2026-10-01T09:01:00Z","stopped":"2026-10-01T09:04:00Z"}]}`)
	case base + "/commits/aaa/statuses":
		fmt.Fprint(w, `[
			{"id":1,"status":"pending","context":"CI / test (pull_request)","description":"Waiting to run","created_at":"2026-10-01T09:00:06Z"},
			{"id":2,"status":"pending","context":"CI / test (pull_request)","description":"Has started running","created_at":"2026-10-01T09:01:00Z"},
			{"id":3,"status":"failure","context":"CI / test (pull_request)","description":"Failing after 3m","created_at":"2026-10-01T09:04:00Z"}]`)
	case base + "/commits/bbb/statuses":
		fmt.Fprint(w, `[
			{"id":4,"status":"pending","context":"CI / test (pull_request)","description":"Waiting to run","created_at":"2026-10-01T10:30:06Z"},
			{"id":5,"status":"pending","context":"CI / test (pull_request)","description":"Has started running","created_at":"2026-10-01T10:31:00Z"},
			{"id":6,"status":"success","context":"CI / test (pull_request)","description":"Successful in 5m","created_at":"2026-10-01T10:36:00Z"}]`)
	case base + "/pulls/18.diff":
		f.diffReads++
		fmt.Fprint(w, "diff --git a/pkg/report.go b/pkg/report.go\n--- a/pkg/report.go\n+++ b/pkg/report.go\n@@ -1,2 +1,4 @@\n func f() {\n+\tif ok {\n+\t\treturn\n+\t}\n }\n"+
			"diff --git a/go.sum b/go.sum\n--- a/go.sum\n+++ b/go.sum\n@@ -1 +1 @@\n+\t\t\t\t\tdeep\n")
	default:
		http.NotFound(w, r)
	}
}

func pipelineMerge() map[string]any {
	event := factsMerge()
	pr := event["pull_request"].(map[string]any)
	pr["head"] = map[string]any{"ref": factsBranch, "sha": "bbb"}
	pr["merged_at"], pr["closed_at"] = "2026-10-01T12:00:00Z", "2026-10-01T12:00:00Z"
	pr["merged_by"] = map[string]any{"login": "anna"}
	return event
}

func pipelineServer(t *testing.T, forge *pipelineForge) (*Server, string) {
	t.Helper()
	reset(t)
	srv := httptest.NewServer(forge)
	t.Cleanup(srv.Close)
	consumers, token := operatorTestConsumers(t, []string{"silver"}, false)
	s := &Server{
		Store: testStore, Log: slog.New(slog.DiscardHandler),
		Forges: map[string]provider.ForgeProvider{"forgejo": &forgejo.Provider{BaseURL: srv.URL, Secret: "shh",
			Log: slog.New(slog.DiscardHandler)}},
		ForgeBots:      []string{"ploeg-bot"},
		OperatorConfig: OperatorConfig{Consumers: consumers, Teams: map[string][]string{"silver": {"builder"}}},
	}
	t.Cleanup(s.pipelineWork.Wait)
	return s, token
}

type pipelinePlay struct {
	Commits            *int       `json:"commits"`
	ForcePushes        *int       `json:"forcePushes"`
	ActivityCapturedAt *time.Time `json:"activityCapturedAt"`
	CIRunsSource       *string    `json:"ciRunsSource"`
	Events             []struct {
		Kind  string `json:"kind"`
		Actor string `json:"actor"`
	} `json:"events"`
	Reviews []struct {
		Reviewer string `json:"reviewer"`
	} `json:"reviews"`
	CIRuns []struct {
		Status string `json:"status"`
		Jobs   []struct {
			Name string `json:"name"`
		} `json:"jobs"`
	} `json:"ciRuns"`
	Files []struct {
		Path        string `json:"path"`
		Indentation *struct {
			Added int `json:"added"`
		} `json:"indentation"`
	} `json:"files"`
}

func pipelinePlays(t *testing.T, s *Server, token string, item int64) []pipelinePlay {
	t.Helper()
	raw := operatorSchemaGET(t, s, token, fmt.Sprintf("work-items/%d/facts", item))
	var body struct {
		Facts struct {
			PullRequests []pipelinePlay `json:"pullRequests"`
		} `json:"facts"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Facts.PullRequests) != 1 {
		t.Fatalf("pull requests = %s", raw)
	}
	return body.Facts.PullRequests
}

func TestForgeWebhook_MergeCapturesTheActivityCIRunsAndFileIndentation(t *testing.T) {
	forge := &pipelineForge{merged: true}
	s, token := pipelineServer(t, forge)
	item, _ := factsItem(t)
	if code := forgePostEvent(t, s.Handler(), "shh", "pull_request", "pipeline-merge", pipelineMerge()); code != http.StatusAccepted {
		t.Fatalf("webhook returned %d", code)
	}
	s.pipelineWork.Wait()

	p := pipelinePlays(t, s, token, item)[0]
	if len(p.Events) != 6 || p.Commits == nil || *p.Commits != 2 || p.ForcePushes == nil || *p.ForcePushes != 0 || p.ActivityCapturedAt == nil {
		t.Fatalf("activity = %+v", p)
	}
	if len(p.CIRuns) != 2 || p.CIRunsSource == nil || *p.CIRunsSource != "actions" || len(p.CIRuns[0].Jobs) != 1 {
		t.Fatalf("ci runs = %+v", p.CIRuns)
	}
	indented := map[string]int{}
	for _, f := range p.Files {
		if f.Indentation != nil {
			indented[f.Path] = f.Indentation.Added
		}
	}
	if len(p.Files) != 4 || len(indented) != 2 || indented["pkg/report.go"] != 4 {
		t.Fatalf("files = %+v; each file the diff shows keeps its indentation", p.Files)
	}
	if forge.diffReads != 1 {
		t.Errorf("diff read %d times; it is read once at the merge", forge.diffReads)
	}
}

func TestForgeWebhook_ActivityIsReadAtMostOncePerWindowAndReviewsAreKeptAtOnce(t *testing.T) {
	forge := &pipelineForge{}
	s, token := pipelineServer(t, forge)
	item, _ := factsItem(t)
	h := s.Handler()
	for i, head := range []string{"aaa", "bbb"} {
		if code := forgePostEvent(t, h, "shh", "pull_request", fmt.Sprintf("pipeline-sync-%d", i), pullRequestEvent("synchronized", head)); code != http.StatusAccepted {
			t.Fatalf("webhook returned %d", code)
		}
		s.pipelineWork.Wait()
	}
	if forge.timelineReads != 1 {
		t.Fatalf("timeline read %d times; open pull requests are read at most once per %s", forge.timelineReads, pipelineRecapture)
	}
	if code := forgePostEvent(t, h, "shh", "pull_request", "pipeline-review", factsReview("approved", "bob", factsBranch)); code != http.StatusAccepted {
		t.Fatalf("webhook returned %d", code)
	}
	s.pipelineWork.Wait()
	p := pipelinePlays(t, s, token, item)[0]
	if len(p.Reviews) == 0 || p.Reviews[len(p.Reviews)-1].Reviewer != "bob" || len(p.CIRuns) == 0 || len(p.Events) == 0 {
		t.Fatalf("play = %+v; a webhook review joins the stored activity without a new read", p)
	}
	for _, f := range p.Files {
		if f.Indentation != nil {
			t.Fatalf("files = %+v; an open pull request is not measured", p.Files)
		}
	}
	if forge.timelineReads != 1 || forge.diffReads != 0 {
		t.Errorf("timeline reads = %d, diff reads = %d", forge.timelineReads, forge.diffReads)
	}
}

func TestForgeWebhook_AFailedActivityReadKeepsTheWebhookReviews(t *testing.T) {
	forge := &pipelineForge{failActivity: true}
	s, token := pipelineServer(t, forge)
	item, _ := factsItem(t)
	h := s.Handler()
	if code := forgePostEvent(t, h, "shh", "pull_request", "pipeline-fail-review", factsReview("approved", "anna", factsBranch)); code != http.StatusAccepted {
		t.Fatalf("webhook returned %d", code)
	}
	s.pipelineWork.Wait()
	p := pipelinePlays(t, s, token, item)[0]
	if len(p.Reviews) != 1 || p.ActivityCapturedAt != nil || len(p.Events) != 0 {
		t.Fatalf("play = %+v; without an activity read only the webhook review is known", p)
	}
	if len(p.CIRuns) != 2 {
		t.Errorf("ci runs = %+v; a failed activity read does not stop the CI read", p.CIRuns)
	}
}
