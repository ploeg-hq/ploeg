package store

import (
	"context"
	"fmt"
	"reflect"
	"testing"
	"time"
)

func TestMigration0036AddsChangedPathTables(t *testing.T) {
	ctx := context.Background()
	for _, table := range []string{"pull_request_path_captures", "pull_request_paths"} {
		var exists bool
		if err := testStore.pool.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, table).Scan(&exists); err != nil || !exists {
			t.Errorf("table %s exists = %v (%v)", table, exists, err)
		}
	}
}

func changedPaths(t *testing.T, item int64) *struct {
	HeadSHA   string        `json:"headSha"`
	Truncated bool          `json:"truncated"`
	Paths     []ChangedPath `json:"paths"`
} {
	t.Helper()
	w := &world{t: t, ctx: context.Background(), now: time.Now()}
	return w.play(item).ChangedPaths
}

func TestChangedPathsAreKeptForTheLatestHeadRead(t *testing.T) {
	ctx := context.Background()
	resetTables(t)
	item, _ := pullRequestItem(t, "agent/vik-1698")
	key := PullRequestKey{Forge: "forgejo", Repo: "webgrip/ploeg", Number: 21}

	if due, err := testStore.ChangedPathsDue(ctx, key, "h1"); err != nil || due {
		t.Fatalf("due = %v (%v) before the pull request is recorded; want false", due, err)
	}
	if ok, err := testStore.RecordChangedPaths(ctx, PullRequestPaths{Key: key, HeadSHA: "h1",
		Paths: []ChangedPath{{Path: "a.go", Status: "added"}}}); err != nil || ok {
		t.Fatalf("recorded = %v (%v) for an unrecorded pull request; want false", ok, err)
	}
	if _, err := testStore.RecordPullRequestFacts(ctx, PullRequestFacts{Forge: "forgejo", Repo: "webgrip/ploeg", Number: 21,
		Branch: "agent/vik-1698", State: "open", HeadSHA: "h1"}); err != nil {
		t.Fatal(err)
	}
	if paths := changedPaths(t, item); paths != nil {
		t.Fatalf("paths = %+v; paths never read stay absent", paths)
	}
	if due, err := testStore.ChangedPathsDue(ctx, key, "h1"); err != nil || !due {
		t.Fatalf("due = %v (%v) for a head without paths; want true", due, err)
	}

	first := []ChangedPath{{Path: "AGENTS.md", Status: "modified"}, {Path: "CLAUDE.md", Status: "renamed", PreviousPath: "docs/CLAUDE.md"},
		{Path: "AGENTS.md", Status: "modified"}, {Path: "", Status: "added"}, {Path: "x.go", Status: "vibes"}}
	if ok, err := testStore.RecordChangedPaths(ctx, PullRequestPaths{Key: key, HeadSHA: "h1", Paths: first}); err != nil || !ok {
		t.Fatalf("recorded = %v (%v)", ok, err)
	}
	paths := changedPaths(t, item)
	want := []ChangedPath{{Path: "AGENTS.md", Status: "modified"}, {Path: "CLAUDE.md", Status: "renamed", PreviousPath: "docs/CLAUDE.md"},
		{Path: "x.go", Status: "modified"}}
	if paths == nil || paths.HeadSHA != "h1" || !reflect.DeepEqual(paths.Paths, want) || paths.Truncated {
		t.Fatalf("paths = %+v; want %v at h1, not truncated", paths, want)
	}
	if due, err := testStore.ChangedPathsDue(ctx, key, "h1"); err != nil || due {
		t.Fatalf("due = %v (%v) for a head already read; want false", due, err)
	}

	if ok, err := testStore.RecordChangedPaths(ctx, PullRequestPaths{Key: key, HeadSHA: "h0",
		Paths: []ChangedPath{{Path: "stale.go", Status: "added"}}}); err != nil || ok {
		t.Fatalf("recorded = %v (%v) for a head the pull request no longer has; want false", ok, err)
	}

	if _, err := testStore.RecordPullRequestFacts(ctx, PullRequestFacts{Forge: "forgejo", Repo: "webgrip/ploeg", Number: 21, HeadSHA: "h2"}); err != nil {
		t.Fatal(err)
	}
	if paths := changedPaths(t, item); paths == nil || paths.HeadSHA != "h1" {
		t.Fatalf("paths = %+v after a push to h2; want the list read at h1, named by its head", paths)
	}
	if due, err := testStore.ChangedPathsDue(ctx, key, "h2"); err != nil || !due {
		t.Fatalf("due = %v (%v) after a push; want true", due, err)
	}
	if ok, err := testStore.RecordChangedPaths(ctx, PullRequestPaths{Key: key, HeadSHA: "h2", Paths: []ChangedPath{}}); err != nil || !ok {
		t.Fatalf("recorded = %v (%v)", ok, err)
	}
	paths = changedPaths(t, item)
	if paths == nil || paths.HeadSHA != "h2" || paths.Paths == nil || len(paths.Paths) != 0 || paths.Truncated {
		t.Fatalf("paths = %+v; a read list with no paths is empty, not absent", paths)
	}
	var captures int
	if err := testStore.pool.QueryRow(ctx, `SELECT count(*) FROM pull_request_path_captures`).Scan(&captures); err != nil || captures != 1 {
		t.Errorf("captures = %d (%v); a newer head replaces the older list", captures, err)
	}
}

func TestChangedPathsAreCappedAndMarkedTruncated(t *testing.T) {
	ctx := context.Background()
	resetTables(t)
	item, _ := pullRequestItem(t, "agent/vik-1698b")
	key := PullRequestKey{Forge: "forgejo", Repo: "webgrip/ploeg", Number: 22}
	if _, err := testStore.RecordPullRequestFacts(ctx, PullRequestFacts{Forge: "forgejo", Repo: "webgrip/ploeg", Number: 22,
		Branch: "agent/vik-1698b", HeadSHA: "h1"}); err != nil {
		t.Fatal(err)
	}
	paths := make([]ChangedPath, 0, MaxStoredChangedPaths+5)
	for i := 0; i < MaxStoredChangedPaths+5; i++ {
		paths = append(paths, ChangedPath{Path: fmt.Sprintf("f%03d.go", i), Status: "added"})
	}
	if ok, err := testStore.RecordChangedPaths(ctx, PullRequestPaths{Key: key, HeadSHA: "h1", Paths: paths}); err != nil || !ok {
		t.Fatalf("recorded = %v (%v)", ok, err)
	}
	stored := changedPaths(t, item)
	if stored == nil || len(stored.Paths) != MaxStoredChangedPaths || !stored.Truncated {
		t.Fatalf("paths = %+v; want %d, truncated", stored, MaxStoredChangedPaths)
	}
	if got := stored.Paths[0].Path; got != "f000.go" {
		t.Errorf("first path = %s; the forge's order is kept", got)
	}
}
