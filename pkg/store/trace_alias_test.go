package store

import (
	"context"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/litellm"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

func TestRunTraceAliasMatchesTheGatewayAlias(t *testing.T) {
	ctx := context.Background()
	resetTables(t)
	id, _, err := testStore.IngestAssigned(ctx, work.WorkItem{
		Provider: "vikunja", ExternalID: "138", Team: "bronze", Title: "t",
		Target: &work.Target{Forge: "home", Owner: "o", Repo: "r"},
	})
	if err != nil {
		t.Fatal(err)
	}
	shiftID, err := testStore.OpenShift(ctx, id, "bronze", "agent/vik-138", 10)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := testStore.OpenRound(ctx, shiftID, 0, []Role{{Name: "builder", Writes: true, Cap: 1}}); err != nil {
		t.Fatal(err)
	}
	run, err := testStore.ClaimRole(ctx, "bronze", "builder", time.Minute, 1)
	if err != nil {
		t.Fatal(err)
	}
	var alias string
	if err := testStore.pool.QueryRow(ctx, `SELECT trace_alias FROM agent_runs WHERE run_token = $1`, run.RunToken).Scan(&alias); err != nil {
		t.Fatal(err)
	}
	if want := litellm.Alias(run.RunToken); alias != want {
		t.Fatalf("trace_alias = %q, want %q", alias, want)
	}
}
