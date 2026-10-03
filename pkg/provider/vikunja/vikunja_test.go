package vikunja

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/provider"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

const assignedBody = `{
  "event_name": "task.assignee.created",
  "time": "2026-07-23T10:00:00Z",
  "data": {
    "task": {"id": 42, "title": "Wire the lease manager", "priority": 3},
    "assignee": {"username": "Crew-Alpha"}
  }
}`

func sign(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

const fixtureSecret = "s3cret"

func signedHook(body string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, WebhookPath, strings.NewReader(body))
	r.Header.Set("X-Vikunja-Signature", sign(fixtureSecret, []byte(body)))
	return r
}

func TestParseWebhookAssigned(t *testing.T) {
	p := &Provider{Secret: "s3cret", DefaultTeam: "default", TeamMap: map[string]string{"crew-alpha": "alpha"}}

	body := []byte(assignedBody)
	r := httptest.NewRequest("POST", "/webhooks/tracker/vikunja", bytes.NewReader(body))
	r.Header.Set("X-Vikunja-Signature", sign("s3cret", body))

	events, err := p.ParseWebhook(r)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}
	ev := events[0]
	if ev.Kind != provider.TrackerAssigned || ev.ExternalID != "42" || ev.Team != "alpha" {
		t.Fatalf("unexpected event: %+v", ev)
	}
	if ev.Item == nil || ev.Item.Title != "Wire the lease manager" || ev.Item.Priority != 3 {
		t.Fatalf("unexpected item snapshot: %+v", ev.Item)
	}
	// The fixture predates project_id; a payload without it must still parse
	// and simply carry no scope (unresolved target ⇒ env fallback).
	if ev.Scope.ID != "" || ev.Item.ExternalScope != "" {
		t.Errorf("payload without project_id should carry no scope, got %+v", ev.Scope)
	}
}

// project_id is the scope the core resolves a Work Target from. Vikunja sends
// it on task events (verified against the live instance: tasks.project_id, and
// the smoke-test task 611 sits on project 11); encoding/json silently dropped
// it before this field existed.
func TestParseWebhookUnassigned(t *testing.T) {
	p := &Provider{Secret: "s3cret", DefaultTeam: "default", TeamMap: map[string]string{"crew-alpha": "alpha"}}
	body := []byte(strings.NewReplacer("task.assignee.created", "task.assignee.deleted", "copper", "Crew-Alpha").Replace(assignedWithProjectBody))
	r := httptest.NewRequest("POST", "/webhooks/tracker/vikunja", bytes.NewReader(body))
	r.Header.Set("X-Vikunja-Signature", sign("s3cret", body))
	events, err := p.ParseWebhook(r)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Kind != provider.TrackerUnassigned || events[0].ExternalID != "611" ||
		events[0].Team != "alpha" || events[0].Scope.ID != "11" {
		t.Fatalf("unexpected unassignment: %+v", events)
	}
}

const assignedWithProjectBody = `{
  "event_name": "task.assignee.created",
  "time": "2026-07-29T10:00:00Z",
  "data": {
    "task": {"id": 611, "project_id": 11, "title": "SMOKE", "priority": 2},
    "assignee": {"username": "copper"}
  }
}`

func TestParseWebhookCarriesProjectScope(t *testing.T) {
	p := &Provider{Secret: "s3cret", DefaultTeam: "default", TeamMap: map[string]string{"copper": "copper"}}
	body := []byte(assignedWithProjectBody)
	r := httptest.NewRequest("POST", "/webhooks/tracker/vikunja", bytes.NewReader(body))
	r.Header.Set("X-Vikunja-Signature", sign("s3cret", body))

	events, err := p.ParseWebhook(r)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}
	ev := events[0]
	if ev.Scope.Kind != "project" || ev.Scope.ID != "11" {
		t.Errorf("scope = %+v, want {project 11}", ev.Scope)
	}
	if ev.Item.ExternalScope != "11" {
		t.Errorf("item.ExternalScope = %q, want 11", ev.Item.ExternalScope)
	}
	// The adapter reports the vendor's container id and nothing else — it must
	// never learn what a repository is (R7).
	if ev.Scope.Name != "" {
		t.Errorf("scope name should stay empty; it is audit-only, never a routing key")
	}
}

func TestParseWebhookRejectsBadSignature(t *testing.T) {
	p := &Provider{Secret: "s3cret"}
	body := []byte(assignedBody)
	r := httptest.NewRequest("POST", "/", bytes.NewReader(body))
	r.Header.Set("X-Vikunja-Signature", sign("wrong-secret", body))
	if _, err := p.ParseWebhook(r); err == nil {
		t.Fatal("expected signature rejection")
	}
}

func TestParseWebhookRejectsEveryDeliveryWithoutAConfiguredSecret(t *testing.T) {
	p := &Provider{DefaultTeam: "default"}
	r := httptest.NewRequest(http.MethodPost, WebhookPath, strings.NewReader(assignedBody))
	if _, err := p.ParseWebhook(r); err == nil {
		t.Fatal("an unsigned delivery was accepted by a provider without a secret")
	}
}

func TestParseWebhookRejectsAMissingOrMalformedSignature(t *testing.T) {
	p := &Provider{Secret: fixtureSecret}
	for _, sig := range []string{"", "not-hex", sign(fixtureSecret, []byte(assignedBody))[:10]} {
		r := httptest.NewRequest(http.MethodPost, WebhookPath, strings.NewReader(assignedBody))
		if sig != "" {
			r.Header.Set("X-Vikunja-Signature", sig)
		}
		if _, err := p.ParseWebhook(r); err == nil {
			t.Errorf("signature %q was accepted", sig)
		}
	}
}

func TestParseWebhookUnhandledEventDropped(t *testing.T) {
	p := &Provider{Secret: fixtureSecret, DefaultTeam: "alpha"}
	events, err := p.ParseWebhook(signedHook(`{"event_name":"task.comment.created","data":{"task":{"id":7,"title":"x"}}}`))
	if err != nil || events != nil {
		t.Fatalf("want (nil, nil), got (%v, %v)", events, err)
	}
}

// --- write-backs (backlog #31) ---------------------------------------------

// Unconfigured must degrade to "the board is not updated", never to "the run
// fails": a deployment without a tracker credential still has to finish runs.
func TestWriteBacks_NoOpWithoutCredentials(t *testing.T) {
	p := &Provider{Log: slog.New(slog.DiscardHandler)}
	if err := p.Comment(context.Background(), "585", "hi"); err != nil {
		t.Errorf("Comment without credentials returned %v, want nil", err)
	}
	if err := p.SetStatus(context.Background(), "585", work.StateDone); err != nil {
		t.Errorf("SetStatus without credentials returned %v, want nil", err)
	}
	if _, err := p.FetchItem(context.Background(), "585"); err == nil {
		t.Error("FetchItem without credentials must error so the caller falls back to the webhook snapshot")
	}
}

// Comment creation is PUT, not POST (docs/ops/board.md) — a POST here does
// something else entirely and the write silently fails to appear.
func TestComment_UsesPUT(t *testing.T) {
	var method, path, body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, path = r.Method, r.URL.Path
		b, _ := io.ReadAll(r.Body)
		body = string(b)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	p := &Provider{BaseURL: srv.URL, Token: "tok"}
	if err := p.Comment(context.Background(), "585", "PR is up: https://forgejo/pr/7"); err != nil {
		t.Fatalf("Comment: %v", err)
	}
	if method != http.MethodPut {
		t.Errorf("method = %s, want PUT (Vikunja creates comments with PUT)", method)
	}
	if path != "/tasks/585/comments" {
		t.Errorf("path = %q", path)
	}
	if !strings.Contains(body, "forgejo/pr/7") {
		t.Errorf("body lost the PR link: %q", body)
	}
}

func TestComment_SurfacesFailureWithoutLeakingTheToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"message":"missing or malformed token"}`))
	}))
	defer srv.Close()

	err := (&Provider{BaseURL: srv.URL, Token: "s3cret"}).Comment(context.Background(), "585", "x")
	if err == nil {
		t.Fatal("a 401 was reported as success")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("error does not name the status: %v", err)
	}
	if strings.Contains(err.Error(), "s3cret") {
		t.Errorf("error leaked the token: %v", err)
	}
}

// needs_human is NOT done. Marking it done would hide the item from the very
// board that has to act on it.
func TestSetStatus_OnlyDoneIsWritten(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	p := &Provider{BaseURL: srv.URL, Token: "tok"}

	for _, st := range []work.State{work.StateNeedsHuman, work.StateStale, work.StateQueued} {
		if err := p.SetStatus(context.Background(), "585", st); err != nil {
			t.Fatalf("SetStatus(%s): %v", st, err)
		}
	}
	if calls != 0 {
		t.Errorf("%d write(s) for non-terminal states; needs_human must not be marked done", calls)
	}
	if err := p.SetStatus(context.Background(), "585", work.StateDone); err != nil {
		t.Fatalf("SetStatus(done): %v", err)
	}
	if calls != 1 {
		t.Errorf("done wrote %d times, want 1", calls)
	}
}

func TestFetchItem_ReadsAuthoritativeState(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			t.Errorf("missing bearer auth: %q", r.Header.Get("Authorization"))
		}
		_, _ = w.Write([]byte(`{"id":585,"title":"fix the thing","description":"<p>d</p>","priority":3,"project_id":11,"updated":"2026-07-29T10:00:00Z","created":"2026-07-01T09:30:00Z"}`))
	}))
	defer srv.Close()

	item, err := (&Provider{BaseURL: srv.URL, Token: "tok"}).FetchItem(context.Background(), "585")
	if err != nil {
		t.Fatalf("FetchItem: %v", err)
	}
	if item.ExternalID != "585" || item.Title != "fix the thing" || item.Priority != 3 {
		t.Errorf("item = %+v", item)
	}
	if item.Revision == "" {
		t.Error("revision not carried; the monotonic gate needs it (backlog #7)")
	}
	if !item.TrackerCreatedAt.Equal(time.Date(2026, 7, 1, 9, 30, 0, 0, time.UTC)) || item.EstimateSeconds != nil {
		t.Errorf("tracker creation %v, estimate %v; Vikunja keeps no estimate", item.TrackerCreatedAt, item.EstimateSeconds)
	}
}

func TestParseWebhookClosedTaskIsAClose(t *testing.T) {
	p := &Provider{Secret: fixtureSecret, DefaultTeam: "default"}
	for _, tc := range []struct {
		task string
		want provider.TrackerEventKind
	}{
		{`{"id": 611, "project_id": 11, "done": true}`, provider.TrackerClosed},
		{`{"id": 611, "project_id": 11, "done": false}`, provider.TrackerUpdated},
	} {
		events, err := p.ParseWebhook(signedHook(`{"event_name": "task.updated", "data": {"task": ` + tc.task + `}}`))
		if err != nil {
			t.Fatal(err)
		}
		if len(events) != 1 || events[0].Kind != tc.want || events[0].ExternalID != "611" {
			t.Fatalf("task %s: got %+v, want one %s event", tc.task, events, tc.want)
		}
	}
}
