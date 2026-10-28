package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/spendcap"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

var spendCapDay = time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)

func withSpendCaps(t *testing.T, caps spendcap.Caps, at time.Time) {
	t.Helper()
	testStore.SetTeamSpendCaps(caps)
	testStore.clock = func() time.Time { return at }
	t.Cleanup(func() {
		testStore.SetTeamSpendCaps(nil)
		testStore.clock = nil
	})
}

func pendingCappedRuns(t *testing.T, team string, n int, roleCap float64) []int64 {
	t.Helper()
	ctx := context.Background()
	items := make([]int64, 0, n)
	for i := 0; i < n; i++ {
		itemID, _, err := testStore.IngestAssigned(ctx, work.WorkItem{
			Provider: "vikunja", ExternalID: fmt.Sprintf("%s-cap-%d", team, i), Team: team, Title: "t",
		})
		if err != nil {
			t.Fatalf("IngestAssigned: %v", err)
		}
		shiftID, err := testStore.OpenShift(ctx, itemID, team, fmt.Sprintf("agent/%s-cap-%d", team, i), 5)
		if err != nil {
			t.Fatalf("OpenShift: %v", err)
		}
		if _, err := testStore.OpenRound(ctx, shiftID, 0, []Role{{Name: "analyst", Cap: roleCap}}); err != nil {
			t.Fatalf("OpenRound: %v", err)
		}
		items = append(items, itemID)
	}
	return items
}

func claimAnalyst(team string, roleCap float64) (*ClaimedRun, error) {
	return testStore.ClaimRole(context.Background(), team, "analyst", time.Minute, roleCap)
}

func spendCapAudits(t *testing.T, team string) []map[string]string {
	t.Helper()
	rows, err := testStore.pool.Query(context.Background(), `SELECT detail->>'team', detail->>'period', detail->>'periodStart', detail->>'reason'
		FROM audit_log WHERE action = 'team.spend_capped' AND detail->>'team' = $1 ORDER BY id`, team)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []map[string]string
	for rows.Next() {
		var team, period, start, reason string
		if err := rows.Scan(&team, &period, &start, &reason); err != nil {
			t.Fatal(err)
		}
		out = append(out, map[string]string{"team": team, "period": period, "periodStart": start, "reason": reason})
	}
	return out
}

func attemptsOf(t *testing.T, itemID int64) int {
	t.Helper()
	var n int
	if err := testStore.pool.QueryRow(context.Background(), `SELECT attempts FROM work_items WHERE id = $1`, itemID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func settle(t *testing.T, run *ClaimedRun, usage string) {
	t.Helper()
	if _, err := testStore.ReportOutcome(context.Background(), run.RunToken, Report(work.OutcomeNoChangeNeeded, "done", "", nil, []byte(usage), nil)); err != nil {
		t.Fatal(err)
	}
}

func TestTeamSpendCapRefusesTheClaimThatWouldCrossIt(t *testing.T) {
	resetTables(t)
	withSpendCaps(t, spendcap.Caps{"silver": {USD: 1, Period: spendcap.Day}}, spendCapDay)
	items := pendingCappedRuns(t, "silver", 2, 0.6)

	if _, err := claimAnalyst("silver", 0.6); err != nil {
		t.Fatalf("first claim within the cap: %v", err)
	}
	_, err := claimAnalyst("silver", 0.6)
	if !errors.Is(err, ErrTeamSpendCapReached) {
		t.Fatalf("second claim = %v, want ErrTeamSpendCapReached", err)
	}
	if n, _ := testStore.PendingRuns(context.Background(), "silver", "analyst"); n != 1 {
		t.Errorf("pending = %d, want 1: a refused claim must leave its Run pending", n)
	}
	if got := attemptsOf(t, items[1]); got != 0 {
		t.Errorf("refused item attempts = %d, want 0", got)
	}
	audits := spendCapAudits(t, "silver")
	if len(audits) != 1 {
		t.Fatalf("spend-cap audits = %v, want one", audits)
	}
	a := audits[0]
	if a["period"] != "day" || a["periodStart"] != "2026-10-27" || !strings.Contains(a["reason"], "team silver reached its spend cap of 1.00 USD for the day starting 2026-10-27") {
		t.Errorf("audit = %v, want the team, period and reason", a)
	}
	if _, err := claimAnalyst("silver", 0.6); !errors.Is(err, ErrTeamSpendCapReached) {
		t.Fatalf("repeated claim = %v, want ErrTeamSpendCapReached", err)
	}
	if n := len(spendCapAudits(t, "silver")); n != 1 {
		t.Errorf("spend-cap audits after a repeated claim = %d, want 1 per Run and period", n)
	}
}

func TestTeamSpendCapStartsRunsUpToTheCap(t *testing.T) {
	resetTables(t)
	withSpendCaps(t, spendcap.Caps{"silver": {USD: 1, Period: spendcap.Day}}, spendCapDay)
	pendingCappedRuns(t, "silver", 3, 0.5)

	for i := 0; i < 2; i++ {
		if _, err := claimAnalyst("silver", 0.5); err != nil {
			t.Fatalf("claim %d up to the cap: %v", i+1, err)
		}
	}
	if _, err := claimAnalyst("silver", 0.5); !errors.Is(err, ErrTeamSpendCapReached) {
		t.Fatalf("claim past the cap = %v, want ErrTeamSpendCapReached", err)
	}
}

func TestTeamWithoutSpendCapClaimsAsBefore(t *testing.T) {
	resetTables(t)
	withSpendCaps(t, spendcap.Caps{"gold": {USD: 1, Period: spendcap.Day}}, spendCapDay)
	pendingCappedRuns(t, "silver", 3, 0.6)

	for i := 0; i < 3; i++ {
		if _, err := claimAnalyst("silver", 0.6); err != nil {
			t.Fatalf("uncapped claim %d: %v", i+1, err)
		}
	}
	if n := len(spendCapAudits(t, "silver")); n != 0 {
		t.Errorf("uncapped team has %d spend-cap audits", n)
	}
}

func TestTeamSpendCapHeadroomReturnsWhenARunSettlesOrReleases(t *testing.T) {
	for name, usage := range map[string]string{"settled below its hold": `{"costUsd":0.3}`, "released with no cost": `{}`} {
		t.Run(name, func(t *testing.T) {
			resetTables(t)
			withSpendCaps(t, spendcap.Caps{"silver": {USD: 1, Period: spendcap.Day}}, spendCapDay)
			pendingCappedRuns(t, "silver", 2, 0.6)
			first, err := claimAnalyst("silver", 0.6)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := claimAnalyst("silver", 0.6); !errors.Is(err, ErrTeamSpendCapReached) {
				t.Fatalf("claim while held = %v, want ErrTeamSpendCapReached", err)
			}
			settle(t, first, usage)
			if _, err := claimAnalyst("silver", 0.6); err != nil {
				t.Fatalf("claim after the hold was settled: %v", err)
			}
		})
	}
}

func TestTeamSpendCapCountsManagedSettlement(t *testing.T) {
	resetTables(t)
	withSpendCaps(t, spendcap.Caps{"silver": {USD: 1, Period: spendcap.Day}}, spendCapDay)
	ctx := context.Background()
	pendingCappedRuns(t, "silver", 3, 0.6)
	first, err := claimAnalyst("silver", 0.6)
	if err != nil {
		t.Fatal(err)
	}
	if err := testStore.ReserveLLMAccount(ctx, LLMAccount{RunToken: first.RunToken, Alias: "cap", Authorized: 0.6, Models: []string{"model"}, TTLSeconds: 60}); err != nil {
		t.Fatal(err)
	}
	settle(t, first, `{"costUsd":0}`)
	if _, err := claimAnalyst("silver", 0.6); !errors.Is(err, ErrTeamSpendCapReached) {
		t.Fatalf("claim while the managed account holds = %v, want ErrTeamSpendCapReached", err)
	}
	if _, err := testStore.pool.Exec(ctx, `UPDATE run_llm_accounts SET state = 'blocked' WHERE run_token = $1`, first.RunToken); err != nil {
		t.Fatal(err)
	}
	if err := testStore.ReconcileLLMAccount(ctx, first.RunToken, 0.45, "gateway spend log"); err != nil {
		t.Fatal(err)
	}
	if _, err := claimAnalyst("silver", 0.6); !errors.Is(err, ErrTeamSpendCapReached) {
		t.Fatalf("claim over settled 0.45 plus 0.6 = %v, want ErrTeamSpendCapReached", err)
	}
	spends, err := testStore.TeamPeriodSpends(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(spends) != 1 || spends[0].SettledUSD != 0.45 || spends[0].HeldUSD != 0 {
		t.Fatalf("period spend = %+v, want 0.45 settled and nothing held", spends)
	}
}

func TestTeamSpendCapRollsOverWithItsPeriod(t *testing.T) {
	resetTables(t)
	now := time.Now().UTC()
	withSpendCaps(t, spendcap.Caps{"silver": {USD: 1, Period: spendcap.Day}}, now)
	pendingCappedRuns(t, "silver", 2, 0.6)
	first, err := claimAnalyst("silver", 0.6)
	if err != nil {
		t.Fatal(err)
	}
	settle(t, first, `{"costUsd":0.6}`)
	if _, err := claimAnalyst("silver", 0.6); !errors.Is(err, ErrTeamSpendCapReached) {
		t.Fatalf("claim over the day's settled spend = %v, want ErrTeamSpendCapReached", err)
	}
	testStore.clock = func() time.Time { return spendcap.Day.End(now).Add(time.Minute) }
	if _, err := claimAnalyst("silver", 0.6); err != nil {
		t.Fatalf("claim in the next day: %v", err)
	}
}

func TestTeamSpendCapHoldsUnderConcurrentClaims(t *testing.T) {
	resetTables(t)
	withSpendCaps(t, spendcap.Caps{"silver": {USD: 1, Period: spendcap.Day}}, spendCapDay)
	pendingCappedRuns(t, "silver", 6, 0.3)

	const claimers = 6
	var wg sync.WaitGroup
	results := make(chan error, claimers)
	start := make(chan struct{})
	for i := 0; i < claimers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := claimAnalyst("silver", 0.3)
			results <- err
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	claimed, refused := 0, 0
	for err := range results {
		switch {
		case err == nil:
			claimed++
		case errors.Is(err, ErrTeamSpendCapReached):
			refused++
		default:
			t.Fatalf("claim failed: %v", err)
		}
	}
	if claimed != 3 || refused != 3 {
		t.Fatalf("claimed %d, refused %d; want 3 and 3 under a 1.00 USD cap at 0.30 each", claimed, refused)
	}
}

func TestLegacyClaimRefusedAtTheTeamSpendCap(t *testing.T) {
	resetTables(t)
	withSpendCaps(t, spendcap.Caps{"silver": {USD: 1, Period: spendcap.Day}}, spendCapDay)
	pendingCappedRuns(t, "silver", 1, 1)
	run, err := claimAnalyst("silver", 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := testStore.IngestAssigned(context.Background(), work.WorkItem{Provider: "vikunja", ExternalID: "legacy", Team: "silver", Title: "t"}); err != nil {
		t.Fatal(err)
	}
	if _, err := testStore.ClaimWithin(context.Background(), "silver", time.Minute, 0); !errors.Is(err, ErrTeamSpendCapReached) {
		t.Fatalf("legacy claim at the cap = %v, want ErrTeamSpendCapReached", err)
	}
	settle(t, run, `{"costUsd":0.2}`)
	if _, err := testStore.ClaimWithin(context.Background(), "silver", time.Minute, 0); err != nil {
		t.Fatalf("legacy claim with headroom: %v", err)
	}
}
