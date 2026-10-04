package store

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/work"
)

func refusedTask(id, team string) work.WorkItem {
	return work.WorkItem{Provider: "vikunja", ExternalID: id, ExternalScope: "10", Team: team,
		Title: "task " + id, Labels: []string{"do-next"}}
}

func refuseTask(t *testing.T, item work.WorkItem, code, reason string, allowed []string) {
	t.Helper()
	if err := testStore.RefuseRoute(context.Background(), item, code, reason, allowed); err != nil {
		t.Fatalf("RefuseRoute: %v", err)
	}
}

func recentRefusals(t *testing.T, teams []string) []RouteRefusal {
	t.Helper()
	got, err := testStore.RecentRouteRefusals(context.Background(), teams, time.Now().Add(-14*24*time.Hour), 50)
	if err != nil {
		t.Fatalf("RecentRouteRefusals: %v", err)
	}
	return got
}

func refusalIDs(refusals []RouteRefusal) string {
	var ids []string
	for _, r := range refusals {
		ids = append(ids, r.ExternalID)
	}
	return strings.Join(ids, ",")
}

func TestRecentRouteRefusalsReturnTheStoredCodeAndAllowedLabels(t *testing.T) {
	resetTables(t)
	refuseTask(t, refusedTask("700", "silver"), "label_missing", "board rule \"10\" requires a repository label", []string{"repo/unfold", "repo/homelab-cluster"})

	got := recentRefusals(t, nil)
	if len(got) != 1 {
		t.Fatalf("refusals = %+v, want one", got)
	}
	r := got[0]
	if r.Provider != "vikunja" || r.ExternalID != "700" || r.ExternalScope != "10" || r.Team != "silver" || r.Title != "task 700" ||
		strings.Join(r.Labels, ",") != "do-next" || r.Code != "label_missing" || !strings.Contains(r.Reason, "requires a repository label") ||
		strings.Join(r.AllowedLabels, ",") != "repo/unfold,repo/homelab-cluster" || r.RefusedAt.IsZero() {
		t.Errorf("refusal = %+v", r)
	}
}

func TestRecentRouteRefusalsKeepOnlyTheNewestRefusalOfATask(t *testing.T) {
	resetTables(t)
	refuseTask(t, refusedTask("701", "silver"), "label_missing", "older", []string{})
	refuseTask(t, refusedTask("701", "silver"), "label_not_allowed", "newer", []string{"repo/unfold"})
	refuseTask(t, refusedTask("702", "silver"), "label_missing", "other task", []string{})

	got := recentRefusals(t, nil)
	if refusalIDs(got) != "702,701" || got[1].Reason != "newer" {
		t.Errorf("refusals = %+v, want 702 then the newer refusal of 701", got)
	}
}

func TestRecentRouteRefusalsDropATaskThatLaterRouted(t *testing.T) {
	resetTables(t)
	ctx := context.Background()
	refuseTask(t, refusedTask("703", "silver"), "label_missing", "no label yet", []string{})
	if _, _, err := testStore.IngestAssigned(ctx, refusedTask("703", "silver")); err != nil {
		t.Fatal(err)
	}
	if got := recentRefusals(t, nil); len(got) != 0 {
		t.Fatalf("refusals after the task queued = %+v, want none", got)
	}

	refuseTask(t, refusedTask("703", "silver"), "label_not_allowed", "refused again after it ran", []string{})
	if got := recentRefusals(t, nil); refusalIDs(got) != "703" {
		t.Fatalf("a refusal newer than the queue audit = %+v, want it listed", got)
	}
	if _, _, err := testStore.IngestAssigned(ctx, refusedTask("703", "silver")); err != nil {
		t.Fatal(err)
	}
	if got := recentRefusals(t, nil); len(got) != 0 {
		t.Errorf("refusals after a refresh = %+v, want none", got)
	}
}

func TestRecentRouteRefusalsForgetRefusalsOutsideTheWindow(t *testing.T) {
	resetTables(t)
	refuseTask(t, refusedTask("704", "silver"), "label_missing", "old", []string{})
	if _, err := testStore.pool.Exec(context.Background(),
		`UPDATE audit_log SET at = now() - interval '15 days' WHERE action = 'work_item.route_refused'`); err != nil {
		t.Fatal(err)
	}
	if got := recentRefusals(t, nil); len(got) != 0 {
		t.Errorf("refusals = %+v, want none older than the window", got)
	}
}

func TestRecentRouteRefusalsStayInsideTheConsumersTeams(t *testing.T) {
	resetTables(t)
	refuseTask(t, refusedTask("705", "silver"), "label_missing", "silver", []string{})
	refuseTask(t, refusedTask("706", "gold"), "label_missing", "gold", []string{})

	if got := recentRefusals(t, []string{"silver"}); refusalIDs(got) != "705" {
		t.Errorf("silver sees %+v, want only 705", got)
	}
	if got := recentRefusals(t, []string{}); len(got) != 0 {
		t.Errorf("a consumer with no teams sees %+v", got)
	}
	if got := recentRefusals(t, nil); refusalIDs(got) != "706,705" {
		t.Errorf("an unscoped consumer sees %+v, want both", got)
	}
}

func TestRecentRouteRefusalsClassifyRowsWrittenBeforeCodesExisted(t *testing.T) {
	resetTables(t)
	if _, err := testStore.pool.Exec(context.Background(), `INSERT INTO audit_log (actor, action, work_item_id, detail)
		VALUES ('webhook:vikunja', 'work_item.route_refused', NULL,
		'{"external_id":"707","team":"silver","title":"legacy","external_scope":"10","labels":null,"reason":"label \"repo/x\" names no registered target"}')`); err != nil {
		t.Fatal(err)
	}
	got := recentRefusals(t, nil)
	if len(got) != 1 || got[0].Code != "unclassified" || got[0].AllowedLabels == nil || len(got[0].AllowedLabels) != 0 ||
		got[0].Labels == nil || !strings.Contains(got[0].Reason, "names no registered target") {
		t.Errorf("legacy refusal = %+v, want code unclassified with empty label arrays", got)
	}
}

func TestRecentRouteRefusalsReturnAtMostTheLimitNewestFirst(t *testing.T) {
	resetTables(t)
	for _, id := range []string{"710", "711", "712"} {
		refuseTask(t, refusedTask(id, "silver"), "label_missing", "r", []string{})
	}
	got, err := testStore.RecentRouteRefusals(context.Background(), nil, time.Now().Add(-time.Hour), 2)
	if err != nil {
		t.Fatal(err)
	}
	if refusalIDs(got) != "712,711" {
		t.Errorf("refusals = %+v, want the two newest", got)
	}
}
