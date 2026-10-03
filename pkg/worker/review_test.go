package worker

import (
	"context"
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
	"github.com/ploeg-hq/ploeg/pkg/llmbroker"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

const reviewBranch = "agent/vik-9"

type reviewForgeMode int32

const (
	forgeServes reviewForgeMode = iota
	forgeUnauthorized
	forgeBroken
	forgeHangs
	forgeBreaksAfterClone
)

type reviewForge struct {
	url    string
	mode   atomic.Int32
	commit string
}

func (f *reviewForge) set(m reviewForgeMode) { f.mode.Store(int32(m)) }

func newReviewForge(t *testing.T, withBranch bool) *reviewForge {
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
	forge := &reviewForge{}
	src := t.TempDir()
	writeTree(t, src, map[string]string{"main.go": "package main\n"})
	git(src, "init")
	git(src, "add", ".")
	git(src, "commit", "-m", "base")
	if withBranch {
		git(src, "checkout", "-b", reviewBranch)
		writeTree(t, src, map[string]string{"main.go": "package main\n\nfunc main() {}\n"})
		git(src, "commit", "-am", "work")
		forge.commit = git(src, "rev-parse", "HEAD")
		git(src, "checkout", "development")
	}
	projects := t.TempDir()
	git(projects, "clone", "--bare", src, filepath.Join(projects, "webgrip", "example.git"))

	release := make(chan struct{})
	var advertisements atomic.Int32
	gitHandler := &cgi.Handler{Path: backend, Env: []string{"GIT_PROJECT_ROOT=" + projects, "GIT_HTTP_EXPORT_ALL=1"}}
	mux := http.NewServeMux()
	mux.HandleFunc("/webgrip/", func(w http.ResponseWriter, r *http.Request) {
		mode := reviewForgeMode(forge.mode.Load())
		if mode == forgeBreaksAfterClone && strings.HasSuffix(r.URL.Path, "/info/refs") && advertisements.Add(1) > 1 {
			mode = forgeBroken
		}
		switch mode {
		case forgeUnauthorized:
			w.Header().Set("WWW-Authenticate", `Basic realm="forge"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
		case forgeBroken:
			http.Error(w, "internal error", http.StatusInternalServerError)
		case forgeHangs:
			select {
			case <-r.Context().Done():
			case <-release:
			}
		default:
			gitHandler.ServeHTTP(w, r)
		}
	})
	var srvURL string
	mux.HandleFunc("/api/v1/repos/webgrip/example/pulls", func(w http.ResponseWriter, _ *http.Request) {
		if !withBranch {
			fmt.Fprint(w, `[]`)
			return
		}
		fmt.Fprintf(w, `[{"html_url":%q,"head":{"ref":%q},"base":{"ref":"development"}}]`, srvURL+"/webgrip/example/pulls/3", reviewBranch)
	})
	srv := httptest.NewServer(mux)
	srvURL = srv.URL
	forge.url = srv.URL
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(release) })
	return forge
}

func (f *reviewForge) clone(t *testing.T) (cloneDir, cloneURL string) {
	t.Helper()
	cloneURL, err := plainURL(f.url, "webgrip", "example")
	if err != nil {
		t.Fatal(err)
	}
	cloneDir = filepath.Join(t.TempDir(), "clone")
	if out, err := runGit(context.Background(), "", cloneURL, "tok", cloneArgs("development", cloneURL, cloneDir)...); err != nil {
		t.Fatalf("clone: %v\n%s", err, out)
	}
	return cloneDir, cloneURL
}

func TestReaderStandsOnTheBranchUnderReviewAndRecordsItsCommit(t *testing.T) {
	forge := newReviewForge(t, true)
	cloneDir, cloneURL := forge.clone(t)
	commit, report, failed := checkoutBranchUnderReview(context.Background(), discardLog(), cloneDir, cloneURL, "tok", reviewBranch, "development", false)
	if failed {
		t.Fatalf("checkout failed: %+v", report)
	}
	if commit != forge.commit {
		t.Fatalf("reviewed commit = %q, want the branch head %q", commit, forge.commit)
	}
	body, err := os.ReadFile(filepath.Join(cloneDir, "main.go"))
	if err != nil || !strings.Contains(string(body), "func main") {
		t.Fatalf("the checkout is not the branch under review: %q %v", body, err)
	}
}

func TestReaderThatCannotReachTheForgeFailsAsInfrastructure(t *testing.T) {
	previous := reviewFetchTimeout
	reviewFetchTimeout = 2 * time.Second
	t.Cleanup(func() { reviewFetchTimeout = previous })

	for _, tc := range []struct {
		name      string
		mode      reviewForgeMode
		preAuthor bool
		want      string
	}{
		{"unauthorized", forgeUnauthorized, false, "could not fetch the branch under review"},
		{"server error", forgeBroken, false, "could not fetch the branch under review"},
		{"timeout", forgeHangs, false, "no answer from the forge within 2s"},
		{"unauthorized before any writer", forgeUnauthorized, true, "could not fetch the branch under review"},
		{"server error before any writer", forgeBroken, true, "could not fetch the branch under review"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			forge := newReviewForge(t, true)
			cloneDir, cloneURL := forge.clone(t)
			forge.set(tc.mode)
			commit, report, failed := checkoutBranchUnderReview(context.Background(), discardLog(), cloneDir, cloneURL, "tok", reviewBranch, "development", tc.preAuthor)
			if !failed || commit != "" {
				t.Fatalf("failed=%v commit=%q: a reader that could not reach the forge must not review anything", failed, commit)
			}
			if report.Outcome != work.OutcomeFailed || report.FailureReason != string(work.FailureInfraNode) ||
				!strings.Contains(report.Summary, tc.want) || report.Verdict != "" {
				t.Fatalf("report = %+v, want failed/infra_node mentioning %q", report, tc.want)
			}
		})
	}
}

func TestAbsentBranchIsReviewedAsTheBaseOnlyBeforeAnyWriter(t *testing.T) {
	t.Run("pre-author reader reviews the base", func(t *testing.T) {
		forge := newReviewForge(t, false)
		cloneDir, cloneURL := forge.clone(t)
		commit, report, failed := checkoutBranchUnderReview(context.Background(), discardLog(), cloneDir, cloneURL, "tok", reviewBranch, "development", true)
		if failed || commit != "" {
			t.Fatalf("failed=%v commit=%q report=%+v, want the base branch reviewed", failed, commit, report)
		}
	})
	t.Run("reader after a writer is stuck", func(t *testing.T) {
		forge := newReviewForge(t, false)
		cloneDir, cloneURL := forge.clone(t)
		commit, report, failed := checkoutBranchUnderReview(context.Background(), discardLog(), cloneDir, cloneURL, "tok", reviewBranch, "development", false)
		if !failed || commit != "" || report.Outcome != work.OutcomeStuck ||
			!strings.Contains(report.StuckReason, "writing Round ran before this reader") {
			t.Fatalf("failed=%v commit=%q report=%+v, want stuck naming the missing branch", failed, commit, report)
		}
	})
}

func runReader(t *testing.T, forge *reviewForge, claimed ClaimResponse, adapter harness.Adapter) (harness.OutcomeReport, []work.Checkpoint) {
	t.Helper()
	var rec checkpointRecorder
	w := New(Config{APIURL: rec.server(t), ForgeURL: forge.url, DefaultForge: harness.ForgeForgejo, BuilderToken: "tok",
		ForgeTokenAccess: ForgeTokenReadOnly, RepoOwner: "webgrip", RepoName: "example", BaseBranch: "development",
		WorkDir: t.TempDir()}, adapter, llmbroker.Static{}, discardLog())
	claimed.RunToken = "rt"
	claimed.WorkItem = work.WorkItem{ID: "1", ExternalID: "9", Title: "t"}
	report := w.execute(context.Background(), &claimed, reviewBranch, "trace", "", "")
	rec.mu.Lock()
	defer rec.mu.Unlock()
	return report, append([]work.Checkpoint(nil), rec.seen...)
}

func TestReviewerWhoseFetchFailsNeverRunsOrApproves(t *testing.T) {
	forge := newReviewForge(t, true)
	forge.set(forgeBreaksAfterClone)
	adapter := &openSpecAdapter{report: harness.OutcomeReport{Outcome: work.OutcomeNoChangeNeeded, Summary: "ok", Verdict: harness.VerdictApprove}}
	report, _ := runReader(t, forge, ClaimResponse{Role: "reviewer", Round: 3}, adapter)
	if adapter.ran {
		t.Fatal("the reviewer ran on the base branch after the forge failed to serve the branch under review")
	}
	if report.Outcome != work.OutcomeFailed || report.FailureReason != string(work.FailureInfraNode) ||
		report.Verdict != "" || len(report.Links) != 0 {
		t.Fatalf("report = %+v, want failed/infra_node with no verdict and no pull request", report)
	}
}

func TestReviewerReportsTheCommitItReviewed(t *testing.T) {
	forge := newReviewForge(t, true)
	adapter := &openSpecAdapter{report: harness.OutcomeReport{Outcome: work.OutcomeNoChangeNeeded, Summary: "ok", Verdict: harness.VerdictApprove}}
	report, checkpoints := runReader(t, forge, ClaimResponse{Role: "reviewer", Round: 3}, adapter)
	if !adapter.ran {
		t.Fatalf("the reviewer did not run: %+v", report)
	}
	if report.Checkpoint == nil || report.Checkpoint.Commit != forge.commit || report.Checkpoint.Branch != reviewBranch {
		t.Fatalf("outcome checkpoint = %+v, want the reviewed commit %s", report.Checkpoint, forge.commit)
	}
	if len(checkpoints) == 0 || checkpoints[0].Commit != forge.commit {
		t.Fatalf("checkpoints = %+v, want the first to record the reviewed commit", checkpoints)
	}
}

func TestAnalystBeforeTheWriterReviewsTheBase(t *testing.T) {
	forge := newReviewForge(t, false)
	adapter := &openSpecAdapter{report: harness.OutcomeReport{Outcome: work.OutcomeNoChangeNeeded, Summary: "ok", Findings: "recon"}}
	report, _ := runReader(t, forge, ClaimResponse{Role: "analyst", Round: 1, PreAuthor: true}, adapter)
	if !adapter.ran || report.Outcome != work.OutcomeNoChangeNeeded {
		t.Fatalf("ran=%v report=%+v, want the analyst to run on the base", adapter.ran, report)
	}
	if !strings.Contains(adapter.prompt, "on the base branch development") {
		t.Error("the analyst's prompt does not say it is on the base branch")
	}
	if report.Checkpoint != nil && report.Checkpoint.Commit != "" {
		t.Fatalf("a base-branch review recorded a reviewed commit: %+v", report.Checkpoint)
	}
}
