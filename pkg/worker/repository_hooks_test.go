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
	"github.com/ploeg-hq/ploeg/pkg/harness/adapters/acp"
	"github.com/ploeg-hq/ploeg/pkg/harness/adapters/openhands"
	"github.com/ploeg-hq/ploeg/pkg/llmbroker"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

const shippedHooks = `{"SessionStart":[{"matcher":"*","hooks":[{"type":"command","command":"touch /tmp/ploeg-hooks-ran"}]}]}` + "\n"

type hooksRig struct {
	bare, checkout, remote string
	proxy                  *forgeTokenProxy
	fence                  *repositoryHooksFence
}

func newHooksRig(t *testing.T, base map[string]string) *hooksRig {
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
	fence := &repositoryHooksFence{dir: checkout, cloneURL: remote, base: "development", start: start, token: staticForgeToken("real-forge-token").current}
	p, err := startForgeTokenProxy(repo, staticForgeToken("real-forge-token"), forgeWriter, writerBranch, testLeakScope(newLeakWatch()), fence)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.close)
	return &hooksRig{bare: bare, checkout: checkout, remote: remote, proxy: p, fence: fence}
}

func (rig *hooksRig) push(t *testing.T) error {
	t.Helper()
	cmd := exec.Command("git", "push", rig.remote, "HEAD:refs/heads/"+writerBranch)
	cmd.Dir = rig.checkout
	cmd.Env = append([]string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir()}, rig.proxy.gitEnvironment()...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%w: %s", err, out)
	}
	return nil
}

func (rig *hooksRig) forgeHasBranch() bool {
	out, _ := exec.Command("git", "-C", rig.bare, "show-ref").CombinedOutput()
	return strings.Contains(string(out), "refs/heads/"+writerBranch)
}

func TestAnOpenHandsRunMayNotPushAChangeToTheRepositorysHooksFile(t *testing.T) {
	for name, tc := range map[string]struct {
		base   map[string]string
		change func(t *testing.T, checkout string)
		named  string
	}{
		"deleting the shipped file": {
			base:   map[string]string{"main.go": "package main\n", openhands.RepositoryHooksPath: shippedHooks},
			change: func(t *testing.T, dir string) { gitIn(t, dir, "rm", "-q", openhands.RepositoryHooksPath) },
			named:  "D " + openhands.RepositoryHooksPath,
		},
		"editing the shipped file": {
			base: map[string]string{"main.go": "package main\n", openhands.RepositoryHooksPath: shippedHooks},
			change: func(t *testing.T, dir string) {
				writeTree(t, dir, map[string]string{openhands.RepositoryHooksPath: `{}`})
			},
			named: "M " + openhands.RepositoryHooksPath,
		},
		"adding a hooks file": {
			base: map[string]string{"main.go": "package main\n"},
			change: func(t *testing.T, dir string) {
				writeTree(t, dir, map[string]string{openhands.RepositoryHooksPath: `{}`})
			},
			named: "A " + openhands.RepositoryHooksPath,
		},
		"replacing .openhands with a link": {
			base: map[string]string{"main.go": "package main\n"},
			change: func(t *testing.T, dir string) {
				if err := os.Symlink("/tmp", filepath.Join(dir, openhands.RepositoryHooksDir)); err != nil {
					t.Fatal(err)
				}
			},
			named: "A " + openhands.RepositoryHooksDir,
		},
	} {
		t.Run(name, func(t *testing.T) {
			rig := newHooksRig(t, tc.base)
			writeTree(t, rig.checkout, map[string]string{"main.go": "package main\n\nfunc main() {}\n"})
			tc.change(t, rig.checkout)
			gitIn(t, rig.checkout, "add", "-A")
			gitIn(t, rig.checkout, "commit", "-qm", "change")

			if err := rig.push(t); err == nil {
				t.Fatal("a push that changes the repository's OpenHands hooks reached the forge")
			}
			if rig.forgeHasBranch() {
				t.Fatal("the forge has the run branch after a refused push")
			}
			got := rig.fence.checkDelivered(harness.OutcomeReport{Outcome: work.OutcomeNoChangeNeeded}, "")
			if got.Outcome != work.OutcomeStuck || got.Summary != repositoryHooksSummary || !strings.Contains(got.StuckReason, tc.named) {
				t.Errorf("report = %+v, want stuck repository_hooks naming %q", got, tc.named)
			}
		})
	}
}

func TestAnOpenHandsRunMayPushWorkThatLeavesTheHooksFileAlone(t *testing.T) {
	rig := newHooksRig(t, map[string]string{"main.go": "package main\n", openhands.RepositoryHooksPath: shippedHooks})
	writeTree(t, rig.checkout, map[string]string{"main.go": "package main\n\nfunc main() {}\n"})
	gitIn(t, rig.checkout, "commit", "-qam", "change")

	if err := rig.push(t); err != nil {
		t.Fatalf("a push that leaves the hooks file alone was refused: %v", err)
	}
	report := harness.OutcomeReport{Outcome: work.OutcomePROpened}
	if got := rig.fence.checkDelivered(report, gitIn(t, rig.checkout, "rev-parse", "HEAD")); got.Outcome != work.OutcomePROpened {
		t.Errorf("outcome = %q (%s), want the pull request kept", got.Outcome, got.StuckReason)
	}
}

func TestAnOpenHandsDeliveryThatBypassedTheProxyAndChangedTheHooksEndsStuck(t *testing.T) {
	rig := newHooksRig(t, map[string]string{"main.go": "package main\n", openhands.RepositoryHooksPath: shippedHooks})
	writeTree(t, rig.checkout, map[string]string{openhands.RepositoryHooksPath: `{}`})
	gitIn(t, rig.checkout, "commit", "-qam", "change")
	gitIn(t, rig.checkout, "push", "-q", rig.bare, "HEAD:refs/heads/"+writerBranch)

	got := rig.fence.checkDelivered(harness.OutcomeReport{Outcome: work.OutcomePROpened}, gitIn(t, rig.checkout, "rev-parse", "HEAD"))
	wantStuck(t, got, "the delivered branch changes the hooks file", "repository_hooks", "M "+openhands.RepositoryHooksPath, "remove it before review")
}

func TestAnOpenHandsYieldThatWouldChangeTheHooksFilePushesNothing(t *testing.T) {
	rig := newHooksRig(t, map[string]string{"main.go": "package main\n", openhands.RepositoryHooksPath: shippedHooks})
	writeTree(t, rig.checkout, map[string]string{"main.go": "package main\n\nfunc main() {}\n"})
	if err := os.Remove(filepath.Join(rig.checkout, filepath.FromSlash(openhands.RepositoryHooksPath))); err != nil {
		t.Fatal(err)
	}

	y := yieldWork(context.Background(), yieldCheck{writes: true, dir: rig.checkout, cloneURL: rig.remote, token: "real-forge-token",
		branch: writerBranch, baseline: writerBaseline{start: rig.fence.start}, leaks: testLeakScope(newLeakWatch()), hooks: rig.fence})

	if y.Pushed || !strings.Contains(y.Detail, "repository_hooks") || !strings.Contains(y.Detail, "D "+openhands.RepositoryHooksPath) {
		t.Fatalf("yield = %+v, want nothing pushed and the hooks file named", y)
	}
	if rig.forgeHasBranch() {
		t.Fatal("the yield pushed a change to the hooks file")
	}
}

func TestLoadsRepositoryHooksNamesBothOpenHandsHarnesses(t *testing.T) {
	profiled, err := acp.New("openhands", acp.ProfileOverrides{}, acp.Options{})
	if err != nil {
		t.Fatal(err)
	}
	other, err := acp.New("goose", acp.ProfileOverrides{}, acp.Options{})
	if err != nil {
		t.Fatal(err)
	}
	for adapter, want := range map[harness.Adapter]bool{
		openhands.NewRunner(""): true,
		profiled:                true,
		other:                   false,
	} {
		if got := loadsRepositoryHooks(adapter); got != want {
			t.Errorf("loadsRepositoryHooks(%s) = %v, want %v", adapter.Name(), got, want)
		}
	}
}

func openHandsAgent(t *testing.T, duringRun string) harness.Adapter {
	t.Helper()
	seen := filepath.Join(t.TempDir(), "seen")
	script := "#!/bin/sh\n" +
		"if [ -e " + openhands.RepositoryHooksPath + " ]; then echo present > " + seen + "; else echo absent > " + seen + "; fi\n" +
		duringRun + "\n"
	bin := filepath.Join(t.TempDir(), "entrypoint.sh")
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if got, err := os.ReadFile(seen); err != nil || strings.TrimSpace(string(got)) != "absent" {
			t.Errorf("the OpenHands harness saw the repository's hooks file: %q (%v)", got, err)
		}
	})
	return openhands.NewRunner(bin)
}

func runOpenHandsWriter(t *testing.T, forge *writerForge, adapter harness.Adapter) harness.OutcomeReport {
	t.Helper()
	var rec checkpointRecorder
	w := New(Config{APIURL: rec.server(t), ForgeURL: forge.url, DefaultForge: harness.ForgeForgejo, BuilderToken: "tok",
		RepoOwner: "webgrip", RepoName: "example", BaseBranch: "development", WorkDir: t.TempDir(), ForgeTokenIsolation: ForgeTokenIsolationProxy},
		adapter, llmbroker.Static{}, discardLog())
	claimed := &ClaimResponse{RunToken: "rt", Role: "builder", Writes: true, WorkItem: work.WorkItem{ID: "1", ExternalID: "7", Title: "t"}}
	return w.execute(context.Background(), claimed, writerBranch, "trace", "", "")
}

const commitAndPush = `git -c user.name=t -c user.email=t@example.com add -A >/dev/null 2>&1; ` +
	`git -c user.name=t -c user.email=t@example.com commit -qm work >/dev/null 2>&1; ` +
	`git push -q origin HEAD:refs/heads/` + writerBranch + ` >/dev/null 2>&1`

func TestAnOpenHandsWriterOnARepositoryThatShipsHooksDeliversTheFileUnchanged(t *testing.T) {
	forge := newWriterForge(t, false, pullsForPushedBranch)
	forge.advanceBase(t, map[string]string{openhands.RepositoryHooksPath: shippedHooks})

	report := runOpenHandsWriter(t, forge, openHandsAgent(t, `printf '// x\n' >> main.go; `+commitAndPush))

	if !forge.hasBranch() {
		t.Fatalf("the writer's work did not reach the forge: %+v", report)
	}
	if report.Outcome != work.OutcomePROpened {
		t.Errorf("outcome = %q (%s), want pr_opened", report.Outcome, report.StuckReason)
	}
	if got := forge.branchFile(t, openhands.RepositoryHooksPath); got+"\n" != shippedHooks {
		t.Errorf("the delivered branch's %s = %q, want the shipped bytes", openhands.RepositoryHooksPath, got)
	}
	if diff := baseGit(t, "", "--git-dir", forge.bare, "diff", "--name-only", "refs/heads/development", "refs/heads/"+writerBranch); diff != "main.go" {
		t.Errorf("the pull request's diff = %q, want only main.go", diff)
	}
}

func TestAnOpenHandsWriterThatWritesAHooksFileDeliversNoneOfIt(t *testing.T) {
	forge := newWriterForge(t, false, pullsForPushedBranch)
	write := `mkdir -p .openhands; printf '{"SessionStart":[]}' > ` + openhands.RepositoryHooksPath + `; printf '// x\n' >> main.go; ` + commitAndPush

	report := runOpenHandsWriter(t, forge, openHandsAgent(t, write))

	if forge.hasBranch() {
		t.Fatal("a branch carrying an agent-written OpenHands hooks file reached the forge")
	}
	wantStuck(t, report, "the proxy refused the push", "repository_hooks", "A "+openhands.RepositoryHooksPath)
}
