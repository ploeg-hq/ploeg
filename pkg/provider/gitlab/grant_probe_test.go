package gitlab

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/provider"
)

func TestAGitLabGrantNeedsDeveloperAccessAndTheAPIScope(t *testing.T) {
	scope := []string{"api"}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /oauth/token/info", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"scope": scope})
	})
	mux.HandleFunc("GET /api/v4/projects/{id}", func(w http.ResponseWriter, r *http.Request) {
		switch r.PathValue("id") {
		case "group/sub/app":
			_ = json.NewEncoder(w).Encode(map[string]any{"id": 9, "path_with_namespace": "group/sub/app", "default_branch": "main",
				"permissions": map[string]any{"project_access": map[string]int{"access_level": 30}}})
		case "group/guest":
			_ = json.NewEncoder(w).Encode(map[string]any{"id": 10, "path_with_namespace": "group/guest",
				"permissions": map[string]any{"project_access": map[string]int{"access_level": 10}}})
		default:
			http.NotFound(w, r)
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	inst := provider.ConnectionInstance{URL: srv.URL}
	cred := provider.ConnectionCredential{Token: "t"}
	ctx := context.Background()
	if g, err := (Connector{}).ProbeRepositoryGrant(ctx, inst, cred, "group/sub/app"); err != nil || !g.CanWrite || g.ID != "9" {
		t.Fatalf("developer with api = %+v, %v", g, err)
	}
	if g, err := (Connector{}).ProbeRepositoryGrant(ctx, inst, cred, "group/guest"); err != nil || g.CanWrite {
		t.Fatalf("guest = %+v, %v", g, err)
	}
	scope = []string{"read_api", "read_repository"}
	if g, err := (Connector{}).ProbeRepositoryGrant(ctx, inst, cred, "group/sub/app"); err != nil || g.CanWrite {
		t.Fatalf("developer through a read-only token = %+v, %v; the token cannot push", g, err)
	}
	if _, err := (Connector{}).ProbeRepositoryGrant(ctx, inst, cred, "group/none"); !errors.Is(err, provider.ErrRepositoryNotFound) {
		t.Fatalf("unknown project = %v", err)
	}
}
