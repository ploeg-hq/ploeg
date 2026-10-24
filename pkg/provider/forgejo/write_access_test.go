package forgejo

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/provider"
)

func permissionForge(t *testing.T, answers map[string]string) *Provider {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "token tok" {
			t.Errorf("missing token auth: %q", r.Header.Get("Authorization"))
		}
		answer, ok := answers[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		if answer == "forbidden" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		_, _ = w.Write([]byte(`{"permission":"` + answer + `"}`))
	}))
	t.Cleanup(srv.Close)
	return &Provider{BaseURL: srv.URL, Token: "tok"}
}

func TestHasWriteAccessCountsWriteAdminAndOwnerOnly(t *testing.T) {
	p := permissionForge(t, map[string]string{
		"/api/v1/repos/webgrip/app/collaborators/maintainer/permission": "admin",
		"/api/v1/repos/webgrip/app/collaborators/owner/permission":      "owner",
		"/api/v1/repos/webgrip/app/collaborators/writer/permission":     "write",
		"/api/v1/repos/webgrip/app/collaborators/reader/permission":     "read",
		"/api/v1/repos/webgrip/app/collaborators/nobody/permission":     "none",
		"/api/v1/repos/webgrip/app/collaborators/hidden/permission":     "forbidden",
	})
	ctx := context.Background()
	for login, want := range map[string]bool{"maintainer": true, "owner": true, "writer": true, "reader": false, "nobody": false, "stranger": false} {
		got, err := provider.HasWriteAccess(ctx, p, "webgrip/app", login)
		if err != nil {
			t.Fatalf("%s: %v", login, err)
		}
		if got != want {
			t.Errorf("%s: write access = %v, want %v", login, got, want)
		}
	}
	if _, err := provider.HasWriteAccess(ctx, p, "webgrip/app", "hidden"); err == nil {
		t.Error("a permission Forgejo refused to show read as an answer")
	}
}
