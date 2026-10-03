package httpapi

import (
	"context"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/store"
)

func settledViaGateway(t *testing.T, logs []map[string]any) (*fakeGateway, *LLMControl, string, int64) {
	t.Helper()
	ctx := context.Background()
	g, broker := newFakeGateway(t)
	c, token, shiftID := settlementFixture(t, broker, true)
	c.CorrectionWindow = time.Hour
	if err := c.Block(ctx, token); err != nil {
		t.Fatal(err)
	}
	candidate := settleCandidate(t, token)
	g.mu.Lock()
	g.logs[candidate.GatewayKeyID] = logs
	g.mu.Unlock()
	if err := c.Settle(ctx, candidate); err != nil {
		t.Fatal(err)
	}
	return g, c, token, shiftID
}

func correctionCandidate(t *testing.T, token string) (store.UnsettledLLMAccount, bool) {
	t.Helper()
	accounts, err := testStore.CorrectableLLMAccounts(context.Background(), 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range accounts {
		if a.RunToken == token {
			return a, true
		}
	}
	return store.UnsettledLLMAccount{}, false
}

func operatorRunViews(t *testing.T, token string) (store.OperatorRunListItem, store.OperatorRun) {
	t.Helper()
	ctx := context.Background()
	var id int64
	if err := testPool.QueryRow(ctx, `SELECT id FROM agent_runs WHERE run_token=$1`, token).Scan(&id); err != nil {
		t.Fatal(err)
	}
	runs, _, err := testStore.OperatorRuns(ctx, store.OperatorRunFilter{Limit: 200})
	if err != nil {
		t.Fatal(err)
	}
	detail, err := testStore.OperatorRun(ctx, id, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range runs {
		if r.ID == detail.ID {
			return r, detail
		}
	}
	t.Fatalf("run %d missing from the operator Run list", id)
	return store.OperatorRunListItem{}, detail
}

func TestCorrectionSweepChargesASpendLogThatArrivesAfterSettlementAsAnAdjustment(t *testing.T) {
	ctx := context.Background()
	g, c, token, shiftID := settledViaGateway(t, []map[string]any{{"spend": 0.1, "model": "trusted-model"}})

	listed, detail := operatorRunViews(t, token)
	if listed.SettledUSD == nil || *listed.SettledUSD != 0.1 || listed.CostFinal || detail.CostFinal || listed.SettledAt == nil || listed.CostFinalAt == nil {
		t.Fatalf("a fresh settlement must read as provisional: list=%+v detail=%+v", listed, detail)
	}
	if got := listed.CostFinalAt.Sub(*listed.SettledAt); got < 59*time.Minute || got > 61*time.Minute {
		t.Fatalf("costFinalAt is not settledAt plus the correction window: %v", got)
	}

	candidate, ok := correctionCandidate(t, token)
	if !ok {
		t.Fatal("a settled account inside its correction window was not offered for correction")
	}
	if changed, err := c.Correct(ctx, candidate); err != nil || changed {
		t.Fatalf("an unchanged re-read must write nothing: changed=%v err=%v", changed, err)
	}

	g.mu.Lock()
	g.logs[candidate.GatewayKeyID] = append(g.logs[candidate.GatewayKeyID], map[string]any{"spend": 0.05, "model": "trusted-model"})
	g.mu.Unlock()
	if changed, err := c.Correct(ctx, candidate); err != nil || !changed {
		t.Fatalf("a late spend log was not charged: changed=%v err=%v", changed, err)
	}

	if l, _ := testStore.Ledger(ctx, shiftID); math.Abs(l.Spent-0.15) > 1e-9 || l.Reserved != 0 {
		t.Fatalf("the Shift was not charged the late delta exactly once: %+v", l)
	}
	var reconciled, settled float64
	var evidence string
	if err := testPool.QueryRow(ctx, `SELECT reconciled_spend, settled_spend, reconciliation_evidence FROM run_llm_accounts WHERE run_token=$1`, token).
		Scan(&reconciled, &settled, &evidence); err != nil {
		t.Fatal(err)
	}
	if reconciled != 0.15 || settled != 0.1 || !strings.Contains(evidence, "entries=1 ") {
		t.Fatalf("the first settlement was rewritten: reconciled=%v settled=%v evidence=%q", reconciled, settled, evidence)
	}
	var previous, spend float64
	var adjustmentEvidence string
	var adjustments int
	if err := testPool.QueryRow(ctx, `SELECT count(*) OVER (), previous_spend, spend, evidence FROM run_llm_adjustments WHERE run_token=$1`, token).
		Scan(&adjustments, &previous, &spend, &adjustmentEvidence); err != nil {
		t.Fatal(err)
	}
	if adjustments != 1 || previous != 0.1 || spend != 0.15 || !strings.Contains(adjustmentEvidence, "entries=2 ") {
		t.Fatalf("adjustment=%d %v→%v %q", adjustments, previous, spend, adjustmentEvidence)
	}
	var delta float64
	if err := testPool.QueryRow(ctx, `SELECT (detail->>'delta')::float8 FROM audit_log
		WHERE action='llm.reconciled' AND detail->>'adjustment'='true'`).Scan(&delta); err != nil {
		t.Fatal(err)
	}
	if math.Abs(delta-0.05) > 1e-9 {
		t.Fatalf("the adjustment's audit delta=%v", delta)
	}
	if listed, _ := operatorRunViews(t, token); listed.SettledUSD == nil || *listed.SettledUSD != 0.15 {
		t.Fatalf("the Run list does not show the corrected cost: %+v", listed)
	}

	if _, err := testPool.Exec(ctx, `UPDATE run_llm_accounts SET corrections_until=now()-interval '1 second' WHERE run_token=$1`, token); err != nil {
		t.Fatal(err)
	}
	if _, ok := correctionCandidate(t, token); ok {
		t.Fatal("an account past its correction window was offered again")
	}
	if listed, detail := operatorRunViews(t, token); !listed.CostFinal || !detail.CostFinal {
		t.Fatalf("a cost past its correction window must read as final: list=%+v detail=%+v", listed, detail)
	}
}

func TestSettlementWithoutAnySpendLogShowsCostAsUnknownUntilALogArrives(t *testing.T) {
	ctx := context.Background()
	g, c, token, shiftID := settledViaGateway(t, nil)

	if l, _ := testStore.Ledger(ctx, shiftID); l.Spent != 0 || l.Reserved != 0 {
		t.Fatalf("an account with no spend logs must still release its hold: %+v", l)
	}
	listed, detail := operatorRunViews(t, token)
	if listed.SettledUSD != nil || detail.CostStatus != "unknown" || (detail.Usage != nil && detail.Usage.CostUSD != nil) {
		t.Fatalf("an account with no spend logs read as a confirmed zero: list=%+v detail=%+v", listed, detail)
	}

	candidate, ok := correctionCandidate(t, token)
	if !ok {
		t.Fatal("an unknown-cost settlement was not offered for correction")
	}
	g.mu.Lock()
	g.logs[candidate.GatewayKeyID] = []map[string]any{{"spend": 0.2, "model": "trusted-model"}}
	g.mu.Unlock()
	if changed, err := c.Correct(ctx, candidate); err != nil || !changed {
		t.Fatalf("late spend log not charged: changed=%v err=%v", changed, err)
	}
	if l, _ := testStore.Ledger(ctx, shiftID); math.Abs(l.Spent-0.2) > 1e-9 {
		t.Fatalf("ledger=%+v", l)
	}
	if listed, detail := operatorRunViews(t, token); listed.SettledUSD == nil || *listed.SettledUSD != 0.2 || detail.CostStatus != "observed" {
		t.Fatalf("a confirmed late cost still reads as unknown: list=%+v detail=%+v", listed, detail)
	}
}

func TestSettlementWhoseSpendLogsConfirmZeroShowsZero(t *testing.T) {
	_, _, token, _ := settledViaGateway(t, []map[string]any{{"spend": 0, "model": "trusted-model"}})
	listed, detail := operatorRunViews(t, token)
	if listed.SettledUSD == nil || *listed.SettledUSD != 0 || detail.CostStatus != "observed" {
		t.Fatalf("a gateway-confirmed zero read as unknown: list=%+v detail=%+v", listed, detail)
	}
}

func TestCorrectRefusesAnAccountThatIsNotSettled(t *testing.T) {
	_, broker := newFakeGateway(t)
	c, token, _ := settlementFixture(t, broker, true)
	if _, err := c.Correct(context.Background(), store.UnsettledLLMAccount{RunToken: token, State: "issued", MintBegan: true}); err == nil {
		t.Fatal("an unsettled account was corrected")
	}
}
