package store

import (
	"context"
	"errors"
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/work"
)

func operatorRequeue(id int64, cmd RequeueCommand) RequeueRequest {
	if cmd.FromRound == 0 {
		cmd.FromRound = 1
	}
	return RequeueRequest{WorkItemID: id, Team: "silver", Actor: "operator:workbench:alice", Command: cmd, Branch: "agent/vik-585"}
}

func TestRequeueWorkItemRestartsStoppedStatesAndResetsBothCounters(t *testing.T) {
	ctx := context.Background()
	for _, prior := range []string{"needs_human", "stale", "awaiting_review"} {
		t.Run(prior, func(t *testing.T) {
			resetTables(t)
			id, _ := ingestItem(t)
			forceItemState(t, id, prior, 3)
			if _, err := testStore.pool.Exec(ctx, `UPDATE work_items SET infra_failures = 2 WHERE id = $1`, id); err != nil {
				t.Fatal(err)
			}

			res, err := testStore.RequeueWorkItem(ctx, operatorRequeue(id, RequeueCommand{Note: "try again"}))
			if err != nil {
				t.Fatalf("RequeueWorkItem: %v", err)
			}
			if res.State != work.StateQueued || res.Replayed || res.ShiftID != 0 {
				t.Fatalf("result = %+v, want a queued item and no Shift", res)
			}
			if state, attempts, infra, _ := itemAllFields(t, id); state != "queued" || attempts != 0 || infra != 0 {
				t.Fatalf("row = (%s, %d, %d), want (queued, 0, 0)", state, attempts, infra)
			}
			if action := lastAuditAction(t, id); action != AuditWorkItemRequeued {
				t.Fatalf("audit action = %q, want %s", action, AuditWorkItemRequeued)
			}
		})
	}
}

func TestRequeueWorkItemRefusesEveryOtherStateWithoutAChange(t *testing.T) {
	ctx := context.Background()
	for _, prior := range []string{"ingested", "proposed", "queued", "leased", "done", "withdrawn"} {
		t.Run(prior, func(t *testing.T) {
			resetTables(t)
			id, _ := ingestItem(t)
			forceItemState(t, id, prior, 2)
			before := lastAuditAction(t, id)

			if _, err := testStore.RequeueWorkItem(ctx, operatorRequeue(id, RequeueCommand{})); !errors.Is(err, ErrNotRequeueable) {
				t.Fatalf("err = %v, want ErrNotRequeueable", err)
			}
			if state, attempts := itemStateAttempts(t, id); state != prior || attempts != 2 {
				t.Fatalf("row = (%s, %d), want (%s, 2)", state, attempts, prior)
			}
			if action := lastAuditAction(t, id); action != before {
				t.Fatalf("audit action = %q, want no new row after %q", action, before)
			}
		})
	}
}

func TestRequeueWorkItemRefusesAnOpenShift(t *testing.T) {
	ctx := context.Background()
	resetTables(t)
	id, _ := ingestItem(t)
	if _, err := testStore.OpenShift(ctx, id, "silver", "agent/vik-585", 1); err != nil {
		t.Fatal(err)
	}
	forceItemState(t, id, "needs_human", 1)
	if _, err := testStore.RequeueWorkItem(ctx, operatorRequeue(id, RequeueCommand{})); !errors.Is(err, ErrNotRequeueable) {
		t.Fatalf("err = %v, want ErrNotRequeueable", err)
	}
}

func TestRequeueWorkItemOpensAShiftBeforeTheStartingRoundOnTheEarlierBranch(t *testing.T) {
	ctx := context.Background()
	resetTables(t)
	id, _ := ingestItem(t)
	previous, err := testStore.OpenShift(ctx, id, "silver", "agent/earlier-branch", 4)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := testStore.CloseShiftAndSettle(ctx, previous, "review_failed", work.StateAwaitingReview, "review failed"); err != nil {
		t.Fatal(err)
	}

	req := operatorRequeue(id, RequeueCommand{FromRound: 3, CommandID: "c-1"})
	req.OpenShift, req.PoolUSD = true, 0.4
	res, err := testStore.RequeueWorkItem(ctx, req)
	if err != nil {
		t.Fatalf("RequeueWorkItem: %v", err)
	}
	si, err := testStore.LiveShiftForItem(ctx, id)
	if err != nil || si == nil || si.ID != res.ShiftID {
		t.Fatalf("live shift = %+v %v, want %d", si, err, res.ShiftID)
	}
	if si.Round != 2 || si.Branch != "agent/earlier-branch" || res.PreviousShiftID != previous || res.PoolUSD != 0.4 {
		t.Fatalf("shift %+v result %+v, want round 2 on the earlier branch with pool 0.4", si, res)
	}
	brief, ok, err := testStore.ShiftRestartBrief(ctx, si.ID)
	if err != nil || !ok || brief.PreviousCloseReason != "review_failed" || brief.FromRound != 3 {
		t.Fatalf("brief = %+v ok=%v err=%v", brief, ok, err)
	}

	again, err := testStore.RequeueWorkItem(ctx, req)
	if err != nil || !again.Replayed || again.ShiftID != res.ShiftID {
		t.Fatalf("replay = %+v %v, want the first result", again, err)
	}
	changed := req
	changed.Command.Note = "different"
	if _, err := testStore.RequeueWorkItem(ctx, changed); !errors.Is(err, ErrRequeueConflict) {
		t.Fatalf("reused command id: err = %v, want ErrRequeueConflict", err)
	}
	var shifts int
	if err := testStore.pool.QueryRow(ctx, `SELECT count(*) FROM shifts WHERE work_item_id = $1`, id).Scan(&shifts); err != nil || shifts != 2 {
		t.Fatalf("shifts = %d %v, want 2", shifts, err)
	}
}
