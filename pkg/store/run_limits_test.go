package store

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/work"
)

// ADR-0114: a Run without a recorded prompt start serves no limits at all; the
// first prompt_started checkpoint records them once on Run detail, the Run
// list and Work Item facts; a limit out of range is no limit; and a start time
// far from ploegd's clock records nothing.
func TestRunLimitsAreRecordedOnceFromThePromptStartedCheckpoint(t *testing.T) {
	resetTables(t)
	ctx := context.Background()
	if _, _, err := testStore.IngestAssigned(ctx, work.WorkItem{Provider: "vikunja", ExternalID: "run-limits", Team: "silver", Title: "limited"}); err != nil {
		t.Fatal(err)
	}
	claimed, err := testStore.Claim(ctx, "silver", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	var runID, itemID int64
	if err := testStore.pool.QueryRow(ctx, `SELECT id, work_item_id FROM agent_runs WHERE run_token = $1`, claimed.RunToken).Scan(&runID, &itemID); err != nil {
		t.Fatal(err)
	}
	read := func() *OperatorRunLimits {
		t.Helper()
		run, err := testStore.OperatorRun(ctx, runID, nil)
		if err != nil {
			t.Fatal(err)
		}
		runs, _, err := testStore.OperatorRuns(ctx, OperatorRunFilter{Limit: 10})
		if err != nil || len(runs) != 1 {
			t.Fatalf("runs = %v, %v", runs, err)
		}
		facts, err := testStore.WorkItemFacts(ctx, itemID, nil, FactsOptions{})
		if err != nil || len(facts.Runs) != 1 {
			t.Fatalf("facts runs = %v, %v", facts.Runs, err)
		}
		detail, _ := json.Marshal(run.Limits)
		for name, other := range map[string]*OperatorRunLimits{"list": runs[0].Limits, "facts": facts.Runs[0].Limits} {
			if got, _ := json.Marshal(other); string(got) != string(detail) {
				t.Fatalf("%s limits %s, detail %s", name, got, detail)
			}
		}
		return run.Limits
	}

	run, err := testStore.OperatorRun(ctx, runID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if body, _ := json.Marshal(run); strings.Contains(string(body), `"limits"`) || read() != nil {
		t.Fatalf("a Run without a prompt start serves limits: %s", body)
	}

	skewed := work.Checkpoint{Phase: work.PhasePromptStarted, Limits: &work.RunLimits{PromptSeconds: 60, PromptStartedAt: time.Now().Add(time.Hour)}}
	if err := testStore.Checkpoint(ctx, claimed.RunToken, skewed); err != nil {
		t.Fatal(err)
	}
	if l := read(); l != nil {
		t.Fatalf("a start an hour ahead of ploegd recorded %+v, want unknown", l)
	}

	started := time.Now().UTC().Truncate(time.Second)
	if err := testStore.Checkpoint(ctx, claimed.RunToken, work.Checkpoint{Phase: work.PhasePromptStarted,
		Limits: &work.RunLimits{PromptSeconds: 5400, IdleSeconds: 600, PromptStartedAt: started}}); err != nil {
		t.Fatal(err)
	}
	if err := testStore.Checkpoint(ctx, claimed.RunToken, work.Checkpoint{Phase: work.PhasePromptStarted,
		Limits: &work.RunLimits{PromptSeconds: 60, IdleSeconds: 30, PromptStartedAt: started.Add(time.Second)}}); err != nil {
		t.Fatal(err)
	}
	l := read()
	if l == nil || l.PromptSeconds == nil || *l.PromptSeconds != 5400 || l.IdleSeconds == nil || *l.IdleSeconds != 600 || !l.PromptStartedAt.Equal(started) {
		t.Fatalf("limits = %+v, want the first report: 5400 s, 600 s from %s", l, started)
	}
	run, err = testStore.OperatorRun(ctx, runID, nil)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(run)
	if want := `"limits":{"promptSeconds":5400,"idleSeconds":600,"promptStartedAt":"`; !strings.Contains(string(body), want) {
		t.Fatalf("run JSON %s, want %s", body, want)
	}

	mustExec(t, `UPDATE agent_runs SET prompt_started_at = NULL, limit_prompt_seconds = NULL, limit_idle_seconds = NULL WHERE id = $1`, runID)
	if err := testStore.Checkpoint(ctx, claimed.RunToken, work.Checkpoint{Phase: work.PhasePromptStarted,
		Limits: &work.RunLimits{PromptSeconds: 8 * 24 * 60 * 60, PromptStartedAt: started}}); err != nil {
		t.Fatal(err)
	}
	if l := read(); l == nil || l.PromptSeconds != nil || l.IdleSeconds != nil || !l.PromptStartedAt.Equal(started) {
		t.Fatalf("limits = %+v, want a recorded start with no limits", l)
	}
}
