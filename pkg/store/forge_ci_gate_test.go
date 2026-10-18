package store

import (
	"context"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/work"
)

// An item already handed to review whose head CI stops being success leaves
// awaiting_review (ADR-0119, rule 6), and the gauge that watches for it sees
// it while it is there.
func TestAnAwaitingReviewItemWhoseCIWentRedLeavesReview(t *testing.T) {
	ctx := context.Background()
	resetTables(t)
	id, _ := ingestItem(t)
	shift, err := testStore.OpenShift(ctx, id, "silver", "agent/vikunja-585", 5)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := testStore.CloseShiftAndSettle(ctx, shift, "review_approved", work.StateAwaitingReview, "plan complete"); err != nil {
		t.Fatal(err)
	}
	record := func(state string) {
		t.Helper()
		if _, err := testStore.RecordPullRequestFacts(ctx, PullRequestFacts{
			Forge: "forgejo", Repo: "webgrip/ploeg", Number: 42, WorkItemID: id, State: "open", HeadSHA: "abc",
			CI: &PullRequestCI{State: state, HeadSHA: "abc", CapturedAt: time.Now(),
				Checks: []PullRequestCheck{{Context: "ci/test", State: state}}},
		}); err != nil {
			t.Fatal(err)
		}
	}
	gauge := func() int {
		t.Helper()
		m, err := testStore.OperationalMetrics(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return m.AwaitingReviewWithUnsuccessfulCI
	}

	record("success")
	if got := gauge(); got != 0 {
		t.Fatalf("gauge = %d with green CI, want 0", got)
	}
	record("failure")
	if got := gauge(); got != 1 {
		t.Fatalf("gauge = %d with red CI in review, want 1", got)
	}

	moved, err := testStore.LeaveReviewForCI(ctx, id, "ci_not_passed", `the forge's CI failed: "ci/test" failure`)
	if err != nil || !moved {
		t.Fatalf("LeaveReviewForCI = %v, %v; want the item moved", moved, err)
	}
	if state, _ := itemStateAttempts(t, id); state != string(work.StateNeedsHuman) {
		t.Errorf("item state = %q, want needs_human", state)
	}
	var reason string
	if err := testStore.pool.QueryRow(ctx, `SELECT close_reason FROM shifts WHERE id = $1`, shift).Scan(&reason); err != nil {
		t.Fatal(err)
	}
	if reason != "ci_not_passed" {
		t.Errorf("close reason = %q, want ci_not_passed", reason)
	}
	if got := gauge(); got != 0 {
		t.Errorf("gauge = %d after the item left review, want 0", got)
	}
	if moved, err := testStore.LeaveReviewForCI(ctx, id, "ci_not_passed", "again"); err != nil || moved {
		t.Errorf("a second LeaveReviewForCI = %v, %v; want nothing moved", moved, err)
	}
}
