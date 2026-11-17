package store

import (
	"context"
	"testing"
	"time"
)

func claimReader(t *testing.T) *ClaimedRun {
	t.Helper()
	ctx := context.Background()
	id, _ := ingestItem(t)
	shiftID, err := testStore.OpenShift(ctx, id, "silver", "agent/vik-585", 10)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := testStore.OpenRound(ctx, shiftID, 0, []Role{{Name: "reviewer", Writes: false, Cap: 1}}); err != nil {
		t.Fatal(err)
	}
	run, err := testStore.ClaimRole(ctx, "silver", "reviewer", time.Minute, 1)
	if err != nil {
		t.Fatal(err)
	}
	return run
}

func TestAReadersForgeCredentialIsSparedWhileItRunsAndReturnedWhenItExpires(t *testing.T) {
	resetTables(t)
	ctx := context.Background()
	run := claimReader(t)
	if err := testStore.RecordForgeToken(ctx, run.RunToken, "read-1"); err != ErrLeaseLost {
		t.Fatalf("a reader recorded a credential on a Lease it does not hold: %v", err)
	}
	if err := testStore.RecordReaderForgeToken(ctx, run.RunToken, "read-1"); err != nil {
		t.Fatal(err)
	}
	if previous, err := testStore.SwapReaderForgeToken(ctx, run.RunToken, "read-2"); err != nil || previous != "read-1" {
		t.Fatalf("swap = %q, %v; want read-1", previous, err)
	}
	if spared, err := testStore.LeasedRunTokens(ctx); err != nil || len(spared) != 1 || spared[0] != run.RunToken {
		t.Fatalf("the forge sweep spares %v, %v; want the running reader", spared, err)
	}
	if _, err := testStore.pool.Exec(ctx, `UPDATE agent_runs SET expires_at = now() - interval '1 second' WHERE run_token = $1`, run.RunToken); err != nil {
		t.Fatal(err)
	}
	expired, err := testStore.ExpireRuns(ctx)
	if err != nil || len(expired) != 1 || expired[0].ForgeTokenID != "read-2" {
		t.Fatalf("expire = %+v, %v; want the reader's credential read-2 to revoke", expired, err)
	}
	if spared, _ := testStore.LeasedRunTokens(ctx); len(spared) != 0 {
		t.Fatalf("the forge sweep still spares %v after the reader expired", spared)
	}
	if err := testStore.RecordReaderForgeToken(ctx, run.RunToken, "read-3"); err != ErrLeaseLost {
		t.Fatalf("recorded a credential on a finished reader: %v", err)
	}
}
