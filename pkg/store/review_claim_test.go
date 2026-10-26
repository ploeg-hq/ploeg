package store

import (
	"context"
	"slices"
	"sync"
	"testing"
	"time"
)

func TestClaimReviewChecksHandsEachItemToOneCaller(t *testing.T) {
	ctx := context.Background()
	resetTables(t)
	var want []int64
	for _, branch := range []string{"agent/vik-1801", "agent/vik-1802", "agent/vik-1803", "agent/vik-1804"} {
		id, _ := pullRequestItem(t, branch)
		forceItemState(t, id, "awaiting_review", 1)
		want = append(want, id)
	}

	var mu sync.Mutex
	var got []int64
	start := make(chan struct{})
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			items, err := testStore.ClaimReviewChecks(ctx, time.Hour)
			if err != nil {
				t.Error(err)
				return
			}
			mu.Lock()
			defer mu.Unlock()
			for _, it := range items {
				got = append(got, it.WorkItemID)
			}
		}()
	}
	close(start)
	wg.Wait()
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Fatalf("claimed by four concurrent callers = %v, want each of %v once", got, want)
	}

	if again, err := testStore.ClaimReviewChecks(ctx, time.Hour); err != nil || len(again) != 0 {
		t.Fatalf("a claim within the lease = %v, %v, want nothing", again, err)
	}
	mustExec(t, `UPDATE work_items SET review_claimed_until = now() - interval '1 second' WHERE id = $1`, want[0])
	if again, err := testStore.ClaimReviewChecks(ctx, time.Hour); err != nil || len(again) != 1 || again[0].WorkItemID != want[0] {
		t.Fatalf("a claim after the lease expired = %v, %v, want item %d", again, err, want[0])
	}
	if listed, err := testStore.AwaitingReview(ctx); err != nil || len(listed) != len(want) {
		t.Fatalf("AwaitingReview while claimed = %d items, %v, want all %d", len(listed), err, len(want))
	}
}

func TestRecordMergeCheckCountsARepeatedConflictOnce(t *testing.T) {
	ctx := context.Background()
	resetTables(t)
	item, _ := pullRequestItem(t, "agent/vik-1805")
	if ok, err := testStore.RecordPullRequestFacts(ctx, PullRequestFacts{Forge: "forgejo", Repo: "webgrip/ploeg", Number: 55,
		WorkItemID: item, State: "open", HeadSHA: "head-1"}); err != nil || !ok {
		t.Fatalf("seed: %v %v", ok, err)
	}
	key := PullRequestKey{Forge: "forgejo", Repo: "webgrip/ploeg", Number: 55}
	at := time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC)
	poll := func(after time.Duration) MergeCheck {
		t.Helper()
		at = at.Add(after)
		check, ok, err := testStore.RecordMergeCheck(ctx, key, item, MergeObservation{
			Mergeable: mergeable(false), HeadSHA: "head-1", RepeatAfter: 5 * time.Minute}, at)
		if err != nil || !ok {
			t.Fatalf("recorded = %v, err = %v", ok, err)
		}
		return check
	}

	if got := poll(0); got.UnmergeablePolls != 1 || got.State != MergeUnknown {
		t.Fatalf("first conflicted poll = %+v, want one poll, unknown", got)
	}
	if got := poll(time.Second); got.UnmergeablePolls != 1 || got.State != MergeUnknown {
		t.Fatalf("a conflicted poll one second later = %+v, want it counted with the first", got)
	}
	if got := poll(10 * time.Minute); got.UnmergeablePolls != 2 || got.State != MergeConflicted {
		t.Fatalf("a conflicted poll one interval later = %+v, want two polls, conflicted", got)
	}
}
