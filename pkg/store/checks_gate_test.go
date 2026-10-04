package store

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/harness"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

func TestAShiftlessPullRequestAwaitsReviewOnlyWhenItsChecksPassed(t *testing.T) {
	ctx := context.Background()
	zero, one := 0, 1
	commit := strings.Repeat("c", 40)
	for _, tc := range []struct {
		name string
		v    *harness.Verification
		want string
	}{
		{"no checks configured", nil, "awaiting_review"},
		{"passed", &harness.Verification{Result: harness.VerificationPassed, Commit: commit,
			Checks: []harness.VerificationCheck{{Command: "true", Result: harness.VerificationPassed, ExitCode: &zero}}}, "awaiting_review"},
		{"failed", &harness.Verification{Result: harness.VerificationFailed, Commit: commit, Stopped: "an earlier check failed",
			Checks: []harness.VerificationCheck{{Command: "false", Result: harness.VerificationFailed, ExitCode: &one}}}, "needs_human"},
		{"incomplete", &harness.Verification{Result: harness.VerificationIncomplete, Commit: commit, Stopped: "the Run was cancelled",
			Checks: []harness.VerificationCheck{{Command: "true", Result: harness.VerificationCheckNotRun}}}, "needs_human"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resetTables(t)
			id, _, err := testStore.IngestAssigned(ctx, work.WorkItem{Provider: "vikunja", ExternalID: "1738", Team: "silver", Title: "t"})
			if err != nil {
				t.Fatal(err)
			}
			run, err := testStore.Claim(ctx, "silver", time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := testStore.ReportOutcome(ctx, run.RunToken,
				Report(work.OutcomePROpened, "opened", "", []string{"https://forgejo/o/r/pulls/1"}, nil, nil).WithVerification(tc.v)); err != nil {
				t.Fatal(err)
			}
			item, err := testStore.WorkItem(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			if string(item.State) != tc.want {
				t.Errorf("item state = %q, want %q", item.State, tc.want)
			}
		})
	}
}
