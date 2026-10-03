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
	b.settled = 0.2
	if err := c.Settle(ctx, candidate); !errors.Is(err, store.ErrLLMAccountState) {
		t.Fatalf("settled below recorded observation: %v", err)
	}
	b.err = errors.New("spend logs unavailable")
	if err := c.Settle(ctx, candidate); err == nil {
		t.Fatal("settled without gateway evidence")
	}
	a, _ := testStore.LLMAccount(ctx, token)
	if l, _ := testStore.Ledger(ctx, shiftID); a.State != "blocked" || l.Reserved != 1 || l.Spent != 0 {
		t.Fatalf("refused settlement changed the ledger: %s %+v", a.State, l)
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
