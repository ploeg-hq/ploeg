package store

import (
	"context"
	"sync"
	"testing"
	"time"
)

func resetDeliveries(t *testing.T) {
	t.Helper()
	if _, err := testStore.pool.Exec(context.Background(), `DELETE FROM forge_deliveries`); err != nil {
		t.Fatal(err)
	}
}

func expireDeliveryClaim(t *testing.T, provider, delivery string) {
	t.Helper()
	if _, err := testStore.pool.Exec(context.Background(),
		`UPDATE forge_deliveries SET claimed_until = now() - interval '1 second' WHERE provider = $1 AND delivery_id = $2`,
		provider, delivery); err != nil {
		t.Fatal(err)
	}
}

func claimState(t *testing.T, provider, delivery string) DeliveryClaim {
	t.Helper()
	c, err := testStore.ClaimDelivery(context.Background(), provider, delivery, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestClaimDelivery_ConcurrentDuplicatesClaimOnce(t *testing.T) {
	resetDeliveries(t)
	const deliveries = 24
	states := make(chan DeliveryState, deliveries)
	var wg sync.WaitGroup
	for range deliveries {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, err := testStore.ClaimDelivery(context.Background(), "forgejo", "same", time.Minute)
			if err != nil {
				t.Error(err)
				return
			}
			states <- c.State
		}()
	}
	wg.Wait()
	close(states)
	counts := map[DeliveryState]int{}
	for s := range states {
		counts[s]++
	}
	if counts[DeliveryClaimed] != 1 || counts[DeliveryInFlight] != deliveries-1 {
		t.Fatalf("claims = %v, want exactly one claimed and the rest in flight", counts)
	}
}

func TestClaimDelivery_CompletedDeliveryIsNotActedOnAgain(t *testing.T) {
	resetDeliveries(t)
	first := claimState(t, "forgejo", "done")
	if first.State != DeliveryClaimed {
		t.Fatalf("first delivery = %s, want claimed", first.State)
	}
	if err := testStore.CompleteDelivery(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	expireDeliveryClaim(t, "forgejo", "done")
	if again := claimState(t, "forgejo", "done"); again.State != DeliveryCompleted {
		t.Fatalf("redelivery of a completed delivery = %s, want completed", again.State)
	}
}

func TestClaimDelivery_ACrashBeforeCompletionLetsTheRedeliveryAct(t *testing.T) {
	resetDeliveries(t)
	crashed := claimState(t, "forgejo", "crash")
	if crashed.State != DeliveryClaimed {
		t.Fatalf("first delivery = %s, want claimed", crashed.State)
	}
	if during := claimState(t, "forgejo", "crash"); during.State != DeliveryInFlight {
		t.Fatalf("redelivery inside the lease = %s, want in flight", during.State)
	}
	expireDeliveryClaim(t, "forgejo", "crash")
	retry := claimState(t, "forgejo", "crash")
	if retry.State != DeliveryClaimed {
		t.Fatalf("redelivery after the crashed claim expired = %s, want claimed", retry.State)
	}
	if err := testStore.ReleaseDelivery(context.Background(), crashed); err != nil {
		t.Fatal(err)
	}
	if err := testStore.CompleteDelivery(context.Background(), crashed); err != nil {
		t.Fatal(err)
	}
	if late := claimState(t, "forgejo", "crash"); late.State != DeliveryInFlight {
		t.Fatalf("after the crashed handler woke up and finished, the delivery = %s; its stale claim must not touch the retry's", late.State)
	}
	if err := testStore.CompleteDelivery(context.Background(), retry); err != nil {
		t.Fatal(err)
	}
	if done := claimState(t, "forgejo", "crash"); done.State != DeliveryCompleted {
		t.Fatalf("after the retry completed, the delivery = %s, want completed", done.State)
	}
}

func TestClaimDelivery_AReleasedClaimIsTakenAtOnce(t *testing.T) {
	resetDeliveries(t)
	failed := claimState(t, "tracker:vikunja", "failed")
	if err := testStore.ReleaseDelivery(context.Background(), failed); err != nil {
		t.Fatal(err)
	}
	if retry := claimState(t, "tracker:vikunja", "failed"); retry.State != DeliveryClaimed {
		t.Fatalf("redelivery after a failed delivery released its claim = %s, want claimed", retry.State)
	}
}

func TestClaimDelivery_AnUnidentifiedDeliveryRecordsNothing(t *testing.T) {
	resetDeliveries(t)
	c := claimState(t, "forgejo", "")
	if c.State != DeliveryUnidentified {
		t.Fatalf("delivery without an id = %s, want unidentified", c.State)
	}
	if err := testStore.CompleteDelivery(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := testStore.pool.QueryRow(context.Background(), `SELECT count(*) FROM forge_deliveries`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("forge_deliveries rows = %d, %v; an id-less delivery records nothing", n, err)
	}
}

func TestClaimDelivery_KeysAreSeparatePerProvider(t *testing.T) {
	resetDeliveries(t)
	if c := claimState(t, "forgejo", "shared"); c.State != DeliveryClaimed {
		t.Fatalf("forgejo claim = %s", c.State)
	}
	if c := claimState(t, "tracker:vikunja", "shared"); c.State != DeliveryClaimed {
		t.Fatalf("the same id from another provider = %s, want claimed", c.State)
	}
}
