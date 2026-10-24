package worker

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/harness"
	"github.com/ploeg-hq/ploeg/pkg/harness/adapters/acp"
	"github.com/ploeg-hq/ploeg/pkg/llmbroker"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

// yieldingAdapter edits the checkout and stops as an adapter does when the
// Run's yield closed: it returns ErrYielded.
type yieldingAdapter struct {
	edit func(repoDir string, env harness.RunEnv)
	spec harness.TaskSpec
	head string
}

func (a *yieldingAdapter) Name() string     { return "yielding" }
func (a *yieldingAdapter) ExpectsLLM() bool { return false }
func (a *yieldingAdapter) Yields() bool     { return true }
func (a *yieldingAdapter) Run(_ context.Context, spec harness.TaskSpec, env harness.RunEnv) (harness.OutcomeReport, error) {
	a.spec = spec
	if out, err := exec.Command("git", "-C", env.RepoDir, "rev-parse", "--abbrev-ref", "HEAD").Output(); err == nil {
		a.head = strings.TrimSpace(string(out))
	}
	if a.edit != nil {
		a.edit(env.RepoDir, env)
	}
	return harness.OutcomeReport{Outcome: work.OutcomeFailed, Summary: "cancelled", Usage: &harness.Usage{CostUSD: 0.02}}, harness.ErrYielded
}

func (f *writerForge) git(t *testing.T, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"--git-dir", f.bare}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func runYieldingWriter(t *testing.T, forge *writerForge, adapter harness.Adapter, handback *work.Handback) harness.OutcomeReport {
	t.Helper()
	var rec checkpointRecorder
	w := New(Config{APIURL: rec.server(t), ForgeURL: forge.url, DefaultForge: harness.ForgeForgejo, BuilderToken: "tok",
		RepoOwner: "webgrip", RepoName: "example", BaseBranch: "development", WorkDir: t.TempDir()},
		adapter, llmbroker.Static{}, discardLog())
	w.yield = newYieldSignal()
	w.yield.raise()
	claimed := &ClaimResponse{RunToken: "rt", Role: "builder", Round: 3, Writes: true, Handback: handback,
		WorkItem: work.WorkItem{ID: "1", ExternalID: "7", Title: "t"}}
	return w.execute(context.Background(), claimed, writerBranch, "trace", "", "")
}

// ADR-0142: a yielded writer's uncommitted work reaches the Work Item's
// branch as one marked work in progress commit, and the outcome says where.
func TestYieldedWriterPushesItsWorkInProgressToTheBranch(t *testing.T) {
	forge := newWriterForge(t, false, pullsNone)
	adapter := &yieldingAdapter{edit: func(dir string, _ harness.RunEnv) {
		writeTree(t, dir, map[string]string{"main.go": "package main\n\nfunc half() {}\n", "notes/todo.md": "next: finish half\n", "bin/out": "ignored\n"})
	}}
	report := runYieldingWriter(t, forge, adapter, nil)

	if report.Outcome != work.OutcomeYielded || report.Yield == nil {
		t.Fatalf("report = %+v, want yielded with a yield report", report)
	}
	y := report.Yield
	head := forge.branchHead()
	if !y.Pushed || y.Commit == "" || y.Commit != head || y.WorkInProgress != head || y.Branch != writerBranch {
		t.Fatalf("yield = %+v, forge head %q", y, head)
	}
	if report.FailureReason != "" || report.Usage == nil || report.Usage.CostUSD != 0.02 {
		t.Fatalf("report = %+v, want no failure and the agent's usage kept", report)
	}
	message := forge.git(t, "log", "-1", "--format=%an%n%B", head)
	for _, want := range []string{"agent-builder", yieldCommitSubject, "VIK-7", "Agent-Trace-Id: trace", "Ploeg-Yield: trace"} {
		if !strings.Contains(message, want) {
			t.Fatalf("commit message lacks %q:\n%s", want, message)
		}
	}
	if got := forge.git(t, "show", head+":notes/todo.md"); got != "next: finish half" {
		t.Fatalf("todo.md = %q", got)
	}
	if out, err := exec.Command("git", "--git-dir", forge.bare, "cat-file", "-e", head+":bin/out").CombinedOutput(); err == nil {
		t.Fatalf("an ignored file was committed: %s", out)
	}
}

// A yield that would push one of the Run's credentials pushes nothing and
// reports the leak instead.
func TestYieldedWriterWithholdsWorkThatCarriesACredential(t *testing.T) {
	forge := newWriterForge(t, false, pullsNone)
	adapter := &yieldingAdapter{edit: func(dir string, env harness.RunEnv) {
		canary := ""
		for _, kv := range env.BaseEnv {
			if v, ok := strings.CutPrefix(kv, canaryEnv+"="); ok {
				canary = v
			}
		}
		if canary == "" {
			t.Fatal("the writer got no canary credential")
		}
		writeTree(t, dir, map[string]string{"config.txt": "token=" + canary + "\n"})
	}}
	report := runYieldingWriter(t, forge, adapter, nil)
	if report.Outcome != work.OutcomeFailed || report.FailureReason != string(work.FailureCredentialLeak) || report.Yield != nil {
		t.Fatalf("report = %+v, want the credential leak and no yield", report)
	}
	if forge.hasBranch() {
		t.Fatal("work carrying a credential reached the forge")
	}
}

// A branch the forge moved past the Run's checkout is never forced: the
// yield reports that the work is not on the forge.
func TestYieldedWriterNeverForcesTheBranch(t *testing.T) {
	forge := newWriterForge(t, true, pullsNone)
	before := forge.branchHead()
	adapter := &yieldingAdapter{edit: func(dir string, _ harness.RunEnv) {
		writeTree(t, dir, map[string]string{"main.go": "package main\n\nfunc other() {}\n"})
	}}
	report := runYieldingWriter(t, forge, adapter, nil)
	if report.Outcome != work.OutcomeYielded || report.Yield == nil || report.Yield.Pushed || report.Yield.Detail == "" {
		t.Fatalf("report = %+v, want a yield whose work is not on the forge, with why", report)
	}
	if forge.branchHead() != before {
		t.Fatal("the yield rewrote the branch")
	}
}

// A writer that changed nothing yields with nothing to push.
func TestYieldedWriterThatChangedNothingPushesNothing(t *testing.T) {
	forge := newWriterForge(t, false, pullsNone)
	report := runYieldingWriter(t, forge, &yieldingAdapter{}, nil)
	if report.Outcome != work.OutcomeYielded || report.Yield == nil || !report.Yield.Pushed || report.Yield.Commit != "" || report.Yield.WorkInProgress != "" {
		t.Fatalf("report = %+v", report)
	}
	if forge.hasBranch() {
		t.Fatal("an empty yield created the branch")
	}
}

// ADR-0142: the first writer after a hand back stands on the branch as the
// person left it and is told which commits they added since the yield.
func TestHandedBackWriterStandsOnThePersonsHeadAndIsToldTheirCommits(t *testing.T) {
	forge := newWriterForge(t, true, pullsNone)
	yieldedAt := forge.git(t, "rev-parse", "refs/heads/development")
	adapter := &yieldingAdapter{}
	runYieldingWriter(t, forge, adapter, &work.Handback{Branch: writerBranch, YieldedCommit: yieldedAt})

	if adapter.head != writerBranch {
		t.Fatalf("the agent stood on %q, want %q", adapter.head, writerBranch)
	}
	if len(adapter.spec.Briefing) == 0 || adapter.spec.Briefing[0].Role != "hand back" {
		t.Fatalf("briefing = %+v, want the hand back first", adapter.spec.Briefing)
	}
	text := adapter.spec.Briefing[0].Findings
	for _, want := range []string{"earlier work", yieldedAt[:12], writerBranch} {
		if !strings.Contains(text, want) {
			t.Fatalf("hand back briefing lacks %q:\n%s", want, text)
		}
	}
	if adapter.spec.Briefing[0].Round != 3 {
		t.Fatalf("briefing round = %d", adapter.spec.Briefing[0].Round)
	}
}

func TestHandbackBriefingWhenTheEarlierRunPushedNothing(t *testing.T) {
	forge := newWriterForge(t, true, pullsNone)
	adapter := &yieldingAdapter{}
	runYieldingWriter(t, forge, adapter, &work.Handback{Branch: writerBranch})
	if text := adapter.spec.Briefing[0].Findings; !strings.Contains(text, "pushed nothing") {
		t.Fatalf("briefing = %s", text)
	}
}

// End to end over the real ACP wire: a lease renewal that answers yield
// stops the agent between steps, and the worker reports yielded with its
// work on the branch.
func TestRenewalYieldStopsAnACPWriterAndReportsYielded(t *testing.T) {
	forge := newWriterForge(t, false, pullsNone)
	script := `#!/bin/sh
emit() { printf '%s\n' "$1"; }
prompt=
while IFS= read -r line; do
  id=$(printf '%s' "$line" | sed -n 's/.*"id":\([0-9]*\).*/\1/p')
  case "$line" in
    *'"method":"initialize"'*)
      emit '{"jsonrpc":"2.0","id":'"$id"',"result":{"protocolVersion":1,"agentCapabilities":{},"authMethods":[]}}' ;;
    *'"method":"session/new"'*)
      emit '{"jsonrpc":"2.0","id":'"$id"',"result":{"sessionId":"s1"}}' ;;
    *'"method":"session/prompt"'*)
      prompt=$id
      printf 'half done\n' > wip.txt
      emit '{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":{"sessionUpdate":"tool_call","toolCallId":"t1","kind":"edit","status":"completed","title":"edit wip.txt"}}}' ;;
    *'"method":"session/cancel"'*)
      emit '{"jsonrpc":"2.0","id":'"$prompt"',"result":{"stopReason":"cancelled"}}' ;;
  esac
done
`
	bin := filepath.Join(t.TempDir(), "agent.sh")
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	adapter, err := acp.New("custom", acp.ProfileOverrides{Argv: []string{bin}}, acp.Options{
		PromptTimeout: time.Minute, IdleTimeout: time.Minute, CancelGrace: 5 * time.Second, TermGrace: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}

	var claimedYields atomic.Bool
	var outcome atomic.Value
	var renewals atomic.Int32
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v1/claim":
			var req map[string]any
			_ = json.NewDecoder(r.Body).Decode(&req)
			claimedYields.Store(req["yields"] == true)
			_ = json.NewEncoder(w).Encode(map[string]any{"runToken": "rt", "deadline": time.Now().Add(15 * time.Second),
				"workItem": map[string]any{"id": "1", "externalId": "7", "title": "t"}, "role": "builder", "writes": true, "branch": writerBranch})
		case strings.HasSuffix(r.URL.Path, "/renew"):
			renewals.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]any{"deadline": time.Now().Add(15 * time.Second), "yield": true})
		case strings.HasSuffix(r.URL.Path, "/outcome"):
			var rep harness.OutcomeReport
			_ = json.NewDecoder(r.Body).Decode(&rep)
			outcome.Store(rep)
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	t.Cleanup(api.Close)
	w := New(Config{APIURL: api.URL, ForgeURL: forge.url, DefaultForge: harness.ForgeForgejo, BuilderToken: "tok",
		RepoOwner: "webgrip", RepoName: "example", BaseBranch: "development", WorkDir: t.TempDir(), Team: "t", Role: "builder"},
		adapter, llmbroker.Static{}, discardLog())
	if err := w.RunContext(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !claimedYields.Load() {
		t.Fatal("the claim did not say the ACP Harness yields")
	}
	rep, _ := outcome.Load().(harness.OutcomeReport)
	if rep.Outcome != work.OutcomeYielded || rep.Yield == nil || !rep.Yield.Pushed || rep.Yield.Commit != forge.branchHead() {
		t.Fatalf("outcome = %+v (renewals %d)", rep, renewals.Load())
	}
	if got := forge.git(t, "show", rep.Yield.Commit+":wip.txt"); got != "half done" {
		t.Fatalf("wip.txt = %q", got)
	}
}
