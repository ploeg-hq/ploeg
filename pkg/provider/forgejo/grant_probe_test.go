package forgejo

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/provider"
)

func TestAForgejoGrantNeedsPushAndATokenScopedToWriteRepositories(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/repos/{owner}/{repo}", func(w http.ResponseWriter, r *http.Request) {
		switch r.PathValue("repo") {
		case "app":
			_ = json.NewEncoder(w).Encode(map[string]any{"id": 5, "full_name": "team/app", "default_branch": "main", "permissions": map[string]bool{"pull": true, "push": true}})
		case "docs":
			_ = json.NewEncoder(w).Encode(map[string]any{"id": 6, "full_name": "team/docs", "permissions": map[string]bool{"pull": true}})
		default:
			http.NotFound(w, r)
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	cred := provider.ConnectionCredential{Token: "t"}
	ctx := context.Background()
	inst := provider.ConnectionInstance{URL: srv.URL}
	if g, err := (Connector{}).ProbeRepositoryGrant(ctx, inst, cred, "team/app"); err != nil || !g.CanWrite || g.ID != "5" {
		t.Fatalf("push with an unscoped token = %+v, %v", g, err)
	}
	if g, err := (Connector{}).ProbeRepositoryGrant(ctx, inst, cred, "team/docs"); err != nil || g.CanWrite {
		t.Fatalf("pull only = %+v, %v", g, err)
	}
	readOnly := provider.ConnectionInstance{URL: srv.URL, OAuth: &provider.OAuthApp{Scopes: []string{"read:repository", "read:user"}}}
	if g, err := (Connector{}).ProbeRepositoryGrant(ctx, readOnly, cred, "team/app"); err != nil || g.CanWrite {
		t.Fatalf("push through a read:repository token = %+v, %v; the token cannot push", g, err)
	}
	if _, err := (Connector{}).ProbeRepositoryGrant(ctx, inst, cred, "team/none"); !errors.Is(err, provider.ErrRepositoryNotFound) {
		t.Fatalf("unknown repository = %v", err)
	}
}
