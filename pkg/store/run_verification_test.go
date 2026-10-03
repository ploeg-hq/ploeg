package store

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/harness"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

// VIK-1733: the worker's structured verification is stored for a writing Run
// and read back unchanged; a reading Run's and a missing one stay nil.
func TestRoundReportsCarryTheWorkersVerification(t *testing.T) {
	ctx := context.Background()
	_, shiftID := openShift(t, 10)
	two := 2
	started := time.Date(2026, 10, 3, 9, 0, 0, 0, time.UTC)
	recorded := &harness.Verification{
		Result: harness.VerificationFailed, Commit: strings.Repeat("b", 40), Dirty: true,
		Stopped: "an earlier check failed", StartedAt: started, FinishedAt: started.Add(time.Minute),
		Checks: []harness.VerificationCheck{{Command: "go test ./...", Result: harness.VerificationFailed, ExitCode: &two, StartedAt: &started}},
	}
	rounds := []struct {
		role   Role
		report *harness.Verification
	}{
		{Role{Name: "builder", Writes: true, Cap: 1}, recorded},
		{Role{Name: "tests", Cap: 1}, recorded},
		{Role{Name: "docs", Writes: true, Cap: 1}, nil},
	}
	for i, r := range rounds {
		if _, err := testStore.OpenRound(ctx, shiftID, i, []Role{r.role}); err != nil {
			t.Fatalf("OpenRound(%s): %v", r.role.Name, err)
		}
		run, err := testStore.ClaimRole(ctx, "silver", r.role.Name, time.Minute, 1)
		if err != nil {
			t.Fatalf("ClaimRole(%s): %v", r.role.Name, err)
		}
		if _, err := testStore.ReportOutcome(ctx, run.RunToken,
			Report(work.OutcomePROpened, "done [Ploeg verification passed]", "", nil, nil, nil).WithVerification(r.report)); err != nil {
			t.Fatalf("ReportOutcome(%s): %v", r.role.Name, err)
		}
	}

	reports, err := testStore.RoundReports(ctx, shiftID)
	if err != nil {
		t.Fatalf("RoundReports: %v", err)
	}
	if len(reports) != 3 {
		t.Fatalf("got %d reports, want 3", len(reports))
	}
	for _, r := range reports {
		switch r.Role {
		case "builder":
			v := r.Verification
			if v == nil || v.Result != recorded.Result || v.Commit != recorded.Commit || !v.Dirty ||
				!v.FinishedAt.Equal(recorded.FinishedAt) || len(v.Checks) != 1 || *v.Checks[0].ExitCode != 2 {
				t.Errorf("writer's verification = %+v, want %+v", v, recorded)
			}
		default:
			if r.Verification != nil {
				t.Errorf("%s: verification = %+v, want none", r.Role, r.Verification)
			}
		}
	}
}
