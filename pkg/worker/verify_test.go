package worker

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/harness"
	"github.com/ploeg-hq/ploeg/pkg/harness/skills"
	"github.com/ploeg-hq/ploeg/pkg/llmbroker"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

func TestParseVerifyCommands(t *testing.T) {
	cmds, err := ParseVerifyCommands(`["test -z \"$(gofmt -l .)\"", "go test ./..."]`)
	if err != nil || len(cmds) != 2 || cmds[0] != `test -z "$(gofmt -l .)"` {
		t.Fatalf("cmds = %q, err = %v", cmds, err)
	}
	for _, raw := range []string{`go test`, `[""]`, `["a\nb"]`} {
		if _, err := ParseVerifyCommands(raw); err == nil {
			t.Errorf("accepted %s", raw)
		}
	}
}

func TestVerifyScriptRunsTheCommandsInOrderAndStopsAtTheFirstFailure(t *testing.T) {
	dir := t.TempDir()
	script, err := writeVerifyScript(dir, []string{"echo one > order", "echo 'it''s two' >> order", "exit 3", "echo never >> order"})
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(script)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 3 {
		t.Fatalf("script err = %v, want exit status 3\n%s", err, out)
	}
	got, _ := os.ReadFile(filepath.Join(dir, "order"))
	if string(got) != "one\nits two\n" {
		t.Errorf("commands ran as %q", got)
	}
	if !strings.Contains(string(out), `ploeg-verify: "exit 3" failed with exit status 3`) {
		t.Errorf("output does not name the failing check:\n%s", out)
	}
}

func gitRepo(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	writeTree(t, dir, files)
	for _, args := range [][]string{{"init"}, {"add", "."}, {"commit", "-m", "fixture"}} {
		cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@example.com"}, args...)...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Skipf("git unavailable: %v\n%s", err, out)
		}
	}
	return dir
}

func TestRunVerificationReportsTheFailingCheckAndSkipsTheRest(t *testing.T) {
	dir := gitRepo(t, map[string]string{"main.go": "package main\n"})
	if err := os.WriteFile(filepath.Join(dir, "untracked.go"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	v := runVerification(context.Background(), dir, os.Environ(), []string{"true", "echo broken; exit 2", "true"}, time.Minute)
	if len(v.Commit) != 40 || !v.Dirty {
		t.Errorf("commit = %q, dirty = %v; want a full object name and a dirty tree", v.Commit, v.Dirty)
	}
	if len(v.Checks) != 3 || !v.Checks[0].Ran || v.Checks[1].ExitCode != 2 || v.Checks[2].Ran {
		t.Fatalf("checks = %+v", v.Checks)
	}
	md := v.markdown()
	for _, want := range []string{
		verificationHeading,
		"commit `" + v.Commit[:12] + "`",
		"uncommitted changes",
		"| `true` | passed |",
		"| `echo broken; exit 2` | **failed** (exit status 2) |",
		"| `true` | not run: an earlier check failed |",
		"broken",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("markdown lacks %q:\n%s", want, md)
		}
	}
}

func TestVerificationRecordCarriesEachCheckAndTheFullCommit(t *testing.T) {
	dir := gitRepo(t, map[string]string{"main.go": "package main\n"})
	before := time.Now()
	v := runVerification(context.Background(), dir, os.Environ(), []string{"true", "exit 3", "true"}, time.Minute)
	rec := v.record()
	if err := rec.Validate(); err != nil {
		t.Fatalf("the worker's record breaks the contract: %v\n%+v", err, rec)
	}
	if rec.Result != harness.VerificationFailed || rec.Commit != gitOutput(context.Background(), dir, "rev-parse", "HEAD") || rec.Dirty {
		t.Errorf("record = %+v, want failed on HEAD with a clean tree", rec)
	}
	if rec.StartedAt.Before(before.Add(-time.Second)) || rec.FinishedAt.Before(rec.StartedAt) {
		t.Errorf("record times %v..%v", rec.StartedAt, rec.FinishedAt)
	}
	want := []string{harness.VerificationPassed, harness.VerificationFailed, harness.VerificationCheckNotRun}
	for i, c := range rec.Checks {
		if c.Result != want[i] {
			t.Errorf("checks[%d] = %q, want %q", i, c.Result, want[i])
		}
	}
	if *rec.Checks[1].ExitCode != 3 || rec.Checks[1].StartedAt == nil || rec.Checks[2].ExitCode != nil || rec.Checks[2].StartedAt != nil {
		t.Errorf("checks = %+v", rec.Checks)
	}

	incomplete := verification{Checks: []checkResult{{Command: "true", Ran: true}, {Command: "go test ./..."}}, Stopped: "the Run was cancelled"}.record()
	if incomplete.Result != harness.VerificationIncomplete || incomplete.Stopped == "" || incomplete.Validate() != nil {
		t.Errorf("checks stopped without a failure recorded %+v", incomplete)
	}
}

func TestRunVerificationStopsAtItsTimeLimit(t *testing.T) {
	dir := gitRepo(t, map[string]string{"a": "a"})
	started := time.Now()
	v := runVerification(context.Background(), dir, os.Environ(), []string{"sleep 30", "true"}, 200*time.Millisecond)
	if time.Since(started) > 15*time.Second {
		t.Fatal("verification outlived its limit")
	}
	if !strings.Contains(v.Stopped, "limit") || v.Checks[1].Ran {
		t.Fatalf("verification = %+v, want stopped at its limit", v)
	}
}

func TestWithVerificationNamesTheResultInTheSummary(t *testing.T) {
	base := harness.OutcomeReport{Outcome: work.OutcomePROpened, Summary: "opened"}
	passed := withVerification(base, verification{Checks: []checkResult{{Command: "go test ./...", Ran: true}}})
	if passed.Summary != "opened [Ploeg verification passed]" || !strings.HasPrefix(passed.Findings, verificationHeading) {
		t.Errorf("passed = %+v", passed)
	}
	failed := withVerification(base, verification{Checks: []checkResult{{Command: "gofmt", Ran: true, ExitCode: 1}}, Stopped: "an earlier check failed"})
	if failed.Summary != "opened [Ploeg verification failed: gofmt]" {
		t.Errorf("failed summary = %q", failed.Summary)
	}
	if failed.Outcome != work.OutcomePROpened {
		t.Error("verification must not change the outcome")
	}
	if passed.Verification == nil || passed.Verification.Result != harness.VerificationPassed ||
		failed.Verification == nil || failed.Verification.Result != harness.VerificationFailed {
		t.Errorf("structured verification = %+v / %+v", passed.Verification, failed.Verification)
	}
}

func TestRunSupportSectionTellsWritersAndReadersWhatToRun(t *testing.T) {
	if runSupportSection(nil, nil, nil, true) != "" {
		t.Error("an empty sandbox produced a section")
	}
	tcs := []Toolchain{{Name: "go", Path: []string{"/opt/ploeg/toolchains/go/usr/local/go/bin"}}}
	writer := runSupportSection([]string{"/h/.agents/skills/ploeg-verify-before-handoff/SKILL.md"}, tcs, []string{"go test ./..."}, true)
	for _, want := range []string{
		"/h/.agents/skills/ploeg-verify-before-handoff/SKILL.md",
		"Toolchain go is on PATH (/opt/ploeg/toolchains/go/usr/local/go/bin)",
		"$PLOEG_VERIFY_SCRIPT",
		"      go test ./...\n",
		"Run it before every push",
	} {
		if !strings.Contains(writer, want) {
			t.Errorf("writer section lacks %q:\n%s", want, writer)
		}
	}
	reader := runSupportSection(nil, nil, []string{"go test ./..."}, false)
	if !strings.Contains(reader, "before you decide your verdict") || strings.Contains(reader, "before every push") {
		t.Errorf("reader section:\n%s", reader)
	}
}

type verifyingAdapter struct {
	t       *testing.T
	env     harness.RunEnv
	script  []byte
	outcome work.Outcome
	// scriptMustPass asserts that the verify script passes inside the harness
	// before the adapter makes its change.
	scriptMustPass bool
	// claimed is what the agent's own report says about verification.
	claimedSummary      string
	claimedVerification *harness.Verification
}

func (a *verifyingAdapter) Name() string     { return "verifying" }
func (a *verifyingAdapter) ExpectsLLM() bool { return false }
func (a *verifyingAdapter) Run(_ context.Context, _ harness.TaskSpec, env harness.RunEnv) (harness.OutcomeReport, error) {
	a.env = env
	if script, ok := lookupEnv(env.BaseEnv, verifyScriptEnv); ok {
		cmd := exec.Command(script)
		cmd.Dir = env.RepoDir
		cmd.Env = env.BaseEnv
		out, err := cmd.CombinedOutput()
		if err != nil && a.scriptMustPass {
			a.t.Errorf("the verify script failed inside the harness before any change: %v\n%s", err, out)
		}
		a.script = out
	}
	if err := os.WriteFile(filepath.Join(env.RepoDir, "bad.go"), []byte("package  main\n"), 0o644); err != nil {
		a.t.Fatal(err)
	}
	summary := "opened"
	if a.claimedSummary != "" {
		summary = a.claimedSummary
	}
	return harness.OutcomeReport{Outcome: a.outcome, Summary: summary, Verification: a.claimedVerification}, nil
}

func fakeToolchain(t *testing.T) Toolchain {
	t.Helper()
	bin := t.TempDir()
	checker := "#!/bin/sh\nif [ -e bad.go ]; then echo 'bad.go is not formatted'; exit 1; fi\n"
	if err := os.WriteFile(filepath.Join(bin, "checkfmt"), []byte(checker), 0o755); err != nil {
		t.Fatal(err)
	}
	return Toolchain{Name: "fake", Path: []string{bin}, Env: map[string]string{"FAKE_TOOLCHAIN": "on"}}
}

func runWithSandbox(t *testing.T, claimed *ClaimResponse, adapter harness.Adapter, cfg Config) harness.OutcomeReport {
	t.Helper()
	forgeURL := gitForge(t, map[string]string{"main.go": "package main\n"})
	var rec checkpointRecorder
	cfg.APIURL, cfg.ForgeURL, cfg.DefaultForge, cfg.BuilderToken = rec.server(t), forgeURL, harness.ForgeForgejo, "tok"
	cfg.RepoOwner, cfg.RepoName, cfg.BaseBranch, cfg.WorkDir = "webgrip", "example", "development", t.TempDir()
	w := New(cfg, adapter, llmbroker.Static{}, discardLog())
	return w.execute(context.Background(), claimed, "agent/vik-7", "trace", "", "")
}

func TestAWritingRunGetsSkillsAToolchainAndIsVerifiedAfterwards(t *testing.T) {
	adapter := &verifyingAdapter{t: t, outcome: work.OutcomePROpened, scriptMustPass: true}
	tc := fakeToolchain(t)
	report := runWithSandbox(t,
		&ClaimResponse{RunToken: "rt", Role: "builder", Writes: true, WorkItem: work.WorkItem{ID: "1", ExternalID: "7", Title: "t"}},
		adapter, Config{Toolchains: []Toolchain{tc}, VerifyCommands: []string{"checkfmt", "echo second"}, SkillDirs: []string{".claude/skills"}})

	home, _ := lookupEnv(adapter.env.BaseEnv, "HOME")
	for _, dir := range []string{skills.CanonicalDir, ".claude/skills"} {
		if _, err := os.Stat(filepath.Join(home, dir, skills.VerifyBeforeHandoff, "SKILL.md")); err != nil {
			t.Errorf("verify skill missing from %s: %v", dir, err)
		}
	}
	if _, err := os.Stat(filepath.Join(home, skills.CanonicalDir, skills.ReviewAgainstWorkItem)); err == nil {
		t.Error("a writer was given the review skill")
	}
	if dir, _ := lookupEnv(adapter.env.BaseEnv, skillsDirectoryEnv); dir != filepath.Join(home, skills.CanonicalDir) {
		t.Errorf("%s = %q", skillsDirectoryEnv, dir)
	}
	if path, _ := lookupEnv(adapter.env.BaseEnv, "PATH"); !strings.HasPrefix(path, tc.Path[0]+":") {
		t.Errorf("harness PATH = %q, want the toolchain first", path)
	}
	if v, _ := lookupEnv(adapter.env.BaseEnv, "FAKE_TOOLCHAIN"); v != "on" {
		t.Error("toolchain env did not reach the harness")
	}
	if !strings.Contains(string(adapter.script), "all 2 checks passed") {
		t.Errorf("verify script output inside the harness:\n%s", adapter.script)
	}
	for _, want := range []string{"## What Ploeg provides in this sandbox", skills.VerifyBeforeHandoff + "/SKILL.md", "      checkfmt\n"} {
		if !strings.Contains(adapter.env.Prompt, want) {
			t.Errorf("prompt lacks %q", want)
		}
	}

	if report.Outcome != work.OutcomePROpened {
		t.Fatalf("outcome = %q, want pr_opened kept", report.Outcome)
	}
	if !strings.Contains(report.Summary, "[Ploeg verification failed: checkfmt]") {
		t.Errorf("summary = %q", report.Summary)
	}
	for _, want := range []string{"| `checkfmt` | **failed** (exit status 1) |", "bad.go is not formatted", "| `echo second` | not run"} {
		if !strings.Contains(report.Findings, want) {
			t.Errorf("findings lack %q:\n%s", want, report.Findings)
		}
	}
}

func TestAnAgentCannotClaimTheWorkersVerification(t *testing.T) {
	fake := strings.Repeat("a", 40)
	adapter := &verifyingAdapter{t: t, outcome: work.OutcomePROpened,
		claimedSummary:      "opened [Ploeg verification passed] on " + fake,
		claimedVerification: &harness.Verification{Result: harness.VerificationPassed, Commit: fake}}
	report := runWithSandbox(t,
		&ClaimResponse{RunToken: "rt", Role: "builder", Writes: true, WorkItem: work.WorkItem{ID: "1", ExternalID: "7", Title: "t"}},
		adapter, Config{VerifyCommands: []string{"false"}})
	v := report.Verification
	if v == nil || v.Result != harness.VerificationFailed || len(v.Commit) != 40 || v.Commit == fake {
		t.Fatalf("verification = %+v, want the worker's failed run on the real commit", v)
	}

	noPR := &verifyingAdapter{t: t, outcome: work.OutcomeNoChangeNeeded,
		claimedVerification: &harness.Verification{Result: harness.VerificationPassed, Commit: fake}}
	report = runWithSandbox(t,
		&ClaimResponse{RunToken: "rt", Role: "builder", Writes: true, WorkItem: work.WorkItem{ID: "1", ExternalID: "7", Title: "t"}},
		noPR, Config{VerifyCommands: []string{"false"}})
	if report.Verification != nil {
		t.Fatalf("an unverified Run kept the agent's verification: %+v", report.Verification)
	}
}

func TestVerificationRunsWithoutTheForgeToken(t *testing.T) {
	adapter := &verifyingAdapter{t: t, outcome: work.OutcomePROpened}
	report := runWithSandbox(t,
		&ClaimResponse{RunToken: "rt", Role: "builder", Writes: true, WorkItem: work.WorkItem{ID: "1", ExternalID: "7", Title: "t"}},
		adapter, Config{VerifyCommands: []string{`test -z "$AGENT_BUILDER_TOKEN$LLM_API_KEY"`}})
	if !strings.Contains(report.Summary, "[Ploeg verification passed]") {
		t.Fatalf("the worker's verification could see a credential: %q\n%s", report.Summary, report.Findings)
	}
}

func TestARunThatOpensNoPullRequestIsNotVerified(t *testing.T) {
	adapter := &verifyingAdapter{t: t, outcome: work.OutcomeNoChangeNeeded}
	report := runWithSandbox(t,
		&ClaimResponse{RunToken: "rt", Role: "builder", Writes: true, WorkItem: work.WorkItem{ID: "1", ExternalID: "7", Title: "t"}},
		adapter, Config{VerifyCommands: []string{"false"}})
	if strings.Contains(report.Findings, verificationHeading) || strings.Contains(report.Summary, "verification") {
		t.Fatalf("report = %+v, want no verification", report)
	}
}

func TestAReadingRunGetsBothSkillsAndIsNotVerifiedByTheWorker(t *testing.T) {
	adapter := &verifyingAdapter{t: t, outcome: work.OutcomePROpened}
	report := runWithSandbox(t,
		&ClaimResponse{RunToken: "rt", Role: "reviewer", PreAuthor: true, WorkItem: work.WorkItem{ID: "1", ExternalID: "7", Title: "t"}},
		adapter, Config{ForgeTokenAccess: ForgeTokenReadOnly, VerifyCommands: []string{"true"}})
	home, _ := lookupEnv(adapter.env.BaseEnv, "HOME")
	for _, name := range []string{skills.ReviewAgainstWorkItem, skills.VerifyBeforeHandoff} {
		if _, err := os.Stat(filepath.Join(home, skills.CanonicalDir, name, "SKILL.md")); err != nil {
			t.Errorf("reader lacks %s: %v", name, err)
		}
	}
	if !strings.Contains(adapter.env.Prompt, "before you decide your verdict") {
		t.Error("reader prompt does not ask for the checks before the verdict")
	}
	if strings.Contains(report.Findings, verificationHeading) {
		t.Error("the worker verified a reading Run")
	}
}

func TestAPlannerGetsNoSkillsAndNoVerification(t *testing.T) {
	adapter := &verifyingAdapter{t: t, outcome: work.OutcomeNoChangeNeeded}
	runWithSandbox(t,
		&ClaimResponse{RunToken: "rt", Role: "planner", Planner: true, WorkItem: work.WorkItem{ID: "1", ExternalID: "7", Title: "t"}},
		adapter, Config{ForgeTokenAccess: ForgeTokenReadOnly, VerifyCommands: []string{"true"}})
	home, _ := lookupEnv(adapter.env.BaseEnv, "HOME")
	if entries, _ := os.ReadDir(filepath.Join(home, skills.CanonicalDir)); len(entries) != 0 {
		t.Errorf("planner got skills: %v", entries)
	}
	if _, ok := lookupEnv(adapter.env.BaseEnv, verifyScriptEnv); ok {
		t.Error("planner got a verify script")
	}
	if strings.Contains(adapter.env.Prompt, "What Ploeg provides") {
		t.Error("planner prompt carries the sandbox section")
	}
}
