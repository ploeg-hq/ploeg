package store

import (
	"context"
	"sync"
	"testing"
)

func reviewRows(t *testing.T, item int64) int {
	t.Helper()
	var n int
	if err := testStore.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM work_item_reviews WHERE work_item_id = $1`, item).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func reviewRequeues(t *testing.T, item int64) int {
	t.Helper()
	var n int
	if err := testStore.pool.QueryRow(context.Background(),
		`SELECT review_requeues FROM work_items WHERE id = $1`, item).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func awaitingReviewItem(t *testing.T) int64 {
	t.Helper()
	resetTables(t)
	id, _ := ingestItem(t)
	forceItemState(t, id, "awaiting_review", 0)
	return id
}

func TestRecordChangesRequested_ARedeliveredReviewIsNotASecondFixRound(t *testing.T) {
	ctx := context.Background()
	item := awaitingReviewItem(t)
	review := ChangesRequested{WorkItemID: item, Provider: "github", Repo: "o/r", PR: 7, Reviewer: "ryan",
		Body: "rename it", HeadSHA: "abc", ReviewID: "9001"}
	first, err := testStore.RecordChangesRequested(ctx, review)
	if err != nil || first != ReviewRequeued {
		t.Fatalf("first review = %q, %v; want requeued", first, err)
	}
	forceItemState(t, item, "awaiting_review", 0)
	again, err := testStore.RecordChangesRequested(ctx, review)
	if err != nil || again != ReviewDuplicate {
		t.Fatalf("redelivered review = %q, %v; want duplicate", again, err)
	}
	if n := reviewRows(t, item); n != 1 {
		t.Fatalf("work_item_reviews rows = %d, want 1", n)
	}
	if n := reviewRequeues(t, item); n != 1 {
		t.Fatalf("review requeues = %d, want 1", n)
	}
	if state, _ := itemStateAttempts(t, item); state != "awaiting_review" {
		t.Fatalf("state after the redelivered review = %q; it must not queue the Work Item again", state)
	}
}

func TestRecordChangesRequested_AReviewWithoutAForgeIDIsStoredOncePerContent(t *testing.T) {
	ctx := context.Background()
	item := awaitingReviewItem(t)
	review := ChangesRequested{WorkItemID: item, Provider: "forgejo", Repo: "o/r", PR: 7, Reviewer: "ryan",
		Body: "rename it", HeadSHA: "abc"}
	if _, err := testStore.RecordChangesRequested(ctx, review); err != nil {
		t.Fatal(err)
	}
	if again, err := testStore.RecordChangesRequested(ctx, review); err != nil || again != ReviewDuplicate {
		t.Fatalf("the same review again = %q, %v; want duplicate", again, err)
	}
	review.HeadSHA = "def"
	if later, err := testStore.RecordChangesRequested(ctx, review); err != nil || later == ReviewDuplicate {
		t.Fatalf("the same words on a new head = %q, %v; that is a new review", later, err)
	}
	if n := reviewRows(t, item); n != 2 {
		t.Fatalf("work_item_reviews rows = %d, want 2", n)
	}
}

func TestRecordChangesRequested_DistinctForgeReviewsAreBothStored(t *testing.T) {
	ctx := context.Background()
	item := awaitingReviewItem(t)
	for _, id := range []string{"1", "2"} {
		if action, err := testStore.RecordChangesRequested(ctx, ChangesRequested{WorkItemID: item, Provider: "github",
			Repo: "o/r", PR: 7, Reviewer: "ryan", Body: "rename it", HeadSHA: "abc", ReviewID: id}); err != nil || action == ReviewDuplicate {
			t.Fatalf("review %s = %q, %v", id, action, err)
		}
	}
	if n := reviewRows(t, item); n != 2 {
		t.Fatalf("work_item_reviews rows = %d, want 2", n)
	}
}

func TestRecordChangesRequested_ConcurrentDuplicatesStoreOneReview(t *testing.T) {
	ctx := context.Background()
	item := awaitingReviewItem(t)
	review := ChangesRequested{WorkItemID: item, Provider: "github", Repo: "o/r", PR: 7, Reviewer: "ryan",
		Body: "rename it", ReviewID: "77"}
	const deliveries = 8
	actions := make(chan ReviewAction, deliveries)
	var wg sync.WaitGroup
	for range deliveries {
		wg.Add(1)
		go func() {
			defer wg.Done()
			a, err := testStore.RecordChangesRequested(ctx, review)
			if err != nil {
				t.Error(err)
				return
			}
			actions <- a
		}()
	}
	wg.Wait()
	close(actions)
	duplicates := 0
	for a := range actions {
		if a == ReviewDuplicate {
			duplicates++
		}
	}
	if duplicates != deliveries-1 || reviewRows(t, item) != 1 || reviewRequeues(t, item) != 1 {
		t.Fatalf("duplicates = %d, rows = %d, requeues = %d; want %d, 1, 1",
			duplicates, reviewRows(t, item), reviewRequeues(t, item), deliveries-1)
	}
}
