package worker

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/cgi"
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

const writerBranch = "agent/vik-7"

func acpAgentDuringPrompt(t *testing.T, duringPrompt string) harness.Adapter {
	t.Helper()
	script := fmt.Sprintf(`#!/bin/sh
emit() { printf '%%s\n' "$1"; }
while IFS= read -r line; do
  id=$(printf '%%s' "$line" | sed -n 's/.*"id":\([0-9]*\).*/\1/p')
  case "$line" in
    *'"method":"initialize"'*)
      emit '{"jsonrpc":"2.0","id":'"$id"',"result":{"protocolVersion":1,"agentCapabilities":{},"authMethods":[]}}' ;;
    *'"method":"session/new"'*)
      emit '{"jsonrpc":"2.0","id":'"$id"',"result":{"sessionId":"s1"}}' ;;
    *'"method":"session/prompt"'*)
      %s
      emit '{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":{"sessionUpdate":"usage_update","used":1200,"size":200000,"cost":0.03}}}'
      emit '{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"Nothing to change."}}}}'
      emit '{"jsonrpc":"2.0","id":'"$id"',"result":{"stopReason":"end_turn"}}' ;;
  esac
done
`, duringPrompt)
	bin := filepath.Join(t.TempDir(), "agent.sh")
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	adapter, err := acp.New("custom", acp.ProfileOverrides{Argv: []string{bin}}, acp.Options{
		PromptTimeout: 30 * time.Second, IdleTimeout: 30 * time.Second, CancelGrace: time.Second, TermGrace: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	return adapter
}

const (
	editToolCall  = `emit '{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":{"sessionUpdate":"tool_call","toolCallId":"t1","kind":"edit","status":"completed","title":"edit main.go","locations":[{"path":"main.go"}]}}}'`
	shellToolCall = `emit '{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":{"sessionUpdate":"tool_call","toolCallId":"t1","kind":"execute","status":"completed","title":"shell"}}}'`
)

type pullListing int32

const (
	pullsNone pullListing = iota
	pullsForPushedBranch
	pullsFailAfterFirstRead
	pullsFailAlways
)

type writerForge struct {
	url     string
	bare    string
	listing atomic.Int32
	reads   atomic.Int32
	// staleHead, when set, is the head commit the pull request listing
	// reports instead of the branch's.
	staleHead atomic.Value
}

func newWriterForge(t *testing.T, branchExists bool, listing pullListing) *writerForge {
	t.Helper()
	execPath, err := exec.Command("git", "--exec-path").Output()
	if err != nil {
		t.Skip("git is not available")
	}
	backend := filepath.Join(strings.TrimSpace(string(execPath)), "git-http-backend")
	if _, err := os.Stat(backend); err != nil {
		t.Skip("git-http-backend is not available")
	}
	git := func(dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@example.com", "-c", "init.defaultBranch=development"}, args...)...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	src := t.TempDir()
	writeTree(t, src, map[string]string{"main.go": "package main\n", ".gitignore": "bin/\n"})
	git(src, "init")
	git(src, "add", ".")
	git(src, "commit", "-m", "fixture")
	if branchExists {
		git(src, "checkout", "-b", writerBranch)
		writeTree(t, src, map[string]string{"README.md": "earlier work\n"})
		git(src, "add", ".")
		git(src, "commit", "-m", "earlier work")
		git(src, "checkout", "development")
	}
	projects := t.TempDir()
	forge := &writerForge{bare: filepath.Join(projects, "webgrip", "example.git")}
	forge.listing.Store(int32(listing))
	git(projects, "clone", "--bare", src, forge.bare)
	git(forge.bare, "config", "http.receivepack", "true")

	mux := http.NewServeMux()
	mux.Handle("/webgrip/", &cgi.Handler{Path: backend, Env: []string{"GIT_PROJECT_ROOT=" + projects, "GIT_HTTP_EXPORT_ALL=1"}})
	var srvURL string
	mux.HandleFunc("/api/v1/repos/webgrip/example/pulls", func(w http.ResponseWriter, _ *http.Request) {
		read := forge.reads.Add(1)
		listing := pullListing(forge.listing.Load())
		if listing == pullsFailAlways || (listing == pullsFailAfterFirstRead && read > 1) {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		if listing == pullsNone || !forge.hasBranch() {
			fmt.Fprint(w, `[]`)
			return
		}
		fmt.Fprintf(w, `[{"number":3,"html_url":%q,"head":{"ref":%q,"sha":%q,"repo":{"full_name":"webgrip/example"}},"base":{"ref":"development","repo":{"full_name":"webgrip/example"}}}]`,
			srvURL+"/webgrip/example/pulls/3", writerBranch, forge.listedHead())
	})
	srv := httptest.NewServer(mux)
	srvURL = srv.URL
	forge.url = srv.URL
	t.Cleanup(srv.Close)
	return forge
}

func (f *writerForge) listedHead() string {
	if stale, ok := f.staleHead.Load().(string); ok && stale != "" {
		return stale
	}
	return f.branchHead()
}

func (f *writerForge) hasBranch() bool {
	return f.branchHead() != ""
}

func (f *writerForge) branchHead() string {
	out, err := exec.Command("git", "--git-dir", f.bare, "rev-parse", "--verify", "--quiet", "refs/heads/"+writerBranch).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func runWriterAgainst(t *testing.T, forge *writerForge, adapter harness.Adapter) harness.OutcomeReport {
	t.Helper()
	return runWriterWithContext(t, context.Background(), forge, adapter)
}

func runWriterWithContext(t *testing.T, ctx context.Context, forge *writerForge, adapter harness.Adapter) harness.OutcomeReport {
	t.Helper()
	var rec checkpointRecorder
	w := New(Config{APIURL: rec.server(t), ForgeURL: forge.url, DefaultForge: harness.ForgeForgejo, BuilderToken: "tok",
		RepoOwner: "webgrip", RepoName: "example", BaseBranch: "development", WorkDir: t.TempDir()},
		adapter, llmbroker.Static{}, discardLog())
	claimed := &ClaimResponse{RunToken: "rt", Role: "builder", Writes: true, WorkItem: work.WorkItem{ID: "1", ExternalID: "7", Title: "t"}}
	return w.execute(ctx, claimed, writerBranch, "trace", "", "")
}

func runWriter(t *testing.T, adapter harness.Adapter) harness.OutcomeReport {
	t.Helper()
	return runWriterAgainst(t, newWriterForge(t, false, pullsNone), adapter)
}

func wantStuck(t *testing.T, report harness.OutcomeReport, why string, reasonParts ...string) {
	t.Helper()
	if report.Outcome != work.OutcomeStuck {
		t.Fatalf("report = %+v, want stuck: %s", report, why)
	}
	for _, part := range reasonParts {
		if !strings.Contains(report.StuckReason, part) {
			t.Errorf("stuck reason %q does not mention %q", report.StuckReason, part)
		}
	}
}

func TestWriterWhoseShellEditNeverReachedAPullRequestIsStuck(t *testing.T) {
	adapter := acpAgentDuringPrompt(t, shellToolCall+`
      printf 'func main() {}\n' >> main.go`)
	report := runWriter(t, adapter)
	wantStuck(t, report, "the shell edit was never published", "uncommitted path(s): main.go")
}

func TestWriterWhoseEditToolChangeNeverReachedAPullRequestIsStuck(t *testing.T) {
	adapter := acpAgentDuringPrompt(t, editToolCall+`
      printf 'func main() {}\n' >> main.go`)
	report := runWriter(t, adapter)
	wantStuck(t, report, "the edit was never published", "uncommitted path(s): main.go")
	if report.Usage == nil || report.Usage.CostUSD != 0.03 {
		t.Errorf("usage = %+v, want the agent's reported cost kept for accounting", report.Usage)
	}
}

func TestWriterWhoseLocalCommitNeverReachedAPullRequestIsStuck(t *testing.T) {
	adapter := acpAgentDuringPrompt(t, editToolCall+`
      printf 'func main() {}\n' >> main.go
      git checkout -q -b `+writerBranch+` >&2 && git commit -qam 'add main' >&2`)
	report := runWriter(t, adapter)
	wantStuck(t, report, "the commit was never pushed", "1 commit(s) not on the forge", "add main")
}

func TestWriterThatAdvancedTheBaseBranchLocallyIsStuck(t *testing.T) {
	adapter := acpAgentDuringPrompt(t, editToolCall+`
      printf 'func main() {}\n' >> main.go
      git commit -qam 'commit on the base' >&2`)
	report := runWriter(t, adapter)
	wantStuck(t, report, "the base branch moved locally and nothing was published", "commit on the base")
}

func TestWriterThatPushedItsBranchButOpenedNoPullRequestIsStuck(t *testing.T) {
	forge := newWriterForge(t, false, pullsNone)
	adapter := acpAgentDuringPrompt(t, editToolCall+`
      printf 'func main() {}\n' >> main.go
      git checkout -q -b `+writerBranch+` >&2 && git commit -qam 'add main' >&2 && git push -q origin `+writerBranch+` >&2`)
	report := runWriterAgainst(t, forge, adapter)
	if !forge.hasBranch() {
		t.Fatal("the fake agent did not push its branch")
	}
	wantStuck(t, report, "a pushed branch without a pull request is not delivered work", writerBranch, "no pull request")
}

func TestWriterWithAnOpenPullRequestThatLeftItsChangesLocalIsStuck(t *testing.T) {
	for _, tc := range []struct {
		name, steps, names string
	}{
		{"uncommitted edit on the pull request's branch", `
      git fetch -q origin ` + writerBranch + ` >&2 && git checkout -q -b ` + writerBranch + ` FETCH_HEAD >&2
      printf 'func main() {}\n' >> main.go`, "uncommitted path(s): main.go"},
		{"local commit on the pull request's branch", `
      git fetch -q origin ` + writerBranch + ` >&2 && git checkout -q -b ` + writerBranch + ` FETCH_HEAD >&2
      printf 'func main() {}\n' >> main.go && git commit -qam 'never pushed' >&2`, "1 commit(s) not on the forge"},
		{"edit on the base checkout", `
      printf 'func main() {}\n' >> main.go`, "uncommitted path(s): main.go"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			forge := newWriterForge(t, true, pullsForPushedBranch)
			report := runWriterAgainst(t, forge, acpAgentDuringPrompt(t, editToolCall+tc.steps))
			wantStuck(t, report, "an existing pull request does not deliver edits that stayed in the clone",
				tc.names, "pushed nothing to its open pull request")
			if report.Usage == nil || report.Usage.CostUSD != 0.03 {
				t.Errorf("usage = %+v, want the agent's reported cost kept for accounting", report.Usage)
			}
		})
	}
}

func TestWriterThatPushedToItsOpenPullRequestIsUpdated(t *testing.T) {
	forge := newWriterForge(t, true, pullsForPushedBranch)
	adapter := acpAgentDuringPrompt(t, editToolCall+`
      git fetch -q origin `+writerBranch+` >&2 && git checkout -q -b `+writerBranch+` FETCH_HEAD >&2
      printf 'func main() {}\n' >> main.go && git commit -qam 'fix it' >&2 && git push -q origin `+writerBranch+` >&2`)
	report := runWriterAgainst(t, forge, adapter)
	if report.Outcome != work.OutcomePRUpdated {
		t.Fatalf("report = %+v, want pr_updated: the writer pushed to its open pull request", report)
	}
}

func TestWriterThatOpenedAPullRequestKeepsIt(t *testing.T) {
	forge := newWriterForge(t, false, pullsForPushedBranch)
	adapter := acpAgentDuringPrompt(t, editToolCall+`
      printf 'func main() {}\n' >> main.go
      git checkout -q -b `+writerBranch+` >&2 && git commit -qam 'add main' >&2 && git push -q origin `+writerBranch+` >&2
      printf 'scratch\n' > notes.txt`)
	report := runWriterAgainst(t, forge, adapter)
	if report.Outcome != work.OutcomePROpened {
		t.Fatalf("report = %+v, want pr_opened: a new pull request is delivered work", report)
	}
}

func TestWriterWhoseForgeReadFailedAfterTheRunIsNotDone(t *testing.T) {
	for _, tc := range []struct {
		name, steps string
	}{
		{"unchanged checkout", ``},
		{"pushed branch", `
      printf 'func main() {}\n' >> main.go
      git checkout -q -b ` + writerBranch + ` >&2 && git commit -qam 'add main' >&2 && git push -q origin ` + writerBranch + ` >&2`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			forge := newWriterForge(t, false, pullsFailAfterFirstRead)
			report := runWriterAgainst(t, forge, acpAgentDuringPrompt(t, shellToolCall+tc.steps))
			wantStuck(t, report, "a failed forge read cannot say no change was needed", "could not read the forge", "HTTP 500")
		})
	}
}

func TestWriterThatChangedNothingStillCompletes(t *testing.T) {
	adapter := acpAgentDuringPrompt(t, shellToolCall+`
      mkdir -p bin && printf 'binary' > bin/app`)
	report := runWriter(t, adapter)
	if report.Outcome != work.OutcomeNoChangeNeeded {
		t.Fatalf("report = %+v, want no_change_needed: ignored build output is not a change", report)
	}
}

func TestUntrackedOutputTheRepositoryDoesNotIgnoreIsAChange(t *testing.T) {
	adapter := acpAgentDuringPrompt(t, shellToolCall+`
      printf 'mode: set\n' > coverage.out`)
	report := runWriter(t, adapter)
	wantStuck(t, report, "only the repository's .gitignore makes generated output noise", "coverage.out")
}

func TestReaderThatLeftFilesInItsCheckoutStillReportsItsReview(t *testing.T) {
	forge := newReviewForge(t, true)
	adapter := &openSpecAdapter{
		report: harness.OutcomeReport{Outcome: work.OutcomeNoChangeNeeded, Summary: "ok", Verdict: harness.VerdictApprove},
		edit:   func(repoDir string) { writeTree(t, repoDir, map[string]string{"scratch.txt": "notes\n"}) },
	}
	report, _ := runReader(t, forge, ClaimResponse{Role: "reviewer", Round: 3}, adapter)
	if report.Outcome != work.OutcomeNoChangeNeeded || report.Verdict != harness.VerdictApprove {
		t.Fatalf("report = %+v, want the reviewer's no_change_needed and approval: a reader delivers no pull request", report)
	}
}

type editingAdapter struct {
	edit func(repoDir string)
	run  func(ctx context.Context) error
}

func (a *editingAdapter) Name() string     { return "editing" }
func (a *editingAdapter) ExpectsLLM() bool { return false }
func (a *editingAdapter) Run(ctx context.Context, _ harness.TaskSpec, env harness.RunEnv) (harness.OutcomeReport, error) {
	a.edit(env.RepoDir)
	return harness.OutcomeReport{}, a.run(ctx)
}

func TestWriterThatFailedKeepsItsFailureWhenItAlsoChangedTheCheckout(t *testing.T) {
	edit := func(repoDir string) {
		writeTree(t, repoDir, map[string]string{"main.go": "package main\n\nfunc main() {}\n"})
	}

	t.Run("harness error", func(t *testing.T) {
		report := runWriterAgainst(t, newWriterForge(t, false, pullsNone),
			&editingAdapter{edit: edit, run: func(context.Context) error { return errors.New("exit status 1") }})
		if report.Outcome != work.OutcomeStuck || report.FailureReason != string(work.FailureAgentError) {
			t.Fatalf("report = %+v, want the harness failure (stuck, agent_error) unchanged", report)
		}
	})
	t.Run("pod terminated", func(t *testing.T) {
		ctx, cancel := context.WithCancelCause(context.Background())
		defer cancel(nil)
		report := runWriterWithContext(t, ctx, newWriterForge(t, false, pullsNone),
			&editingAdapter{edit: edit, run: func(context.Context) error {
				cancel(errTerminated)
				return context.Canceled
			}})
		if report.Outcome != work.OutcomeFailed || report.FailureReason != string(work.FailureInfraNode) {
			t.Fatalf("report = %+v, want the termination (failed, infra_node) unchanged", report)
		}
	})
}
