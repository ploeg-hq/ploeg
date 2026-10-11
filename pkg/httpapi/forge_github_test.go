package httpapi

import (
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
	"github.com/ploeg-hq/ploeg/pkg/provider/github"
)

func githubPost(t *testing.T, h http.Handler, secret, delivery string, body any) int {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/webhooks/forge/github", strings.NewReader(string(b)))
	req.Header.Set("X-GitHub-Event", "pull_request_review")
	req.Header.Set("X-GitHub-Delivery", delivery)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(b)
	req.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code
}

func TestForgeWebhook_GitHubRedeliveryIsRecordedOnce(t *testing.T) {
	reset(t)
	h := (&Server{
		Store: testStore,
		Log:   slog.New(slog.DiscardHandler),
		Forges: map[string]provider.ForgeProvider{
			"github": &github.Provider{Secret: "shh", Log: slog.New(slog.DiscardHandler)},
		},
	}).Handler()
	body := map[string]any{
		"action":       "submitted",
		"repository":   map[string]any{"full_name": "acme/widgets"},
		"pull_request": map[string]any{"number": 7, "head": map[string]any{"ref": "agent/vik-585", "sha": "h1"}},
		"review":       map[string]any{"state": "changes_requested", "body": "still broken"},
	}
	for range 2 {
		if code := githubPost(t, h, "shh", "gh-delivery-1", body); code != http.StatusAccepted {
			t.Fatalf("github webhook returned %d, want 202", code)
		}
	}
	if n := forgeAuditCount(t); n != 1 {
		t.Fatalf("audit rows = %d after a redelivery, want 1", n)
	}
	if code := githubPost(t, h, "wrong", "gh-delivery-2", body); code != http.StatusBadRequest {
		t.Fatalf("a wrongly signed github webhook returned %d, want 400", code)
	}
}
