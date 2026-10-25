package httpapi

import (
	"context"
	"fmt"
	"math"
	"math/rand/v2"
	"sync"
	"testing"
	"time"
)

const ledgerSeed = 19

type ledgerRun struct {
	team, role, token, alias string
	shiftID                  int64
	authorized               float64
}

func TestAControlledLedgerSettlesEveryRunToItsOwnAccountExactlyOnce(t *testing.T) {
	ctx := context.Background()
	reset(t)
	g, broker := newLedgerGateway(t)
	c, err := NewLLMControl(testStore, broker, ledgerRoles)
	if err != nil {
		t.Fatal(err)
	}
	c.CorrectionWindow = time.Hour
	shifts := map[int64]float64{}
	for i, team := range []string{"bronze", "bronze", "silver", "silver"} {
		shifts[ledgerShift(t, team, fmt.Sprintf("ledger-%d", i), 1.5)] = 1.5
	}
	slots := []struct{ team, role string }{}
	for _, team := range []string{"bronze", "silver"} {
		for range 2 {
			slots = append(slots, struct{ team, role string }{team, "reviewer"}, struct{ team, role string }{team, "tester"})
		}
	}

	runs := make([]ledgerRun, len(slots))
	running, release := sync.WaitGroup{}, make(chan struct{})
	var done sync.WaitGroup
	errs := make(chan error, len(slots))
	for i, slot := range slots {
		running.Add(1)
		done.Add(1)
		go func() {
			defer done.Done()
			claimed, err := testStore.ClaimRole(ctx, slot.team, slot.role, 10*time.Minute, 1)
			if err == nil {
				err = c.Reserve(ctx, claimed.RunToken)
			}
			if err == nil {
				_, err = c.Issue(ctx, claimed.RunToken)
			}
			if err != nil {
				running.Done()
				errs <- fmt.Errorf("run %d start: %w", i, err)
				return
			}
			account, _ := testStore.LLMAccount(ctx, claimed.RunToken)
			runs[i] = ledgerRun{team: slot.team, role: slot.role, token: claimed.RunToken, alias: account.Alias, shiftID: claimed.ShiftID, authorized: account.Authorized}
			running.Done()
			<-release
			rng := rand.New(rand.NewPCG(ledgerSeed, uint64(i)))
			for range 1 + rng.IntN(6) {
				_ = g.charge(account.Alias, float64(100+rng.IntN(2400))/1e4)
				if _, err := c.Spend(ctx, claimed.RunToken); err != nil {
					errs <- fmt.Errorf("run %d observe: %w", i, err)
				}
			}
			if err := c.Block(ctx, claimed.RunToken); err != nil {
				errs <- fmt.Errorf("run %d block: %w", i, err)
			}
			if err := finishLedgerRun(ctx, claimed.RunToken); err != nil {
				errs <- fmt.Errorf("run %d finish: %w", i, err)
			}
		}()
	}
	running.Wait()
	select {
	case err := <-errs:
		close(release)
		done.Wait()
		t.Fatal(err)
	default:
	}

	for shiftID, budget := range shifts {
		l, err := testStore.Ledger(ctx, shiftID)
		if err != nil {
			t.Fatal(err)
		}
		if l.Spent+l.Reserved > budget+1e-9 {
			t.Fatalf("admission let Shift %d hold %v and spend %v against a budget of %v", shiftID, l.Reserved, l.Spent, budget)
		}
	}
	seen := map[string]bool{}
	for _, r := range runs {
		mint, ok := g.mintFor(r.alias)
		if !ok || mint.MaxBudget != r.authorized || r.authorized > 0.8 || seen[r.alias] {
			t.Fatalf("run %s minted %+v for an account authorized %v (duplicate alias: %v)", r.alias, mint, r.authorized, seen[r.alias])
		}
		seen[r.alias] = true
	}
	assertAccountingInvariants(t)
	close(release)
	done.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	if t.Failed() {
		t.FailNow()
	}

	rng := rand.New(rand.NewPCG(ledgerSeed, math.MaxUint32))
	for _, r := range runs[:len(runs)/2] {
		g.landInFlight(r.alias, float64(1+rng.IntN(500))/1e4)
	}
	before := assertAccountingInvariants(t)
	var sweeps sync.WaitGroup
	for range 3 {
		sweeps.Go(func() { runLedgerSweeps(ctx, c) })
	}
	sweeps.Wait()
	after := assertAccountingInvariants(t)
	assertNeverDecreases(t, before, after, "concurrent settlement sweeps")

	for _, r := range runs[len(runs)/2:] {
		g.landInFlight(r.alias, float64(1+rng.IntN(500))/1e4)
	}
	for range 3 {
		sweeps.Go(func() { runLedgerSweeps(ctx, c) })
	}
	sweeps.Wait()
	assertNeverDecreases(t, after, assertAccountingInvariants(t), "concurrent correction sweeps")
	assertSettledToGatewayTruth(t, g)

	var adjustments, settlements int
	if err := testPool.QueryRow(ctx, `SELECT (SELECT count(*) FROM run_llm_adjustments),
		(SELECT count(*) FROM audit_log WHERE action='llm.reconciled' AND detail->>'adjustment' IS NULL)`).Scan(&adjustments, &settlements); err != nil {
		t.Fatal(err)
	}
	if settlements != len(runs) || adjustments != len(runs)/2 {
		t.Fatalf("%d first settlements and %d adjustments for %d Runs", settlements, adjustments, len(runs))
	}
}
