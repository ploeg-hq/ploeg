package store

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/work"
)

func TestPendingLLMBlocksRotatesBoundedFinishedAccounts(t *testing.T) {
	resetTables(t)
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if _, _, err := testStore.IngestAssigned(ctx, work.WorkItem{Provider: "vikunja", ExternalID: fmt.Sprint(i), Team: "silver", Title: "Block retry fixture"}); err != nil {
			t.Fatal(err)
		}
		run, err := testStore.Claim(ctx, "silver", time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := testStore.pool.Exec(ctx, `UPDATE agent_runs SET authorized=1 WHERE run_token=$1`, run.RunToken); err != nil {
			t.Fatal(err)
		}
		if err := testStore.ReserveLLMAccount(ctx, LLMAccount{RunToken: run.RunToken, Alias: "ploeg-" + run.RunToken[:12], Authorized: 1, Models: []string{"fixture"}, TTLSeconds: 60}); err != nil {
			t.Fatal(err)
		}
		if _, err := testStore.pool.Exec(ctx, `UPDATE run_llm_accounts SET state='unknown' WHERE run_token=$1`, run.RunToken); err != nil {
			t.Fatal(err)
		}
		if i < 2 {
			if _, err := testStore.ReportOutcome(ctx, run.RunToken, Report(work.OutcomeNoChangeNeeded, "done", "", nil, nil, nil)); err != nil {
				t.Fatal(err)
			}
		}
	}
	first, err := testStore.PendingLLMBlocks(ctx, 0, 1)
	if err != nil || len(first) != 1 {
		t.Fatalf("first cleanup page: %d %v", len(first), err)
	}
	second, err := testStore.PendingLLMBlocks(ctx, first[0].RunID, 1)
	if err != nil || len(second) != 1 || second[0].RunID <= first[0].RunID {
		t.Fatalf("cleanup cursor did not advance: %d %v", len(second), err)
	}
	end, err := testStore.PendingLLMBlocks(ctx, second[0].RunID, 1)
	if err != nil || len(end) != 0 {
		t.Fatalf("live account entered cleanup: %d %v", len(end), err)
	}
	if err := testStore.RecordLLMBlocked(ctx, first[0].RunToken, nil); err != nil {
		t.Fatal(err)
	}
	again, err := testStore.PendingLLMBlocks(ctx, 0, 100)
	if err != nil || len(again) != 1 || again[0].RunID != second[0].RunID {
		t.Fatalf("blocked account repeated cleanup: %d %v", len(again), err)
	}
	if _, err := testStore.PendingLLMBlocks(ctx, 0, 101); err == nil {
		t.Fatal("unbounded cleanup accepted")
	}
}
