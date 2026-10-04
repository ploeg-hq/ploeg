package shiftengine

import (
	"context"
	"errors"
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/provider"
)

type pathsForge struct {
	*fakeForge
	paths provider.ChangedPaths
	err   error
	reads int
}

func (f *pathsForge) ChangedPaths(context.Context, string, int) (provider.ChangedPaths, error) {
	f.reads++
	return f.paths, f.err
}

func pathsWatch(forge *pathsForge) *ReviewWatch {
	w := statusWatch(&statusForge{fakeForge: forge.fakeForge})
	w.Forges = map[string]provider.ForgeProvider{"webgrip": forge, "forgejo": forge}
	return w
}

func storedPaths(t *testing.T, item int64) (head string, paths int, found bool) {
	t.Helper()
	err := testPool.QueryRow(context.Background(), `SELECT c.head_sha, (SELECT count(*) FROM pull_request_paths pp
		WHERE pp.pull_request_id = c.pull_request_id AND pp.head_sha = c.head_sha)
		FROM pull_request_path_captures c JOIN pull_requests p ON p.id = c.pull_request_id WHERE p.work_item_id = $1`, item).
		Scan(&head, &paths)
	if err != nil {
		return "", 0, false
	}
	return head, paths, true
}

func TestReviewWatch_ReconcileRecordsChangedPathsOncePerHead(t *testing.T) {
	ctx := context.Background()
	resetTables(t)
	item := awaitingReview(t, "652", "74")
	forge := &pathsForge{
		fakeForge: &fakeForge{prFacts: map[int]provider.PullRequestFacts{74: {HeadSHA: "h74"}}},
		paths: provider.ChangedPaths{Paths: []provider.ChangedPath{{Path: "AGENTS.md", Status: provider.PathModified},
			{Path: "main.go", Status: provider.PathAdded}}},
	}
	w := pathsWatch(forge)
	w.Reconcile(ctx)
	if head, n, ok := storedPaths(t, item); !ok || head != "h74" || n != 2 {
		t.Fatalf("stored paths at %q: %d (found %v); want 2 at h74", head, n, ok)
	}
	w.Reconcile(ctx)
	if forge.reads != 1 {
		t.Errorf("paths read %d times for one head; want once", forge.reads)
	}
}

func TestReviewWatch_ReconcileLeavesChangedPathsAbsentWhenTheReadFails(t *testing.T) {
	ctx := context.Background()
	resetTables(t)
	item := awaitingReview(t, "653", "75")
	forge := &pathsForge{
		fakeForge: &fakeForge{prFacts: map[int]provider.PullRequestFacts{75: {HeadSHA: "h75"}}},
		err:       errors.New("forge down"),
	}
	pathsWatch(forge).Reconcile(ctx)
	if _, _, ok := storedPaths(t, item); ok {
		t.Fatal("a failed read stored a list; it must leave the paths absent")
	}
}
