package store

import (
	"context"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/work"
)

func settlementRunFixture(t *testing.T, externalID string) *ClaimedRun {
	t.Helper()
	ctx := context.Background()
	itemID, _, err := testStore.IngestAssigned(ctx, work.WorkItem{Provider: "vikunja", ExternalID: externalID, Team: "silver", Title: "Settlement fixture"})
	if err != nil {
		t.Fatal(err)
	}
	shift, err := testStore.OpenShift(ctx, itemID, "silver", "agent/vik-"+externalID, 5)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := testStore.OpenRound(ctx, shift, 0, []Role{{Name: "reviewer", Cap: 3}}); err != nil {
		t.Fatal(err)
	}
	run, err := testStore.ClaimRole(ctx, "silver", "reviewer", time.Minute, 3)
	if err != nil {
		t.Fatal(err)
	}
	if err := testStore.ReserveLLMAccount(ctx, LLMAccount{RunToken: run.RunToken, Alias: "ploeg-" + run.RunToken[:12], Authorized: 3, Models: []string{"model"}, TTLSeconds: 60}); err != nil {
		t.Fatal(err)
	}
	return run
}

func finishManagedRun(t *testing.T, run *ClaimedRun) {
	t.Helper()
	if _, err := testStore.ReportOutcome(context.Background(), run.RunToken, Report(work.OutcomeNoChangeNeeded, "done", "", nil, nil, nil)); err != nil {
		t.Fatal(err)
	}
}

func unsettledByToken(t *testing.T, quietFor time.Duration) map[string]UnsettledLLMAccount {
	t.Helper()
	accounts, err := testStore.UnsettledLLMAccounts(context.Background(), 0, quietFor, 100)
	if err != nil {
		t.Fatal(err)
	}
	byToken := map[string]UnsettledLLMAccount{}
	for _, a := range accounts {
		byToken[a.RunToken] = a
	}
	return byToken
}

func TestUnsettledAccountsOfferOnlyFinishedAndQuietHolds(t *testing.T) {
	resetTables(t)
	ctx := context.Background()
	live := settlementRunFixture(t, "901")
	untouched := settlementRunFixture(t, "902")
	finishManagedRun(t, untouched)
	unissued := settlementRunFixture(t, "903")
	finishManagedRun(t, unissued)
	if blocked, err := testStore.BlockUnissuedLLMAccount(ctx, unissued.RunToken); err != nil || !blocked {
		t.Fatalf("unissued block: %t %v", blocked, err)
	}
	minted := settlementRunFixture(t, "904")
	if _, err := testStore.BeginLLMMint(ctx, minted.RunToken); err != nil {
		t.Fatal(err)
	}
	finishManagedRun(t, minted)
	spend := 0.4
	if err := testStore.RecordLLMBlocked(ctx, minted.RunToken, &spend); err != nil {
		t.Fatal(err)
	}
	unresolved := settlementRunFixture(t, "905")
	if _, err := testStore.BeginLLMMint(ctx, unresolved.RunToken); err != nil {
		t.Fatal(err)
	}
	if err := testStore.MarkLLMUnknown(ctx, unresolved.RunToken); err != nil {
		t.Fatal(err)
	}
	finishManagedRun(t, unresolved)

	recent := unsettledByToken(t, time.Hour)
	if len(recent) != 1 || recent[untouched.RunToken].State != "reserved" || recent[untouched.RunToken].MintBegan {
		t.Fatalf("recently blocked or live accounts offered for settlement: %+v", recent)
	}
	quiet := unsettledByToken(t, 0)
	if len(quiet) != 3 {
		t.Fatalf("settlement candidates: %+v", quiet)
	}
	if _, ok := quiet[live.RunToken]; ok {
		t.Fatal("running account offered for settlement")
	}
	if _, ok := quiet[unresolved.RunToken]; ok {
		t.Fatal("unknown external effect offered for settlement")
	}
	if a := quiet[unissued.RunToken]; a.State != "blocked" || a.MintBegan {
		t.Fatalf("unissued block lost its never-minted evidence: %+v", a)
	}
	if a := quiet[minted.RunToken]; a.State != "blocked" || !a.MintBegan {
		t.Fatalf("minted account looked untouched: %+v", a)
	}
	if err := testStore.ReconcileLLMAccount(ctx, untouched.RunToken, 0, "fixture"); err != nil {
		t.Fatal(err)
	}
	if _, ok := unsettledByToken(t, 0)[untouched.RunToken]; ok {
		t.Fatal("reconciled account offered again")
	}
	if _, err := testStore.UnsettledLLMAccounts(ctx, 0, 0, 101); err == nil {
		t.Fatal("unbounded settlement page accepted")
	}
}

func TestStoreUnsettledLLMAccountsCarryShiftID(t *testing.T) {
	resetTables(t)
	ctx := context.Background()

	withShift := settlementRunFixture(t, "950")
	finishManagedRun(t, withShift)

	// A historical Run that predates Shifts: finished, with an account, and
	// shift_id NULL. It must be offered for settlement but carry no ShiftID.
	// Reserve requires a running Run, so it is inserted running, reserved,
	// then finished — the same order the real path uses.
	itemID, _, err := testStore.IngestAssigned(ctx, work.WorkItem{Provider: "vikunja", ExternalID: "951", Team: "silver", Title: "legacy"})
	if err != nil {
		t.Fatal(err)
	}
	legacyToken := "legacytoken000000000001"
	if _, err := testStore.pool.Exec(ctx, `
		INSERT INTO agent_runs (work_item_id, team, run_token, state, authorized)
		VALUES ($1, 'silver', $2, 'running', 1)`, itemID, legacyToken); err != nil {
		t.Fatal(err)
	}
	if err := testStore.ReserveLLMAccount(ctx, LLMAccount{RunToken: legacyToken, Alias: "ploeg-" + legacyToken[:12], Authorized: 1, Models: []string{"m"}, TTLSeconds: 60}); err != nil {
		t.Fatal(err)
	}
	if _, err := testStore.pool.Exec(ctx, `
		UPDATE agent_runs SET state='finished', finished_at=now(), outcome='failed' WHERE run_token=$1`, legacyToken); err != nil {
		t.Fatal(err)
	}

	byToken := unsettledByToken(t, 0)
	with, ok := byToken[withShift.RunToken]
	if !ok {
		t.Fatalf("run with a shift was not offered: %+v", byToken)
	}
	if with.ShiftID == nil {
		t.Error("a Run in a Shift carried no ShiftID")
	}
	legacy, ok := byToken[legacyToken]
	if !ok {
		t.Fatalf("historical run was not offered: %+v", byToken)
	}
	if legacy.ShiftID != nil {
		t.Errorf("historical run carried ShiftID %d, want nil", *legacy.ShiftID)
	}
}

func correctableByToken(t *testing.T) map[string]UnsettledLLMAccount {
	t.Helper()
	accounts, err := testStore.CorrectableLLMAccounts(context.Background(), 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	byToken := map[string]UnsettledLLMAccount{}
	for _, a := range accounts {
		byToken[a.RunToken] = a
	}
	return byToken
}

func TestCorrectableAccountsOfferOnlySettlementsInsideTheirWindow(t *testing.T) {
	resetTables(t)
	ctx := context.Background()
	neverMinted := settlementRunFixture(t, "960")
	finishManagedRun(t, neverMinted)
	if err := testStore.ReconcileLLMAccount(ctx, neverMinted.RunToken, 0, "ploegd:mint-never-began"); err != nil {
		t.Fatal(err)
	}
	windowed := settlementRunFixture(t, "961")
	finishManagedRun(t, windowed)
	if _, err := testStore.SettleLLMAccount(ctx, windowed.RunToken, LLMSettlement{Spend: 0, Evidence: "fixture", CostKnown: true, CorrectionWindow: time.Hour}); err != nil {
		t.Fatal(err)
	}
	unsettled := settlementRunFixture(t, "962")
	finishManagedRun(t, unsettled)

	offered := correctableByToken(t)
	if len(offered) != 1 {
		t.Fatalf("correction candidates: %+v", offered)
	}
	a, ok := offered[windowed.RunToken]
	if !ok || a.State != "reconciled" || !a.MintBegan || a.QuietSince.IsZero() || a.ShiftID == nil {
		t.Fatalf("settlement inside its window not offered as a reconciled candidate: %+v", offered)
	}
	if _, err := testStore.pool.Exec(ctx, `UPDATE run_llm_accounts SET corrections_until=now() WHERE run_token=$1`, windowed.RunToken); err != nil {
		t.Fatal(err)
	}
	if offered := correctableByToken(t); len(offered) != 0 {
		t.Fatalf("settlement past its window offered: %+v", offered)
	}
	if _, err := testStore.CorrectableLLMAccounts(ctx, 0, 101); err == nil {
		t.Fatal("unbounded correction page accepted")
	}
	if _, err := testStore.SettleLLMAccount(ctx, unsettled.RunToken, LLMSettlement{Spend: 0, Evidence: "fixture", CorrectionWindow: -time.Second}); err == nil {
		t.Fatal("negative correction window accepted")
	}
}

func TestCorrectionNeverLowersASettlement(t *testing.T) {
	resetTables(t)
	ctx := context.Background()
	run := settlementRunFixture(t, "963")
	if _, err := testStore.BeginLLMMint(ctx, run.RunToken); err != nil {
		t.Fatal(err)
	}
	finishManagedRun(t, run)
	if err := testStore.RecordLLMBlocked(ctx, run.RunToken, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := testStore.SettleLLMAccount(ctx, run.RunToken, LLMSettlement{Spend: 0.5, Evidence: "first", CostKnown: true, CorrectionWindow: time.Hour}); err != nil {
		t.Fatal(err)
	}
	if _, err := testStore.SettleLLMAccount(ctx, run.RunToken, LLMSettlement{Spend: 0.3, Evidence: "lower", CostKnown: true}); err == nil {
		t.Fatal("a correction lowered the settled spend")
	}
	var adjustments int
	if err := testStore.pool.QueryRow(ctx, `SELECT count(*) FROM run_llm_adjustments WHERE run_token=$1`, run.RunToken).Scan(&adjustments); err != nil || adjustments != 0 {
		t.Fatalf("adjustments=%d err=%v", adjustments, err)
	}
}
