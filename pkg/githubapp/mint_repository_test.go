package githubapp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func pinnedServer(t *testing.T, revoked *int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "POST /app/installations/77/access_tokens":
			var body struct {
				Repositories  []string          `json:"repositories"`
				RepositoryIDs []int64           `json:"repository_ids"`
				Permissions   map[string]string `json:"permissions"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			if len(body.Repositories) > 0 {
				t.Errorf("a pinned mint sent repository names %v; GitHub reads them against the installation's own account", body.Repositories)
			}
			full := map[int64]string{301: "other/app", 302: "acme/app"}[body.RepositoryIDs[0]]
			if full == "" {
				w.WriteHeader(http.StatusUnprocessableEntity)
				return
			}
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{"token": "ghs_pinned", "permissions": body.Permissions,
				"repositories": []map[string]any{{"id": body.RepositoryIDs[0], "full_name": full}}})
		case "DELETE /installation/token":
			*revoked++
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestAPinnedMinterMintsByRepositoryIDForThatRepositoryAlone(t *testing.T) {
	revoked := 0
	m := testMinter(t, pinnedServer(t, &revoked).URL)
	m.Installation = 77
	ctx := context.Background()
	tok, err := m.MintRepository(ctx, "other", "app", 301, WriterPermissions())
	if err != nil || tok.Value != "ghs_pinned" {
		t.Fatalf("mint by id = %+v, %v", tok, err)
	}
	if _, err := m.MintRepository(ctx, "other", "app", 302, WriterPermissions()); !errors.Is(err, ErrNotGranted) || revoked != 1 {
		t.Fatalf("an id that is another repository = %v (revoked %d), want ErrNotGranted and the token revoked", err, revoked)
	}
	if _, err := m.MintRepository(ctx, "other", "app", 999, WriterPermissions()); !errors.Is(err, ErrNotGranted) {
		t.Fatalf("an id outside the installation = %v", err)
	}
	if _, err := m.Mint(ctx, "other", "app", WriterPermissions()); err == nil {
		t.Fatal("a pinned minter minted by a bare repository name")
	}
}
