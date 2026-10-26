package shiftengine

import (
	"context"
	"slices"
	"sync"
	"testing"
	"time"
)

func reconcileConcurrently(ctx context.Context, watches ...*ReviewWatch) {
	start := make(chan struct{})
	var wg sync.WaitGroup
	for _, w := range watches {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			w.Reconcile(ctx)
		}()
	}
	close(start)
	wg.Wait()
}

func TestReviewWatch_TwoReplicasReadEachPullRequestOnce(t *testing.T) {
	ctx := context.Background()
	resetTables(t)
	awaitingReview(t, "1701", "51")
	awaitingReview(t, "1702", "52")
	awaitingReview(t, "1703", "53")
	forge := &fakeForge{}
	replica := func() *ReviewWatch {
		w := newReviewWatch(forge, &fakeTracker{})
		w.ClaimFor = time.Hour
		return w
	}

	reconcileConcurrently(ctx, replica(), replica())

	reads := slices.Clone(forge.reads)
	slices.Sort(reads)
	if !slices.Equal(reads, []int{51, 52, 53}) {
		t.Fatalf("forge reads by two replicas = %v, want each pull request once", reads)
	}

	replica().Reconcile(ctx)
	if len(forge.reads) != 3 {
		t.Errorf("a reconcile within the claim read %v again", forge.reads[3:])
	}
	if _, err := testPool.Exec(ctx, `UPDATE work_items SET review_claimed_until = now() - interval '1 second'`); err != nil {
		t.Fatal(err)
	}
	replica().Reconcile(ctx)
	if len(forge.reads) != 6 {
		t.Errorf("forge reads after the claim expired = %v, want every pull request read again", forge.reads)
	}
}

func TestReviewWatch_TwoReplicasCountOneConflictedPoll(t *testing.T) {
	ctx := context.Background()
	resetTables(t)
	id := awaitingReview(t, "1704", "54")
	forge := &mergeForge{fakeForge: &fakeForge{}, polls: []mergePoll{
		{mergeable: no(), head: "aaa"},
		{mergeable: no(), head: "aaa"},
	}}
	replica := func() *ReviewWatch {
		w := reviewWatchOn(forge)
		w.ClaimFor = time.Hour
		return w
	}

	reconcileConcurrently(ctx, replica(), replica())

	var polls int
	if err := testPool.QueryRow(ctx, `SELECT unmergeable_polls FROM pull_requests WHERE work_item_id = $1`, id).Scan(&polls); err != nil {
		t.Fatal(err)
	}
	if polls != 1 {
		t.Errorf("unmergeable polls after one interval on two replicas = %d, want 1", polls)
	}
	if pr := operatorPullRequest(t, id); pr.MergeState != "unknown" {
		t.Errorf("merge state = %q, want unknown after one interval", pr.MergeState)
	}
	if got := mergeAudit(t, id); len(got) != 0 {
		t.Errorf("audit = %v, want no conflict confirmed", got)
	}
}
