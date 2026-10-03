package store

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/work"
)

func pendingAnalysts(t *testing.T, team string, n int) {
	t.Helper()
	ctx := context.Background()
	for i := 0; i < n; i++ {
		itemID, _, err := testStore.IngestAssigned(ctx, work.WorkItem{
			Provider: "vikunja", ExternalID: fmt.Sprintf("%s-%d", team, i), Team: team, Title: "t",
		})
		if err != nil {
			t.Fatalf("IngestAssigned: %v", err)
		}
		shiftID, err := testStore.OpenShift(ctx, itemID, team, fmt.Sprintf("agent/%s-%d", team, i), 10)
		if err != nil {
			t.Fatalf("OpenShift: %v", err)
		}
		if _, err := testStore.OpenRound(ctx, shiftID, 0, []Role{{Name: "analyst", Cap: 1}}); err != nil {
			t.Fatalf("OpenRound: %v", err)
		}
	}
}

func TestClaimRoleWithin_CapHoldsUnderConcurrentClaims(t *testing.T) {
	resetTables(t)
	ctx := context.Background()
	pendingAnalysts(t, "silver", 8)
	pendingAnalysts(t, "gold", 2)

	const maxRunning, claimers = 3, 8
	var wg sync.WaitGroup
	results := make(chan error, claimers)
	start := make(chan struct{})
	for i := 0; i < claimers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := testStore.ClaimRoleWithin(ctx, "silver", "analyst", time.Minute, 1, maxRunning)
			results <- err
		}()
	}
	close(start)
	wg.Wait()
	close(results)

	claimed, empty := 0, 0
	for err := range results {
		switch {
		case err == nil:
			claimed++
		case errors.Is(err, ErrNoWork):
			empty++
		default:
			t.Fatalf("claim failed: %v", err)
		}
	}
	if claimed != maxRunning || empty != claimers-maxRunning {
		t.Fatalf("claimed %d, empty-handed %d; want %d and %d", claimed, empty, maxRunning, claimers-maxRunning)
	}
	if n, _ := testStore.RunningRuns(ctx, "silver"); n != maxRunning {
		t.Errorf("silver running = %d, want %d", n, maxRunning)
	}
	if n, _ := testStore.PendingRuns(ctx, "silver", "analyst"); n != 8-maxRunning {
		t.Errorf("silver pending = %d, want %d: a refused claim must not consume work", n, 8-maxRunning)
	}
	for i := 0; i < 2; i++ {
		if _, err := testStore.ClaimRoleWithin(ctx, "gold", "analyst", time.Minute, 1, 1); i == 0 && err != nil {
			t.Fatalf("another team's cap was consumed by silver: %v", err)
		} else if i == 1 && !errors.Is(err, ErrNoWork) {
			t.Fatalf("gold's second claim = %v, want ErrNoWork at its own cap", err)
		}
	}
}

func TestClaimRoleWithin_FinishedRunReleasesTheCap(t *testing.T) {
	resetTables(t)
	ctx := context.Background()
	pendingAnalysts(t, "silver", 3)

	first, err := testStore.ClaimRoleWithin(ctx, "silver", "analyst", time.Minute, 1, 1)
	if err != nil {
		t.Fatalf("first claim: %v", err)
	}
	if _, err := testStore.ClaimRoleWithin(ctx, "silver", "analyst", time.Minute, 1, 1); !errors.Is(err, ErrNoWork) {
		t.Fatalf("claim at the cap = %v, want ErrNoWork", err)
	}
	if _, err := testStore.ReportOutcome(ctx, first.RunToken,
		Report(work.OutcomeNoChangeNeeded, "read it", "", nil, nil, nil)); err != nil {
		t.Fatalf("ReportOutcome: %v", err)
	}
	second, err := testStore.ClaimRoleWithin(ctx, "silver", "analyst", time.Minute, 1, 1)
	if err != nil {
		t.Fatalf("claim after the Run finished: %v", err)
	}

	if _, err := testStore.pool.Exec(ctx,
		`UPDATE agent_runs SET expires_at = now() - interval '1 minute' WHERE run_token = $1`, second.RunToken); err != nil {
		t.Fatal(err)
	}
	if _, err := testStore.ExpireRuns(ctx); err != nil {
		t.Fatalf("ExpireRuns: %v", err)
	}
	if _, err := testStore.ClaimRoleWithin(ctx, "silver", "analyst", time.Minute, 1, 1); err != nil {
		t.Fatalf("claim after the sweeper expired the Run: %v", err)
	}
}

func TestClaimWithin_CapsTheLegacyClaim(t *testing.T) {
	resetTables(t)
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if _, _, err := testStore.IngestAssigned(ctx, work.WorkItem{
			Provider: "vikunja", ExternalID: fmt.Sprint(700 + i), Team: "silver", Title: "t",
		}); err != nil {
			t.Fatal(err)
		}
	}
	first, err := testStore.ClaimWithin(ctx, "silver", time.Minute, 1)
	if err != nil {
		t.Fatalf("first claim: %v", err)
	}
	if _, err := testStore.ClaimWithin(ctx, "silver", time.Minute, 1); !errors.Is(err, ErrNoWork) {
		t.Fatalf("claim at the cap = %v, want ErrNoWork", err)
	}
	if _, err := testStore.ReportOutcome(ctx, first.RunToken,
		Report(work.OutcomeNoChangeNeeded, "done", "", nil, nil, nil)); err != nil {
		t.Fatalf("ReportOutcome: %v", err)
	}
	if _, err := testStore.ClaimWithin(ctx, "silver", time.Minute, 1); err != nil {
		t.Fatalf("claim after the Run finished: %v", err)
	}
}

func TestClaimWithin_ZeroIsUnlimited(t *testing.T) {
	resetTables(t)
	ctx := context.Background()
	pendingAnalysts(t, "silver", 4)
	for i := 0; i < 4; i++ {
		if _, err := testStore.ClaimRoleWithin(ctx, "silver", "analyst", time.Minute, 1, 0); err != nil {
			t.Fatalf("claim %d without a cap: %v", i, err)
		}
	}
}
