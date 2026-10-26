package store

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestClaimPullRequestComment_OneHolderAtATime(t *testing.T) {
	ctx := context.Background()
	slot := CommentSlot{Forge: "forgejo", Repo: "webgrip/ploeg", Number: 7, Key: "one-holder"}
	var wg sync.WaitGroup
	var mu sync.Mutex
	var releases []func()
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			release, held, err := testStore.ClaimPullRequestComment(ctx, slot, time.Minute)
			if err != nil {
				t.Error(err)
				return
			}
			if held {
				mu.Lock()
				releases = append(releases, release)
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if len(releases) != 1 {
		t.Fatalf("holders = %d, want 1", len(releases))
	}
	releases[0]()
	release, held, err := testStore.ClaimPullRequestComment(ctx, slot, time.Minute)
	if err != nil || !held {
		t.Fatalf("claim after release: held %v err %v", held, err)
	}
	release()
}

func TestClaimPullRequestComment_ExpiredLeaseRecoversACrashedHolder(t *testing.T) {
	ctx := context.Background()
	slot := CommentSlot{Forge: "forgejo", Repo: "webgrip/ploeg", Number: 7, Key: "crashed"}
	crashedRelease, held, err := testStore.ClaimPullRequestComment(ctx, slot, 50*time.Millisecond)
	if err != nil || !held {
		t.Fatalf("first claim: held %v err %v", held, err)
	}
	if _, held, _ := testStore.ClaimPullRequestComment(ctx, slot, time.Minute); held {
		t.Fatal("a live lease was taken over")
	}
	time.Sleep(100 * time.Millisecond)
	release, held, err := testStore.ClaimPullRequestComment(ctx, slot, time.Minute)
	if err != nil || !held {
		t.Fatalf("claim after expiry: held %v err %v", held, err)
	}
	crashedRelease()
	if _, held, _ := testStore.ClaimPullRequestComment(ctx, slot, time.Minute); held {
		t.Fatal("the expired holder's release ended its successor's lease")
	}
	release()
}

func TestClaimPullRequestComment_HoldsNoConnection(t *testing.T) {
	ctx := context.Background()
	slot := CommentSlot{Forge: "forgejo", Repo: "webgrip/ploeg", Number: 7, Key: "no-connection"}
	before := testStore.pool.Stat().AcquiredConns()
	release, held, err := testStore.ClaimPullRequestComment(ctx, slot, time.Minute)
	if err != nil || !held {
		t.Fatalf("claim: held %v err %v", held, err)
	}
	defer release()
	if after := testStore.pool.Stat().AcquiredConns(); after != before {
		t.Errorf("acquired connections while the lease is held = %d, want %d", after, before)
	}
}
