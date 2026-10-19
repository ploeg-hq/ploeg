package shiftengine

import (
	"context"
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/store"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

func TestStuckReasonsNameARolelessRunAndACanaryStop(t *testing.T) {
	for _, c := range []struct {
		run                  store.RunReport
		closeWant, humanWant string
	}{
		{store.RunReport{Role: "builder", Round: 2, Writes: true, Summary: "blocked"},
			"run stuck: builder round 2", "shift stopped: builder reported stuck in round 2 — blocked"},
		{store.RunReport{Round: 1, Writes: true, Summary: "no access"},
			"run stuck: writer round 1", "shift stopped: writer reported stuck in round 1 — no access"},
		{store.RunReport{Round: 1, Summary: "unreadable"},
			"run stuck: reader round 1", "shift stopped: reader reported stuck in round 1 — unreadable"},
		{store.RunReport{Round: 1, Writes: true, Canary: true, Summary: "ploeg-canary stopped on purpose"},
			"canary stopped: writer round 1", "a canary Run (writer, round 1) stopped on purpose; nothing needs a person — ploeg-canary stopped on purpose"},
	} {
		closeReason, humanReason := stuckReasons(c.run)
		if closeReason != c.closeWant || humanReason != c.humanWant {
			t.Errorf("%+v:\n close %q, want %q\n human %q, want %q", c.run, closeReason, c.closeWant, humanReason, c.humanWant)
		}
	}
}

func TestACanaryStopClosesTheShiftSayingNothingNeedsAPerson(t *testing.T) {
	resetTables(t)
	ctx := context.Background()
	e := newEngine(singleWriterPlan(t))
	itemID, shiftID := openShift(t, e, "canary-1")
	run := claim(t, "builder")
	if err := testStore.RecordRunConfig(ctx, run.RunToken, store.RunConfig{Canary: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := testStore.ReportOutcome(ctx, run.RunToken,
		store.Report(work.OutcomeStuck, "ploeg-canary stopped on purpose", "ploeg-canary stops every Run so the stop note is written", nil, nil, nil)); err != nil {
		t.Fatal(err)
	}
	evaluate(t, e, itemID)
	if _, closed, reason := shiftRow(t, shiftID); !closed || reason != "canary stopped: builder round 1" {
		t.Fatalf("closed=%v reason=%q", closed, reason)
	}
	var state, human string
	if err := testPool.QueryRow(ctx, `SELECT i.state, a.detail->>'reason' FROM work_items i
		JOIN audit_log a ON a.work_item_id = i.id AND a.action = 'work_item.needs_human' WHERE i.id = $1
		ORDER BY a.id DESC LIMIT 1`, itemID).Scan(&state, &human); err != nil {
		t.Fatal(err)
	}
	if state != string(work.StateNeedsHuman) || human != "a canary Run (builder, round 1) stopped on purpose; nothing needs a person — ploeg-canary stopped on purpose" {
		t.Fatalf("state %q, needs_human reason %q", state, human)
	}
}
