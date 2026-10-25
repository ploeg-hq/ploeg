package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

type settledAccountRow struct {
	state            string
	reconciled       *float64
	costKnown        bool
	correctionsUntil *time.Time
	evidence         string
	spent            float64
	hold             float64
}

func readSettledAccount(t *testing.T, runID int64) settledAccountRow {
	t.Helper()
	var v settledAccountRow
	if err := testStore.pool.QueryRow(context.Background(), `SELECT a.state,a.reconciled_spend::float8,a.cost_known,a.corrections_until,
		a.reconciliation_evidence,COALESCE(s.spent,0)::float8,h.reserved::float8
		FROM agent_runs r JOIN run_llm_accounts a USING(run_token) LEFT JOIN shifts s ON s.id=r.shift_id JOIN run_budget_holds h USING(run_token)
		WHERE r.id=$1`, runID).Scan(&v.state, &v.reconciled, &v.costKnown, &v.correctionsUntil, &v.evidence, &v.spent, &v.hold); err != nil {
		t.Fatal(err)
	}
	return v
}

func shiftAccountFixture(t *testing.T, externalID, state string, observed float64) int64 {
	t.Helper()
	run := settlementRunFixture(t, externalID)
	finishManagedRun(t, run)
	var runID int64
	if err := testStore.pool.QueryRow(context.Background(), `UPDATE run_llm_accounts a SET state=$2,observed_spend=$3,updated_at=now()-interval '3 hours'
		FROM agent_runs r WHERE a.run_token=$1 AND r.run_token=a.run_token RETURNING r.id`, run.RunToken, state, observed).Scan(&runID); err != nil {
		t.Fatal(err)
	}
	return runID
}

func settleCommand(commandID string, amount float64) OperatorSettlementCommand {
	return OperatorSettlementCommand{Consumer: "workbench", CommandID: commandID, Actor: "operator:workbench:ryan",
		AmountUSD: amount, Evidence: "provider invoice 2026-10 line 14: 0.42 USD"}
}

func operatorSettlementEvents(t *testing.T, runID int64) []map[string]any {
	t.Helper()
	rows, err := testStore.pool.Query(context.Background(), `SELECT actor,detail FROM audit_log
		WHERE action='llm.reconciled' AND (detail->>'run')::bigint=$1 ORDER BY id`, runID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var events []map[string]any
	for rows.Next() {
		var actor string
		var raw []byte
		if err := rows.Scan(&actor, &raw); err != nil {
			t.Fatal(err)
		}
		detail := map[string]any{"actor": actor}
		if err := json.Unmarshal(raw, &detail); err != nil {
			t.Fatal(err)
		}
		detail["actor"] = actor
		events = append(events, detail)
	}
	return events
}

func TestOperatorSettlesAnUnsettledAccountFromEvidenceAndReleasesItsHold(t *testing.T) {
	resetTables(t)
	runID := shiftAccountFixture(t, "911", "unknown", 0.3)
	before := readSettledAccount(t, runID)
	if before.hold != 3 {
		t.Fatalf("fixture hold %v, want 3", before.hold)
	}
	ctx := WithAttribution(context.Background(), Attribution{Consumer: "workbench", Actor: "ryan", Proof: "asserted"})

	settled, err := testStore.SettleLLMAccountFromEvidence(ctx, runID, []string{"silver"}, settleCommand("settle-1", 0.42004))
	if err != nil {
		t.Fatal(err)
	}
	if settled.Replayed || settled.SettledUSD != 0.42 || settled.ReleasedUSD != 3 || settled.PreviousState != "unknown" ||
		settled.ObservedUSD == nil || *settled.ObservedUSD != 0.3 || settled.Team != "silver" || settled.WorkItemID == nil || settled.SettledAt.IsZero() {
		t.Fatalf("settlement: %+v", settled)
	}
	after := readSettledAccount(t, runID)
	if after.state != "reconciled" || after.reconciled == nil || *after.reconciled != 0.42 || !after.costKnown || after.correctionsUntil != nil {
		t.Fatalf("account not settled final and known: %+v", after)
	}
	if after.spent != before.spent+0.42 || after.hold != 0 {
		t.Fatalf("shift spent %v (was %v), hold %v", after.spent, before.spent, after.hold)
	}
	if !strings.Contains(after.evidence, "provider invoice 2026-10 line 14") || !strings.Contains(after.evidence, "command=settle-1") {
		t.Fatalf("evidence not stored: %q", after.evidence)
	}
	events := operatorSettlementEvents(t, runID)
	if len(events) != 1 {
		t.Fatalf("want one llm.reconciled event, got %d", len(events))
	}
	event := events[0]
	attribution, _ := event[AuditAttributionKey].(map[string]any)
	if event["actor"] != "operator:workbench:ryan" || event[AuditOperatorSettlement] != true || event["spend"] != 0.42 || event["delta"] != 0.42 ||
		attribution["actor"] != "ryan" || attribution["consumer"] != "workbench" {
		t.Fatalf("audit event does not name the operator: %+v", event)
	}
	page, err := testStore.UnsettledFinishedAccounts(context.Background(), nil, 200)
	if err != nil {
		t.Fatal(err)
	}
	if page.Count != 0 {
		t.Fatalf("settled account still listed: %+v", page.Accounts)
	}
	correctable, err := testStore.CorrectableLLMAccounts(context.Background(), 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(correctable) != 0 {
		t.Fatalf("an operator settlement is offered to the correction sweep: %+v", correctable)
	}
}

func TestOperatorSettlementNeverGoesBelowARecordedObservation(t *testing.T) {
	resetTables(t)
	runID := shiftAccountFixture(t, "912", "issued", 0.3238)
	before := readSettledAccount(t, runID)

	_, err := testStore.SettleLLMAccountFromEvidence(context.Background(), runID, nil, settleCommand("under", 0.3229))
	if !errors.Is(err, ErrBelowObservation) {
		t.Fatalf("settling below the observation: err = %v, want ErrBelowObservation", err)
	}
	if after := readSettledAccount(t, runID); after.state != "issued" || after.spent != before.spent || after.hold != before.hold {
		t.Fatalf("a refused settlement changed the account: %+v", after)
	}
	if events := operatorSettlementEvents(t, runID); len(events) != 0 {
		t.Fatalf("a refused settlement was audited: %+v", events)
	}

	settled, err := testStore.SettleLLMAccountFromEvidence(context.Background(), runID, nil, settleCommand("at", 0.3238))
	if err != nil || settled.SettledUSD != 0.3238 {
		t.Fatalf("settling at the observation: %+v, %v", settled, err)
	}
}

func TestOperatorSettlementIsIdempotentPerCommand(t *testing.T) {
	resetTables(t)
	runID := shiftAccountFixture(t, "913", "unknown", 0)
	other := shiftAccountFixture(t, "914", "unknown", 0)
	first, err := testStore.SettleLLMAccountFromEvidence(context.Background(), runID, nil, settleCommand("once", 0.5))
	if err != nil {
		t.Fatal(err)
	}
	spent := readSettledAccount(t, runID).spent

	again, err := testStore.SettleLLMAccountFromEvidence(context.Background(), runID, nil, settleCommand("once", 0.5))
	if err != nil || !again.Replayed || again.SettledUSD != first.SettledUSD || again.ReleasedUSD != first.ReleasedUSD || !again.SettledAt.Equal(first.SettledAt) {
		t.Fatalf("replay: %+v, %v (first %+v)", again, err, first)
	}
	if after := readSettledAccount(t, runID); after.spent != spent {
		t.Fatalf("a replay charged the Shift again: %v, want %v", after.spent, spent)
	}
	if events := operatorSettlementEvents(t, runID); len(events) != 1 {
		t.Fatalf("a replay wrote another audit event: %d", len(events))
	}

	conflicts := map[string]struct {
		run int64
		cmd OperatorSettlementCommand
	}{
		"another amount": {runID, settleCommand("once", 0.6)},
		"another Run":    {other, settleCommand("once", 0.5)},
		"another evidence": {runID, func() OperatorSettlementCommand {
			c := settleCommand("once", 0.5)
			c.Evidence = "a different invoice"
			return c
		}()},
		"another actor": {runID, func() OperatorSettlementCommand {
			c := settleCommand("once", 0.5)
			c.Actor = "operator:workbench:someone"
			return c
		}()},
	}
	for name, c := range conflicts {
		if _, err := testStore.SettleLLMAccountFromEvidence(context.Background(), c.run, nil, c.cmd); !errors.Is(err, ErrSettlementCommandConflict) {
			t.Fatalf("%s: err = %v, want ErrSettlementCommandConflict", name, err)
		}
	}
	if _, err := testStore.SettleLLMAccountFromEvidence(context.Background(), runID, nil, settleCommand("twice", 0.5)); !errors.Is(err, ErrAccountAlreadySettled) {
		t.Fatalf("a second command on a settled account: err = %v, want ErrAccountAlreadySettled", err)
	}
	if state := readSettledAccount(t, other).state; state != "unknown" {
		t.Fatalf("a conflicting command settled another Run: %s", state)
	}
}

func TestOperatorSettlesOnlyAnAccountTheUnsettledListShows(t *testing.T) {
	resetTables(t)
	ctx := context.Background()
	refused := map[string]int64{
		"reserved":         seedUnsettledFixture(t, 0, unsettledFixture{team: "silver", state: "reserved", finished: true, authorized: 1, age: time.Hour}),
		"running":          seedUnsettledFixture(t, 1, unsettledFixture{team: "silver", state: "issued", finished: false, authorized: 1, age: time.Hour}),
		"recently blocked": seedUnsettledFixture(t, 2, unsettledFixture{team: "silver", state: "blocked", finished: true, authorized: 1, age: 0}),
	}
	stuck := seedUnsettledFixture(t, 3, unsettledFixture{team: "silver", state: "blocked", finished: true, authorized: 1, age: 0})
	if _, err := testStore.pool.Exec(ctx, `UPDATE agent_runs SET finished_at=now()-interval '2 hours' WHERE id=$1`, stuck); err != nil {
		t.Fatal(err)
	}
	gold := seedUnsettledFixture(t, 4, unsettledFixture{team: "gold", state: "unknown", finished: true, authorized: 1, age: time.Hour})

	for name, runID := range refused {
		if _, err := testStore.SettleLLMAccountFromEvidence(ctx, runID, nil, settleCommand("cmd-"+strings.ReplaceAll(name, " ", "-"), 0.1)); !errors.Is(err, ErrAccountNotUnsettled) {
			t.Fatalf("%s account: err = %v, want ErrAccountNotUnsettled", name, err)
		}
	}
	if _, err := testStore.SettleLLMAccountFromEvidence(ctx, gold, []string{"silver"}, settleCommand("out-of-scope", 0.1)); !errors.Is(err, ErrUnknownRun) {
		t.Fatalf("another team's Run: err = %v, want ErrUnknownRun", err)
	}
	if _, err := testStore.SettleLLMAccountFromEvidence(ctx, 1<<40, nil, settleCommand("missing", 0.1)); !errors.Is(err, ErrUnknownRun) {
		t.Fatalf("a missing Run: err = %v, want ErrUnknownRun", err)
	}
	if settled, err := testStore.SettleLLMAccountFromEvidence(ctx, stuck, nil, settleCommand("stuck", 0.1)); err != nil || settled.PreviousState != "blocked" {
		t.Fatalf("a blocked account stuck past the hour: %+v, %v", settled, err)
	}
}

func TestOperatorSettlementRefusesAnAmountOrEvidenceItCannotRecord(t *testing.T) {
	resetTables(t)
	runID := seedUnsettledFixture(t, 0, unsettledFixture{team: "silver", state: "unknown", finished: true, authorized: 1, age: time.Hour})
	invalid := map[string]OperatorSettlementCommand{
		"negative amount": settleCommand("a", -0.01),
		"huge amount":     settleCommand("b", 1e8),
		"blank evidence":  func() OperatorSettlementCommand { c := settleCommand("c", 0.1); c.Evidence = "  "; return c }(),
		"long evidence": func() OperatorSettlementCommand {
			c := settleCommand("d", 0.1)
			c.Evidence = strings.Repeat("x", MaxSettlementEvidenceRunes+1)
			return c
		}(),
		"no actor":   func() OperatorSettlementCommand { c := settleCommand("e", 0.1); c.Actor = ""; return c }(),
		"no command": settleCommand("", 0.1),
	}
	for name, cmd := range invalid {
		if _, err := testStore.SettleLLMAccountFromEvidence(context.Background(), runID, nil, cmd); !errors.Is(err, ErrInvalidSettlement) {
			t.Fatalf("%s: err = %v, want ErrInvalidSettlement", name, err)
		}
	}
	if state := readSettledAccount(t, runID).state; state != "unknown" {
		t.Fatalf("an invalid settlement changed the account: %s", state)
	}
	if _, err := testStore.SettleLLMAccountFromEvidence(context.Background(), runID, nil, settleCommand("zero", 0)); err != nil {
		t.Fatalf("a zero amount with evidence: %v", err)
	}
}
