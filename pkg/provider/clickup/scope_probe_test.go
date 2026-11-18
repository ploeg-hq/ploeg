package clickup

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/provider"
)

func scopeAPI(t *testing.T) *Provider {
	t.Helper()
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reply := func(v any) { _ = json.NewEncoder(w).Encode(v) }
		switch r.URL.Path {
		case "/list/1":
			reply(map[string]any{"id": 1, "name": "Board", "permission_level": "create", "statuses": []map[string]any{{"status": "open"}, {"status": "In Review"}}})
		case "/list/1/member":
			reply(map[string]any{"members": []map[string]any{{"username": "agent", "email": "agent@example.test"}}})
		case "/list/2":
			reply(map[string]any{"id": "2", "name": "Read only", "permission_level": "read"})
		case "/list/2/member":
			http.Error(w, "nope", http.StatusForbidden)
		case "/list/3":
			http.Error(w, `{"err":"Team not authorized"}`, http.StatusUnauthorized)
		case "/team/W/space":
			reply(map[string]any{"spaces": []map[string]any{{"id": "9"}}})
		case "/space/9/list":
			reply(map[string]any{"lists": []map[string]any{{"id": "1", "name": "Board"}, {"id": "4", "name": "Twice"}}})
		case "/space/9/folder":
			reply(map[string]any{"folders": []map[string]any{{"lists": []map[string]any{{"id": "5", "name": "Twice"}}}}})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(api.Close)
	return &Provider{BaseURL: api.URL, Token: "pk", Workspace: "W"}
}

func TestProbeScopeReportsWhatTheTokenCanDo(t *testing.T) {
	p := scopeAPI(t)
	ctx := context.Background()
	got, err := p.ProbeScope(ctx, "1")
	if err != nil || !got.CanRead || !got.CanWrite || got.Name != "Board" || len(got.Members) != 2 || len(got.Statuses) != 2 {
		t.Fatalf("readable List = %+v, %v", got, err)
	}
	got, err = p.ProbeScope(ctx, "2")
	if err != nil || !got.CanRead || got.CanWrite || got.Members != nil {
		t.Fatalf("read-only List = %+v, %v", got, err)
	}
	got, err = p.ProbeScope(ctx, "3")
	if err != nil || got.CanRead {
		t.Fatalf("unreadable List = %+v, %v", got, err)
	}
	if _, err = p.ProbeScope(ctx, "404"); !errors.Is(err, provider.ErrScopeNotFound) {
		t.Fatalf("unknown List: %v", err)
	}
}

func TestScopeIDByNameFindsOneListOrRefusesToGuess(t *testing.T) {
	p := scopeAPI(t)
	ctx := context.Background()
	if id, err := p.ScopeIDByName(ctx, "Board"); err != nil || id != "1" {
		t.Fatalf("Board = %q, %v", id, err)
	}
	if _, err := p.ScopeIDByName(ctx, "Twice"); !errors.Is(err, provider.ErrScopeAmbiguous) {
		t.Fatalf("Twice: %v", err)
	}
	if _, err := p.ScopeIDByName(ctx, "Absent"); !errors.Is(err, provider.ErrScopeNotFound) {
		t.Fatalf("Absent: %v", err)
	}
}

func TestProbeThenWebhookCheckReadsTheListOnceAndANameResolvesOnce(t *testing.T) {
	calls := map[string]int{}
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls[r.URL.Path]++
		reply := func(v any) { _ = json.NewEncoder(w).Encode(v) }
		switch r.URL.Path {
		case "/list/1":
			reply(map[string]any{"id": "1", "name": "Board", "permission_level": "create", "folder": map[string]any{"id": "8"}, "space": map[string]any{"id": "9"}})
		case "/list/1/member":
			reply(map[string]any{"members": []map[string]any{{"username": "agent"}}})
		case "/team/W/webhook":
			reply(map[string]any{"webhooks": []any{}})
		case "/team/W/space":
			reply(map[string]any{"spaces": []map[string]any{{"id": "9"}}})
		case "/space/9/list":
			reply(map[string]any{"lists": []map[string]any{{"id": "1", "name": "Board"}}})
		case "/space/9/folder":
			reply(map[string]any{"folders": []any{}})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(api.Close)
	p := &Provider{BaseURL: api.URL, Token: "pk", Workspace: "W"}
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		if _, err := p.ScopeIDByName(ctx, "Board"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := p.ProbeScope(ctx, "1"); err != nil {
		t.Fatal(err)
	}
	p.CheckWebhooks(ctx, []string{"1"}, "")
	if calls["/list/1"] != 1 || calls["/list/1/member"] != 1 || calls["/team/W/space"] != 1 {
		t.Fatalf("calls = %v, want the List, its members and the Spaces read once", calls)
	}
}

func TestADeliverySignedWithARegisteredListWebhookSecretIsAccepted(t *testing.T) {
	p := &Provider{Secret: "workspace-secret", WebhookSecret: func(_ context.Context, id string) (string, bool) {
		return "list-secret", id == "wh-7"
	}}
	body := []byte(`{"event":"taskUpdated","task_id":"t1","webhook_id":"wh-7","history_items":[]}`)
	for secret, want := range map[string]bool{"list-secret": true, "workspace-secret": true, "forged": false} {
		r := httptest.NewRequest(http.MethodPost, WebhookPath, bytes.NewReader(body))
		r.Header.Set("X-Signature", sign(secret, body))
		if _, err := p.ParseWebhook(r); (err == nil) != want {
			t.Errorf("signed with %s: err = %v, want accepted %v", secret, err, want)
		}
	}
}
