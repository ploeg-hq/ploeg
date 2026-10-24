package worker

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/harness"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

type rebasingAgent struct {
	t      *testing.T
	script string
	before func()
	after  func()
	output []byte
	env    []string
}

func (a *rebasingAgent) Name() string     { return "rebasing" }
func (a *rebasingAgent) ExpectsLLM() bool { return false }
func (a *rebasingAgent) Run(_ context.Context, spec harness.TaskSpec, env harness.RunEnv) (harness.OutcomeReport, error) {
	a.env = env.BaseEnv
	if a.before != nil {
		a.before()
	}
	cmd := exec.Command("sh", "-ec", a.script)
	cmd.Dir, cmd.Env = env.RepoDir, append(append([]string{}, env.BaseEnv...), "BRANCH="+spec.Branch, "GIT_EDITOR=true")
	out, err := cmd.CombinedOutput()
	a.output = out
	if a.after != nil {
		a.after()
	}
	return harness.OutcomeReport{}, err
}

func baseGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=base", "-c", "user.email=base@example.com"}, args...)...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func (f *writerForge) advanceBase(t *testing.T, files map[string]string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "base")
	baseGit(t, "", "clone", "-q", "--branch", "development", f.bare, dir)
	writeTree(t, dir, files)
	baseGit(t, dir, "add", ".")
	baseGit(t, dir, "commit", "-qm", "the base moves on")
	baseGit(t, dir, "push", "-q", "origin", "development")
}

func (f *writerForge) baseHead(t *testing.T) string {
	t.Helper()
	return baseGit(t, "", "--git-dir", f.bare, "rev-parse", "refs/heads/development")
}

func (f *writerForge) branchContainsBase(t *testing.T) bool {
	t.Helper()
	cmd := exec.Command("git", "--git-dir", f.bare, "merge-base", "--is-ancestor", "refs/heads/development", "refs/heads/"+writerBranch)
	return cmd.Run() == nil
}

func (f *writerForge) branchFile(t *testing.T, path string) string {
	t.Helper()
	return baseGit(t, "", "--git-dir", f.bare, "show", "refs/heads/"+writerBranch+":"+path)
}

func claimWithFastCheck(cmds ...string) *ClaimResponse {
	c := writerClaim()
	c.FastCheck = cmds
	return c
}

const (
	featureOnMain = "printf 'package main\\n\\nconst feature = 1\\n' > main.go\n"
	baseOnMain    = "package main\n\nconst base = 1\n"
	agentDelivers = "git checkout -q -b \"$BRANCH\"\n" + featureOnMain + "git commit -qam feature\n"
	agentPushes   = "git push -q origin HEAD:refs/heads/\"$BRANCH\"\n"
)

func TestAWriterRebasesOntoAConflictingBaseAndPassesTheFastCheckBeforeReview(t *testing.T) {
	forge := newWriterForge(t, false, pullsForPushedBranch)
	agent := &rebasingAgent{t: t,
		before: func() { forge.advanceBase(t, map[string]string{"main.go": baseOnMain}) },
		script: agentDelivers + `git fetch -q origin development
if ! git rebase -q origin/development >/dev/null 2>&1; then
  printf 'package main\n\nconst base = 1\n\nconst feature = 1\n' > main.go
  git add main.go
  git rebase --continue >/dev/null
fi
"$PLOEG_FAST_CHECK_SCRIPT"
git push -q --force-with-lease origin HEAD:refs/heads/"$BRANCH"
`}
	report := runWithSandboxOn(t, forge, claimWithFastCheck("grep -q 'const base' main.go", "grep -q 'const feature' main.go"), agent, Config{})

	if report.Outcome != work.OutcomePROpened {
		t.Fatalf("outcome = %q (%s), want pr_opened; agent output:\n%s", report.Outcome, report.StuckReason, agent.output)
	}
	if !strings.Contains(string(agent.output), "ploeg-fast-check: all 2 checks passed") {
		t.Errorf("the agent did not pass the fast check inside the Run:\n%s", agent.output)
	}
	if !forge.branchContainsBase(t) {
		t.Error("the pull request's branch does not contain the base branch's head")
	}
	if got := forge.branchFile(t, "main.go"); !strings.Contains(got, "const base = 1") || !strings.Contains(got, "const feature = 1") {
		t.Errorf("main.go on the branch = %q, want both sides kept", got)
	}
	if !strings.Contains(report.Findings, baseSyncHeading) || !strings.Contains(report.Findings, "already contains `development`") {
		t.Errorf("findings do not record the branch as up to date with its base:\n%s", report.Findings)
	}
	if report.Delivery == nil || report.Delivery.Head != forge.branchHead() {
		t.Errorf("delivery = %+v, want the pushed head %s", report.Delivery, forge.branchHead())
	}
}

func TestTheWorkerRebasesABranchTheBaseMovedUnderAndRunsTheFastCheckBeforePushing(t *testing.T) {
	forge := newWriterForge(t, false, pullsForPushedBranch)
	agent := &rebasingAgent{t: t, script: agentDelivers + agentPushes,
		after: func() { forge.advanceBase(t, map[string]string{"CHANGELOG.md": "base\n"}) }}
	report := runWithSandboxOn(t, forge, claimWithFastCheck("test -f CHANGELOG.md", "grep -q 'const feature' main.go"), agent,
		Config{VerifyCommands: []string{"test -f CHANGELOG.md"}})

	if report.Outcome != work.OutcomePROpened {
		t.Fatalf("outcome = %q (%s), want pr_opened", report.Outcome, report.StuckReason)
	}
	if !forge.branchContainsBase(t) {
		t.Fatal("the worker did not push a branch that contains the base branch's head")
	}
	if !strings.Contains(report.Summary, "[rebased onto development]") {
		t.Errorf("summary = %q, want it to say the branch was rebased", report.Summary)
	}
	for _, want := range []string{"Ploeg rebased", "| `test -f CHANGELOG.md` | passed |", "| `grep -q 'const feature' main.go` | passed |"} {
		if !strings.Contains(report.Findings, want) {
			t.Errorf("findings lack %q:\n%s", want, report.Findings)
		}
	}
	if v := report.Verification; v == nil || v.Result != harness.VerificationPassed || v.Commit != forge.branchHead() {
		t.Errorf("verification = %+v, want a pass on the rebased head %s", v, forge.branchHead())
	}
	if report.Delivery == nil || report.Delivery.Head != forge.branchHead() {
		t.Errorf("delivery = %+v, want the rebased head", report.Delivery)
	}
}

func TestAConflictTheAgentCouldNotResolveEndsTheRunForAPersonNamingThePaths(t *testing.T) {
	forge := newWriterForge(t, false, pullsForPushedBranch)
	var agentHead string
	agent := &rebasingAgent{t: t, script: agentDelivers + agentPushes,
		after: func() {
			agentHead = forge.branchHead()
			forge.advanceBase(t, map[string]string{"main.go": baseOnMain, "notes.md": "base\n"})
		}}
	report := runWithSandboxOn(t, forge, claimWithFastCheck("true"), agent, Config{VerifyCommands: []string{"true"}})

	wantStuck(t, report, "a conflict the agent left must reach a person, not a reviewer", "main.go", "development")
	if !strings.Contains(report.Summary, "main.go") || strings.Contains(report.Summary, "notes.md") {
		t.Errorf("summary = %q, want it to name the conflicting path main.go and only that", report.Summary)
	}
	if head := forge.branchHead(); head != agentHead {
		t.Errorf("branch moved to %s, want the agent's %s left alone", head, agentHead)
	}
	if report.Verification != nil {
		t.Errorf("a conflicted pull request was verified: %+v", report.Verification)
	}
	if len(report.Links) != 1 || !strings.HasSuffix(report.Links[0], "/pulls/3") {
		t.Errorf("links = %v, want the pull request kept for the person", report.Links)
	}
}

func TestARebaseWhoseFastCheckFailsIsNotPushedAndReachesAPerson(t *testing.T) {
	forge := newWriterForge(t, false, pullsForPushedBranch)
	var agentHead string
	agent := &rebasingAgent{t: t, script: agentDelivers + agentPushes,
		after: func() {
			agentHead = forge.branchHead()
			forge.advanceBase(t, map[string]string{"broken": "base\n"})
		}}
	report := runWithSandboxOn(t, forge, claimWithFastCheck("test ! -e broken"), agent, Config{})

	wantStuck(t, report, "a rebase that breaks the fast check must not be pushed", "test ! -e broken")
	if head := forge.branchHead(); head != agentHead {
		t.Errorf("branch moved to %s, want the agent's %s left alone", head, agentHead)
	}
	if !strings.Contains(report.Findings, "| `test ! -e broken` | **failed**") {
		t.Errorf("findings do not show the failed fast check:\n%s", report.Findings)
	}
}

func TestARepositoryWithoutAFastCheckIsStillRebasedAndTheSkipIsRecorded(t *testing.T) {
	forge := newWriterForge(t, false, pullsForPushedBranch)
	agent := &rebasingAgent{t: t, script: agentDelivers + agentPushes,
		after: func() { forge.advanceBase(t, map[string]string{"CHANGELOG.md": "base\n"}) }}
	report := runWithSandboxOn(t, forge, writerClaim(), agent, Config{})

	if report.Outcome != work.OutcomePROpened {
		t.Fatalf("outcome = %q (%s), want pr_opened", report.Outcome, report.StuckReason)
	}
	if !forge.branchContainsBase(t) {
		t.Error("the branch was not rebased onto its base")
	}
	if !strings.Contains(report.Findings, "declares no fast check, so none ran") {
		t.Errorf("findings do not record the skipped fast check:\n%s", report.Findings)
	}
	if _, set := lookupEnv(agent.env, fastCheckScriptEnv); set {
		t.Error("a Run without a fast check was given a fast check script")
	}
}

func TestABranchThatAlreadyContainsItsBaseIsLeftAlone(t *testing.T) {
	forge := newWriterForge(t, false, pullsForPushedBranch)
	var agentHead string
	agent := &rebasingAgent{t: t, script: agentDelivers + agentPushes, after: func() { agentHead = forge.branchHead() }}
	report := runWithSandboxOn(t, forge, claimWithFastCheck("false"), agent, Config{})

	if report.Outcome != work.OutcomePROpened {
		t.Fatalf("outcome = %q (%s), want pr_opened", report.Outcome, report.StuckReason)
	}
	if head := forge.branchHead(); head != agentHead {
		t.Errorf("branch moved to %s, want %s", head, agentHead)
	}
	if strings.Contains(report.Summary, "rebased") || !strings.Contains(report.Findings, "already contains `development`") {
		t.Errorf("summary %q, findings:\n%s", report.Summary, report.Findings)
	}
}
