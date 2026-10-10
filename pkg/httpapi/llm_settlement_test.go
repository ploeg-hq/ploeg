package httpapi

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/llmbroker"
	"github.com/ploeg-hq/ploeg/pkg/store"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

type settlementBroker struct {
	observed    float64
	settled     float64
	err         error
	settleCalls int
	keyIDs      []string
	// byAlias makes the reading come from the alias search with no entries,
	// as for a key whose mint was never confirmed.
	byAlias bool
}

func (b *settlementBroker) Mint(_ context.Context, r llmbroker.MintRequest) (llmbroker.Credential, error) {
	return llmbroker.Credential{APIKey: "fixture-inference-" + r.RunToken[:12], Alias: "ploeg-" + r.RunToken[:12]}, nil
}
func (*settlementBroker) Revoke(context.Context, llmbroker.Credential) error { return nil }
func (*settlementBroker) RevokeForRun(context.Context, string) error         { return nil }
func (b *settlementBroker) SpendForRun(context.Context, string) (float64, error) {
	return b.observed, nil
}
func (b *settlementBroker) SettledSpendForRun(_ context.Context, _ string, keyIDs []string) (llmbroker.SettledSpend, error) {
	b.settleCalls++
	b.keyIDs = keyIDs
	if b.byAlias {
		return llmbroker.SettledSpend{ByAlias: true}, b.err
	}
	return llmbroker.SettledSpend{USD: b.settled, Keys: 1, Entries: 3}, b.err
}

func settlementFixture(t *testing.T, b ManagedLLMBroker, mint bool) (*LLMControl, string, int64) {
	t.Helper()
	ctx := context.Background()
	reset(t)
	shiftID := shiftFixture(t, "settlement", 5, []store.Role{{Name: "reviewer", Cap: 1}})
	c, err := NewLLMControl(testStore, b, `[{"team":"bronze","role":"reviewer","budgetUsd":1,"models":["trusted-model"],"ttl":"1h"}]`)
	if err != nil {
		t.Fatal(err)
	}
	run, err := testStore.ClaimRole(ctx, "bronze", "reviewer", time.Minute, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Reserve(ctx, run.RunToken); err != nil {
		t.Fatal(err)
	}
	if mint {
		if _, err := c.Issue(ctx, run.RunToken); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := testStore.ReportOutcome(ctx, run.RunToken, store.Report(work.OutcomeNoChangeNeeded, "done", "", nil, []byte(`{"costUsd":0}`), nil)); err != nil {
		t.Fatal(err)
	}
	return c, run.RunToken, shiftID
}

func settleCandidate(t *testing.T, token string) store.UnsettledLLMAccount {
	t.Helper()
	accounts, err := testStore.UnsettledLLMAccounts(context.Background(), 0, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range accounts {
		if a.RunToken == token {
			return a
		}
	}
	t.Fatalf("account not offered for settlement: %+v", accounts)
	return store.UnsettledLLMAccount{}
}

func TestControllerSettlesBlockedAccountFromSpendLogsOnce(t *testing.T) {
	ctx := context.Background()
	b := &settlementBroker{observed: 0, settled: 0.45}
	c, token, shiftID := settlementFixture(t, b, true)
	if err := c.Block(ctx, token); err != nil {
		t.Fatal(err)
	}
	if l, _ := testStore.Ledger(ctx, shiftID); l.Reserved != 1 || l.Spent != 0 {
		t.Fatalf("blocked account released its hold before settlement: %+v", l)
	}
	candidate := settleCandidate(t, token)
	for i := 0; i < 2; i++ {
		if err := c.Settle(ctx, candidate); err != nil {
			t.Fatal(err)
		}
	}
	if len(b.keyIDs) != 1 || b.keyIDs[0] == "" || b.keyIDs[0] != candidate.GatewayKeyID {
		t.Fatalf("recorded key identity not used for spend logs: %v", b.keyIDs)
	}
	l, _ := testStore.Ledger(ctx, shiftID)
	if l.Reserved != 0 || math.Abs(l.Spent-0.45) > 0.00001 {
		t.Fatalf("settlement did not charge spend-log total exactly once: %+v", l)
	}
	a, err := testStore.LLMAccount(ctx, token)
	if err != nil || a.State != "reconciled" {
		t.Fatalf("account=%+v %v", a, err)
	}
	var evidence string
	if err := testPool.QueryRow(ctx, `SELECT reconciliation_evidence FROM run_llm_accounts WHERE run_token=$1`, token).Scan(&evidence); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(evidence, "litellm:spend-logs alias=ploeg-") || !strings.Contains(evidence, "entries=3") || strings.Contains(evidence, token) {
		t.Fatalf("settlement evidence=%q", evidence)
	}
}

func TestControllerSettlesBlockedAccountWhoseObservationRoundedUp(t *testing.T) {
	ctx := context.Background()
	b := &settlementBroker{observed: 0.242263, settled: 0.242263}
	c, token, shiftID := settlementFixture(t, b, true)
	if err := c.Block(ctx, token); err != nil {
		t.Fatal(err)
	}
	candidate := settleCandidate(t, token)
	if err := c.Settle(ctx, candidate); err != nil {
		t.Fatal(err)
	}
	a, err := testStore.LLMAccount(ctx, token)
	if err != nil || a.State != "reconciled" {
		t.Fatalf("account=%+v %v", a, err)
	}
	l, _ := testStore.Ledger(ctx, shiftID)
	if l.Reserved != 0 || math.Abs(l.Spent-0.2423) > 0.00001 {
		t.Fatalf("settlement did not round the spend-log total to column precision: %+v", l)
	}
	var reconciled float64
	if err := testPool.QueryRow(ctx, `SELECT reconciled_spend FROM run_llm_accounts WHERE run_token=$1`, token).Scan(&reconciled); err != nil {
		t.Fatal(err)
	}
	if math.Abs(reconciled-0.2423) > 0.00001 {
		t.Fatalf("reconciled_spend=%v want 0.2423", reconciled)
	}
}

func TestControllerSettlementNeverUndercutsObservationOrGuessesMissingSpend(t *testing.T) {
	ctx := context.Background()
	b := &settlementBroker{observed: 0.45}
	c, token, shiftID := settlementFixture(t, b, true)
	if err := c.Block(ctx, token); err != nil {
		t.Fatal(err)
	}
	candidate := settleCandidate(t, token)
	b.err = errors.New("spend logs unavailable")
	if err := c.Settle(ctx, candidate); err == nil || !strings.Contains(err.Error(), "spend logs unavailable") {
		t.Fatalf("settled without gateway evidence, or hid why: %v", err)
	}
	a, _ := testStore.LLMAccount(ctx, token)
	if l, _ := testStore.Ledger(ctx, shiftID); a.State != "blocked" || l.Reserved != 1 || l.Spent != 0 {
		t.Fatalf("refused settlement changed the ledger: %s %+v", a.State, l)
	}
	b.err = nil
	b.settled = 0.2
	if err := c.Settle(ctx, candidate); err != nil {
		t.Fatalf("spend logs below the observation left the hold in place: %v", err)
	}
	var settled float64
	var evidence string
	if err := testPool.QueryRow(ctx, `SELECT reconciled_spend::float8, reconciliation_evidence FROM run_llm_accounts WHERE run_token=$1`, token).Scan(&settled, &evidence); err != nil {
		t.Fatal(err)
	}
	if settled != 0.45 || !strings.Contains(evidence, "usd=0.2") || !strings.Contains(evidence, "settled-at-observation=0.45") {
		t.Fatalf("settled at %v with evidence %q, want the observation 0.45 and the spend-log total recorded", settled, evidence)
	}
	if l, _ := testStore.Ledger(ctx, shiftID); l.Reserved != 0 || l.Spent != 0.45 {
		t.Fatalf("ledger after settling at the observation: %+v", l)
	}
}

func TestControllerNeverSettlesMintedAccountWithoutDurableSpendSource(t *testing.T) {
	ctx := context.Background()
	c, token, shiftID := settlementFixture(t, &managedBrokerFixture{}, true)
	if err := c.Block(ctx, token); err != nil {
		t.Fatal(err)
	}
	if err := c.Settle(ctx, settleCandidate(t, token)); err == nil {
		t.Fatal("minted account settled from the key's running total")
	}
	if l, _ := testStore.Ledger(ctx, shiftID); l.Spent != 0 || l.Reserved != 1 {
		t.Fatalf("ledger changed without durable spend: %+v", l)
	}
}

func TestControllerSettlesUntouchedReservationAtZeroWithoutGateway(t *testing.T) {
	ctx := context.Background()
	b := &settlementBroker{settled: 9}
	c, token, shiftID := settlementFixture(t, b, false)
	candidate := settleCandidate(t, token)
	if candidate.MintBegan {
		t.Fatal("untouched reservation looked minted")
	}
	if err := c.Settle(ctx, candidate); err != nil {
		t.Fatal(err)
	}
	if l, _ := testStore.Ledger(ctx, shiftID); l.Reserved != 0 || l.Spent != 0 || b.settleCalls != 0 {
		t.Fatalf("untouched reservation settled incorrectly: %+v gateway=%d", l, b.settleCalls)
	}
	if err := c.Settle(ctx, store.UnsettledLLMAccount{RunToken: token, State: "reserved", MintBegan: true}); !errors.Is(err, store.ErrLLMAccountState) {
		t.Fatalf("minted reservation settled without a block: %v", err)
	}
}

func TestControllerReleasesTheHoldOfAKeyThatNeverSpentWithAnUnknownCost(t *testing.T) {
	ctx := context.Background()
	b := &settlementBroker{byAlias: true}
	c, token, shiftID := settlementFixture(t, b, true)
	if err := c.Block(ctx, token); err != nil {
		t.Fatal(err)
	}
	if err := c.Settle(ctx, settleCandidate(t, token)); err != nil {
		t.Fatalf("an alias search with no entries kept the hold: %v", err)
	}
	var known bool
	var evidence string
	if err := testPool.QueryRow(ctx, `SELECT cost_known, reconciliation_evidence FROM run_llm_accounts WHERE run_token=$1`, token).Scan(&known, &evidence); err != nil {
		t.Fatal(err)
	}
	if known || !strings.HasPrefix(evidence, "litellm:spend-logs-by-alias ") || !strings.Contains(evidence, "entries=0") {
		t.Fatalf("cost_known=%v evidence=%q; want an unknown cost read by alias", known, evidence)
	}
	if l, _ := testStore.Ledger(ctx, shiftID); l.Reserved != 0 || l.Spent != 0 {
		t.Fatalf("ledger after settling an unused key: %+v", l)
	}
}
