package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/provider"
	"github.com/ploeg-hq/ploeg/pkg/provider/vikunja"
)

const assignedTaskHook = `{"event_name":"task.assignee.created","time":"2026-10-28T09:00:00Z",` +
	`"data":{"task":{"id":2940,"title":"dedupe me","project_id":7,"updated":"2026-10-28T09:00:00Z"},"assignee":{"username":"builder"}}}`

func clearDeliveries(t *testing.T) {
	t.Helper()
	if _, err := testPool.Exec(context.Background(), `DELETE FROM forge_deliveries`); err != nil {
		t.Fatal(err)
	}
}

func leftClaim(t *testing.T, key, delivery, until string) {
	t.Helper()
	if _, err := testPool.Exec(context.Background(), `
		INSERT INTO forge_deliveries (provider, delivery_id, claim, claimed_until)
		VALUES ($1, $2, 'claim of a handler that crashed', now() + $3::interval)`, key, delivery, until); err != nil {
		t.Fatal(err)
	}
}

func auditCount(t *testing.T, pattern string) int {
	t.Helper()
	var n int
	if err := testPool.QueryRow(context.Background(),
		`SELECT count(*) FROM audit_log WHERE action LIKE $1`, pattern).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func vikunjaDedupeServer(t *testing.T) http.Handler {
	t.Helper()
	reset(t)
	clearDeliveries(t)
	return (&Server{
		Store: testStore,
		Log:   slog.New(slog.DiscardHandler),
		Trackers: map[string]provider.TrackerProvider{
			"vikunja": &vikunja.Provider{Secret: testTrackerSecret, DefaultTeam: "silver", Log: slog.New(slog.DiscardHandler)},
		},
	}).Handler()
}

func postTracker(h http.Handler, body string) int {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, signedTrackerHook("vikunja", body))
	return rec.Code
}

func syntheticVikunjaID(t *testing.T, body string) string {
	t.Helper()
	id := (&vikunja.Provider{}).DeliveryID(signedTrackerHook("vikunja", body))
	if id == "" {
		t.Fatal("the fixture yields no synthetic delivery id")
	}
	return id
}

func concurrently(n int, deliver func() int) []int {
	codes := make([]int, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			codes[i] = deliver()
		}()
	}
	wg.Wait()
	return codes
}

func onlyAcceptedOrInFlight(t *testing.T, codes []int) {
	t.Helper()
	for _, c := range codes {
		if c != http.StatusAccepted && c != http.StatusConflict {
			t.Fatalf("a duplicate delivery returned %d, want 202 or 409", c)
		}
	}
}

func TestTrackerWebhook_ConcurrentDuplicateDeliveriesActOnce(t *testing.T) {
	h := vikunjaDedupeServer(t)
	codes := concurrently(8, func() int { return postTracker(h, assignedTaskHook) })
	onlyAcceptedOrInFlight(t, codes)
	if n := auditCount(t, "work_item.%"); n != 1 {
		t.Fatalf("work item audit rows = %d after eight copies of one Vikunja delivery, want 1", n)
	}
	if code := postTracker(h, assignedTaskHook); code != http.StatusAccepted {
		t.Fatalf("a later redelivery returned %d, want 202", code)
	}
	if n := auditCount(t, "work_item.%"); n != 1 {
		t.Fatalf("work item audit rows = %d after a later redelivery, want 1", n)
	}
}

func TestTrackerWebhook_ACrashBetweenClaimAndEffectIsActedOnByTheRedelivery(t *testing.T) {
	h := vikunjaDedupeServer(t)
	leftClaim(t, "tracker:vikunja", syntheticVikunjaID(t, assignedTaskHook), "-1 second")
	if code := postTracker(h, assignedTaskHook); code != http.StatusAccepted {
		t.Fatalf("redelivery after a crashed delivery returned %d, want 202", code)
	}
	if n := auditCount(t, "work_item.%"); n != 1 {
		t.Fatalf("work item audit rows = %d, want 1: the redelivery must run the effects the crash lost", n)
	}
}

func TestTrackerWebhook_ADeliveryStillActingAnswersConflict(t *testing.T) {
	h := vikunjaDedupeServer(t)
	leftClaim(t, "tracker:vikunja", syntheticVikunjaID(t, assignedTaskHook), "1 minute")
	if code := postTracker(h, assignedTaskHook); code != http.StatusConflict {
		t.Fatalf("redelivery while the first delivery holds its claim returned %d, want 409", code)
	}
	if n := auditCount(t, "work_item.%"); n != 0 {
		t.Fatalf("work item audit rows = %d, want 0 while another handler holds the delivery", n)
	}
}

func TestForgeWebhook_ConcurrentDuplicateDeliveriesActOnce(t *testing.T) {
	h := forgeServer(t, "shh")
	clearDeliveries(t)
	codes := concurrently(8, func() int { return forgePost(t, h, "shh", "concurrent", reviewBody()) })
	onlyAcceptedOrInFlight(t, codes)
	if n := forgeAuditCount(t); n != 1 {
		t.Fatalf("forge audit rows = %d after eight copies of one delivery, want 1", n)
	}
}

func TestForgeWebhook_ACrashBetweenClaimAndEffectIsActedOnByTheRedelivery(t *testing.T) {
	h := forgeServer(t, "shh")
	clearDeliveries(t)
	leftClaim(t, "forgejo", "crashed", "-1 second")
	if code := forgePost(t, h, "shh", "crashed", reviewBody()); code != http.StatusAccepted {
		t.Fatalf("redelivery after a crashed delivery returned %d, want 202", code)
	}
	if n := forgeAuditCount(t); n != 1 {
		t.Fatalf("forge audit rows = %d, want 1: the redelivery must run the effects the crash lost", n)
	}
	if code := forgePost(t, h, "shh", "crashed", reviewBody()); code != http.StatusAccepted || forgeAuditCount(t) != 1 {
		t.Fatalf("redelivery after the effects completed returned %d with %d audit rows, want 202 and 1", code, forgeAuditCount(t))
	}
}

func TestForgeWebhook_ADeliveryStillActingAnswersConflict(t *testing.T) {
	h := forgeServer(t, "shh")
	clearDeliveries(t)
	leftClaim(t, "forgejo", "in-flight", "1 minute")
	if code := forgePost(t, h, "shh", "in-flight", reviewBody()); code != http.StatusConflict {
		t.Fatalf("redelivery while the first delivery holds its claim returned %d, want 409", code)
	}
	if n := forgeAuditCount(t); n != 0 {
		t.Fatalf("forge audit rows = %d, want 0 while another handler holds the delivery", n)
	}
}
