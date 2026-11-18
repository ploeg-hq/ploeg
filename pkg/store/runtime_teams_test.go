package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/spendcap"
	"github.com/ploeg-hq/ploeg/pkg/team"
)

func runtimeManifest(key string, usd float64) team.Manifest {
	return team.Manifest{
		Key:   key,
		Name:  "builder · openhands",
		Plan:  team.RuntimePlan{Rounds: []team.RuntimeRound{{Roles: []team.RuntimeRole{{Name: "builder", Writes: true, Harness: "openhands", Model: "m"}}}}},
		Spend: team.RuntimeSpend{Cap: &spendcap.Cap{USD: usd, Period: spendcap.Period("month")}},
	}
}

var activeDecision = team.Decision{State: team.StateActive}

func teamLimits(now time.Time) TeamLimits {
	return TeamLimits{Max: 3, Allowance: spendcap.Cap{USD: 100, Period: spendcap.Period("month")}, Reservation: team.AliasReservation, Now: now}
}

func person(actor string) TeamAudit {
	return TeamAudit{Attribution: Attribution{Consumer: "workbench", Actor: actor, Proof: "proven"}}
}

func teamCode(t *testing.T, err error) string {
	t.Helper()
	var te *TeamError
	if !errors.As(err, &te) {
		t.Fatalf("want a TeamError, got %v", err)
	}
	return te.Code
}

func mustCreate(t *testing.T, key string, usd float64) RuntimeTeam {
	t.Helper()
	got, created, err := testStore.CreateRuntimeTeam(context.Background(), "default", runtimeManifest(key, usd), activeDecision, teamLimits(time.Now()), person("p"), team.MustNewID)
	if err != nil || !created {
		t.Fatalf("create %s: %v %v", key, created, err)
	}
	return got
}

func TestCreatingARuntimeTeamIsIdempotentByKey(t *testing.T) {
	withCleanTeams(t)
	ctx := context.Background()
	first := mustCreate(t, "docs-review", 10)
	if !team.IsID(first.ID) || first.Key != "docs-review" || first.State != team.StateActive {
		t.Fatalf("created %+v", first)
	}
	again, created, err := testStore.CreateRuntimeTeam(ctx, "default", runtimeManifest("docs-review", 10), activeDecision, teamLimits(time.Now()), person("p"), team.MustNewID)
	if err != nil || created || again.ID != first.ID {
		t.Fatalf("a repeat created %v (%v) id %s", created, err, again.ID)
	}
	_, _, err = testStore.CreateRuntimeTeam(ctx, "default", runtimeManifest("docs-review", 20), activeDecision, teamLimits(time.Now()), person("p"), team.MustNewID)
	if teamCode(t, err) != TeamExists {
		t.Fatalf("another body: %v", err)
	}
	if auditCount(t, AuditTeamCreated) != 1 {
		t.Fatal("not one team.created")
	}
}

func TestARuntimeKeyNeverTakesAStaticKey(t *testing.T) {
	withCleanTeams(t)
	if _, err := testStore.ReconcileStaticTeams(context.Background(), []StaticTeam{{Key: "bronze"}}, sequentialIDs()); err != nil {
		t.Fatal(err)
	}
	_, _, err := testStore.CreateRuntimeTeam(context.Background(), "default", runtimeManifest("bronze", 10), activeDecision, teamLimits(time.Now()), person("p"), team.MustNewID)
	if teamCode(t, err) != TeamConflictsStatic {
		t.Fatalf("got %v", err)
	}
}

func TestTheTenantsLimitAndSpendAllowanceBoundCreation(t *testing.T) {
	withCleanTeams(t)
	ctx := context.Background()
	mustCreate(t, "a", 60)
	_, _, err := testStore.CreateRuntimeTeam(ctx, "default", runtimeManifest("b", 50), activeDecision, teamLimits(time.Now()), person("p"), team.MustNewID)
	var te *TeamError
	if !errors.As(err, &te) || te.Code != TeamSpendExceeds || te.Headroom == nil || *te.Headroom != 40 {
		t.Fatalf("over the allowance: %v %+v", err, te)
	}
	weekly := runtimeManifest("w", 1)
	weekly.Spend.Cap.Period = "week"
	if _, _, err := testStore.CreateRuntimeTeam(ctx, "default", weekly, activeDecision, teamLimits(time.Now()), person("p"), team.MustNewID); teamCode(t, err) != TeamSpendExceeds {
		t.Fatalf("another period: %v", err)
	}
	mustCreate(t, "b", 20)
	mustCreate(t, "c", 20)
	if _, _, err := testStore.CreateRuntimeTeam(ctx, "default", runtimeManifest("d", 0.5), activeDecision, teamLimits(time.Now()), person("p"), team.MustNewID); teamCode(t, err) != TeamLimitReached {
		t.Fatalf("over teamCreation.max: %v", err)
	}
	if headroom, err := testStore.SpendHeadroom(ctx, "default", "", spendcap.Cap{USD: 100, Period: "month"}); err != nil || headroom != 0 {
		t.Fatalf("headroom %v %v", headroom, err)
	}
}

func TestOfTwoConcurrentCreatesOnlyTheOneThatFitsIsStored(t *testing.T) {
	withCleanTeams(t)
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i, key := range []string{"left", "right"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, errs[i] = testStore.CreateRuntimeTeam(context.Background(), "default", runtimeManifest(key, 70), activeDecision, teamLimits(time.Now()), person("p"), team.MustNewID)
		}()
	}
	wg.Wait()
	failed := 0
	for _, err := range errs {
		if err != nil {
			if teamCode(t, err) != TeamSpendExceeds {
				t.Fatalf("unexpected %v", err)
			}
			failed++
		}
	}
	if failed != 1 {
		t.Fatalf("%d of two over-allowance creates failed", failed)
	}
}

func TestARenameKeepsTheOldKeyAsAnAliasAndAReleasedKeyStaysReservedFor90Days(t *testing.T) {
	withCleanTeams(t)
	ctx := context.Background()
	now := time.Now()
	docs := mustCreate(t, "bronze-docs", 10)
	renamed, err := testStore.ReplaceRuntimeTeam(ctx, docs.ID, docs.Revision, runtimeManifest("docs-review", 10), activeDecision, teamLimits(now), person("p"))
	if err != nil {
		t.Fatal(err)
	}
	if renamed.ID != docs.ID || renamed.Key != "docs-review" || len(renamed.Aliases) != 1 || renamed.Aliases[0] != "bronze-docs" {
		t.Fatalf("renamed %+v", renamed)
	}
	if _, err := testStore.ReplaceRuntimeTeam(ctx, docs.ID, docs.Revision, runtimeManifest("other", 10), activeDecision, teamLimits(now), person("p")); teamCode(t, err) != TeamStaleRevision {
		t.Fatalf("a stale revision: %v", err)
	}
	if _, _, err := testStore.CreateRuntimeTeam(ctx, "default", runtimeManifest("bronze-docs", 1), activeDecision, teamLimits(now), person("p"), team.MustNewID); teamCode(t, err) != TeamKeyTaken {
		t.Fatalf("an alias held: %v", err)
	}
	if _, err := testStore.ReleaseTeamAlias(ctx, docs.ID, "bronze-docs", false, person("p")); err != nil {
		t.Fatal(err)
	}
	_, _, err = testStore.CreateRuntimeTeam(ctx, "default", runtimeManifest("bronze-docs", 1), activeDecision, teamLimits(now), person("p"), team.MustNewID)
	var te *TeamError
	if !errors.As(err, &te) || te.Code != TeamKeyReserved || te.ReservedUntil == nil || te.ReservedUntil.Sub(now) < 89*24*time.Hour {
		t.Fatalf("a released alias: %v %+v", err, te)
	}
	if until, err := testStore.ReservedKey(ctx, "default", "bronze-docs", teamLimits(now)); err != nil || until == nil {
		t.Fatalf("ReservedKey = %v %v", until, err)
	}
	current, _ := testStore.RuntimeTeam(ctx, docs.ID)
	back, err := testStore.ReplaceRuntimeTeam(ctx, docs.ID, current.Revision, runtimeManifest("bronze-docs", 10), activeDecision, teamLimits(now), person("p"))
	if err != nil || back.Key != "bronze-docs" {
		t.Fatalf("the holder takes its key back: %+v %v", back, err)
	}
	if _, err := testStore.ReplaceRuntimeTeam(ctx, docs.ID, back.Revision, runtimeManifest("gold", 10), activeDecision, teamLimits(now), person("p")); err != nil {
		t.Fatal(err)
	}
	gold, _ := testStore.RuntimeTeam(ctx, docs.ID)
	if _, err := testStore.ReleaseTeamAlias(ctx, docs.ID, "docs-review", false, person("p")); err != nil {
		t.Fatal(err)
	}
	day91 := now.Add(91 * 24 * time.Hour)
	other, created, err := testStore.CreateRuntimeTeam(ctx, "default", runtimeManifest("docs-review", 1), activeDecision, teamLimits(day91), person("p"), team.MustNewID)
	if err != nil || !created || other.ID == gold.ID {
		t.Fatalf("after 90 days the key is free for a new id: %+v %v", other, err)
	}
}

func TestARemovedRuntimeTeamsKeyIsReserved(t *testing.T) {
	withCleanTeams(t)
	ctx := context.Background()
	docs := mustCreate(t, "docs", 10)
	if removed, err := testStore.RemoveRuntimeTeam(ctx, docs.ID, nil, person("p")); err != nil || !removed {
		t.Fatalf("remove %v %v", removed, err)
	}
	if removed, err := testStore.RemoveRuntimeTeam(ctx, docs.ID, nil, person("p")); err != nil || removed {
		t.Fatalf("a second remove %v %v", removed, err)
	}
	if _, _, err := testStore.CreateRuntimeTeam(ctx, "default", runtimeManifest("docs", 10), activeDecision, teamLimits(time.Now()), person("p"), team.MustNewID); teamCode(t, err) != TeamKeyReserved {
		t.Fatalf("got %v", err)
	}
}

func TestARuntimeTeamInUseIsNotRemovedAndADrainRemovesItWhenItsWorkEnds(t *testing.T) {
	withCleanTeams(t)
	ctx := context.Background()
	docs := mustCreate(t, "docs", 10)
	item, _ := teamSelectionItem(t, "docs", "d-1", "", "")
	if id := teamIDOf(t, "work_items", "id = $1", item); id == nil || *id != docs.ID {
		t.Fatalf("a runtime Team's Work Item has team_id %v", id)
	}
	_, err := testStore.RemoveRuntimeTeam(ctx, docs.ID, nil, person("p"))
	var te *TeamError
	if !errors.As(err, &te) || te.Code != TeamInUseCode || len(te.InUse.WorkItems) != 1 {
		t.Fatalf("in use: %v %+v", err, te)
	}
	if _, err := testStore.RemoveRuntimeTeam(ctx, docs.ID, []string{"silver"}, person("p")); teamCode(t, err) != TeamInUseCode {
		t.Fatal("a refinementTeam reference did not block removal")
	}
	draining, err := testStore.DrainRuntimeTeam(ctx, docs.ID, nil, person("p"))
	if err != nil || draining.DrainingSince == nil || draining.DrainDeadline().Sub(*draining.DrainingSince) != 14*24*time.Hour {
		t.Fatalf("drain %+v %v", draining, err)
	}
	if _, err := testStore.ReplaceRuntimeTeam(ctx, docs.ID, draining.Revision, runtimeManifest("docs", 5), activeDecision, teamLimits(time.Now()), person("p")); teamCode(t, err) != TeamDraining {
		t.Fatalf("a PUT while draining: %v", err)
	}
	if drained, err := testStore.FinishDrains(ctx, time.Now()); err != nil || len(drained.Removed) != 0 {
		t.Fatalf("a drain with open work finished: %v %v", drained, err)
	}
	forceItemState(t, item, "done", 0)
	if drained, err := testStore.FinishDrains(ctx, time.Now()); err != nil || len(drained.Removed) != 1 {
		t.Fatalf("a drained Team was not removed: %v %v", drained, err)
	}
	if auditCount(t, AuditTeamRemoved) != 1 || auditCount(t, AuditTeamDrainExpired) != 0 {
		t.Fatal("audit")
	}
}

func TestADrainThatRunsOutSendsOpenWorkToAPerson(t *testing.T) {
	withCleanTeams(t)
	ctx := context.Background()
	docs := mustCreate(t, "docs", 10)
	item, shift := teamSelectionItem(t, "docs", "d-2", "", "")
	mustExec(t, `INSERT INTO agent_runs (work_item_id, shift_id, team, run_token, role, harness_requested, state) VALUES ($1, $2, 'docs', 'drain-run', 'builder', 'openhands', 'pending')`, item, shift)
	mustExec(t, `INSERT INTO agent_runs (work_item_id, shift_id, team, run_token, role, harness_requested, state, forge_token_id) VALUES ($1, $2, 'docs', 'drain-running', 'reviewer', 'openhands', 'running', 'forge-7')`, item, shift)
	if _, err := testStore.DrainRuntimeTeam(ctx, docs.ID, nil, person("p")); err != nil {
		t.Fatal(err)
	}
	mustExec(t, `INSERT INTO tracker_bindings (team, connection, scope_id, created_by, updated_by, state, team_id, webhook_state, webhook_source)
		VALUES ('docs', 'clickup', 'late', 'p', 'p', 'active', $1, 'present', 'existing')`, docs.ID)
	t.Cleanup(func() { mustExec(t, `DELETE FROM tracker_bindings`) })
	if drained, err := testStore.FinishDrains(ctx, time.Now().Add(13*24*time.Hour)); err != nil || len(drained.Removed) != 0 {
		t.Fatalf("day 13: %v %v", drained, err)
	}
	drained, err := testStore.FinishDrains(ctx, time.Now().Add(15*24*time.Hour))
	if err != nil || len(drained.Removed) != 1 {
		t.Fatalf("day 15: %v %v", drained, err)
	}
	if fmt.Sprint(drained.StoppedRunTokens) != "[drain-running]" || fmt.Sprint(drained.ForgeTokenIDs) != "[forge-7]" {
		t.Fatalf("stopped %v, forge tokens %v; want the running Run's credentials to revoke", drained.StoppedRunTokens, drained.ForgeTokenIDs)
	}
	var running, liveShifts int
	if err := testStore.pool.QueryRow(ctx, `SELECT count(*) FILTER (WHERE state = 'running') FROM agent_runs WHERE team_id = $1`, docs.ID).Scan(&running); err != nil || running != 0 {
		t.Fatalf("%d Runs still running (%v)", running, err)
	}
	if err := testStore.pool.QueryRow(ctx, `SELECT count(*) FROM shifts WHERE id = $1 AND closed_at IS NULL`, shift).Scan(&liveShifts); err != nil || liveShifts != 0 {
		t.Fatalf("the Shift is still open (%v)", err)
	}
	var bindingState, bindingReason string
	if err := testStore.pool.QueryRow(ctx, `SELECT state, invalid_reason FROM tracker_bindings WHERE scope_id = 'late'`).Scan(&bindingState, &bindingReason); err != nil ||
		bindingState != "invalid" || bindingReason != "team_removed" {
		t.Fatalf("the removed Team's binding is %s %s (%v)", bindingState, bindingReason, err)
	}
	if state, _ := itemStateAttempts(t, item); state != "needs_human" {
		t.Fatalf("Work Item is %s", state)
	}
	var reason, runState string
	if err := testStore.pool.QueryRow(ctx, `SELECT detail->>'reason' FROM audit_log WHERE action = 'work_item.needs_human' AND work_item_id = $1`, item).Scan(&reason); err != nil || reason != NeedsHumanTeamDrainExpired {
		t.Fatalf("reason %q %v", reason, err)
	}
	if err := testStore.pool.QueryRow(ctx, `SELECT state FROM agent_runs WHERE run_token = 'drain-run'`).Scan(&runState); err != nil || runState != "finished" {
		t.Fatalf("pending Run is %s %v", runState, err)
	}
	if auditCount(t, AuditTeamDrainExpired) != 1 || auditCount(t, AuditTeamRemoved) != 1 {
		t.Fatal("audit")
	}
}

func TestADrainIsRefusedWhileABindingNamesTheTeam(t *testing.T) {
	withCleanTeams(t)
	ctx := context.Background()
	docs := mustCreate(t, "docs", 10)
	teamSelectionItem(t, "docs", "d-3", "", "")
	mustExec(t, `INSERT INTO tracker_bindings (team, connection, scope_id, created_by, updated_by, state, team_id, webhook_state, webhook_source)
		VALUES ('docs', 'clickup', 'list-1', 'p', 'p', 'active', $1, 'present', 'existing')`, docs.ID)
	t.Cleanup(func() { mustExec(t, `DELETE FROM tracker_bindings`) })
	if _, err := testStore.DrainRuntimeTeam(ctx, docs.ID, nil, person("p")); teamCode(t, err) != TeamInUseCode {
		t.Fatalf("got %v", err)
	}
}

func TestStaticWinsAndAStateChangeIsAuditedOnce(t *testing.T) {
	withCleanTeams(t)
	ctx := context.Background()
	docs := mustCreate(t, "docs", 10)
	shadow := team.Decision{State: team.StateShadowed, ShadowedBy: "team_static"}
	for range 2 {
		if _, err := testStore.ApplyTeamDecision(ctx, docs.ID, shadow, nil); err != nil {
			t.Fatal(err)
		}
	}
	if auditCount(t, AuditTeamShadowed) != 1 {
		t.Fatal("not one team.shadowed")
	}
	if changed, err := testStore.ApplyTeamDecision(ctx, docs.ID, activeDecision, nil); err != nil || !changed {
		t.Fatal(err)
	}
	if auditCount(t, AuditTeamUnshadowed) != 1 {
		t.Fatal("no team.unshadowed")
	}
	if _, err := testStore.ApplyTeamDecision(ctx, docs.ID, team.Decision{State: team.StateInvalid, InvalidReason: team.InvalidHarnessNotOffered}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := testStore.ApplyTeamDecision(ctx, docs.ID, activeDecision, nil); err != nil {
		t.Fatal(err)
	}
	if auditCount(t, AuditTeamInvalid) != 1 || auditCount(t, AuditTeamRestored) != 1 {
		t.Fatal("invalid and restored not audited")
	}
}

func TestAStaticKeyReleasesARuntimeAlias(t *testing.T) {
	withCleanTeams(t)
	ctx := context.Background()
	docs := mustCreate(t, "old", 10)
	renamed, err := testStore.ReplaceRuntimeTeam(ctx, docs.ID, docs.Revision, runtimeManifest("docs", 10), activeDecision, teamLimits(time.Now()), person("p"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := testStore.ApplyTeamDecision(ctx, renamed.ID, activeDecision, map[string]string{"old": "team_static"}); err != nil {
		t.Fatal(err)
	}
	after, _ := testStore.RuntimeTeam(ctx, docs.ID)
	if len(after.Aliases) != 0 || after.State != team.StateActive || auditCount(t, AuditTeamAliasReleased) != 1 {
		t.Fatalf("after a static key took the alias: %+v", after)
	}
}

func TestAPoolClaimsOnlyActiveRuntimeTeamsRunsAndAStaticClaimNeverDoes(t *testing.T) {
	withCleanTeams(t)
	ctx := context.Background()
	if _, err := testStore.ReconcileStaticTeams(ctx, []StaticTeam{{Key: "silver"}}, sequentialIDs()); err != nil {
		t.Fatal(err)
	}
	docs := mustCreate(t, "docs", 10)
	runtimeItem, runtimeShift := teamSelectionItem(t, "docs", "r-1", "", "")
	staticItem, staticShift := teamSelectionItem(t, "silver", "s-1", "", "")
	mustExec(t, `INSERT INTO agent_runs (work_item_id, shift_id, team, run_token, role, harness_requested, state) VALUES ($1, $2, 'docs', 'pool-run', 'builder', 'openhands', 'pending')`, runtimeItem, runtimeShift)
	mustExec(t, `INSERT INTO agent_runs (work_item_id, shift_id, team, run_token, role, harness_requested, state) VALUES ($1, $2, 'silver', 'static-run', 'builder', 'openhands', 'pending')`, staticItem, staticShift)

	next, err := testStore.NextPoolRun(ctx, "default", "openhands")
	if err != nil || next.TeamID != docs.ID || next.Team != "docs" || next.Role != "builder" {
		t.Fatalf("NextPoolRun = %+v %v", next, err)
	}
	if _, err := testStore.NextPoolRun(ctx, "other", "openhands"); !errors.Is(err, ErrNoWork) {
		t.Fatalf("another Tenant's pool: %v", err)
	}
	var scaled int
	if err := testStore.pool.QueryRow(ctx, PoolScalerQuery, "default", "openhands").Scan(&scaled); err != nil || scaled != 1 {
		t.Fatalf("scaler counts %d %v, want the one runtime Run", scaled, err)
	}

	if _, err := testStore.ClaimRoleFor(ctx, "docs", "builder", "openhands", time.Minute, 0, 0); !errors.Is(err, ErrNoWork) {
		t.Fatalf("a static Team's worker claimed a runtime Team's Run: %v", err)
	}
	pool := WithPoolClaim(ctx, PoolClaim{TeamID: docs.ID})
	if _, err := testStore.ClaimRoleFor(pool, "silver", "builder", "openhands", time.Minute, 0, 0); !errors.Is(err, ErrNoWork) {
		t.Fatalf("a pool claimed a static Team's Run: %v", err)
	}
	if _, err := testStore.ApplyTeamDecision(ctx, docs.ID, team.Decision{State: team.StateShadowed, ShadowedBy: "team_x"}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := testStore.ClaimRoleFor(pool, "docs", "builder", "openhands", time.Minute, 0, 0); !errors.Is(err, ErrNoWork) {
		t.Fatalf("a pool claimed a shadowed Team's Run: %v", err)
	}
	if _, err := testStore.NextPoolRun(ctx, "default", "openhands"); !errors.Is(err, ErrNoWork) {
		t.Fatalf("a shadowed Team's Run is selectable: %v", err)
	}
	if _, err := testStore.ApplyTeamDecision(ctx, docs.ID, activeDecision, nil); err != nil {
		t.Fatal(err)
	}
	claimed, err := testStore.ClaimRoleFor(pool, "docs", "builder", "openhands", time.Minute, 0, 0)
	if err != nil || claimed.RunToken != "pool-run" {
		t.Fatalf("pool claim: %+v %v", claimed, err)
	}
	if claimed, err := testStore.ClaimRoleFor(ctx, "silver", "builder", "openhands", time.Minute, 0, 0); err != nil || claimed.RunToken != "static-run" {
		t.Fatalf("static claim: %+v %v", claimed, err)
	}
}

var poolScalerQuery = regexp.MustCompile(`query: "(SELECT COUNT\(\*\) FROM agent_runs r JOIN teams t[^"]*t\.tenant = '([^']+)'[^"]*r\.harness_requested = '([^']+)'[^"]*)"`)

func TestTheChartsPoolScalerQueryCountsWhatThePoolClaims(t *testing.T) {
	withCleanTeams(t)
	ctx := context.Background()
	golden, err := os.ReadFile("../../ops/helm/ploeg/ci/golden/executor-runtime-teams.yaml")
	if err != nil {
		t.Fatal(err)
	}
	queries := poolScalerQuery.FindAllStringSubmatch(string(golden), -1)
	if len(queries) != 2 {
		t.Fatalf("found %d pool scaler queries, want 2", len(queries))
	}
	if _, err := testStore.ReconcileStaticTeams(ctx, []StaticTeam{{Key: "silver"}}, sequentialIDs()); err != nil {
		t.Fatal(err)
	}
	docs := mustCreate(t, "docs", 10)
	other, _, err := testStore.CreateRuntimeTeam(ctx, "other", runtimeManifest("docs", 10), activeDecision, teamLimits(time.Now()), person("p"), team.MustNewID)
	if err != nil {
		t.Fatal(err)
	}
	for i, run := range []struct{ team, teamID, harness string }{
		{"docs", docs.ID, "openhands"}, {"docs", docs.ID, "openhands"}, {"docs", docs.ID, "acp/openhands"},
		{"silver", "", "openhands"}, {"docs", other.ID, "openhands"},
	} {
		item, shift := teamSelectionItem(t, run.team, "pool-"+strconv.Itoa(i), "", "")
		mustExec(t, `INSERT INTO agent_runs (work_item_id, shift_id, team, team_id, run_token, role, harness_requested, state)
			VALUES ($1, $2, $3, nullif($4, ''), $5, 'builder', $6, 'pending')`, item, shift, run.team, run.teamID, "pool-run-"+strconv.Itoa(i), run.harness)
	}
	want := map[string]int{"openhands": 2, "acp/openhands": 1}
	for _, q := range queries {
		var scaled, claimable int
		if err := testStore.pool.QueryRow(ctx, q[1]).Scan(&scaled); err != nil {
			t.Fatalf("%s: %v", q[1], err)
		}
		if err := testStore.pool.QueryRow(ctx, PoolScalerQuery, q[2], q[3]).Scan(&claimable); err != nil {
			t.Fatal(err)
		}
		if scaled != claimable || scaled != want[q[3]] {
			t.Errorf("%s/%s: the KEDA query counts %d, the pool claim %d, want %d", q[2], q[3], scaled, claimable, want[q[3]])
		}
	}
}

func TestTheTeamIdTriggerResolvesRuntimeKeysByTenantAndNeverDropsARuntimeId(t *testing.T) {
	withCleanTeams(t)
	ctx := context.Background()
	if _, err := testStore.ReconcileStaticTeams(ctx, []StaticTeam{{Key: "silver"}}, sequentialIDs()); err != nil {
		t.Fatal(err)
	}
	docs := mustCreate(t, "docs", 10)
	item, _ := teamSelectionItem(t, "docs", "trigger-1", "", "")
	if id := teamIDOf(t, "work_items", "id = $1", item); id == nil || *id != docs.ID {
		t.Fatalf("a runtime Team's Work Item has team_id %v", id)
	}
	mustExec(t, `UPDATE work_items SET team = 'nobody' WHERE id = $1`, item)
	if id := teamIDOf(t, "work_items", "id = $1", item); id == nil || *id != docs.ID {
		t.Fatalf("renaming the team text dropped the runtime team_id: %v", id)
	}
	var other, static *string
	if err := testStore.pool.QueryRow(ctx, `SELECT ploeg_team_id_for('docs', 'other'), ploeg_team_id_for('silver', 'other')`).Scan(&other, &static); err != nil {
		t.Fatal(err)
	}
	if other != nil || static != nil {
		t.Fatalf("another Tenant resolved docs to %v and the static silver to %v", other, static)
	}
	var def *string
	if err := testStore.pool.QueryRow(ctx, `SELECT ploeg_team_id_for('docs', '')`).Scan(&def); err != nil || def == nil || *def != docs.ID {
		t.Fatalf("the installation Tenant resolved docs to %v (%v)", def, err)
	}
}

func TestARuntimeTeamsSpendCapCountsItsSpendUnderEveryKeyItHad(t *testing.T) {
	withCleanTeams(t)
	ctx := context.Background()
	m := runtimeManifest("docs", 1)
	docs, _, err := testStore.CreateRuntimeTeam(ctx, "default", m, activeDecision, teamLimits(time.Now()), person("p"), team.MustNewID)
	if err != nil {
		t.Fatal(err)
	}
	spent, spentShift := teamSelectionItem(t, "docs-old", "cap-1", "", "")
	mustExec(t, `UPDATE work_items SET team_id = $2 WHERE id = $1`, spent, docs.ID)
	mustExec(t, `INSERT INTO agent_runs (work_item_id, shift_id, team, team_id, run_token, role, state, finished_at, usage)
		VALUES ($1, $2, 'docs-old', $3, 'cap-spent', 'builder', 'finished', now(), '{"costUsd": 1}')`, spent, spentShift, docs.ID)
	item, shift := teamSelectionItem(t, "docs", "cap-2", "", "")
	mustExec(t, `INSERT INTO agent_runs (work_item_id, shift_id, team, run_token, role, harness_requested, state, authorized)
		VALUES ($1, $2, 'docs', 'cap-next', 'builder', 'openhands', 'pending', 0.5)`, item, shift)
	pool := WithPoolClaim(ctx, PoolClaim{TeamID: docs.ID, SpendCap: *m.Spend.Cap})
	if _, err := testStore.ClaimRoleFor(pool, "docs", "builder", "openhands", time.Minute, 0.5, 0); !errors.Is(err, ErrTeamSpendCapReached) {
		t.Fatalf("a renamed runtime Team's earlier spend did not count against its cap: %v", err)
	}
	if _, err := testStore.ClaimRoleFor(WithPoolClaim(ctx, PoolClaim{TeamID: docs.ID}), "docs", "builder", "openhands", time.Minute, 0.5, 0); err != nil {
		t.Fatalf("without a cap on the marker the claim is refused: %v", err)
	}
}

func TestATenantsAllowanceCountsWhatRemovedRuntimeTeamsSpent(t *testing.T) {
	withCleanTeams(t)
	ctx := context.Background()
	gone := mustCreate(t, "gone", 60)
	item, shift := teamSelectionItem(t, "gone", "spent-1", "", "")
	mustExec(t, `INSERT INTO agent_runs (work_item_id, shift_id, team, run_token, role, state, finished_at, usage)
		VALUES ($1, $2, 'gone', 'spent-run', 'builder', 'finished', now(), '{"costUsd": 70}')`, item, shift)
	forceItemState(t, item, "done", 0)
	if removed, err := testStore.RemoveRuntimeTeam(ctx, gone.ID, nil, person("p")); err != nil || !removed {
		t.Fatalf("remove %v %v", removed, err)
	}
	_, _, err := testStore.CreateRuntimeTeam(ctx, "default", runtimeManifest("next", 40), activeDecision, teamLimits(time.Now()), person("p"), team.MustNewID)
	var te *TeamError
	if !errors.As(err, &te) || te.Code != TeamSpendExceeds || te.Headroom == nil || *te.Headroom != 30 {
		t.Fatalf("a removed Team's 70 USD was not counted: %v %+v", err, te)
	}
	if _, _, err := testStore.CreateRuntimeTeam(ctx, "default", runtimeManifest("next", 30), activeDecision, teamLimits(time.Now()), person("p"), team.MustNewID); err != nil {
		t.Fatalf("what is left fits: %v", err)
	}
}

func TestAKeyIsReservedInItsOwnTenantAndAReleasedStaticKeyEverywhere(t *testing.T) {
	withCleanTeams(t)
	ctx := context.Background()
	other, _, err := testStore.CreateRuntimeTeam(ctx, "other", runtimeManifest("docs", 10), activeDecision, teamLimits(time.Now()), person("p"), team.MustNewID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := testStore.RemoveRuntimeTeam(ctx, other.ID, nil, person("p")); err != nil {
		t.Fatal(err)
	}
	if until, err := testStore.ReservedKey(ctx, "default", "docs", teamLimits(time.Now())); err != nil || until != nil {
		t.Fatalf("another Tenant's released key is reserved here until %v (%v)", until, err)
	}
	mustCreate(t, "docs", 10)
	ids := sequentialIDs()
	if _, err := testStore.ReconcileStaticTeams(ctx, []StaticTeam{{Key: "bronze"}}, ids); err != nil {
		t.Fatal(err)
	}
	if _, err := testStore.ReconcileStaticTeams(ctx, nil, ids); err != nil {
		t.Fatal(err)
	}
	if _, _, err := testStore.CreateRuntimeTeam(ctx, "default", runtimeManifest("bronze", 1), activeDecision, teamLimits(time.Now()), person("p"), team.MustNewID); teamCode(t, err) != TeamKeyReserved {
		t.Fatalf("a key a static Team released: %v", err)
	}
}

func TestAPoolClaimTakesOnlyTheRunOfTheTeamItsMarkerNames(t *testing.T) {
	withCleanTeams(t)
	ctx := context.Background()
	mine := mustCreate(t, "docs", 10)
	theirs, _, err := testStore.CreateRuntimeTeam(ctx, "acme", runtimeManifest("docs", 10), activeDecision, teamLimits(time.Now()), person("p"), team.MustNewID)
	if err != nil {
		t.Fatal(err)
	}
	for i, id := range []string{theirs.ID, mine.ID} {
		item, shift := teamSelectionItem(t, "docs", "tenant-"+strconv.Itoa(i), "", "")
		mustExec(t, `INSERT INTO agent_runs (work_item_id, shift_id, team, team_id, run_token, role, harness_requested, state)
			VALUES ($1, $2, 'docs', $3, $4, 'builder', 'openhands', 'pending')`, item, shift, id, "tenant-run-"+strconv.Itoa(i))
	}
	claimed, err := testStore.ClaimRoleFor(WithPoolClaim(ctx, PoolClaim{TeamID: mine.ID}), "docs", "builder", "openhands", time.Minute, 0, 0)
	if err != nil || claimed.RunToken != "tenant-run-1" {
		t.Fatalf("the default pool claimed %+v (%v), want its own Team's Run", claimed, err)
	}
	if _, err := testStore.ClaimRoleFor(WithPoolClaim(ctx, PoolClaim{TeamID: mine.ID}), "docs", "builder", "openhands", time.Minute, 0, 0); !errors.Is(err, ErrNoWork) {
		t.Fatalf("the default pool reached acme's Run: %v", err)
	}
	if _, err := testStore.ClaimRoleFor(WithPoolClaim(ctx, PoolClaim{TeamID: "not-an-id"}), "docs", "builder", "openhands", time.Minute, 0, 0); !errors.Is(err, ErrNoWork) {
		t.Fatalf("a marker without a valid id claimed: %v", err)
	}
}

func TestAnAuditRowThatNamesARuntimeTeamGetsItsId(t *testing.T) {
	withCleanTeams(t)
	docs := mustCreate(t, "docs", 10)
	mustExec(t, `INSERT INTO audit_log (actor, action, detail) VALUES ('x', 'test.runtime_event', '{"team":"docs"}')`)
	var id string
	if err := testStore.pool.QueryRow(context.Background(), `SELECT coalesce(detail->>'team_id', '') FROM audit_log WHERE action = 'test.runtime_event'`).Scan(&id); err != nil || id != docs.ID {
		t.Fatalf("detail.team_id = %q (%v), want %s", id, err, docs.ID)
	}
}
