package store

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestAdmitAskRefusesOnceTheAllowanceCannotCoverTheBudget(t *testing.T) {
	resetTables(t)
	ctx := context.Background()
	id, _ := ingestItem(t)
	at := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 2; i++ {
		in := askInput(id, fmt.Sprintf("fill-%d", i))
		in.LimitUSD, in.At = 0.05, at
		if _, _, err := testStore.AdmitAsk(ctx, "workbench", "alice", in); err != nil {
			t.Fatalf("admission %d: %v", i, err)
		}
	}
	in := askInput(id, "over")
	in.LimitUSD, in.At = 0.05, at
	_, _, err := testStore.AdmitAsk(ctx, "workbench", "alice", in)
	var exhausted *AllowanceExhaustedError
	if !errors.As(err, &exhausted) || !errors.Is(err, ErrAllowanceExhausted) {
		t.Fatalf("third Ask admitted past the allowance: %v", err)
	}
	a := exhausted.Allowance
	if a.LimitUSD != 0.05 || a.HeldUSD != 0.04 || a.RemainingUSD != 0.01 || a.AskCount != 2 ||
		!a.ResetAt.Equal(time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)) || a.ScopeKind != "team" || a.ScopeID != "silver" {
		t.Fatalf("refused allowance: %+v", a)
	}
	var runs int
	if err := testStore.pool.QueryRow(ctx, `SELECT count(*) FROM agent_runs WHERE role = 'ask'`).Scan(&runs); err != nil || runs != 2 {
		t.Fatalf("refusal recorded a Run: %d %v", runs, err)
	}
}

func TestAdmitAskAdmitsExactlyOneOfTwoConcurrentAsksThatTogetherExceedTheAllowance(t *testing.T) {
	resetTables(t)
	ctx := context.Background()
	id, _ := ingestItem(t)
	for round := 0; round < 5; round++ {
		if _, err := testStore.pool.Exec(ctx, `DELETE FROM agent_runs WHERE role = 'ask'`); err != nil {
			t.Fatal(err)
		}
		if _, err := testStore.pool.Exec(ctx, `DELETE FROM inference_allowances`); err != nil {
			t.Fatal(err)
		}
		start := make(chan struct{})
		errs := make([]error, 2)
		var wg sync.WaitGroup
		for i := range errs {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				in := askInput(id, fmt.Sprintf("race-%d-%d", round, i))
				in.LimitUSD = 0.03
				<-start
				_, _, errs[i] = testStore.AdmitAsk(ctx, "workbench", "alice", in)
			}(i)
		}
		close(start)
		wg.Wait()
		admitted, refused := 0, 0
		for _, err := range errs {
			switch {
			case err == nil:
				admitted++
			case errors.Is(err, ErrAllowanceExhausted):
				refused++
			default:
				t.Fatalf("round %d: %v", round, err)
			}
		}
		if admitted != 1 || refused != 1 {
			t.Fatalf("round %d: admitted %d, refused %d", round, admitted, refused)
		}
	}
}

func TestAdmitAskCountsAgainstTheMonthItWasAskedIn(t *testing.T) {
	resetTables(t)
	ctx := context.Background()
	id, _ := ingestItem(t)
	lastSecond := time.Date(2026, 1, 31, 23, 59, 59, 0, time.UTC)
	in := askInput(id, "january")
	in.LimitUSD, in.At = 0.02, lastSecond
	if _, _, err := testStore.AdmitAsk(ctx, "workbench", "alice", in); err != nil {
		t.Fatal(err)
	}
	in = askInput(id, "january-again")
	in.LimitUSD, in.At = 0.02, lastSecond
	if _, _, err := testStore.AdmitAsk(ctx, "workbench", "alice", in); !errors.Is(err, ErrAllowanceExhausted) {
		t.Fatalf("January allowance not exhausted: %v", err)
	}
	in = askInput(id, "february")
	in.LimitUSD, in.At = 0.02, lastSecond.Add(time.Second)
	if _, _, err := testStore.AdmitAsk(ctx, "workbench", "alice", in); err != nil {
		t.Fatalf("February counted January's Ask: %v", err)
	}
	jan, err := testStore.Allowance(ctx, AllowancePurposeAsk, AllowanceScopeTeam, "silver", lastSecond, 2)
	if err != nil {
		t.Fatal(err)
	}
	if !jan.PeriodStart.Equal(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)) || !jan.ResetAt.Equal(time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)) ||
		jan.AskCount != 1 || jan.LimitUSD != 0.02 {
		t.Fatalf("January: %+v", jan)
	}
	march, err := testStore.Allowance(ctx, AllowancePurposeAsk, AllowanceScopeTeam, "silver", time.Date(2026, 3, 2, 0, 0, 0, 0, time.UTC), 2)
	if err != nil || march.LimitUSD != 2 || march.RemainingUSD != 2 || march.AskCount != 0 {
		t.Fatalf("an unopened period: %+v %v", march, err)
	}
}

func TestFinishAskFinishesItsRunAndSettlementMovesTheHoldToSettled(t *testing.T) {
	resetTables(t)
	ctx := context.Background()
	id, _ := ingestItem(t)
	ask, _, err := testStore.AdmitAsk(ctx, "workbench", "alice", askInput(id, "settle"))
	if err != nil {
		t.Fatal(err)
	}
	if err := testStore.ReserveLLMAccount(ctx, LLMAccount{RunToken: ask.RunToken, Alias: "alias-settle", Authorized: 0.02, Models: []string{"m"}, TTLSeconds: 300}); err != nil {
		t.Fatal(err)
	}
	finished, err := testStore.FinishAsk(ctx, "workbench", "alice", "settle")
	if err != nil || finished.State != "finished" || finished.FinishedAt == nil {
		t.Fatalf("finish: %v %+v", err, finished)
	}
	again, err := testStore.FinishAsk(ctx, "workbench", "alice", "settle")
	if err != nil || !again.FinishedAt.Equal(*finished.FinishedAt) {
		t.Fatalf("second finish: %v %+v", err, again)
	}
	var state string
	if err := testStore.pool.QueryRow(ctx, `SELECT state FROM agent_runs WHERE run_token = $1`, ask.RunToken).Scan(&state); err != nil || state != "finished" {
		t.Fatalf("run state %s %v", state, err)
	}
	held, err := testStore.Allowance(ctx, AllowancePurposeAsk, AllowanceScopeTeam, "silver", time.Now(), 2)
	if err != nil || held.HeldUSD != 0.02 || held.SettledUSD != 0 {
		t.Fatalf("a finished, unsettled Ask must still hold: %+v %v", held, err)
	}
	if _, err := testStore.pool.Exec(ctx, `UPDATE run_llm_accounts SET state = 'blocked' WHERE run_token = $1`, ask.RunToken); err != nil {
		t.Fatal(err)
	}
	if _, err := testStore.SettleLLMAccount(ctx, ask.RunToken, LLMSettlement{Spend: 0.0123, Evidence: "test", CostKnown: true}); err != nil {
		t.Fatal(err)
	}
	settled, err := testStore.Allowance(ctx, AllowancePurposeAsk, AllowanceScopeTeam, "silver", time.Now(), 2)
	if err != nil || settled.HeldUSD != 0 || settled.SettledUSD != 0.0123 || settled.RemainingUSD != 1.9877 {
		t.Fatalf("settled allowance: %+v %v", settled, err)
	}
	read, err := testStore.Ask(ctx, "workbench", "alice", "settle")
	if err != nil || read.Spend.CostStatus != "settled" || read.Spend.USD == nil || *read.Spend.USD != 0.0123 || read.Spend.KeyState != "reconciled" {
		t.Fatalf("settled ask: %+v %v", read, err)
	}
}
