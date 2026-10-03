package httpapi

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/provider"
	"github.com/ploeg-hq/ploeg/pkg/provider/forgejo"
)

// The forge endpoint is the first inbound surface carrying text written
// outside the factory, so these are mostly about what it REFUSES.

func forgeServer(t *testing.T, secret string) http.Handler {
	t.Helper()
	reset(t)
	return (&Server{
		Store: testStore,
		Log:   slog.New(slog.DiscardHandler),
		Forges: map[string]provider.ForgeProvider{
			"forgejo": &forgejo.Provider{Secret: secret, Log: slog.New(slog.DiscardHandler)},
		},
	}).Handler()
}

func forgePost(t *testing.T, h http.Handler, secret, delivery string, body any) int {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/webhooks/forge/forgejo", strings.NewReader(string(b)))
	req.Header.Set("X-Forgejo-Event", "pull_request_review")
	if delivery != "" {
		req.Header.Set("X-Forgejo-Delivery", delivery)
	}
	if secret != "" {
		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write(b)
		req.Header.Set("X-Forgejo-Signature", hex.EncodeToString(mac.Sum(nil)))
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code
}

func reviewBody() map[string]any {
	return map[string]any{
		"repository":   map[string]any{"full_name": "webgrip/ploeg"},
		"pull_request": map[string]any{"number": 7, "head": map[string]any{"ref": "agent/vik-585"}},
		"review":       map[string]any{"type": "pull_request_review_rejected", "content": "still broken"},
	}
}

func forgeAuditCount(t *testing.T) int {
	t.Helper()
	var n int
	if err := testPool.QueryRow(context.Background(),
		`SELECT count(*) FROM audit_log WHERE action LIKE 'forge.%'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestForgeWebhook_VerifiedEventIsRecorded(t *testing.T) {
	h := forgeServer(t, "shh")
	if code := forgePost(t, h, "shh", "d-1", reviewBody()); code != http.StatusAccepted {
		t.Fatalf("verified webhook returned %d, want 202", code)
	}
	if n := forgeAuditCount(t); n != 1 {
		t.Errorf("audit rows = %d, want 1", n)
	}
}

// An unverified webhook is rejected before anything is touched.
func TestForgeWebhook_RejectsBadSignature(t *testing.T) {
	h := forgeServer(t, "shh")
	if code := forgePost(t, h, "wrong-secret", "d-2", reviewBody()); code != http.StatusBadRequest {
		t.Errorf("wrongly-signed webhook returned %d, want 400", code)
	}
	if code := forgePost(t, h, "", "d-3", reviewBody()); code != http.StatusBadRequest {
		t.Errorf("unsigned webhook returned %d, want 400", code)
	}
	if n := forgeAuditCount(t); n != 0 {
		t.Errorf("a rejected webhook recorded %d audit rows", n)
	}
}

func TestForgeWebhook_RejectedDeliveryDoesNotSuppressVerifiedRetry(t *testing.T) {
	for _, secret := range []string{"", "wrong-secret"} {
		t.Run("rejected-secret-"+secret, func(t *testing.T) {
			h := forgeServer(t, "shh")
			delivery := t.Name()
			t.Cleanup(func() {
				if _, err := testPool.Exec(context.Background(),
					`DELETE FROM forge_deliveries WHERE provider = 'forgejo' AND delivery_id = $1`, delivery); err != nil {
					t.Error(err)
				}
			})
			if code := forgePost(t, h, secret, delivery, reviewBody()); code != http.StatusBadRequest {
				t.Fatalf("unverified delivery returned %d, want 400", code)
			}
			var deliveries int
			if err := testPool.QueryRow(context.Background(),
				`SELECT count(*) FROM forge_deliveries WHERE provider = 'forgejo' AND delivery_id = $1`, delivery).Scan(&deliveries); err != nil {
				t.Fatal(err)
			}
			if deliveries != 0 {
				t.Fatalf("unverified delivery persisted %d dedup rows, want 0", deliveries)
			}
			if code := forgePost(t, h, "shh", delivery, reviewBody()); code != http.StatusAccepted {
				t.Fatalf("verified retry returned %d, want 202", code)
			}
			if n := forgeAuditCount(t); n != 1 {
				t.Fatalf("verified retry recorded %d audit rows, want 1", n)
			}
			if code := forgePost(t, h, "shh", delivery, reviewBody()); code != http.StatusAccepted {
				t.Fatalf("verified redelivery returned %d, want 202", code)
			}
			if n := forgeAuditCount(t); n != 1 {
				t.Fatalf("verified redelivery recorded %d audit rows, want 1", n)
			}
			if code := forgePost(t, h, secret, delivery, reviewBody()); code != http.StatusBadRequest {
				t.Fatalf("unverified duplicate returned %d, want 400", code)
			}
		})
	}
}

// A retry that acts twice turns one review into two fix rounds.
func TestForgeWebhook_RedeliveryActsOnce(t *testing.T) {
	h := forgeServer(t, "shh")
	for i := 0; i < 3; i++ {
		if code := forgePost(t, h, "shh", "same-delivery", reviewBody()); code != http.StatusAccepted {
			t.Fatalf("delivery %d returned %d", i, code)
		}
	}
	if n := forgeAuditCount(t); n != 1 {
		t.Errorf("audit rows = %d after 3 deliveries of one id, want 1", n)
	}
}

// A forge with no delivery header must not have every event dropped.
func TestForgeWebhook_NoDeliveryHeaderIsNotADuplicate(t *testing.T) {
	h := forgeServer(t, "shh")
	forgePost(t, h, "shh", "", reviewBody())
	forgePost(t, h, "shh", "", reviewBody())
	if n := forgeAuditCount(t); n != 2 {
		t.Errorf("audit rows = %d, want 2 — no delivery id means no dedup, not silent drops", n)
	}
}

func TestForgeWebhook_UnknownProviderIs404(t *testing.T) {
	h := forgeServer(t, "")
	req := httptest.NewRequest(http.MethodPost, "/webhooks/forge/github", strings.NewReader("{}"))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("unknown provider returned %d, want 404", rec.Code)
	}
}

// A forge subscribes wider than the core consumes: an irrelevant event is
// acknowledged and creates nothing.
func TestForgeWebhook_IrrelevantEventCreatesNothing(t *testing.T) {
	h := forgeServer(t, "shh")
	code := forgePost(t, h, "shh", "d-push", map[string]any{
		"repository": map[string]any{"full_name": "webgrip/ploeg"},
		"action":     "push",
	})
	if code != http.StatusAccepted {
		t.Errorf("irrelevant event returned %d, want 202", code)
	}
	if n := forgeAuditCount(t); n != 0 {
		t.Errorf("an irrelevant event recorded %d audit rows", n)
	}
	var items int
	if err := testPool.QueryRow(context.Background(), `SELECT count(*) FROM work_items`).Scan(&items); err != nil {
		t.Fatal(err)
	}
	if items != 0 {
		t.Errorf("a push webhook created %d work items", items)
	}
}

type recordingSettler struct {
	forges []string
	events []provider.ForgeEvent
}

func (r *recordingSettler) HandleForgeEvent(_ context.Context, forge string, ev provider.ForgeEvent) error {
	r.forges = append(r.forges, forge)
	r.events = append(r.events, ev)
	return nil
}

func TestForgeWebhook_ClosedPullRequestReachesTheReviewSettler(t *testing.T) {
	reset(t)
	settler := &recordingSettler{}
	h := (&Server{
		Store:   testStore,
		Log:     slog.New(slog.DiscardHandler),
		Reviews: settler,
		Forges: map[string]provider.ForgeProvider{
			"forgejo": &forgejo.Provider{Secret: "shh", Log: slog.New(slog.DiscardHandler)},
		},
	}).Handler()

	merged := map[string]any{
		"action":       "closed",
		"repository":   map[string]any{"full_name": "webgrip/ploeg"},
		"pull_request": map[string]any{"number": 7, "merged": true, "head": map[string]any{"ref": "agent/vik-585"}},
	}
	if code := forgePost(t, h, "shh", "d-merged", merged); code != http.StatusAccepted {
		t.Fatalf("merged webhook returned %d, want 202", code)
	}
	if code := forgePost(t, h, "shh", "d-review", reviewBody()); code != http.StatusAccepted {
		t.Fatalf("review webhook returned %d, want 202", code)
	}
	if len(settler.events) != 1 || settler.events[0].Kind != provider.ForgePRMerged ||
		settler.events[0].PR != 7 || settler.forges[0] != "forgejo" {
		t.Fatalf("settler saw %+v on %v, want one merged event on forgejo", settler.events, settler.forges)
	}
}
