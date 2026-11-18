package github

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
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
	p := &Provider{Secret: "static", AppSecret: heldApp1001}
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
	if events, err := parse("static", "2002"); err != nil || len(events) != 0 {
		t.Fatalf("a static-secret installation delivery = %v, %v; it must yield no grant", events, err)
	}
}

func heldApp1001(_ context.Context, appID string) (string, bool, error) {
	if appID == "1001" {
		return "app-secret", true, nil
	}
	return "", false, nil
}

func installationDelivery(t *testing.T, p *Provider, secret string, headers map[string]string, body []byte) ([]provider.ForgeEvent, error) {
	t.Helper()
	r := httptest.NewRequest("POST", "/webhooks/forge/github", bytes.NewReader(body))
	r.Header.Set("X-GitHub-Event", "installation")
	r.Header.Set("X-GitHub-Delivery", "d1")
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	r.Header.Set("X-Hub-Signature-256", grantSigned(secret, body))
	return p.ParseWebhook(r)
}

func TestTheStaticSecretCannotSpoofAGrantForAHeldApp(t *testing.T) {
	p := &Provider{Secret: "static", AppSecret: heldApp1001}
	body := []byte(`{"action":"deleted","installation":{"id":77,"app_id":1001,"account":{"id":5}}}`)
	cases := map[string]map[string]string{
		"target id without the integration type":        {HookTargetHeader: "1001", "X-GitHub-Hook-Installation-Target-Type": "repository"},
		"no target headers, app_id only in the payload": {},
	}
	for name, headers := range cases {
		events, err := installationDelivery(t, p, "static", headers, body)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(events) != 0 {
			t.Fatalf("%s: a static-secret delivery yielded %+v", name, events[0].Grant)
		}
	}
	events, err := installationDelivery(t, p, "app-secret", map[string]string{HookTargetHeader: "1001", "X-GitHub-Hook-Installation-Target-Type": "integration"},
		[]byte(`{"action":"deleted","installation":{"id":77,"app_id":2002,"account":{"id":5}}}`))
	if err != nil || len(events) != 0 {
		t.Fatalf("a payload naming another App = %v, %v", events, err)
	}
}

func TestADeliveryForAnAppIsRefusedWhenItsSecretCannotBeRead(t *testing.T) {
	p := &Provider{Secret: "static", AppSecret: func(context.Context, string) (string, bool, error) {
		return "", false, errors.New("store unreachable")
	}}
	body := []byte(`{"action":"suspend","installation":{"id":77,"app_id":1001,"account":{"id":5}}}`)
	if events, err := installationDelivery(t, p, "static", map[string]string{HookTargetHeader: "1001", "X-GitHub-Hook-Installation-Target-Type": "integration"}, body); err == nil {
		t.Fatalf("a delivery verified with the static secret while the store was unreachable = %v", events)
	}
}

func TestEveryInstallationActionWithoutARepositoryIsAGrantEvent(t *testing.T) {
	p := &Provider{Secret: "static", AppSecret: heldApp1001}
	cases := []struct {
		event, action, body string
	}{
		{"installation", "deleted", `{"action":"deleted","installation":{"id":77,"app_id":1001,"account":{"id":5}}}`},
		{"installation", "suspend", `{"action":"suspend","installation":{"id":77,"app_id":1001,"account":{"id":5}}}`},
		{"installation", "unsuspend", `{"action":"unsuspend","installation":{"id":77,"app_id":1001,"account":{"id":5}}}`},
		{"installation_repositories", "added", `{"action":"added","installation":{"id":77,"app_id":1001,"account":{"id":5}},"repositories_added":[{"id":42,"full_name":"acme/app"}]}`},
		{"installation_repositories", "removed", `{"action":"removed","installation":{"id":77,"app_id":1001,"account":{"id":5}},"repositories_removed":[{"id":42,"full_name":"acme/app"}]}`},
	}
	for _, c := range cases {
		body := []byte(c.body)
		r := httptest.NewRequest("POST", "/webhooks/forge/github", bytes.NewReader(body))
		r.Header.Set("X-GitHub-Event", c.event)
		r.Header.Set("X-GitHub-Delivery", "d-"+c.action)
		r.Header.Set(HookTargetHeader, "1001")
		r.Header.Set("X-GitHub-Hook-Installation-Target-Type", "integration")
		r.Header.Set("X-Hub-Signature-256", grantSigned("app-secret", body))
		events, err := p.ParseWebhook(r)
		if err != nil || len(events) != 1 || events[0].Kind != provider.ForgeGrantChanged || events[0].Grant == nil {
			t.Fatalf("%s %s: events = %+v, %v; a delivery without repository must not be dropped", c.event, c.action, events, err)
		}
		g := events[0].Grant
		if g.Event != c.event || g.Action != c.action || g.InstallationID != "77" {
			t.Fatalf("%s %s: grant = %+v", c.event, c.action, g)
		}
		if c.action == "removed" && (len(g.RemovedIDs) != 1 || g.RemovedIDs[0] != "42") {
			t.Fatalf("removed ids = %v", g.RemovedIDs)
		}
	}
}

func TestADeliveryWhoseSignatureDoesNotMatchItsNamedAppIsRefusedBeforeItsPayloadIsRead(t *testing.T) {
	p := &Provider{Secret: "static", AppSecret: heldApp1001}
	r := httptest.NewRequest("POST", "/webhooks/forge/github", bytes.NewReader([]byte(`not json at all`)))
	r.Header.Set("X-GitHub-Event", "installation")
	r.Header.Set("X-GitHub-Delivery", "d1")
	r.Header.Set(HookTargetHeader, "1001")
	r.Header.Set("X-GitHub-Hook-Installation-Target-Type", "integration")
	r.Header.Set("X-Hub-Signature-256", grantSigned("wrong", []byte(`not json at all`)))
	if _, err := p.ParseWebhook(r); err == nil || err.Error() != "invalid webhook signature" {
		t.Fatalf("err = %v, want the signature refusal before any parse", err)
	}
}
