package clickup

import (
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
