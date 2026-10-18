package worker

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/harness"
	"github.com/ploeg-hq/ploeg/pkg/llmbroker"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

type promptingAdapter struct{}

func (promptingAdapter) Name() string     { return "acp" }
func (promptingAdapter) ExpectsLLM() bool { return false }
func (promptingAdapter) Run(ctx context.Context, _ harness.TaskSpec, env harness.RunEnv) (harness.OutcomeReport, error) {
	harness.ReportPromptStart(ctx, env, 45*time.Minute, 10*time.Minute)
	return harness.OutcomeReport{Outcome: work.OutcomeNoChangeNeeded, Summary: "nothing to change"}, nil
}

func TestTheWorkerReportsThePromptStartAndTheLimitThatStopsItFirst(t *testing.T) {
	var mu sync.Mutex
	var checkpoints []work.Checkpoint
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch r.URL.Path {
		case "/api/v1/claim":
			_ = json.NewEncoder(w).Encode(ClaimResponse{RunToken: "rt", Deadline: time.Now().Add(time.Minute),
				WorkItem: work.WorkItem{ID: "1", ExternalID: "7", Title: "t"}, Role: "reviewer", PreAuthor: true})
		case "/api/v1/runs/rt/checkpoint":
			var cp work.Checkpoint
			if err := json.NewDecoder(r.Body).Decode(&cp); err != nil {
				t.Errorf("checkpoint body: %v", err)
			}
			checkpoints = append(checkpoints, cp)
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	defer srv.Close()
	cfg := Config{Team: "bronze", Role: "reviewer", WorkDir: t.TempDir(), HarnessTimeout: 30 * time.Minute,
		APIURL: srv.URL, ForgeURL: gitForge(t, map[string]string{"README.md": "# example\n"}),
		DefaultForge: harness.ForgeForgejo, BuilderToken: "tok", ForgeTokenAccess: ForgeTokenReadOnly,
		RepoOwner: "webgrip", RepoName: "example", BaseBranch: "development"}
	before := time.Now().UTC().Truncate(time.Second)
	if err := New(cfg, promptingAdapter{}, llmbroker.Static{Key: "sk-test"}, discardLog()).RunContext(context.Background()); err != nil {
		t.Fatalf("RunContext: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	var started []work.Checkpoint
	for _, cp := range checkpoints {
		if cp.Phase == work.PhasePromptStarted {
			started = append(started, cp)
		}
	}
	if len(started) != 1 || started[0].Limits == nil {
		t.Fatalf("checkpoints = %+v, want one prompt_started with limits", checkpoints)
	}
	l := started[0].Limits
	if l.PromptSeconds > 1800 || l.PromptSeconds < 1790 || l.IdleSeconds != 600 {
		t.Fatalf("limits = %+v, want the 30m harness wall before the 45m prompt timeout, and 600 s idle", l)
	}
	if l.PromptStartedAt.Before(before) || l.PromptStartedAt.After(time.Now()) || l.PromptStartedAt.Nanosecond() != 0 {
		t.Fatalf("prompt started at %s, want a whole second during the Run", l.PromptStartedAt)
	}
}
