package httpapi

import (
	"context"
	"fmt"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/llmbroker"
	"github.com/ploeg-hq/ploeg/pkg/store"
)

var replaySeeds = []uint64{19, 20, 21}

type replayEvent struct {
	name  string
	apply func(context.Context)
}

type replayRun struct {
	token, alias string
	readings     []float64
}

func startReplayRun(t *testing.T, c *LLMControl, team, role string) replayRun {
	t.Helper()
	ctx := context.Background()
	claimed, err := testStore.ClaimRole(ctx, team, role, 10*time.Minute, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Reserve(ctx, claimed.RunToken); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Issue(ctx, claimed.RunToken); err != nil {
		t.Fatal(err)
	}
	account, err := testStore.LLMAccount(ctx, claimed.RunToken)
	if err != nil {
		t.Fatal(err)
	}
	return replayRun{token: claimed.RunToken, alias: account.Alias}
}

func replayEventsFor(g *ledgerGateway, c *LLMControl, rng *rand.Rand, r *replayRun) []replayEvent {
	var events []replayEvent
	for n := range 1 + rng.IntN(4) {
		usd := float64(100+rng.IntN(2400)) / 1e4
		events = append(events,
			replayEvent{fmt.Sprintf("%s charge %d", r.alias, n), func(context.Context) { _ = g.charge(r.alias, usd) }},
			replayEvent{fmt.Sprintf("%s observe %d", r.alias, n), func(ctx context.Context) {
				if spend, err := c.Spend(ctx, r.token); err == nil {
					r.readings = append(r.readings, spend)
				}
			}})
	}
	events = append(events,
		replayEvent{r.alias + " block", func(ctx context.Context) { _ = c.Block(ctx, r.token) }},
		replayEvent{r.alias + " finish", func(ctx context.Context) { _ = finishLedgerRun(ctx, r.token) }},
		replayEvent{r.alias + " in-flight entry", func(context.Context) { g.landInFlight(r.alias, float64(1+rng.IntN(300))/1e4) }},
		replayEvent{r.alias + " stale observation", func(ctx context.Context) {
			if len(r.readings) > 0 {
				_ = testStore.RecordLLMObserved(ctx, r.token, r.readings[0])
			}
		}},
	)
	return events
}

func staleSweepEvents(c *LLMControl) []replayEvent {
	var listed []store.UnsettledLLMAccount
	return []replayEvent{
		{"list for a later sweep", func(ctx context.Context) {
			unsettled, _ := testStore.UnsettledLLMAccounts(ctx, 0, 0, 100)
			correctable, _ := testStore.CorrectableLLMAccounts(ctx, 0, 100)
			listed = append(unsettled, correctable...)
		}},
		{"act on the stale listing", func(ctx context.Context) {
			for _, a := range listed {
				if a.State == "reconciled" {
					_, _ = c.Correct(ctx, a)
					continue
				}
				_ = c.Settle(ctx, a)
			}
		}},
	}
}

func shuffledWithDuplicates(rng *rand.Rand, events []replayEvent) []replayEvent {
	replayed := append([]replayEvent{}, events...)
	for range len(events) / 2 {
		replayed = append(replayed, events[rng.IntN(len(events))])
	}
	rng.Shuffle(len(replayed), func(i, j int) { replayed[i], replayed[j] = replayed[j], replayed[i] })
	return replayed
}

func drainLedger(t *testing.T, c *LLMControl, tokens []string) {
	t.Helper()
	ctx := context.Background()
	for _, token := range tokens {
		_ = finishLedgerRun(ctx, token)
	}
	for range 3 {
		runLedgerSweeps(ctx, c)
	}
}

func TestReplayedOutOfOrderEventsSettleEveryRunToGatewayTruth(t *testing.T) {
	for _, seed := range replaySeeds {
		t.Run(fmt.Sprintf("seed %d", seed), func(t *testing.T) {
			ctx := context.Background()
			reset(t)
			g, broker := newLedgerGateway(t)
			c, err := NewLLMControl(testStore, broker, ledgerRoles)
			if err != nil {
				t.Fatal(err)
			}
			c.CorrectionWindow = time.Hour
			rng := rand.New(rand.NewPCG(seed, seed))
			var events []replayEvent
			var tokens []string
			for i, team := range []string{"bronze", "silver"} {
				ledgerShift(t, team, fmt.Sprintf("replay-%d-%d", seed, i), 1.5)
				for _, role := range []string{"reviewer", "tester"} {
					r := startReplayRun(t, c, team, role)
					tokens = append(tokens, r.token)
					events = append(events, replayEventsFor(g, c, rng, &r)...)
				}
			}
			events = append(events, staleSweepEvents(c)...)
			events = append(events, replayEvent{"settlement sweep", func(ctx context.Context) { runLedgerSweeps(ctx, c) }})

			before := assertAccountingInvariants(t)
			for _, e := range shuffledWithDuplicates(rng, events) {
				e.apply(ctx)
				after := assertAccountingInvariants(t)
				assertNeverDecreases(t, before, after, e.name)
				before = after
				if t.Failed() {
					t.Fatalf("seed %d stopped after %q", seed, e.name)
				}
			}
			drainLedger(t, c, tokens)
			assertNeverDecreases(t, before, assertAccountingInvariants(t), "drain")
			assertSettledToGatewayTruth(t, g)
		})
	}
}

func crashedRun(t *testing.T, c *LLMControl, crash func(context.Context, string)) (string, string) {
	t.Helper()
	ctx := context.Background()
	ledgerShift(t, "bronze", "crash-"+t.Name(), 1.5)
	claimed, err := testStore.ClaimRole(ctx, "bronze", "reviewer", 10*time.Minute, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Reserve(ctx, claimed.RunToken); err != nil {
		t.Fatal(err)
	}
	crash(ctx, claimed.RunToken)
	account, err := testStore.LLMAccount(ctx, claimed.RunToken)
	if err != nil {
		t.Fatal(err)
	}
	return claimed.RunToken, account.Alias
}

func TestACrashAtEveryMintBoundarySettlesToGatewayTruth(t *testing.T) {
	for name, c := range map[string]struct {
		crash  func(*testing.T, *ledgerGateway, *LLMControl) func(context.Context, string)
		charge float64
	}{
		"the mint response is lost": {charge: 0.21, crash: func(t *testing.T, g *ledgerGateway, ctl *LLMControl) func(context.Context, string) {
			return func(ctx context.Context, token string) {
				a, _ := testStore.LLMAccount(ctx, token)
				g.mu.Lock()
				g.loseResponse[a.Alias] = true
				g.mu.Unlock()
				if _, err := ctl.Issue(ctx, token); err == nil {
					t.Fatal("a lost mint response issued a credential")
				}
			}
		}},
		"ploegd stops before it calls the gateway": {crash: func(t *testing.T, _ *ledgerGateway, _ *LLMControl) func(context.Context, string) {
			return func(ctx context.Context, token string) {
				if _, err := testStore.BeginLLMMint(ctx, token); err != nil {
					t.Fatal(err)
				}
			}
		}},
		"ploegd stops after the mint, before recording it": {charge: 0.37, crash: func(t *testing.T, _ *ledgerGateway, ctl *LLMControl) func(context.Context, string) {
			return func(ctx context.Context, token string) {
				a, err := testStore.BeginLLMMint(ctx, token)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := ctl.Broker.Mint(ctx, llmbroker.MintRequest{RunToken: token, BudgetUSD: a.Authorized, Models: a.Models, TTL: time.Hour}); err != nil {
					t.Fatal(err)
				}
			}
		}},
	} {
		t.Run(name, func(t *testing.T) {
			reset(t)
			g, broker := newLedgerGateway(t)
			ctl, err := NewLLMControl(testStore, broker, ledgerRoles)
			if err != nil {
				t.Fatal(err)
			}
			ctl.CorrectionWindow = time.Hour
			token, alias := crashedRun(t, ctl, c.crash(t, g, ctl))
			if c.charge > 0 {
				if err := g.charge(alias, c.charge); err != nil {
					t.Fatal(err)
				}
			}
			assertAccountingInvariants(t)
			drainLedger(t, ctl, []string{token})
			assertAccountingInvariants(t)
			assertSettledToGatewayTruth(t, g)
			if got := g.totals()[alias]; got != c.charge {
				t.Fatalf("the gateway recorded %v for %s, the crash case charged %v", got, alias, c.charge)
			}
		})
	}
}
