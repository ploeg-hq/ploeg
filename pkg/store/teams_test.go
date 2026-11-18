package store

import (
	"context"
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
	for _, table := range []string{"team_keys", "teams"} {
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
