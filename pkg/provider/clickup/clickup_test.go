package clickup

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/provider"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

// The provider is the only place ClickUp's REST dialect is allowed to exist,
// so these tests are what keep the rest of the codebase honest about it.
// External services are faked with httptest and never need network.

func sign(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

func post(t *testing.T, p *Provider, body any) ([]provider.TrackerEvent, error) {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, "/webhooks/tracker/clickup", strings.NewReader(string(b)))
	if p.Secret == "" {
		p.Secret = "s3cret"
	}
	r.Header.Set("X-Signature", sign(p.Secret, b))
	return p.ParseWebhook(r)
}

func TestParseWebhookRejectsBadSignature(t *testing.T) {
	p := &Provider{Secret: "s3cret"}
	b, _ := json.Marshal(map[string]any{"event": "taskUpdated", "task_id": "abc"})
	r := httptest.NewRequest(http.MethodPost, "/webhooks/tracker/clickup", strings.NewReader(string(b)))
	r.Header.Set("X-Signature", sign("wrong", b))
	if _, err := p.ParseWebhook(r); err == nil {
		t.Fatal("want an error for a signature over the wrong secret")
	}
}

func TestParseWebhookRejectsEveryDeliveryWithoutAConfiguredSecret(t *testing.T) {
	p := &Provider{Token: "api-token", DefaultTeam: "default"}
	r := httptest.NewRequest(http.MethodPost, "/webhooks/tracker/clickup", strings.NewReader(`{"event":"taskAssigneeUpdated","task_id":"t1"}`))
	if _, err := p.ParseWebhook(r); err == nil {
		t.Fatal("an unsigned delivery was accepted by a provider configured with only an API token")
	}
}

func TestParseWebhookRejectsAMissingOrMalformedSignature(t *testing.T) {
	p := &Provider{Secret: "s3cret"}
	body := `{"event":"taskAssigneeUpdated","task_id":"t1"}`
	for _, sig := range []string{"", "not-hex", sign("s3cret", []byte(body))[:10]} {
		r := httptest.NewRequest(http.MethodPost, "/webhooks/tracker/clickup", strings.NewReader(body))
		if sig != "" {
			r.Header.Set("X-Signature", sig)
		}
		if _, err := p.ParseWebhook(r); err == nil {
			t.Errorf("signature %q was accepted", sig)
		}
	}
}

func TestParseWebhookAssignedRoutesToTeam(t *testing.T) {
	p := &Provider{
		Secret:      "s3cret",
		DefaultTeam: "default",
		TeamMap:     map[string]string{"builder": "silver"},
	}
	evs, err := post(t, p, map[string]any{
		"event":   "taskAssigneeUpdated",
		"task_id": "86a1b2c3",
		"history_items": []any{
			map[string]any{"field": "assignee_add", "after": map[string]any{"username": "Builder"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 1 {
		t.Fatalf("want 1 event, got %d", len(evs))
	}
	if evs[0].Kind != provider.TrackerAssigned {
		t.Errorf("kind = %v, want assigned", evs[0].Kind)
	}
	if evs[0].ExternalID != "86a1b2c3" {
		t.Errorf("external id = %q", evs[0].ExternalID)
	}
	// Case must not decide routing: the map is lowercased on lookup.
	if evs[0].Team != "silver" {
		t.Errorf("team = %q, want silver (TeamMap lookup is case-insensitive)", evs[0].Team)
	}
}

func TestParseWebhookUnknownAssigneeGetsDefaultTeam(t *testing.T) {
	p := &Provider{DefaultTeam: "default", TeamMap: map[string]string{"builder": "silver"}}
	evs, err := post(t, p, map[string]any{
		"event":   "taskAssigneeUpdated",
		"task_id": "t1",
		"history_items": []any{
			map[string]any{"field": "assignee_add", "after": map[string]any{"username": "someone-else"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 1 || evs[0].Team != "default" {
		t.Fatalf("want the default team, got %+v", evs)
	}
}

// One ClickUp event covers assignment in both directions; only the history
// item's field separates them.
func TestParseWebhookUnassign(t *testing.T) {
	p := &Provider{DefaultTeam: "default"}
	evs, err := post(t, p, map[string]any{
		"event":   "taskAssigneeUpdated",
		"task_id": "t1",
		"history_items": []any{
			map[string]any{"field": "assignee_rem", "before": map[string]any{"username": "builder"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 1 || evs[0].Kind != provider.TrackerUnassigned {
		t.Fatalf("want unassigned, got %+v", evs)
	}
}

func TestParseWebhookUpdated(t *testing.T) {
	p := &Provider{DefaultTeam: "default"}
	for _, event := range []string{"taskUpdated", "taskStatusUpdated", "taskPriorityUpdated"} {
		evs, err := post(t, p, map[string]any{"event": event, "task_id": "t1"})
		if err != nil {
			t.Fatalf("%s: %v", event, err)
		}
		if len(evs) != 1 || evs[0].Kind != provider.TrackerUpdated {
			t.Errorf("%s: want updated, got %+v", event, evs)
		}
	}
}

func TestParseWebhookUnhandledEventIsDropped(t *testing.T) {
	p := &Provider{DefaultTeam: "default"}
	evs, err := post(t, p, map[string]any{"event": "taskCommentPosted", "task_id": "t1"})
	if err != nil {
		t.Fatalf("providers subscribe wider than the core consumes: %v", err)
	}
	if len(evs) != 0 {
		t.Errorf("want no events, got %+v", evs)
	}
}

func TestParseWebhookWithoutTaskErrors(t *testing.T) {
	p := &Provider{}
	if _, err := post(t, p, map[string]any{"event": "taskUpdated"}); err == nil {
		t.Fatal("a payload with no task is malformed, not noise")
	}
}

func newTaskServer(t *testing.T, body string) (*httptest.Server, *string) {
	t.Helper()
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv, &gotAuth
}

// ClickUp wants the raw token. A "Bearer " prefix 401s, so this is a
// regression test as much as a wiring one.
func TestFetchItemSendsRawToken(t *testing.T) {
	srv, gotAuth := newTaskServer(t, `{"id":"t1","name":"x","list":{"id":"9"}}`)
	p := &Provider{BaseURL: srv.URL, Token: "pk_123", HC: srv.Client()}
	if _, err := p.FetchItem(context.Background(), "t1"); err != nil {
		t.Fatal(err)
	}
	if *gotAuth != "pk_123" {
		t.Errorf("Authorization = %q, want the bare token with no Bearer prefix", *gotAuth)
	}
}

func TestFetchItemMirrorsTask(t *testing.T) {
	srv, _ := newTaskServer(t, `{
		"id":"t1","name":"Fix the thing","description":"do it",
		"date_updated":"1756100000000",
		"priority":{"id":"2","priority":"high"},
		"list":{"id":"901","name":"Ploeg Test"}
	}`)
	p := &Provider{BaseURL: srv.URL, Token: "pk", HC: srv.Client()}
	item, err := p.FetchItem(context.Background(), "t1")
	if err != nil {
		t.Fatal(err)
	}
	if item.Title != "Fix the thing" || item.Description != "do it" {
		t.Errorf("unexpected mirror %+v", item)
	}
	if item.Revision != "1756100000000" {
		t.Errorf("revision = %q", item.Revision)
	}
	if item.ExternalScope != "901" {
		t.Errorf("scope = %q, want the List id", item.ExternalScope)
	}
	if item.Provider != "clickup" {
		t.Errorf("provider = %q", item.Provider)
	}
}

// description may be empty where text_content is not.
func TestFetchItemFallsBackToTextContent(t *testing.T) {
	srv, _ := newTaskServer(t, `{"id":"t1","name":"n","text_content":"plain body","list":{"id":"1"}}`)
	p := &Provider{BaseURL: srv.URL, Token: "pk", HC: srv.Client()}
	item, err := p.FetchItem(context.Background(), "t1")
	if err != nil {
		t.Fatal(err)
	}
	if item.Description != "plain body" {
		t.Errorf("description = %q, want the text_content fallback", item.Description)
	}
}

// ClickUp counts down (1 = urgent); Ploeg counts up. Getting this backwards
// would quietly run the least important work first.
func TestPriorityIsInverted(t *testing.T) {
	for _, tc := range []struct {
		body string
		want int
	}{
		{`{"id":"t","priority":{"id":"1","priority":"urgent"},"list":{"id":"1"}}`, 4},
		{`{"id":"t","priority":{"id":"2","priority":"high"},"list":{"id":"1"}}`, 3},
		{`{"id":"t","priority":{"id":"3","priority":"normal"},"list":{"id":"1"}}`, 2},
		{`{"id":"t","priority":{"id":"4","priority":"low"},"list":{"id":"1"}}`, 1},
		{`{"id":"t","list":{"id":"1"}}`, 0},
		// An unrecognised id falls back to the label rather than to urgent.
		{`{"id":"t","priority":{"id":"99","priority":"high"},"list":{"id":"1"}}`, 3},
	} {
		srv, _ := newTaskServer(t, tc.body)
		p := &Provider{BaseURL: srv.URL, Token: "pk", HC: srv.Client()}
		item, err := p.FetchItem(context.Background(), "t")
		if err != nil {
			t.Fatal(err)
		}
		if item.Priority != tc.want {
			t.Errorf("%s -> priority %d, want %d", tc.body, item.Priority, tc.want)
		}
	}
}

func TestFetchItemUnconfiguredErrors(t *testing.T) {
	p := &Provider{}
	if _, err := p.FetchItem(context.Background(), "t1"); err == nil {
		t.Fatal("unconfigured FetchItem must error so the caller falls back to the webhook snapshot")
	}
}

// Write-backs degrade to a no-op, never to a failed run.
func TestWriteBacksNoOpWhenUnconfigured(t *testing.T) {
	p := &Provider{}
	if err := p.Comment(context.Background(), "t1", "hi"); err != nil {
		t.Errorf("Comment: %v", err)
	}
	if err := p.SetStatus(context.Background(), "t1", work.StateDone); err != nil {
		t.Errorf("SetStatus: %v", err)
	}
}

func TestCommentPostsToTask(t *testing.T) {
	var gotPath, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		_, _ = io.WriteString(w, `{}`)
	}))
	defer srv.Close()
	p := &Provider{BaseURL: srv.URL, Token: "pk", HC: srv.Client()}
	if err := p.Comment(context.Background(), "t1", "the MR is up"); err != nil {
		t.Fatal(err)
	}
	if gotPath != "/task/t1/comment" {
		t.Errorf("path = %q", gotPath)
	}
	if !strings.Contains(gotBody, `"comment_text":"the MR is up"`) {
		t.Errorf("body = %q", gotBody)
	}
	if !strings.Contains(gotBody, `"notify_all":false`) {
		t.Errorf("a bot narrating its own run must not mail every watcher: %q", gotBody)
	}
}

// needs_human and stale are NOT done — marking them done hides the item from
// the board that raised it.
func TestSetStatusOnlyWritesForDone(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		_, _ = io.WriteString(w, `{}`)
	}))
	defer srv.Close()
	p := &Provider{BaseURL: srv.URL, Token: "pk", DoneStatus: "complete", HC: srv.Client()}

	for _, st := range []work.State{work.StateNeedsHuman, work.StateStale, work.StateQueued} {
		if err := p.SetStatus(context.Background(), "t1", st); err != nil {
			t.Fatalf("%s: %v", st, err)
		}
	}
	if calls != 0 {
		t.Fatalf("non-terminal states must not touch the board, got %d calls", calls)
	}
	if err := p.SetStatus(context.Background(), "t1", work.StateDone); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("done must write exactly once, got %d", calls)
	}
}

// ClickUp statuses are per-List custom strings. Without a configured name
// there is nothing safe to send, and guessing would 400 or move the task
// somewhere nobody chose.
func TestSetStatusSkipsWithoutDoneStatus(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		_, _ = io.WriteString(w, `{}`)
	}))
	defer srv.Close()
	p := &Provider{BaseURL: srv.URL, Token: "pk", HC: srv.Client()}
	if err := p.SetStatus(context.Background(), "t1", work.StateDone); err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Fatalf("want no call without DoneStatus, got %d", calls)
	}
}

func TestListsByName(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"lists":[{"id":"901","name":"Ploeg Test"},{"id":"902","name":"Other"}]}`)
	}))
	defer srv.Close()
	p := &Provider{BaseURL: srv.URL, Token: "pk", HC: srv.Client()}
	got, err := p.ListsByName(context.Background(), "42")
	if err != nil {
		t.Fatal(err)
	}
	if got["Ploeg Test"] != "901" || got["Other"] != "902" {
		t.Errorf("unexpected map %v", got)
	}
}

func TestListsByNameNeedsSpace(t *testing.T) {
	p := &Provider{Token: "pk"}
	if _, err := p.ListsByName(context.Background(), ""); err == nil {
		t.Fatal("want an error without a space id")
	}
}

func TestName(t *testing.T) {
	if (&Provider{}).Name() != "clickup" {
		t.Fatal("Name must stay stable: it is the webhook route and the map key")
	}
}
