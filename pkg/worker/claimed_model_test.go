package worker

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/harness"
	"github.com/ploeg-hq/ploeg/pkg/harness/adapters/claudecode"
	"github.com/ploeg-hq/ploeg/pkg/llmbroker"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

type modelAdapter struct{ env harness.RunEnv }

func (a *modelAdapter) Name() string     { return "openhands" }
func (a *modelAdapter) ExpectsLLM() bool { return true }
func (a *modelAdapter) Run(_ context.Context, _ harness.TaskSpec, env harness.RunEnv) (harness.OutcomeReport, error) {
	a.env = env
	return harness.OutcomeReport{Outcome: work.OutcomeNoChangeNeeded, Summary: "nothing to change"}, nil
}

type scopeBroker struct {
	mu     sync.Mutex
	models []string
}

func (b *scopeBroker) Mint(_ context.Context, req llmbroker.MintRequest) (llmbroker.Credential, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.models = req.Models
	return llmbroker.Credential{APIKey: "sk-run", Alias: "ploeg-rt"}, nil
}
func (b *scopeBroker) Revoke(context.Context, llmbroker.Credential) error { return nil }

func modelAPI(t *testing.T, model string) string {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/claim" {
			_ = json.NewEncoder(w).Encode(ClaimResponse{RunToken: "rt", Deadline: time.Now().Add(time.Minute),
				WorkItem: work.WorkItem{ID: "1", ExternalID: "7", Title: "t"}, Role: "reviewer", PreAuthor: true, Model: model})
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func TestTheClaimsModelReachesTheHarnessAndTheKey(t *testing.T) {
	for _, tc := range []struct{ claimed, wantEnv, wantScope string }{
		{"kimi-k3", "litellm_proxy/kimi-k3", "kimi-k3"},
		{"", "litellm_proxy/deepseek-v4", "deepseek-v4"},
	} {
		adapter := &modelAdapter{}
		broker := &scopeBroker{}
		cfg := Config{Team: "bronze", Role: "reviewer", WorkDir: t.TempDir(),
			APIURL: modelAPI(t, tc.claimed), ForgeURL: gitForge(t, map[string]string{"README.md": "# example\n"}),
			DefaultForge: harness.ForgeForgejo, BuilderToken: "tok", ForgeTokenAccess: ForgeTokenReadOnly,
			RepoOwner: "webgrip", RepoName: "example", BaseBranch: "development",
			LLMModel: "litellm_proxy/deepseek-v4", LLMModels: ModelList("litellm_proxy/deepseek-v4")}
		w := New(cfg, adapter, broker, discardLog())
		if err := w.RunContext(context.Background()); err != nil {
			t.Fatalf("RunContext: %v", err)
		}
		if !slices.Contains(adapter.env.BaseEnv, "LLM_MODEL="+tc.wantEnv) {
			t.Errorf("claimed %q: harness environment lacks LLM_MODEL=%s", tc.claimed, tc.wantEnv)
		}
		if adapter.env.LLM.Model != tc.wantScope {
			t.Errorf("claimed %q: harness model = %q, want %q", tc.claimed, adapter.env.LLM.Model, tc.wantScope)
		}
		if !slices.Equal(broker.models, []string{tc.wantScope}) {
			t.Errorf("claimed %q: minted for %v, want only %s", tc.claimed, broker.models, tc.wantScope)
		}
		if w.Cfg.LLMModel != "litellm_proxy/deepseek-v4" {
			t.Errorf("the claimed model outlived its Run: %q", w.Cfg.LLMModel)
		}
	}
}

func TestClaudeCodePinsEveryAliasToTheClaimedModel(t *testing.T) {
	cfg := withClaimedModel(Config{LLMModel: "claude-sonnet-5", LLMModels: ModelList("claude-sonnet-5")}, "claude-haiku-5-5")
	inv, err := claudecode.New("", "").Prepare(harness.TaskSpec{}, harness.RunEnv{ScratchDir: t.TempDir(),
		LLM: harness.LLMEnv{BaseURL: "http://gateway/v1", Model: cfg.LLMModels[0]}})
	if err != nil {
		t.Fatal(err)
	}
	for _, alias := range append([]string{"ANTHROPIC_MODEL"}, claudecode.ModelAliasEnv...) {
		if !slices.Contains(inv.ExtraEnv, alias+"=claude-haiku-5-5") {
			t.Errorf("%s is not pinned to the claimed model: %s", alias, strings.Join(inv.ExtraEnv, " "))
		}
	}
	if !slices.Contains(inv.ExtraEnv, claudecode.AdvisorDisabled) {
		t.Error("the advisor must stay off with a claimed model")
	}
}
