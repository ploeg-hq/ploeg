package worker

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
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

type capturingAdapter struct {
	limits *[2]time.Duration
	prompt *string
}

func (capturingAdapter) Name() string     { return "exec" }
func (capturingAdapter) ExpectsLLM() bool { return false }
func (a capturingAdapter) Run(_ context.Context, _ harness.TaskSpec, env harness.RunEnv) (harness.OutcomeReport, error) {
	*a.prompt = env.Prompt
	return harness.OutcomeReport{Outcome: work.OutcomeNoChangeNeeded, Summary: "nothing to change"}, nil
}

type limitingAdapter struct{ capturingAdapter }

func (a limitingAdapter) PromptLimits() (time.Duration, time.Duration) {
	return a.limits[0], a.limits[1]
}

func TestTheConfiguredLimitsReachTheRunsPrompt(t *testing.T) {
	for _, tc := range []struct {
		name    string
		limiter bool
		want    []string
	}{
		{name: "the worker's watchdogs", want: []string{
			"Ploeg stops this Run 100 minutes after your prompt starts, or after 15 minutes without activity",
			"Model budget: US$ 1.50 for this Run.",
			"Round 2 of at most 4 in this Shift.",
		}},
		{name: "an adapter's own shorter limits", limiter: true, want: []string{
			"Ploeg stops this Run 45 minutes after your prompt starts, or after 10 minutes without activity",
			"Model budget: US$ 1.50 for this Run.",
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/v1/claim" {
					_ = json.NewEncoder(w).Encode(ClaimResponse{RunToken: "rt", Deadline: time.Now().Add(time.Minute),
						WorkItem: work.WorkItem{ID: "1", ExternalID: "7", Title: "t"}, Role: "reviewer", PreAuthor: true,
						Round: 2, MaxRounds: 4, Authorized: 1.5})
					return
				}
				w.WriteHeader(http.StatusNoContent)
			}))
			defer srv.Close()
			cfg := Config{Team: "bronze", Role: "reviewer", WorkDir: t.TempDir(), KeyBudget: 9,
				HarnessTimeout: 100 * time.Minute, HarnessIdleTimeout: 15 * time.Minute,
				APIURL: srv.URL, ForgeURL: gitForge(t, map[string]string{"README.md": "# example\n"}),
				DefaultForge: harness.ForgeForgejo, BuilderToken: "tok", ForgeTokenAccess: ForgeTokenReadOnly,
				RepoOwner: "webgrip", RepoName: "example", BaseBranch: "development"}
			var prompt string
			capture := capturingAdapter{limits: &[2]time.Duration{45 * time.Minute, 10 * time.Minute}, prompt: &prompt}
			var adapter harness.Adapter = capture
			if tc.limiter {
				adapter = limitingAdapter{capture}
			}
			if err := New(cfg, adapter, llmbroker.Static{Key: "sk-test"}, discardLog()).RunContext(context.Background()); err != nil {
				t.Fatalf("RunContext: %v", err)
			}
			if !strings.Contains(prompt, "## Your Run\n") {
				t.Fatalf("prompt has no Your Run section:\n%s", prompt)
			}
			for _, want := range tc.want {
				if !strings.Contains(prompt, want) {
					t.Errorf("prompt lacks %q:\n%s", want, prompt)
				}
			}
			if strings.Contains(prompt, "Orient for at most") {
				t.Errorf("a reading Run was given the orientation budget:\n%s", prompt)
			}
		})
	}
}
