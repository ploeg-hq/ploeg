package worker

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/harness"
	"github.com/ploeg-hq/ploeg/pkg/harness/adapters/claudecode"
	"github.com/ploeg-hq/ploeg/pkg/harness/adapters/openhands"
	"github.com/ploeg-hq/ploeg/pkg/llmbroker"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

func discardLog() *slog.Logger { return slog.New(slog.DiscardHandler) }

// --- resolveOutcome: the outcome-precedence table, pinning the historical
// exit-code mapping byte-for-byte (VIK-596 owns changing it) plus the new
// structured-report and usage-merge branches. ---

func TestResolveOutcome_Precedence(t *testing.T) {
	usage := &harness.Usage{CostUSD: 0.5, SessionID: "s"}
	tests := []struct {
		name        string
		report      harness.OutcomeReport
		runErr      error
		ctxErr      error
		prURL       string
		prExisted   bool
		expectsLLM  bool
		wantOutcome work.Outcome
		wantSummary string
		wantReason  string
		wantLinks   []string
		wantPhase   string
		wantFailure string
	}{
		{
			name:        "new PR always wins",
			runErr:      errors.New("exit status 1"), // even over a failed process
			prURL:       "http://forge/pr/7",
			wantOutcome: work.OutcomePROpened,
			wantSummary: "openhands run opened a PR for fix the thing, then exited with an error before finishing",
			wantReason:  "tail-of-log",
			wantLinks:   []string{"http://forge/pr/7"},
			wantPhase:   "pr_opened",
			wantFailure: "agent_error",
		},
		{
			name:        "a clean run that opened a PR records no failure",
			prURL:       "http://forge/pr/7",
			wantOutcome: work.OutcomePROpened,
			wantSummary: "openhands run opened a PR for fix the thing",
			wantLinks:   []string{"http://forge/pr/7"},
			wantPhase:   "pr_opened",
		},
		{
			name:        "a PR opened before the harness timeout keeps the timeout",
			runErr:      errHarnessTimeout,
			prURL:       "http://forge/pr/7",
			wantOutcome: work.OutcomePROpened,
			wantSummary: "openhands run opened a PR for fix the thing, then was stopped: " + errHarnessTimeout.Error(),
			wantReason:  "tail-of-log",
			wantLinks:   []string{"http://forge/pr/7"},
			wantPhase:   "pr_opened",
			wantFailure: "timeout",
		},
		{
			name:        "a PR opened before the lease was lost keeps the lease loss",
			runErr:      errors.New("signal: killed"),
			ctxErr:      context.Canceled,
			prURL:       "http://forge/pr/7",
			wantOutcome: work.OutcomePROpened,
			wantSummary: "openhands run opened a PR for fix the thing, then lost its lease",
			wantReason:  "tail-of-log",
			wantLinks:   []string{"http://forge/pr/7"},
			wantPhase:   "pr_opened",
			wantFailure: "lease_lost",
		},
		// A PR that predates the run is a previous turn's work. Crediting it
		// to this run is how a crashed reviewer marked an item done having
		// burned zero tokens.
		{
			name:        "crashed run does NOT inherit a pre-existing PR",
			runErr:      errors.New("exit status 1"),
			prURL:       "http://forge/pr/7",
			prExisted:   true,
			wantOutcome: work.OutcomeStuck,
			wantSummary: "openhands run failed",
			wantReason:  "tail-of-log",
			wantLinks:   []string{"http://forge/pr/7"},
			wantFailure: "agent_error",
		},
		{
			name:        "clean run on a pre-existing PR = pr_updated",
			prURL:       "http://forge/pr/7",
			prExisted:   true,
			wantOutcome: work.OutcomePRUpdated,
			wantSummary: "openhands run updated the open PR for fix the thing",
			wantLinks:   []string{"http://forge/pr/7"},
			wantPhase:   "pr_updated",
		},
		{
			name:        "lost lease on a pre-existing PR stays stuck",
			runErr:      errors.New("signal: killed"),
			ctxErr:      context.Canceled,
			prURL:       "http://forge/pr/7",
			prExisted:   true,
			wantOutcome: work.OutcomeStuck,
			wantSummary: "run aborted (lease lost)",
			wantReason:  "lease renewal failed; run cancelled to avoid a double claim",
			wantFailure: "lease_lost",
		},
		{
			name:        "a terminated pod reports failed/infra_node, never stuck",
			runErr:      errors.New("signal: killed"),
			ctxErr:      errTerminated,
			wantOutcome: work.OutcomeFailed,
			wantSummary: "openhands run was terminated mid-run (pod shutdown)",
			wantFailure: "infra_node",
		},
		{
			name:        "a PR opened before termination still wins",
			runErr:      errors.New("signal: killed"),
			ctxErr:      errTerminated,
			prURL:       "http://forge/pr/9",
			wantOutcome: work.OutcomePROpened,
			wantSummary: "openhands run opened a PR for fix the thing, then was terminated mid-run (pod shutdown)",
			wantReason:  "tail-of-log",
			wantLinks:   []string{"http://forge/pr/9"},
			wantPhase:   "pr_opened",
			wantFailure: "infra_node",
		},
		{
			name:        "structured report beats heuristics",
			report:      harness.OutcomeReport{Outcome: work.OutcomeNoChangeNeeded, Summary: "harness says nothing to do"},
			runErr:      nil,
			wantOutcome: work.OutcomeNoChangeNeeded,
			wantSummary: "harness says nothing to do",
		},
		// A reviewer's own report carries findings and a verdict but no link —
		// it is never given the PR URL. Ploeg polled the forge, so it fills
		// the gap; publishRound finds the pull request by scanning reported
		// links, and without this a review-only Shift published nothing.
		{
			name:        "a structured report gains the PR link it could not know",
			report:      harness.OutcomeReport{Outcome: work.OutcomeNoChangeNeeded, Summary: "review finished", Findings: "- broker.go:88 inverted TTL", Verdict: harness.VerdictRequestChanges},
			prURL:       "http://forge/pr/7",
			prExisted:   true,
			wantOutcome: work.OutcomeNoChangeNeeded,
			wantSummary: "review finished",
			wantLinks:   []string{"http://forge/pr/7"},
		},
		// ...but never invents one, and never overwrites what the agent said.
		{
			name:        "a structured report with no PR keeps its outcome but never its own links (ADR-0059)",
			report:      harness.OutcomeReport{Outcome: work.OutcomeFollowUpCreated, Summary: "opened a follow-up", Links: []string{"http://tracker/task/9"}},
			wantOutcome: work.OutcomeFollowUpCreated,
			wantSummary: "opened a follow-up",
		},
		{
			name:        "structured stuck without reason synthesizes one from the tail",
			report:      harness.OutcomeReport{Outcome: work.OutcomeStuck, Summary: "blocked"},
			runErr:      errors.New("exit status 2"),
			wantOutcome: work.OutcomeStuck,
			wantSummary: "blocked",
			wantReason:  "tail-of-log",
		},
		{
			name:        "exit 0 without PR = no_change_needed",
			wantOutcome: work.OutcomeNoChangeNeeded,
			wantSummary: "openhands run finished without opening a PR",
		},
		{
			name:        "ctx cancelled = stuck (lease lost)",
			runErr:      errors.New("signal: killed"),
			ctxErr:      context.Canceled,
			wantOutcome: work.OutcomeStuck,
			wantSummary: "run aborted (lease lost)",
			wantReason:  "lease renewal failed; run cancelled to avoid a double claim",
			wantFailure: "lease_lost",
		},
		{
			name:        "nonzero exit = stuck with log tail",
			runErr:      errors.New("exit status 7"),
			wantOutcome: work.OutcomeStuck,
			wantSummary: "openhands run failed",
			wantReason:  "tail-of-log",
			wantFailure: "agent_error",
		},
		// VIK-586: LLM adapter with zero spend and no PR → failed/infra_llm
		{
			name:        "LLM adapter zero spend exit 0 = infra_llm",
			report:      harness.OutcomeReport{Usage: &harness.Usage{CostUSD: 0}},
			expectsLLM:  true,
			wantOutcome: work.OutcomeFailed,
			wantSummary: "openhands run finished with zero LLM spend and no PR — likely LLM infra failure",
			wantFailure: "infra_llm",
		},
		// nill usage = no telemetry = keep no_change_needed
		{
			name:        "LLM adapter nil usage exit 0 = no_change_needed",
			report:      harness.OutcomeReport{Usage: nil},
			expectsLLM:  true,
			wantOutcome: work.OutcomeNoChangeNeeded,
			wantSummary: "openhands run finished without opening a PR",
		},
		// exec adapter zero spend exit 0 = no_change_needed (no LLM spend expected)
		{
			name:        "exec adapter zero spend exit 0 = no_change_needed",
			report:      harness.OutcomeReport{Usage: &harness.Usage{CostUSD: 0}},
			expectsLLM:  false,
			wantOutcome: work.OutcomeNoChangeNeeded,
			wantSummary: "openhands run finished without opening a PR",
		},
		// LLM adapter with structured failed + zero spend → infra_llm
		{
			name:        "LLM adapter structured failed zero spend maps failure_reason",
			report:      harness.OutcomeReport{Outcome: work.OutcomeFailed, Usage: &harness.Usage{CostUSD: 0}},
			expectsLLM:  true,
			wantOutcome: work.OutcomeFailed,
			wantFailure: "infra_llm",
		},
		// ACP reports UsageUpdate.cost as OPTIONAL, so plenty of agents send
		// token counts and no cost. Keying VIK-586 on cost alone would
		// relabel a real agent error as an LLM infra failure and requeue it.
		{
			name: "tokens without cost is NOT infra_llm",
			report: harness.OutcomeReport{
				Outcome: work.OutcomeFailed,
				Usage:   &harness.Usage{InputTokens: 40000, OutputTokens: 900, CostUSD: 0},
			},
			expectsLLM:  true,
			wantOutcome: work.OutcomeFailed,
			wantFailure: "", // unclassified beats misclassified
		},
		{
			name: "an adapter-set failure reason survives the heuristic",
			report: harness.OutcomeReport{
				Outcome:       work.OutcomeFailed,
				FailureReason: string(work.FailureAgentError),
				Usage:         &harness.Usage{CostUSD: 0},
			},
			expectsLLM:  true,
			wantOutcome: work.OutcomeFailed,
			wantFailure: "agent_error",
		},
		// ADR-0045: an ACP agent that reports tool calls but no tokens did
		// reach its model. Keeping the tally must not relabel it infra_llm.
		{
			name: "tool calls without tokens or cost is NOT infra_llm",
			report: harness.OutcomeReport{
				Outcome: work.OutcomeFailed,
				Usage:   &harness.Usage{ToolCalls: new(int64(12))},
			},
			expectsLLM:  true,
			wantOutcome: work.OutcomeFailed,
			wantFailure: "",
		},
		{
			name:        "exit 0 with tokens but no cost = no_change_needed",
			report:      harness.OutcomeReport{Usage: &harness.Usage{InputTokens: 1200, CostUSD: 0}},
			expectsLLM:  true,
			wantOutcome: work.OutcomeNoChangeNeeded,
			wantSummary: "openhands run finished without opening a PR",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := resolveOutcome("openhands", tc.report, tc.runErr, tc.ctxErr,
				tc.prURL, tc.prExisted, "fix the thing", "agent/vik-585", []byte("tail-of-log"), tc.expectsLLM, true)
			if got.Outcome != tc.wantOutcome {
				t.Errorf("outcome = %q, want %q", got.Outcome, tc.wantOutcome)
			}
			if got.Summary != tc.wantSummary {
				t.Errorf("summary = %q, want %q", got.Summary, tc.wantSummary)
			}
			if got.StuckReason != tc.wantReason {
				t.Errorf("stuckReason = %q, want %q", got.StuckReason, tc.wantReason)
			}
			if len(tc.wantLinks) > 0 && (len(got.Links) != 1 || got.Links[0] != tc.wantLinks[0]) {
				t.Errorf("links = %v, want %v", got.Links, tc.wantLinks)
			}
			if tc.wantPhase != "" && (got.Checkpoint == nil || got.Checkpoint.Phase != tc.wantPhase) {
				t.Errorf("checkpoint = %+v, want phase %q", got.Checkpoint, tc.wantPhase)
			}
			if got.FailureReason != tc.wantFailure {
				t.Errorf("failureReason = %q, want %q", got.FailureReason, tc.wantFailure)
			}
		})
	}
	t.Run("usage survives every branch", func(t *testing.T) {
		report := harness.OutcomeReport{Usage: usage} // no structured outcome, usage only
		got := resolveOutcome("claude-code", report, nil, nil, "http://forge/pr/8", false, "t", "b", nil, true, true)
		if got.Usage != usage {
			t.Errorf("usage lost on the PR branch: %+v", got.Usage)
		}
		got = resolveOutcome("claude-code", report, nil, nil, "http://forge/pr/8", true, "t", "b", nil, true, true)
		if got.Usage != usage {
			t.Errorf("usage lost on the pr_updated branch: %+v", got.Usage)
		}
		got = resolveOutcome("claude-code", report, nil, nil, "", false, "t", "b", nil, true, true)
		if got.Usage != usage {
			t.Errorf("usage lost on the no-change branch: %+v", got.Usage)
		}
	})
	t.Run("the writer's problem and solution survive every branch", func(t *testing.T) {
		report := harness.OutcomeReport{Problem: "Refunds over €500 fail.", Solution: "Check the limit first."}
		for name, got := range map[string]harness.OutcomeReport{
			"pr_opened":      resolveOutcome("claude-code", report, nil, nil, "http://forge/pr/8", false, "t", "b", nil, true, true),
			"pr_updated":     resolveOutcome("claude-code", report, nil, nil, "http://forge/pr/8", true, "t", "b", nil, true, true),
			"no PR":          resolveOutcome("claude-code", report, nil, nil, "", false, "t", "b", nil, false, true),
			"agent error":    resolveOutcome("claude-code", report, errors.New("exit 1"), nil, "http://forge/pr/8", true, "t", "b", nil, true, true),
			"late PR opened": resolveOutcome("claude-code", report, errors.New("exit 1"), nil, "http://forge/pr/8", false, "t", "b", nil, true, true),
		} {
			if got.Problem != report.Problem || got.Solution != report.Solution {
				t.Errorf("%s: problem %q solution %q, want the writer's account", name, got.Problem, got.Solution)
			}
		}
	})
}

// --- runAgent: the four key-lifecycle regressions (mint → run → deferred
// revoke on every return path), now at the broker seam. ---

type recordingBroker struct {
	llmbroker.Broker
	mintErr  error
	key      string
	minted   int
	revoked  int
	lastCred llmbroker.Credential
	groups   []string // MCP access groups the minted credential carries
}

func (b *recordingBroker) Mint(_ context.Context, req llmbroker.MintRequest) (llmbroker.Credential, error) {
	b.minted++
	if b.mintErr != nil {
		return llmbroker.Credential{}, b.mintErr
	}
	return llmbroker.Credential{APIKey: b.key, Alias: "ploeg-abc123def456", MCPAccessGroups: b.groups}, nil
}

func (b *recordingBroker) Revoke(_ context.Context, cred llmbroker.Credential) error {
	b.revoked++
	b.lastCred = cred
	return nil
}

func fakeAgentAdapter(t *testing.T, exitCode int) harness.Adapter {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "fake-agent.sh")
	body := "#!/bin/sh\nexit " + string(rune('0'+exitCode)) + "\n"
	if err := os.WriteFile(bin, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return harness.RunCommand(openhands.New(bin))
}

func runEnv(t *testing.T) harness.RunEnv {
	return harness.RunEnv{
		RepoDir:    t.TempDir(),
		ScratchDir: t.TempDir(),
		Prompt:     "task",
		BaseEnv:    os.Environ(),
		Log:        discardLog(),
	}
}

func testTaskSpec() harness.TaskSpec {
	return harness.TaskSpec{TraceID: "ploeg-abc123def456"}
}

func TestRegression_AgentFailure_RevokesKey(t *testing.T) {
	broker := &recordingBroker{key: "sk-test-fake-key"}
	_, mintErr, runErr := runAgent(context.Background(), discardLog(), broker,
		fakeAgentAdapter(t, 4), testTaskSpec(), runEnv(t), llmbroker.MintRequest{RunToken: "abc123def456ff"}, 0, "")
	if mintErr != nil {
		t.Fatal(mintErr)
	}
	if runErr == nil {
		t.Fatal("expected the agent failure to surface")
	}
	if broker.revoked != 1 {
		t.Fatal("revoke was NOT called after agent failure — key leak")
	}
	if broker.lastCred.APIKey != "sk-test-fake-key" {
		t.Errorf("revoked wrong credential: %+v", broker.lastCred)
	}
}

func TestRegression_AgentSuccess_RevokesKey(t *testing.T) {
	broker := &recordingBroker{key: "sk-test-fake-key"}
	_, mintErr, runErr := runAgent(context.Background(), discardLog(), broker,
		fakeAgentAdapter(t, 0), testTaskSpec(), runEnv(t), llmbroker.MintRequest{RunToken: "abc123def456ff"}, 0, "")
	if mintErr != nil || runErr != nil {
		t.Fatalf("mintErr=%v runErr=%v", mintErr, runErr)
	}
	if broker.revoked != 1 {
		t.Fatal("revoke was NOT called after agent success — key leak")
	}
}

func TestRegression_MintFailure_NoRevoke_NoRun(t *testing.T) {
	broker := &recordingBroker{mintErr: errors.New("HTTP 500")}
	_, mintErr, _ := runAgent(context.Background(), discardLog(), broker,
		fakeAgentAdapter(t, 0), testTaskSpec(), runEnv(t), llmbroker.MintRequest{RunToken: "abc123def456ff"}, 0, "")
	if mintErr == nil {
		t.Fatal("expected mint error")
	}
	if broker.revoked != 0 {
		t.Fatal("revoke was called even though mint failed")
	}
}

func TestRunAgent_InjectsKeyAndTraceIntoEnv(t *testing.T) {
	env := runEnv(t)
	marker := filepath.Join(env.ScratchDir, "env.txt")
	bin := filepath.Join(t.TempDir(), "env-dump.sh")
	script := "#!/bin/sh\nprintf '%s|%s' \"$LLM_API_KEY\" \"$LLM_TRACE_ID\" > " + marker + "\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	broker := &recordingBroker{key: "sk-minted"}
	_, mintErr, runErr := runAgent(context.Background(), discardLog(), broker,
		harness.RunCommand(openhands.New(bin)), testTaskSpec(), env, llmbroker.MintRequest{RunToken: "abc123def456ff"}, 0, "")
	if mintErr != nil || runErr != nil {
		t.Fatalf("mintErr=%v runErr=%v", mintErr, runErr)
	}
	b, err := os.ReadFile(marker)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "sk-minted|ploeg-abc123def456" {
		t.Errorf("agent env = %q, want minted key and trace", b)
	}
}

func TestRunAgent_StaticBrokerNoKeyStillRuns(t *testing.T) {
	broker := &recordingBroker{key: ""}
	_, mintErr, runErr := runAgent(context.Background(), discardLog(), broker,
		fakeAgentAdapter(t, 0), testTaskSpec(), runEnv(t), llmbroker.MintRequest{RunToken: "abc123def456ff"}, 0, "")
	if mintErr != nil || runErr != nil {
		t.Fatalf("mintErr=%v runErr=%v", mintErr, runErr)
	}
}

func TestRunAgent_ClaudeWriterAccountReachesTheNewPullRequestReport(t *testing.T) {
	const (
		problem  = "Refunds over €500 fail with a 500."
		solution = "`refund.go` checks the limit before the call."
		prURL    = "http://forge/o/r/pulls/8"
	)
	bin := filepath.Join(t.TempDir(), "claude")
	script := "#!/bin/sh\n" +
		`printf '%s\n' '{"problem":"` + problem + `","solution":"` + solution + `"}' > "$PLOEG_OUTCOME_FILE"` + "\n" +
		`printf '%s\n' 'Claude Code banner' '{"type":"result","total_cost_usd":'` + "\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	report, mintErr, runErr := runAgent(context.Background(), discardLog(), &recordingBroker{key: "sk-test"},
		harness.RunCommand(claudecode.New(bin, "")), testTaskSpec(), runEnv(t), llmbroker.MintRequest{RunToken: "abc123def456ff"}, 0, "")
	if mintErr != nil || runErr != nil {
		t.Fatalf("mintErr=%v runErr=%v", mintErr, runErr)
	}

	final := resolveOutcome("claude-code", report, runErr, nil, prURL, false, "Refund limit", "agent/vik-1", nil, true, true)
	if final.Outcome != work.OutcomePROpened || len(final.Links) != 1 || final.Links[0] != prURL {
		t.Fatalf("the observed pull request was not the outcome: %+v", final)
	}
	if final.Problem != problem || final.Solution != solution {
		t.Errorf("final report problem %q solution %q, want the writer's account", final.Problem, final.Solution)
	}
	if final.Usage != nil {
		t.Errorf("usage = %+v, want unknown: the envelope was unreadable", final.Usage)
	}
}

// --- ModelList (moved from cmd/ploeg-worker) ---

func TestModelList_StripsProxyPrefixes(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"", ""},
		{"claude-sonnet-5", "claude-sonnet-5"},
		{"litellm_proxy/claude-sonnet-5", "claude-sonnet-5"},
		{"openai/gpt-4", "gpt-4"},
		{"litellm_proxy/gpt-4-turbo", "gpt-4-turbo"},
		{"openai_other/gpt-4", "openai_other/gpt-4"}, // not a prefix match
	}
	for _, tc := range tests {
		got := ModelList(tc.input)
		if tc.input == "" {
			if got != nil {
				t.Errorf("ModelList(%q) = %v, want nil", tc.input, got)
			}
			continue
		}
		if len(got) != 1 || got[0] != tc.want {
			t.Errorf("ModelList(%q) = %v, want [%q]", tc.input, got, tc.want)
		}
	}
}

// --- adapter selection ---

func TestNewAdapter_Selection(t *testing.T) {
	for _, tc := range []struct {
		hc      HarnessConfig
		want    string
		wantErr bool
	}{
		{HarnessConfig{}, "openhands", false},
		{HarnessConfig{Name: "openhands"}, "openhands", false},
		{HarnessConfig{Name: "exec", Args: []string{"/bin/agent"}}, "exec", false},
		{HarnessConfig{Name: "exec"}, "", true}, // exec without args fails fast
		{HarnessConfig{Name: "claude-code"}, "claude-code", false},
		{HarnessConfig{Name: "bogus"}, "", true},
	} {
		ad, err := NewAdapter(tc.hc)
		if tc.wantErr {
			if err == nil {
				t.Errorf("NewAdapter(%+v): expected error", tc.hc)
			}
			continue
		}
		if err != nil {
			t.Errorf("NewAdapter(%+v): %v", tc.hc, err)
			continue
		}
		if ad.Name() != tc.want {
			t.Errorf("NewAdapter(%+v).Name() = %q, want %q", tc.hc, ad.Name(), tc.want)
		}
	}
}

// --- helpers shared with the ported tests ---

func testItem() work.WorkItem {
	return work.WorkItem{ExternalID: "585", Title: "fix the thing", Description: "<p>details</p>"}
}

func testCfg(base string) Config {
	return Config{
		RepoOwner:  "webgrip",
		RepoName:   "ploeg",
		BaseBranch: base,
		ForgeURL:   "http://forgejo-http.forgejo.svc.cluster.local:3000",
	}
}

func specFor(cfg Config, item work.WorkItem, branch, trace string) harness.TaskSpec {
	return harness.TaskSpec{
		WorkItem: item,
		Repo: harness.RepoRef{
			ForgeURL: cfg.ForgeURL, Owner: cfg.RepoOwner, Name: cfg.RepoName, BaseBranch: cfg.BaseBranch,
		},
		Branch:  branch,
		TraceID: trace,
	}
}

func TestComposePrompt_DefaultBaseIsMain(t *testing.T) {
	task := ComposePrompt(specFor(testCfg(""), testItem(), "agent/vik-585", "ploeg-abc123def456"), true, "", true)
	for _, want := range []string{
		"created from main",
		"NEVER commit to main",
		"base\n  branch main",
	} {
		if !strings.Contains(task, want) {
			t.Errorf("task missing %q:\n%s", want, task)
		}
	}
}

func TestComposePrompt_ConfiguredBase(t *testing.T) {
	task := ComposePrompt(specFor(testCfg("development"), testItem(), "agent/vik-585", "ploeg-abc123def456"), true, "", true)
	for _, want := range []string{
		"created from development",
		"NEVER commit to development",
		"base\n  branch development",
	} {
		if !strings.Contains(task, want) {
			t.Errorf("task missing %q:\n%s", want, task)
		}
	}
	for _, reject := range []string{"from main", "commit to main", "branch main"} {
		if strings.Contains(task, reject) {
			t.Errorf("task still references the default base (%q):\n%s", reject, task)
		}
	}
}

func TestCloneArgs(t *testing.T) {
	got := cloneArgs("", "http://x/repo.git", "/work/dir")
	want := []string{"clone", "--depth", "50", "http://x/repo.git", "/work/dir"}
	assertSlice(t, got, want)

	got = cloneArgs("development", "http://x/repo.git", "/work/dir")
	want = []string{"clone", "--depth", "50", "--branch", "development", "http://x/repo.git", "/work/dir"}
	assertSlice(t, got, want)
}

func assertSlice(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("arg %d: got %q, want %q (full: %v)", i, got[i], want[i], got)
		}
	}
}

func TestIsRunChangeRequest(t *testing.T) {
	cases := []struct {
		name                             string
		headRef, baseRef, wantHead, base string
		want                             bool
	}{
		{"head match, no base configured", "agent/vik-1", "main", "agent/vik-1", "", true},
		{"head mismatch", "agent/vik-2", "main", "agent/vik-1", "", false},
		{"head+base match", "agent/vik-1", "development", "agent/vik-1", "development", true},
		{"wrong base rejected when configured", "agent/vik-1", "main", "agent/vik-1", "development", false},
	}
	for _, c := range cases {
		if got := isRunChangeRequest(changeRequest{HeadBranch: c.headRef, BaseBranch: c.baseRef}, c.wantHead, c.base); got != c.want {
			t.Errorf("%s: isRunChangeRequest(%q,%q,%q,%q) = %v, want %v",
				c.name, c.headRef, c.baseRef, c.wantHead, c.base, got, c.want)
		}
	}
}

// The regression these two guard: a worker that is killed mid-run used to die
// on the default signal disposition — no outcome, no revoked key, no released
// Lease — so ploegd could only learn of it by waiting out the full lease TTL,
// and the Round was charged an attempt for a machine's decision. See the
// 2026-08-13 shift 73, parked needs_human after three such deaths.
func TestAbortOnTermination_CancelsRunWithTerminatedCause(t *testing.T) {
	parent, stop := context.WithCancel(context.Background())
	runCtx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)

	done := make(chan struct{})
	go func() {
		abortOnTermination(parent, runCtx, cancel, discardLog(), "98", "builder")
		close(done)
	}()

	stop() // the pod is going away
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("abortOnTermination did not return after the parent was cancelled")
	}

	<-runCtx.Done()
	if cause := context.Cause(runCtx); !errors.Is(cause, errTerminated) {
		t.Fatalf("cause = %v, want errTerminated — a lease_lost cause would send this to stuck and park the Shift", cause)
	}
	// The distinction is load-bearing: resolveOutcome maps the two causes to
	// opposite verdicts, one retryable and one requiring a person.
	if errors.Is(context.Cause(runCtx), errLeaseLost) {
		t.Fatal("a terminated pod must not be reported as a lost lease")
	}
}

func TestAbortOnTermination_ReturnsWhenTheRunEndsFirst(t *testing.T) {
	parent := context.Background()
	runCtx, cancel := context.WithCancelCause(context.Background())

	done := make(chan struct{})
	go func() {
		abortOnTermination(parent, runCtx, cancel, discardLog(), "98", "builder")
		close(done)
	}()

	cancel(nil) // the run finished on its own
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("abortOnTermination leaked: it outlived the run it was watching")
	}
	if cause := context.Cause(runCtx); errors.Is(cause, errTerminated) {
		t.Fatalf("cause = %v, want the run's own cause — nothing terminated the pod", cause)
	}
}
