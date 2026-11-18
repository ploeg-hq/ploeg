package github

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/provider"
)

func grantProbeServer(t *testing.T, perms map[string]string) (provider.ConnectionInstance, provider.ConnectionCredential) {
	t.Helper()
	repos := map[string]map[string]any{
		"/repos/acme/app":      {"id": 101, "full_name": "acme/app", "default_branch": "main"},
		"/repos/public/lib":    {"id": 202, "full_name": "public/lib", "default_branch": "main"},
		"/repos/acme/archived": {"id": 103, "full_name": "acme/archived", "default_branch": "main", "archived": true},
	}
	mux := http.NewServeMux()
	answer := func(w http.ResponseWriter, v any) { _ = json.NewEncoder(w).Encode(v) }
	mux.HandleFunc("GET /app/installations/77", func(w http.ResponseWriter, _ *http.Request) {
		answer(w, map[string]any{"permissions": perms})
	})
	mux.HandleFunc("POST /app/installations/77/access_tokens", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
		answer(w, map[string]any{"token": "ghs_x"})
	})
	mux.HandleFunc("DELETE /installation/token", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	mux.HandleFunc("GET /installation/repositories", func(w http.ResponseWriter, _ *http.Request) {
		answer(w, map[string]any{"total_count": 2, "repositories": []map[string]any{{"id": 101}, {"id": 103}}})
	})
	mux.HandleFunc("GET /repos/{owner}/{repo}", func(w http.ResponseWriter, r *http.Request) {
		repo, ok := repos[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		answer(w, repo)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	pemKey := string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
	return provider.ConnectionInstance{ID: "github", Kind: "github", APIURL: srv.URL}, provider.ConnectionCredential{AppID: "1001", PrivateKey: pemKey, InstallationID: "77"}
}

func TestAGitHubGrantIsTheInstallationsRepositoriesNotWhatItsTokenCanRead(t *testing.T) {
	writer := map[string]string{"contents": "write", "pull_requests": "write", "metadata": "read"}
	inst, cred := grantProbeServer(t, writer)
	ctx := context.Background()
	grant, err := Connector{}.ProbeRepositoryGrant(ctx, inst, cred, "acme/app")
	if err != nil || grant.ID != "101" || !grant.CanWrite || grant.DefaultBranch != "main" {
		t.Fatalf("granted repository = %+v, %v", grant, err)
	}
	if _, err := (Connector{}).ProbeRepositoryGrant(ctx, inst, cred, "public/lib"); !errors.Is(err, provider.ErrRepositoryNotGranted) {
		t.Fatalf("a public repository the token reads but the installation was not granted = %v, want ErrRepositoryNotGranted", err)
	}
	if _, err := (Connector{}).ProbeRepositoryGrant(ctx, inst, cred, "acme/nothing"); !errors.Is(err, provider.ErrRepositoryNotGranted) {
		t.Fatalf("an unknown repository = %v", err)
	}
	reader := map[string]string{"contents": "read", "pull_requests": "read", "metadata": "read"}
	inst, cred = grantProbeServer(t, reader)
	if grant, err := (Connector{}).ProbeRepositoryGrant(ctx, inst, cred, "acme/app"); err != nil || grant.CanWrite {
		t.Fatalf("a read-only installation = %+v, %v", grant, err)
	}
}
