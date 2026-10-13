package store

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/harness"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

// shadowRound opens a Shift on a fresh Work Item whose first Round is a
// reviewer beside a shadow (ADR-0094), and returns the Work Item and Shift.
func shadowRound(t *testing.T, externalID string) (int64, int64) {
	t.Helper()
	ctx := context.Background()
	id, _, err := testStore.IngestAssigned(ctx, work.WorkItem{Provider: "vikunja", ExternalID: externalID, Team: "bronze", Title: "shadowed"})
	if err != nil {
		t.Fatal(err)
	}
	shiftID, err := testStore.OpenShift(ctx, id, "bronze", "ploeg/"+externalID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := testStore.OpenRound(ctx, shiftID, 0, []Role{
		{Name: "reviewer", Cap: 0.4}, {Name: "shadow-reviewer", Cap: 0.4, Shadow: true},
	}); err != nil {
		t.Fatal(err)
	}
	return id, shiftID
}

func reportShadowRound(t *testing.T, role string, rep harnessReport) string {
	t.Helper()
	ctx := context.Background()
	run, err := testStore.ClaimRole(ctx, "bronze", role, time.Minute, 1)
	if err != nil {
		t.Fatalf("claim %s: %v", role, err)
	}
	if _, err := testStore.ReportOutcome(ctx, run.RunToken, rep); err != nil {
		t.Fatalf("report %s: %v", role, err)
	}
	return run.RunToken
}

func TestShadowRunIsRecordedAndServedButNeverActs(t *testing.T) {
	resetTables(t)
	ctx := context.Background()
	id, shiftID := shadowRound(t, "shadow-1")

	reportShadowRound(t, "reviewer", Report(work.OutcomeNoChangeNeeded, "fine", "", nil, nil, nil).
		WithVerdict(harness.VerdictApprove).WithFindings("- reviewer"))
	token := reportShadowRound(t, "shadow-reviewer", Report(work.OutcomeNoChangeNeeded, "not fine", "", nil, nil, nil).
		WithVerdict(harness.VerdictRequestChanges).WithFindings("- shadow").
		WithCreatedWork([]harness.CreatedWorkItem{{Title: "split it", Description: "d", Kind: work.CreatedDiscovered}}, nil, nil).
		WithNotes([]harness.Learning{{Type: "fact", Title: "t", Body: "b"}}))

	reports, err := testStore.RoundReports(ctx, shiftID)
	if err != nil {
		t.Fatal(err)
	}
	shadows := 0
	for _, r := range reports {
		if r.Shadow {
			shadows++
			if r.Role != "shadow-reviewer" || r.Verdict != harness.VerdictRequestChanges || r.Findings != "- shadow" {
				t.Fatalf("shadow report = %+v", r)
			}
		}
	}
	if shadows != 1 {
		t.Fatalf("reports = %+v, want one shadow", reports)
	}

	var created, notes int
	if err := testStore.pool.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM work_items WHERE source_work_item_id = $1),
		(SELECT count(*) FROM work_item_note_outbox o JOIN agent_runs r ON r.id = o.run_id WHERE r.run_token = $2)`,
		id, token).Scan(&created, &notes); err != nil {
		t.Fatal(err)
	}
	if created != 0 || notes != 0 {
		t.Fatalf("a shadow created %d Work Items and %d notes, want none", created, notes)
	}
	var dropped string
	if err := testStore.pool.QueryRow(ctx, `SELECT detail->>'shadow_dropped_created_work_items' || '/' || (detail->>'shadow_dropped_notes')
		FROM audit_log WHERE action = 'outcome.no_change_needed' AND (detail->>'shadow')::boolean`).Scan(&dropped); err != nil {
		t.Fatal(err)
	}
	if dropped != "1/1" {
		t.Fatalf("audit records %q dropped created Work Items/notes, want 1/1", dropped)
	}

	runs, _, err := testStore.OperatorRuns(ctx, OperatorRunFilter{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	listed := map[string]bool{}
	for _, r := range runs {
		listed[r.Role] = r.Shadow
		runID, err := strconv.ParseInt(r.ID, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		detail, err := testStore.OperatorRun(ctx, runID, nil)
		if err != nil {
			t.Fatal(err)
		}
		if detail.Shadow != r.Shadow {
			t.Fatalf("Run %s: detail shadow %v, list shadow %v", r.ID, detail.Shadow, r.Shadow)
		}
	}
	if !listed["shadow-reviewer"] || listed["reviewer"] {
		t.Fatalf("listed shadow flags = %v", listed)
	}

	m, err := testStore.OperationalMetrics(ctx)
	if err != nil {
		t.Fatal(err)
	}
	key := ShadowReviewKey{Team: "bronze", Role: "shadow-reviewer", Primary: harness.VerdictApprove, Shadow: harness.VerdictRequestChanges}
	if m.ShadowReviews[key] != 1 || len(m.ShadowReviews) != 1 {
		t.Fatalf("shadow reviews = %+v, want one approve/request_changes pair", m.ShadowReviews)
	}
}

// The pull request's agentVerdict is the acting reviewer's, even when the
// shadow finished last.
func TestShadowVerdictIsNeverThePullRequestsAgentVerdict(t *testing.T) {
	resetTables(t)
	ctx := context.Background()
	id, _ := shadowRound(t, "shadow-2")
	mustExec(t, `INSERT INTO agent_runs (work_item_id, shift_id, team, role, round, writes, state, run_token, outcome, links, finished_at)
		SELECT work_item_id, shift_id, team, 'builder', 0, true, 'finished', 'tok-builder', 'pr_opened',
		       ARRAY['https://forgejo.example/o/r/pulls/3'], now() - interval '1 hour'
		FROM agent_runs WHERE work_item_id = $1 LIMIT 1`, id)
	reportShadowRound(t, "reviewer", Report(work.OutcomeNoChangeNeeded, "fine", "", nil, nil, nil).WithVerdict(harness.VerdictApprove))
	reportShadowRound(t, "shadow-reviewer", Report(work.OutcomeNoChangeNeeded, "no", "", nil, nil, nil).WithVerdict(harness.VerdictRequestChanges))
	mustExec(t, `UPDATE agent_runs SET finished_at = now() + interval '1 minute' WHERE role = 'shadow-reviewer'`)

	detail, err := testStore.OperatorItem(ctx, id, nil)
	if err != nil {
		t.Fatal(err)
	}
	if detail.Item.PullRequest == nil || detail.Item.PullRequest.AgentVerdict != harness.VerdictApprove {
		t.Fatalf("pull request = %+v, want the reviewer's approve", detail.Item.PullRequest)
	}
}

func TestOpenRoundRefusesAWritingShadow(t *testing.T) {
	resetTables(t)
	ctx := context.Background()
	id, _, err := testStore.IngestAssigned(ctx, work.WorkItem{Provider: "vikunja", ExternalID: "shadow-3", Team: "bronze", Title: "t"})
	if err != nil {
		t.Fatal(err)
	}
	shiftID, err := testStore.OpenShift(ctx, id, "bronze", "b", 10)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := testStore.OpenRound(ctx, shiftID, 0, []Role{{Name: "builder", Writes: true, Shadow: true}}); err == nil {
		t.Fatal("a writing shadow Role opened a Round")
	}
}
