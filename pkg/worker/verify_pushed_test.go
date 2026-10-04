package worker

import (
	"strings"
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/harness"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

func writerClaim() *ClaimResponse {
	return &ClaimResponse{RunToken: "rt", Role: "builder", Writes: true, WorkItem: work.WorkItem{ID: "1", ExternalID: "7", Title: "t"}}
}

func TestVerificationRunsOnThePushedCommitNotOnTheAgentsCheckout(t *testing.T) {
	forge := newWriterForge(t, false, pullsForPushedBranch)
	adapter := &verifyingAdapter{t: t, outcome: work.OutcomePROpened, delivers: true, fixesLocally: true}
	report := runWithSandboxOn(t, forge, writerClaim(), adapter,
		Config{Toolchains: []Toolchain{fakeToolchain(t)}, VerifyCommands: []string{"checkfmt"}})

	v := report.Verification
	if v == nil || v.Result != harness.VerificationFailed {
		t.Fatalf("verification = %+v, want failed: the pushed commit carries bad.go, only the agent's checkout lost it", v)
	}
	if pushed := forge.branchHead(); v.Commit != pushed {
		t.Errorf("verified commit = %q, want the pushed head %q", v.Commit, pushed)
	}
	if v.Dirty {
		t.Error("a fresh checkout of the pushed commit was reported dirty")
	}
	if !strings.Contains(report.Findings, "fresh checkout") {
		t.Errorf("findings do not say the checks ran on a fresh checkout:\n%s", report.Findings)
	}
}

func TestAPullRequestHeadThatIsNotTheBranchIsNotVerified(t *testing.T) {
	forge := newWriterForge(t, false, pullsForPushedBranch)
	stale := strings.Repeat("d", 40)
	forge.staleHead.Store(stale)
	adapter := &verifyingAdapter{t: t, outcome: work.OutcomePROpened, delivers: true}
	report := runWithSandboxOn(t, forge, writerClaim(), adapter, Config{VerifyCommands: []string{"true"}})

	v := report.Verification
	if v == nil || v.Result != harness.VerificationIncomplete {
		t.Fatalf("verification = %+v, want incomplete", v)
	}
	if err := v.Validate(); err != nil {
		t.Errorf("the record is invalid: %v", err)
	}
	if v.Commit != stale {
		t.Errorf("commit = %q, want the pull request's head %q that Ploeg could not verify", v.Commit, stale)
	}
	for _, c := range v.Checks {
		if c.Result != harness.VerificationCheckNotRun {
			t.Errorf("check %q ran on a commit that is not the pull request's head", c.Command)
		}
	}
	if !strings.Contains(v.Stopped, stale[:12]) {
		t.Errorf("stopped = %q, want it to name the expected commit", v.Stopped)
	}
}

func TestAPushWhileTheChecksRunLeavesTheVerificationIncomplete(t *testing.T) {
	forge := newWriterForge(t, false, pullsForPushedBranch)
	adapter := &verifyingAdapter{t: t, outcome: work.OutcomePROpened, delivers: true}
	push := "git -c user.name=t -c user.email=t@example.com commit -q --allow-empty -m moved && git push -q origin HEAD:refs/heads/" + writerBranch
	report := runWithSandboxOn(t, forge, writerClaim(), adapter, Config{VerifyCommands: []string{push}})

	v := report.Verification
	if v == nil || v.Result != harness.VerificationIncomplete {
		t.Fatalf("verification = %+v, want incomplete: the branch moved while the checks ran", v)
	}
	if err := v.Validate(); err != nil {
		t.Errorf("the record is invalid: %v", err)
	}
	if !strings.Contains(v.Stopped, "moved") {
		t.Errorf("stopped = %q, want it to say the branch moved", v.Stopped)
	}
	if strings.Contains(report.Summary, "verification passed") {
		t.Errorf("summary = %q claims a pass", report.Summary)
	}
}
