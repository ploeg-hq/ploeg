package store

import (
	"context"
	"fmt"
	"reflect"
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/work"
)

func TestActiveWorkListsOtherLiveShiftsInTheSameRepositoryWithTheirPaths(t *testing.T) {
	ctx := context.Background()
	resetTables(t)
	me, myShift := pullRequestItem(t, "agent/vik-me")
	pullRequestItem(t, "agent/vik-other")
	_, closedShift := pullRequestItem(t, "agent/vik-closed")
	elsewhere, _, err := testStore.IngestAssigned(ctx, work.WorkItem{Provider: "vikunja", ExternalID: "elsewhere", Team: "silver", Title: "x",
		Target: &work.Target{Forge: "forgejo", Owner: "webgrip", Repo: "app"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := testStore.OpenShift(ctx, elsewhere, "silver", "agent/vik-elsewhere", 0); err != nil {
		t.Fatal(err)
	}
	if _, err := testStore.pool.Exec(ctx, `UPDATE shifts SET closed_at = now() WHERE id = $1`, closedShift); err != nil {
		t.Fatal(err)
	}
	if _, err := testStore.pool.Exec(ctx, `UPDATE shifts SET planned_paths = '{pkg/store/}' WHERE id = $1`, myShift); err != nil {
		t.Fatal(err)
	}
	if _, err := testStore.RecordPullRequestFacts(ctx, PullRequestFacts{Forge: "forgejo", Repo: "webgrip/ploeg", Number: 31,
		Branch: "agent/vik-other", State: "open", HeadSHA: "h1"}); err != nil {
		t.Fatal(err)
	}
	if ok, err := testStore.RecordChangedPaths(ctx, PullRequestPaths{Key: PullRequestKey{Forge: "forgejo", Repo: "webgrip/ploeg", Number: 31},
		HeadSHA: "h1", Paths: []ChangedPath{{Path: "pkg/store/store.go", Status: "modified"}, {Path: "README.md", Status: "modified"}}}); err != nil || !ok {
		t.Fatalf("paths recorded = %v, %v", ok, err)
	}

	mine, err := testStore.WorkItemPaths(ctx, me)
	if err != nil {
		t.Fatal(err)
	}
	if mine.Repo != "ploeg" || mine.ChangedKnown || !reflect.DeepEqual(mine.Planned, []string{"pkg/store/"}) {
		t.Fatalf("own paths = %+v", mine)
	}
	got, err := testStore.ActiveWork(ctx, me)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].WorkItemID == "" || got[0].ExternalID != "agent/vik-other" {
		t.Fatalf("active work = %+v; want only the other live Shift in webgrip/ploeg", got)
	}
	p := got[0].Paths
	if p.PullRequest != 31 || p.HeadSHA != "h1" || !p.ChangedKnown || !reflect.DeepEqual(p.Changed, []string{"pkg/store/store.go", "README.md"}) {
		t.Fatalf("other paths = %+v", p)
	}
}

func TestPlannedPathsNeedARunInALiveShift(t *testing.T) {
	if ok, err := testStore.RecordPlannedPaths(context.Background(), "no-such-run", []string{"a.go"}); err != nil || ok {
		t.Fatalf("recorded = %v, %v", ok, err)
	}
	if got := cleanPlannedPaths([]string{"a.go", "", "a.go", "b/"}); !reflect.DeepEqual(got, []string{"a.go", "b/"}) {
		t.Fatalf("clean = %v", got)
	}
}

func TestDependenciesAreReplacedAndNameTheirWorkItem(t *testing.T) {
	ctx := context.Background()
	resetTables(t)
	if _, err := testStore.pool.Exec(ctx, `DELETE FROM work_item_dependencies`); err != nil {
		t.Fatal(err)
	}
	blocked, _, err := testStore.IngestAssigned(ctx, work.WorkItem{Provider: "vikunja", ExternalID: "42", Team: "silver", Title: "b"})
	if err != nil {
		t.Fatal(err)
	}
	blocker, _, err := testStore.IngestAssigned(ctx, work.WorkItem{Provider: "vikunja", ExternalID: "41", Team: "silver", Title: "a"})
	if err != nil {
		t.Fatal(err)
	}
	changed, err := testStore.RecordDependencies(ctx, blocked, "vikunja", []Dependency{
		{ExternalID: "41", Title: "Schema", Kind: "blocked_by"}, {ExternalID: "39", Kind: "follows", Done: true},
		{ExternalID: "42", Kind: "blocked_by"}, {ExternalID: "7", Kind: "related"}})
	if err != nil || !changed {
		t.Fatalf("changed = %v, %v", changed, err)
	}
	deps, err := testStore.WorkItemDependencies(ctx, blocked)
	if err != nil || len(deps.Items) != 2 || deps.CheckedAt == nil {
		t.Fatalf("deps = %+v, %v", deps, err)
	}
	open := deps.Open()
	if len(open) != 1 || open[0].ExternalID != "41" || open[0].WorkItemID == "" || open[0].WorkItemID != fmt.Sprint(blocker) {
		t.Fatalf("open = %+v; want 41 naming its Work Item", open)
	}
	if changed, err := testStore.RecordDependencies(ctx, blocked, "vikunja", []Dependency{{ExternalID: "41", Kind: "blocked_by", Done: true}}); err != nil || !changed {
		t.Fatalf("done: changed = %v, %v", changed, err)
	}
	deps, _ = testStore.WorkItemDependencies(ctx, blocked)
	if len(deps.Items) != 1 || len(deps.Open()) != 0 {
		t.Fatalf("after done = %+v", deps)
	}
	var audits int
	if err := testStore.pool.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE work_item_id = $1 AND action = 'work_item.dependencies_seen'`, blocked).Scan(&audits); err != nil || audits != 2 {
		t.Fatalf("audits = %d, %v", audits, err)
	}
}
