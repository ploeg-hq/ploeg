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

type identifyingAdapter struct{ ran bool }

func (a *identifyingAdapter) Name() string     { return "acp" }
func (a *identifyingAdapter) Profile() string  { return "opencode" }
func (a *identifyingAdapter) ExpectsLLM() bool { return false }
func (a *identifyingAdapter) Run(_ context.Context, _ harness.TaskSpec, env harness.RunEnv) (harness.OutcomeReport, error) {
	a.ran = true
	if env.AgentInfo != nil {
		env.AgentInfo(harness.HarnessAgent{Name: "opencode", Version: "1.18.30"})
		env.AgentInfo(harness.HarnessAgent{Name: "later", Version: "2"})
	}
	return harness.OutcomeReport{Outcome: work.OutcomeNoChangeNeeded, Summary: "nothing to change",
		Harness: &harness.RunHarness{Name: "forged", Version: "9.9.9", Agent: &harness.HarnessAgent{Name: "forged"}}}, nil
}

type harnessAPI struct {
	mu      sync.Mutex
	claim   map[string]string
	outcome harness.OutcomeReport
}

func (a *harnessAPI) server(t *testing.T) string {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a.mu.Lock()
		defer a.mu.Unlock()
		switch {
		case r.URL.Path == "/api/v1/claim":
			_ = json.NewDecoder(r.Body).Decode(&a.claim)
			_ = json.NewEncoder(w).Encode(ClaimResponse{RunToken: "rt", Deadline: time.Now().Add(time.Minute),
				WorkItem: work.WorkItem{ID: "1", ExternalID: "7", Title: "t"}, Role: "reviewer", PreAuthor: true})
		case r.URL.Path == "/api/v1/runs/rt/outcome":
			if err := json.NewDecoder(r.Body).Decode(&a.outcome); err != nil {
				t.Errorf("outcome body: %v", err)
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func TestTheWorkerReportsItsOwnHarnessOnTheClaimAndTheOutcome(t *testing.T) {
	api := &harnessAPI{}
	adapter := &identifyingAdapter{}
	cfg := Config{Team: "bronze", Role: "reviewer", HarnessVersion: "opencode 1.18.30", WorkDir: t.TempDir(),
		APIURL: api.server(t), ForgeURL: gitForge(t, map[string]string{"README.md": "# example\n"}),
		DefaultForge: harness.ForgeForgejo, BuilderToken: "tok", ForgeTokenAccess: ForgeTokenReadOnly,
		RepoOwner: "webgrip", RepoName: "example", BaseBranch: "development"}
	w := New(cfg, adapter, llmbroker.Static{Key: "sk-test"}, discardLog())
	if err := w.RunContext(context.Background()); err != nil {
		t.Fatalf("RunContext: %v", err)
	}
	api.mu.Lock()
	defer api.mu.Unlock()
	if !adapter.ran {
		t.Fatalf("the adapter did not run; outcome %+v", api.outcome)
	}
	if api.claim["harness"] != "acp" || api.claim["harnessProfile"] != "opencode" || api.claim["harnessVersion"] != "opencode 1.18.30" {
		t.Fatalf("claim body = %v", api.claim)
	}
	h := api.outcome.Harness
	if h == nil || h.Name != "acp" || h.Profile != "opencode" || h.Version != "opencode 1.18.30" ||
		h.Agent == nil || *h.Agent != (harness.HarnessAgent{Name: "opencode", Version: "1.18.30"}) {
		t.Fatalf("outcome harness = %+v, want the worker's own", h)
	}
}

func TestAnUnknownHarnessVersionIsLeftOutOfTheClaimAndTheOutcome(t *testing.T) {
	var claim map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&claim)
		rw.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	w := New(Config{APIURL: srv.URL}, &identifyingAdapter{}, nil, discardLog())
	if _, err := w.API.Claim("bronze", ""); err != nil {
		t.Fatal(err)
	}
	if _, ok := claim["harnessVersion"]; ok {
		t.Fatalf("claim body = %v, want no harnessVersion", claim)
	}
	if h := w.runHarness(); h == nil || h.Version != "" || h.Agent != nil || h.Name != "acp" {
		t.Fatalf("run harness = %+v", h)
	}
	if h := (&Worker{API: &APIClient{}}).runHarness(); h != nil {
		t.Fatalf("a worker that knows nothing reported %+v", h)
	}
}
