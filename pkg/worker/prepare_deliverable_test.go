package worker

import (
	"context"
	"fmt"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/harness"
	"github.com/ploeg-hq/ploeg/pkg/llmbroker"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

type prepareRig struct {
	bare, checkout string
	proxy          *forgeTokenProxy
	fence          *deliverableFence
	remote         string
}

func newPrepareRig(t *testing.T, base map[string]string) *prepareRig {
	t.Helper()
	execPath, err := exec.Command("git", "--exec-path").Output()
	if err != nil {
		t.Skip("git is not available")
	}
	backend := filepath.Join(strings.TrimSpace(string(execPath)), "git-http-backend")
	if _, err := os.Stat(backend); err != nil {
		t.Skip("git-http-backend is not available")
	}
	projects := t.TempDir()
	bare := filepath.Join(projects, "webgrip", "example.git")
	gitIn(t, "", "init", "--bare", bare)
	gitIn(t, bare, "config", "http.receivepack", "true")
	seed := t.TempDir()
	writeTree(t, seed, base)
	gitIn(t, seed, "init")
	gitIn(t, seed, "add", ".")
	gitIn(t, seed, "commit", "-m", "base")
	gitIn(t, seed, "push", bare, "HEAD:refs/heads/development")
	forge := httptest.NewServer(&cgi.Handler{Path: backend, Env: []string{"GIT_PROJECT_ROOT=" + projects, "GIT_HTTP_EXPORT_ALL=1"}})
	t.Cleanup(forge.Close)
	repo := harness.RepoRef{ForgeURL: forge.URL, Owner: "webgrip", Name: "example"}
	remote, err := plainURL(repo.ForgeURL, repo.Owner, repo.Name)
	if err != nil {
		t.Fatal(err)
	}
	checkout := filepath.Join(t.TempDir(), "clone")
	gitIn(t, "", "clone", "--branch", "development", bare, checkout)
	start := gitIn(t, checkout, "rev-parse", "HEAD")
	gitIn(t, checkout, "checkout", "-b", writerBranch)
	fence := &deliverableFence{dir: checkout, cloneURL: remote, base: "development", start: start, token: staticForgeToken("real-forge-token").current}
	p, err := startForgeTokenProxy(repo, staticForgeToken("real-forge-token"), forgeWriter, writerBranch, testLeakScope(newLeakWatch()), fence)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.close)
	return &prepareRig{bare: bare, checkout: checkout, proxy: p, fence: fence, remote: remote}
}

func (rig *prepareRig) commit(t *testing.T, files map[string]string) {
	t.Helper()
	writeTree(t, rig.checkout, files)
	gitIn(t, rig.checkout, "add", "-A")
	gitIn(t, rig.checkout, "commit", "-m", "prepare")
}

func (rig *prepareRig) push(t *testing.T) error {
	t.Helper()
	cmd := exec.Command("git", "push", rig.remote, "HEAD:refs/heads/"+writerBranch)
	cmd.Dir = rig.checkout
	cmd.Env = append([]string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir()}, rig.proxy.gitEnvironment()...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%w: %s", err, out)
	}
	return nil
}

func (rig *prepareRig) forgeHasBranch(t *testing.T) bool {
	t.Helper()
	out, _ := exec.Command("git", "-C", rig.bare, "show-ref").CombinedOutput()
	return strings.Contains(string(out), "refs/heads/"+writerBranch)
}

func TestAPrepareRunMayPushABranchThatOnlyAddsTheRootInstructionFile(t *testing.T) {
	rig := newPrepareRig(t, map[string]string{"main.go": "package main\n"})
	rig.commit(t, map[string]string{"AGENTS.md": "# Example\n"})

	if err := rig.push(t); err != nil {
		t.Fatalf("pushing only AGENTS.md was refused: %v", err)
	}
	if !rig.forgeHasBranch(t) {
		t.Fatal("the run branch did not reach the forge")
	}
	report := harness.OutcomeReport{Outcome: work.OutcomePROpened, Summary: "opened"}
	if got := rig.fence.checkDelivered(report, gitIn(t, rig.checkout, "rev-parse", "HEAD")); got.Outcome != work.OutcomePROpened {
		t.Errorf("outcome = %q (%s), want the pull request kept", got.Outcome, got.StuckReason)
	}
}

func TestAPrepareRunThatChangesAnythingElsePushesNothingAndEndsStuck(t *testing.T) {
	for name, tc := range map[string]struct {
		base, change map[string]string
		named        string
	}{
		"another path as well":        {base: map[string]string{"main.go": "package main\n"}, change: map[string]string{"AGENTS.md": "# x\n", "main.go": "package main\n\nfunc main() {}\n"}, named: "M main.go"},
		"an existing AGENTS.md":       {base: map[string]string{"main.go": "package main\n", "AGENTS.md": "# old\n"}, change: map[string]string{"AGENTS.md": "# new\n"}, named: "M AGENTS.md"},
		"a nested AGENTS.md":          {base: map[string]string{"main.go": "package main\n"}, change: map[string]string{"docs/AGENTS.md": "# x\n"}, named: "A docs/AGENTS.md"},
		"a CLAUDE.md beside the file": {base: map[string]string{"main.go": "package main\n"}, change: map[string]string{"AGENTS.md": "# x\n", "CLAUDE.md": "@AGENTS.md\n"}, named: "A CLAUDE.md"},
	} {
		t.Run(name, func(t *testing.T) {
			rig := newPrepareRig(t, tc.base)
			rig.commit(t, tc.change)

			if err := rig.push(t); err == nil {
				t.Fatal("the push outside the deliverable reached the forge")
			}
			if rig.forgeHasBranch(t) {
				t.Fatal("the forge has the run branch after a refused push")
			}
			got := rig.fence.checkDelivered(harness.OutcomeReport{Outcome: work.OutcomeNoChangeNeeded}, "")
			if got.Outcome != work.OutcomeStuck || got.Summary != outsideDeliverableSummary ||
				!strings.Contains(got.StuckReason, "outside_deliverable") || !strings.Contains(got.StuckReason, tc.named) {
				t.Errorf("report = %+v, want stuck outside_deliverable naming %q", got, tc.named)
			}
		})
	}
}

func TestAPrepareRunCannotHideAChangeBehindItsOwnBaseRef(t *testing.T) {
	rig := newPrepareRig(t, map[string]string{"main.go": "package main\n"})
	rig.commit(t, map[string]string{"main.go": "package main\n\nfunc main() {}\n"})
	gitIn(t, rig.checkout, "update-ref", "refs/remotes/origin/development", "HEAD")
	rig.commit(t, map[string]string{"AGENTS.md": "# x\n"})

	if err := rig.push(t); err == nil {
		t.Fatal("a change hidden behind a moved origin/development reached the forge")
	}
}

func TestTheDeliverableRuleAllowsOnlyOneAddedRootInstructionFile(t *testing.T) {
	for name, tc := range map[string]struct {
		changes []work.ChangedPath
		outside int
	}{
		"only the file":    {changes: []work.ChangedPath{{Status: "A", Path: "AGENTS.md", Mode: "100644"}}},
		"nothing at all":   {outside: 1},
		"a modified file":  {changes: []work.ChangedPath{{Status: "M", Path: "AGENTS.md", Mode: "100644"}}, outside: 2},
		"a deleted path":   {changes: []work.ChangedPath{{Status: "A", Path: "AGENTS.md", Mode: "100644"}, {Status: "D", Path: "README.md"}}, outside: 1},
		"a symbolic link":  {changes: []work.ChangedPath{{Status: "A", Path: "AGENTS.md", Mode: "120000"}}, outside: 2},
		"a gitlink":        {changes: []work.ChangedPath{{Status: "A", Path: "AGENTS.md", Mode: "160000"}}, outside: 2},
		"a case lookalike": {changes: []work.ChangedPath{{Status: "A", Path: "agents.md", Mode: "100644"}}, outside: 2},
	} {
		t.Run(name, func(t *testing.T) {
			if got := work.OutsidePrepareDeliverable(tc.changes); len(got) != tc.outside {
				t.Errorf("outside = %q, want %d entries", got, tc.outside)
			}
		})
	}
}

type namedAdapter struct {
	harness.Adapter
	name string
}

func (a namedAdapter) Name() string { return a.name }

func runPrepareWriter(t *testing.T, forge *writerForge, adapter harness.Adapter, isolation string) harness.OutcomeReport {
	t.Helper()
	var rec checkpointRecorder
	w := New(Config{APIURL: rec.server(t), ForgeURL: forge.url, DefaultForge: harness.ForgeForgejo, BuilderToken: "tok",
		RepoOwner: "webgrip", RepoName: "example", BaseBranch: "development", WorkDir: t.TempDir(), ForgeTokenIsolation: isolation},
		adapter, llmbroker.Static{}, discardLog())
	claimed := &ClaimResponse{RunToken: "rt", Role: "builder", Writes: true,
		WorkItem: work.WorkItem{ID: "1", ExternalID: "7", Title: "Add AGENTS.md to webgrip/example", Kind: work.KindPrepareRepository}}
	return w.execute(context.Background(), claimed, writerBranch, "trace", "", "")
}

func TestAPrepareWriterOnAWorkerWithoutTheForgeProxyEndsStuckBeforeItsHarnessRuns(t *testing.T) {
	forge := newWriterForge(t, false, pullsNone)
	report := runPrepareWriter(t, forge, acpAgentDuringPrompt(t, `printf x > AGENTS.md`), "")

	wantStuck(t, report, "a prepare Run needs the forge proxy", "PLOEG_FORGE_TOKEN_ISOLATION=proxy")
	if report.Summary != "prepare_unsupported_for_team" {
		t.Errorf("summary = %q, want prepare_unsupported_for_team", report.Summary)
	}
}

func TestAPrepareWriterInAClaudeEnvironmentEndsStuckBeforeItsHarnessRuns(t *testing.T) {
	forge := newWriterForge(t, false, pullsNone)
	adapter := namedAdapter{Adapter: acpAgentDuringPrompt(t, `printf x > AGENTS.md`), name: "claude-environment"}
	report := runPrepareWriter(t, forge, adapter, ForgeTokenIsolationProxy)

	wantStuck(t, report, "a Claude environment's runner pushes on its own", "Claude environment")
	if forge.hasBranch() {
		t.Error("the run branch reached the forge")
	}
}

func TestAPrepareWriterThatPushesMoreThanTheFileIsRefusedAtTheProxyAndEndsOutsideTheDeliverable(t *testing.T) {
	forge := newWriterForge(t, false, pullsForPushedBranch)
	push := `printf '# Example\n' > AGENTS.md; printf '// x\n' >> main.go; ` +
		`git -c user.name=t -c user.email=t@example.com add -A >/dev/null 2>&1; ` +
		`git -c user.name=t -c user.email=t@example.com commit -qm prepare >/dev/null 2>&1; ` +
		`git push -q origin HEAD:refs/heads/` + writerBranch + ` >/dev/null 2>&1`
	report := runPrepareWriter(t, forge, acpAgentDuringPrompt(t, push), ForgeTokenIsolationProxy)

	if forge.hasBranch() {
		t.Fatal("a push outside the deliverable reached the forge")
	}
	wantStuck(t, report, "the proxy refused the push", "outside_deliverable", "M main.go")
}

func TestAPrepareYieldThatWouldPushMoreThanTheFilePushesNothing(t *testing.T) {
	rig := newPrepareRig(t, map[string]string{"main.go": "package main\n"})
	writeTree(t, rig.checkout, map[string]string{"AGENTS.md": "# x\n", "main.go": "package main\n\nfunc main() {}\n"})

	y := yieldWork(context.Background(), yieldCheck{writes: true, dir: rig.checkout, cloneURL: rig.remote, token: "real-forge-token",
		branch: writerBranch, baseline: writerBaseline{start: rig.fence.start}, leaks: testLeakScope(newLeakWatch()), deliverable: rig.fence})

	if y.Pushed || !strings.Contains(y.Detail, "outside_deliverable") || !strings.Contains(y.Detail, "M main.go") {
		t.Fatalf("yield = %+v, want nothing pushed and the outside path named", y)
	}
	if rig.forgeHasBranch(t) {
		t.Fatal("the yield pushed work outside the deliverable")
	}
}

func TestAPrepareYieldOfOnlyTheFilePushes(t *testing.T) {
	rig := newPrepareRig(t, map[string]string{"main.go": "package main\n"})
	writeTree(t, rig.checkout, map[string]string{"AGENTS.md": "# x\n"})

	y := yieldWork(context.Background(), yieldCheck{writes: true, dir: rig.checkout, cloneURL: rig.remote, token: "real-forge-token",
		branch: writerBranch, baseline: writerBaseline{start: rig.fence.start}, leaks: testLeakScope(newLeakWatch()), deliverable: rig.fence})

	if !y.Pushed || !rig.forgeHasBranch(t) {
		t.Fatalf("yield = %+v, want only AGENTS.md pushed", y)
	}
}

func TestAPrepareRunMayNotDeliverASymlinkOrAnExecutableAsItsFile(t *testing.T) {
	for name, prepare := range map[string]func(t *testing.T, dir string){
		"a symbolic link": func(t *testing.T, dir string) {
			if err := os.Symlink("README.md", filepath.Join(dir, "AGENTS.md")); err != nil {
				t.Fatal(err)
			}
		},
		"an executable": func(t *testing.T, dir string) {
			if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte("#!/bin/sh\n"), 0o755); err != nil {
				t.Fatal(err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			rig := newPrepareRig(t, map[string]string{"README.md": "# r\n"})
			prepare(t, rig.checkout)
			gitIn(t, rig.checkout, "add", "-A")
			gitIn(t, rig.checkout, "commit", "-m", "prepare")

			if err := rig.push(t); err == nil {
				t.Fatal("an AGENTS.md that is not a regular file reached the forge")
			}
		})
	}
}

func TestAPrepareRunCannotRetargetItsPullRequest(t *testing.T) {
	fence := &deliverableFence{base: "development"}
	for name, tc := range map[string]struct {
		method, path, body string
		refused            bool
	}{
		"open against the base":       {method: "POST", path: "/api/v1/repos/webgrip/example/pulls", body: `{"base":"development","head":"agent/vik-7"}`},
		"open against another branch": {method: "POST", path: "/api/v1/repos/webgrip/example/pulls", body: `{"base":"main","head":"agent/vik-7"}`, refused: true},
		"edit the base":               {method: "PATCH", path: "/api/v1/repos/webgrip/example/pulls/3", body: `{"base":"main"}`, refused: true},
		"edit the title":              {method: "PATCH", path: "/api/v1/repos/webgrip/example/pulls/3", body: `{"title":"t"}`},
		"retarget a merge request":    {method: "PUT", path: "/api/v4/projects/1/merge_requests/3", body: `target_branch=main`, refused: true},
	} {
		t.Run(name, func(t *testing.T) {
			r := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
			_, err := fence.admitAPI(r)
			if (err != nil) != tc.refused {
				t.Errorf("refused = %v (%v), want %v", err != nil, err, tc.refused)
			}
		})
	}
}
