package github

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http/httptest"
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/provider"
)

func grantSigned(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func TestInstallationDeliveriesAreGrantEventsVerifiedWithTheirAppsSecret(t *testing.T) {
	p := &Provider{Secret: "static", AppSecret: func(_ context.Context, appID string) (string, bool) {
		return "app-secret", appID == "1001"
	}}
	body := []byte(`{"action":"removed","installation":{"id":77,"app_id":1001,"account":{"id":5}},"repositories_removed":[{"full_name":"acme/app"}]}`)
	parse := func(secret, target string) ([]provider.ForgeEvent, error) {
		r := httptest.NewRequest("POST", "/webhooks/forge/github", bytes.NewReader(body))
		r.Header.Set("X-GitHub-Event", "installation_repositories")
		r.Header.Set("X-GitHub-Delivery", "d1")
		r.Header.Set(HookTargetHeader, target)
		r.Header.Set("X-GitHub-Hook-Installation-Target-Type", "integration")
		r.Header.Set("X-Hub-Signature-256", grantSigned(secret, body))
		return p.ParseWebhook(r)
	}
	events, err := parse("app-secret", "1001")
	if err != nil || len(events) != 1 || events[0].Grant == nil {
		t.Fatalf("events = %+v, %v", events, err)
	}
	g := events[0].Grant
	if g.InstallationID != "77" || g.AppID != "1001" || g.Action != "removed" || len(g.Removed) != 1 || g.Removed[0] != "acme/app" {
		t.Fatalf("grant = %+v", g)
	}
	if _, err := parse("static", "1001"); err == nil {
		t.Fatal("a held App's delivery verified with the static secret")
	}
	if _, err := parse("app-secret", "2002"); err == nil {
		t.Fatal("another App's delivery verified with a held App's secret")
	}
	if events, err := parse("static", "2002"); err != nil || len(events) != 1 {
		t.Fatalf("the static App's delivery = %v, %v", events, err)
	}
}
