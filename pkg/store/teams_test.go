package store

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/team"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

func resetTeams(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	for _, tt := range teamTables {
		if _, err := testStore.pool.Exec(ctx, "UPDATE "+tt.table+" SET team_id = NULL WHERE team_id IS NOT NULL"); err != nil {
			t.Fatalf("clear %s: %v", tt.table, err)
		}
	}
	for _, table := range []string{"team_id_backfills", "team_keys", "teams"} {
		if _, err := testStore.pool.Exec(ctx, "DELETE FROM "+table); err != nil {
			t.Fatalf("reset %s: %v", table, err)
		}
	}
}

func withCleanTeams(t *testing.T) {
	t.Helper()
	resetTables(t)
	resetTeams(t)
	t.Cleanup(func() { resetTables(t); resetTeams(t) })
}

func sequentialIDs() func() string {
	n := 0
	return func() string {
		n++
		return fmt.Sprintf("team_%026d", n)
	}
}

func teamIDOf(t *testing.T, table, where string, args ...any) *string {
	t.Helper()
	var id *string
	if err := testStore.pool.QueryRow(context.Background(), "SELECT team_id FROM "+table+" WHERE "+where, args...).Scan(&id); err != nil {
		t.Fatalf("read %s team_id: %v", table, err)
	}
	return id
}

func liveTeamByKey(t *testing.T, key string) TeamRecord {
	t.Helper()
	teams, err := testStore.LiveTeams(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, rec := range teams {
		if rec.Key == key {
			return rec
		}
	}
	t.Fatalf("no live Team with key %q in %+v", key, teams)
	return TeamRecord{}
}

func auditCount(t *testing.T, action string) int {
	t.Helper()
	var n int
	if err := testStore.pool.QueryRow(context.Background(), `SELECT count(*) FROM audit_log WHERE action = $1`, action).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestStaticTeamsGetAnIdOnFirstBootAndExistingRowsAreBackfilled(t *testing.T) {
	withCleanTeams(t)
	ctx := context.Background()
	bronzeItem, _ := teamSelectionItem(t, "bronze", "b-1", "", "")
	silverItem, silverShift := teamSelectionItem(t, "silver", "s-1", "", "")
	mustExec(t, `INSERT INTO agent_runs (work_item_id, shift_id, team, run_token) VALUES ($1, $2, 'silver', 'teams-run')`, silverItem, silverShift)
	if id := teamIDOf(t, "work_items", "id = $1", bronzeItem); id != nil {
		t.Fatalf("a Work Item written before the reconciler has team_id %q", *id)
	}

	newID := sequentialIDs()
	got, err := testStore.ReconcileStaticTeams(ctx, []StaticTeam{{Key: "bronze"}, {Key: "silver", Name: "Silver builders"}}, newID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Created) != 2 {
		t.Fatalf("created %v", got.Created)
	}
	bronze, silver := liveTeamByKey(t, "bronze"), liveTeamByKey(t, "silver")
	if bronze.ID == silver.ID || bronze.Source != team.SourceStatic || silver.Name != "Silver builders" {
		t.Fatalf("bronze %+v silver %+v", bronze, silver)
	}

	set, err := testStore.BackfillTeamIDs(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if set < 5 {
		t.Fatalf("backfill set %d rows, want the two Work Items, two Shifts and the Run", set)
	}
	for table, where := range map[string]string{
		"work_items": fmt.Sprintf("id = %d", silverItem),
		"shifts":     fmt.Sprintf("id = %d", silverShift),
		"agent_runs": "run_token = 'teams-run'",
	} {
		if id := teamIDOf(t, table, where); id == nil || *id != silver.ID {
			t.Errorf("%s team_id = %v, want %s", table, id, silver.ID)
		}
	}
	if id := teamIDOf(t, "work_items", "id = $1", bronzeItem); id == nil || *id != bronze.ID {
		t.Errorf("bronze Work Item team_id = %v", id)
	}
	if again, err := testStore.BackfillTeamIDs(ctx, 10000); err != nil || again != 0 {
		t.Fatalf("a second backfill set %d rows (%v)", again, err)
	}

	second, err := testStore.ReconcileStaticTeams(ctx, []StaticTeam{{Key: "bronze"}, {Key: "silver", Name: "Silver builders"}}, newID)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Created)+len(second.Renamed)+len(second.Removed) != 0 {
		t.Fatalf("a second boot changed %+v", second)
	}
	if liveTeamByKey(t, "bronze").ID != bronze.ID || liveTeamByKey(t, "silver").ID != silver.ID {
		t.Fatal("a second boot changed an id")
	}
}

func TestANewRowTakesItsTeamIdFromTheStaticKey(t *testing.T) {
	withCleanTeams(t)
	ctx := context.Background()
	if _, err := testStore.ReconcileStaticTeams(ctx, []StaticTeam{{Key: "silver"}}, sequentialIDs()); err != nil {
		t.Fatal(err)
	}
	silver := liveTeamByKey(t, "silver")
	item, shift := teamSelectionItem(t, "silver", "s-2", "", "")
	if id := teamIDOf(t, "work_items", "id = $1", item); id == nil || *id != silver.ID {
		t.Fatalf("new Work Item team_id = %v", id)
	}
	if id := teamIDOf(t, "shifts", "id = $1", shift); id == nil || *id != silver.ID {
		t.Fatalf("new Shift team_id = %v", id)
	}
	other, _, err := testStore.IngestAssigned(ctx, work.WorkItem{Provider: "vikunja", ExternalID: "u-1", Team: "unknown", Title: "t"})
	if err != nil {
		t.Fatal(err)
	}
	if id := teamIDOf(t, "work_items", "id = $1", other); id != nil {
		t.Fatalf("a Work Item of a Team with no row got team_id %q", *id)
	}
	mustExec(t, `UPDATE work_items SET team = 'silver' WHERE id = $1`, other)
	if id := teamIDOf(t, "work_items", "id = $1", other); id == nil || *id != silver.ID {
		t.Fatalf("a Work Item moved to silver has team_id %v", id)
	}
	mustExec(t, `INSERT INTO audit_log (actor, action, detail) VALUES ('x', 'test.team_event', '{"team":"silver"}')`)
	var detailID string
	if err := testStore.pool.QueryRow(ctx, `SELECT detail->>'team_id' FROM audit_log WHERE action = 'test.team_event'`).Scan(&detailID); err != nil || detailID != silver.ID {
		t.Fatalf("audit detail.team_id = %q (%v)", detailID, err)
	}
}

func TestAStaticTeamRenamedWithItsOldKeyAsAnAliasKeepsItsId(t *testing.T) {
	withCleanTeams(t)
	ctx := context.Background()
	newID := sequentialIDs()
	if _, err := testStore.ReconcileStaticTeams(ctx, []StaticTeam{{Key: "bronze"}}, newID); err != nil {
		t.Fatal(err)
	}
	bronze := liveTeamByKey(t, "bronze")
	got, err := testStore.ReconcileStaticTeams(ctx, []StaticTeam{{Key: "docs-review", Aliases: []string{"bronze"}}}, newID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Renamed) != 1 || len(got.Created) != 0 || len(got.Removed) != 0 {
		t.Fatalf("rename reconciled as %+v", got)
	}
	renamed := liveTeamByKey(t, "docs-review")
	if renamed.ID != bronze.ID || len(renamed.Aliases) != 1 || renamed.Aliases[0] != "bronze" {
		t.Fatalf("renamed Team %+v, want id %s with alias bronze", renamed, bronze.ID)
	}
	if auditCount(t, AuditTeamRenamed) != 1 {
		t.Fatal("no team.renamed")
	}
	item, _ := teamSelectionItem(t, "bronze", "b-old", "", "")
	if id := teamIDOf(t, "work_items", "id = $1", item); id == nil || *id != bronze.ID {
		t.Fatalf("a Work Item written with the alias has team_id %v", id)
	}
}

func TestAStaticTeamWhoseKeyChangedWithoutAnAliasIsANewTeam(t *testing.T) {
	withCleanTeams(t)
	ctx := context.Background()
	newID := sequentialIDs()
	if _, err := testStore.ReconcileStaticTeams(ctx, []StaticTeam{{Key: "bronze"}}, newID); err != nil {
		t.Fatal(err)
	}
	bronze := liveTeamByKey(t, "bronze")
	got, err := testStore.ReconcileStaticTeams(ctx, []StaticTeam{{Key: "copper"}}, newID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Created) != 1 || len(got.Removed) != 1 {
		t.Fatalf("reconciled as %+v", got)
	}
	if liveTeamByKey(t, "copper").ID == bronze.ID {
		t.Fatal("copper took bronze's id")
	}
	if auditCount(t, AuditTeamRemoved) != 1 {
		t.Fatal("no team.removed")
	}

	back, err := testStore.ReconcileStaticTeams(ctx, []StaticTeam{{Key: "copper"}, {Key: "bronze"}}, newID)
	if err != nil {
		t.Fatal(err)
	}
	if len(back.Created) != 1 || liveTeamByKey(t, "bronze").ID != bronze.ID {
		t.Fatalf("bronze came back as %+v with id %s, want %s", back, liveTeamByKey(t, "bronze").ID, bronze.ID)
	}
}

func TestStaticAliasesFollowTheFile(t *testing.T) {
	withCleanTeams(t)
	ctx := context.Background()
	newID := sequentialIDs()
	if _, err := testStore.ReconcileStaticTeams(ctx, []StaticTeam{{Key: "gold", Aliases: []string{"bronze", "copper"}}}, newID); err != nil {
		t.Fatal(err)
	}
	if _, err := testStore.ReconcileStaticTeams(ctx, []StaticTeam{{Key: "gold", Aliases: []string{"copper", "tin"}}}, newID); err != nil {
		t.Fatal(err)
	}
	gold := liveTeamByKey(t, "gold")
	if fmt.Sprint(gold.Aliases) != "[copper tin]" {
		t.Fatalf("aliases %v", gold.Aliases)
	}
	if auditCount(t, AuditTeamAliasReleased) != 1 {
		t.Fatal("releasing bronze wrote no team.alias_released")
	}
}

func TestAStaticNameChangeInTheFileIsAudited(t *testing.T) {
	withCleanTeams(t)
	ctx := context.Background()
	newID := sequentialIDs()
	if _, err := testStore.ReconcileStaticTeams(ctx, []StaticTeam{{Key: "gold"}}, newID); err != nil {
		t.Fatal(err)
	}
	if _, err := testStore.ReconcileStaticTeams(ctx, []StaticTeam{{Key: "gold", Name: "Gold"}}, newID); err != nil {
		t.Fatal(err)
	}
	if liveTeamByKey(t, "gold").Name != "Gold" || auditCount(t, AuditTeamNameChanged) != 1 {
		t.Fatal("the file's name was not stored and audited")
	}
}

func TestAnAliasMovedBetweenTeamsInOneBootKeepsBothTeams(t *testing.T) {
	withCleanTeams(t)
	ctx := context.Background()
	newID := sequentialIDs()
	if _, err := testStore.ReconcileStaticTeams(ctx, []StaticTeam{{Key: "alpha"}, {Key: "beta", Aliases: []string{"old"}}}, newID); err != nil {
		t.Fatal(err)
	}
	alpha, beta := liveTeamByKey(t, "alpha"), liveTeamByKey(t, "beta")
	if _, err := testStore.ReconcileStaticTeams(ctx, []StaticTeam{{Key: "alpha", Aliases: []string{"old"}}, {Key: "beta"}}, newID); err != nil {
		t.Fatalf("moving alias old from beta to alpha: %v", err)
	}
	movedTo, left := liveTeamByKey(t, "alpha"), liveTeamByKey(t, "beta")
	if movedTo.ID != alpha.ID || left.ID != beta.ID {
		t.Fatalf("ids changed: alpha %s→%s beta %s→%s", alpha.ID, movedTo.ID, beta.ID, left.ID)
	}
	if fmt.Sprint(movedTo.Aliases) != "[old]" || len(left.Aliases) != 0 {
		t.Fatalf("alpha aliases %v, beta aliases %v", movedTo.Aliases, left.Aliases)
	}
}

func TestATeamFoundByItsKeyIsNeverTakenByAnotherTeamsAlias(t *testing.T) {
	withCleanTeams(t)
	ctx := context.Background()
	newID := sequentialIDs()
	if _, err := testStore.ReconcileStaticTeams(ctx, []StaticTeam{{Key: "beta", Aliases: []string{"old"}}}, newID); err != nil {
		t.Fatal(err)
	}
	beta := liveTeamByKey(t, "beta")
	got, err := testStore.ReconcileStaticTeams(ctx, []StaticTeam{{Key: "alpha", Aliases: []string{"old"}}, {Key: "beta"}}, newID)
	if err != nil {
		t.Fatal(err)
	}
	if liveTeamByKey(t, "beta").ID != beta.ID || fmt.Sprint(got.Created) != "[alpha]" || len(got.Renamed) != 0 {
		t.Fatalf("beta %s, want %s; reconciled %+v", liveTeamByKey(t, "beta").ID, beta.ID, got)
	}
	if fmt.Sprint(liveTeamByKey(t, "alpha").Aliases) != "[old]" {
		t.Fatal("alpha did not take the alias old")
	}
}

func TestAKeyThatLeftAsAnotherWithTheSameConfigurationAppearedIsFlagged(t *testing.T) {
	withCleanTeams(t)
	ctx := context.Background()
	newID := sequentialIDs()
	if _, err := testStore.ReconcileStaticTeams(ctx, []StaticTeam{{Key: "bronze", Digest: "same"}, {Key: "tin", Digest: "other"}}, newID); err != nil {
		t.Fatal(err)
	}
	bronze := liveTeamByKey(t, "bronze")
	got, err := testStore.ReconcileStaticTeams(ctx, []StaticTeam{{Key: "copper", Digest: "same"}, {Key: "tin", Digest: "other"}}, newID)
	if err != nil {
		t.Fatal(err)
	}
	copper := liveTeamByKey(t, "copper")
	if len(got.SuspectedRenames) != 1 || got.SuspectedRenames[0] != (SuspectedRename{From: "bronze", FromID: bronze.ID, To: "copper", ToID: copper.ID}) {
		t.Fatalf("suspected renames %+v", got.SuspectedRenames)
	}
	if auditCount(t, AuditTeamRenameSuspected) != 1 {
		t.Fatal("no team.rename_suspected")
	}
	again, err := testStore.ReconcileStaticTeams(ctx, []StaticTeam{{Key: "copper", Digest: "changed"}, {Key: "zinc", Digest: "other"}}, newID)
	if err != nil {
		t.Fatal(err)
	}
	if len(again.SuspectedRenames) != 1 || again.SuspectedRenames[0].From != "tin" {
		t.Fatalf("a stored digest was not used: %+v", again.SuspectedRenames)
	}
}

func TestOnlyOnePloegdBackfillsTeamIDs(t *testing.T) {
	withCleanTeams(t)
	ctx := context.Background()
	conn, err := testStore.pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock(`+teamBackfillLockKey+`)`); err != nil {
		t.Fatal(err)
	}
	if _, err := testStore.BackfillTeamIDs(ctx, 10); !errors.Is(err, ErrTeamBackfillElsewhere) {
		t.Fatalf("a second backfill ran beside the first: %v", err)
	}
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_unlock(`+teamBackfillLockKey+`)`); err != nil {
		t.Fatal(err)
	}
	if _, err := testStore.BackfillTeamIDs(ctx, 10); err != nil {
		t.Fatalf("backfill after the lock was released: %v", err)
	}
}

func TestTheBackfillSkipsRowsLiveWorkHoldsAndFinishesThemLater(t *testing.T) {
	withCleanTeams(t)
	ctx := context.Background()
	held, _ := teamSelectionItem(t, "silver", "s-held", "", "")
	free, _ := teamSelectionItem(t, "silver", "s-free", "", "")
	if _, err := testStore.ReconcileStaticTeams(ctx, []StaticTeam{{Key: "silver"}}, sequentialIDs()); err != nil {
		t.Fatal(err)
	}
	silver := liveTeamByKey(t, "silver")

	tx, err := testStore.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `SELECT 1 FROM work_items WHERE id = $1 FOR UPDATE`, held); err != nil {
		t.Fatal(err)
	}
	if _, err := testStore.BackfillTeamIDs(ctx, 10); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("backfill waited on or failed at a held row: %v", err)
	}
	if id := teamIDOf(t, "work_items", "id = $1", free); id == nil || *id != silver.ID {
		t.Fatalf("the free row has team_id %v", id)
	}
	if id := teamIDOf(t, "work_items", "id = $1", held); id != nil {
		t.Fatalf("the held row was written under its lock: %v", *id)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := testStore.BackfillTeamIDs(ctx, 10); err != nil {
		t.Fatal(err)
	}
	if id := teamIDOf(t, "work_items", "id = $1", held); id == nil || *id != silver.ID {
		t.Fatalf("the skipped row was never backfilled: %v", id)
	}
}

func TestACompletedBackfillIsNotRepeatedUntilATeamTakesANewKey(t *testing.T) {
	withCleanTeams(t)
	ctx := context.Background()
	newID := sequentialIDs()
	silverItem, _ := teamSelectionItem(t, "silver", "s-done", "", "")
	if _, err := testStore.ReconcileStaticTeams(ctx, []StaticTeam{{Key: "silver"}}, newID); err != nil {
		t.Fatal(err)
	}
	if set, err := testStore.BackfillTeamIDs(ctx, 10); err != nil || set == 0 {
		t.Fatalf("the first backfill set %d rows (%v)", set, err)
	}
	item, _ := teamSelectionItem(t, "gold", "g-1", "", "")
	if _, err := testStore.ReconcileStaticTeams(ctx, []StaticTeam{{Key: "silver"}}, newID); err != nil {
		t.Fatal(err)
	}
	mustExec(t, `UPDATE work_items SET team_id = NULL WHERE id = $1`, silverItem)
	if set, err := testStore.BackfillTeamIDs(ctx, 10); err != nil || set != 0 {
		t.Fatalf("a boot that changed nothing rescanned and set %d rows (%v)", set, err)
	}
	if _, err := testStore.ReconcileStaticTeams(ctx, []StaticTeam{{Key: "silver"}, {Key: "gold"}}, newID); err != nil {
		t.Fatal(err)
	}
	if set, err := testStore.BackfillTeamIDs(ctx, 10); err != nil || set == 0 {
		t.Fatalf("a new Team's rows were not backfilled: %d (%v)", set, err)
	}
	if id := teamIDOf(t, "work_items", "id = $1", item); id == nil || *id != liveTeamByKey(t, "gold").ID {
		t.Fatalf("gold's Work Item has team_id %v", id)
	}
}

func TestATenantScopedRowNeverTakesAStaticIdByKey(t *testing.T) {
	withCleanTeams(t)
	ctx := context.Background()
	if _, err := testStore.ReconcileStaticTeams(ctx, []StaticTeam{{Key: "silver"}}, sequentialIDs()); err != nil {
		t.Fatal(err)
	}
	silver := liveTeamByKey(t, "silver")
	t.Cleanup(func() { mustExec(t, `DELETE FROM tracker_bindings WHERE connection = 'team-id-tenant-test'`) })
	var tenantRow, staticRow int64
	insert := `INSERT INTO tracker_bindings (tenant, team, connection, scope_id, state, webhook_state, webhook_source, created_by, updated_by)
		VALUES ($1, 'silver', 'team-id-tenant-test', $2, 'active', 'unchecked', 'none', 'test', 'test') RETURNING id`
	if err := testStore.pool.QueryRow(ctx, insert, "acme", "scope-tenant").Scan(&tenantRow); err != nil {
		t.Fatal(err)
	}
	if err := testStore.pool.QueryRow(ctx, insert, "", "scope-static").Scan(&staticRow); err != nil {
		t.Fatal(err)
	}
	if id := teamIDOf(t, "tracker_bindings", "id = $1", tenantRow); id != nil {
		t.Fatalf("a tenant's binding took the static id %s", *id)
	}
	if id := teamIDOf(t, "tracker_bindings", "id = $1", staticRow); id == nil || *id != silver.ID {
		t.Fatalf("a binding without a tenant has team_id %v", id)
	}
	mustExec(t, `UPDATE tracker_bindings SET team_id = NULL WHERE id = $1`, staticRow)
	if _, err := testStore.BackfillTeamIDs(ctx, 10); err != nil {
		t.Fatal(err)
	}
	if id := teamIDOf(t, "tracker_bindings", "id = $1", tenantRow); id != nil {
		t.Fatalf("the backfill gave a tenant's binding the static id %s", *id)
	}
	if id := teamIDOf(t, "tracker_bindings", "id = $1", staticRow); id == nil || *id != silver.ID {
		t.Fatalf("the backfill left a binding without a tenant at %v", id)
	}
	mustExec(t, `INSERT INTO audit_log (actor, action, detail) VALUES ('x', 'test.tenant_event', '{"team":"silver","tenant":"acme"}')`)
	var detailID *string
	if err := testStore.pool.QueryRow(ctx, `SELECT detail->>'team_id' FROM audit_log WHERE action = 'test.tenant_event'`).Scan(&detailID); err != nil || detailID != nil {
		t.Fatalf("a tenant's audit row took detail.team_id %v (%v)", detailID, err)
	}
}

func TestMovingARowToAnUnknownKeyKeepsItsRuntimeTeamId(t *testing.T) {
	withCleanTeams(t)
	ctx := context.Background()
	if _, err := testStore.ReconcileStaticTeams(ctx, []StaticTeam{{Key: "silver"}}, sequentialIDs()); err != nil {
		t.Fatal(err)
	}
	runtimeID := fmt.Sprintf("team_%026d", 99)
	mustExec(t, `INSERT INTO teams (id, tenant, source, manifest, created_by, updated_by) VALUES ($1, 'acme', 'runtime', '{}', 'test', 'test')`, runtimeID)
	item, _ := teamSelectionItem(t, "builders", "r-1", "", "")
	mustExec(t, `UPDATE work_items SET team_id = $2 WHERE id = $1`, item, runtimeID)
	mustExec(t, `UPDATE work_items SET team = 'builders-renamed' WHERE id = $1`, item)
	if id := teamIDOf(t, "work_items", "id = $1", item); id == nil || *id != runtimeID {
		t.Fatalf("a runtime team_id became %v on update", id)
	}
	mustExec(t, `UPDATE work_items SET team = 'silver' WHERE id = $1`, item)
	if id := teamIDOf(t, "work_items", "id = $1", item); id == nil || *id != liveTeamByKey(t, "silver").ID {
		t.Fatalf("a row moved to a static Team has team_id %v", id)
	}
}
