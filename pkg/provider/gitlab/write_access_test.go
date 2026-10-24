package gitlab

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/provider"
)

func TestHasWriteAccessNeedsAnExactActiveDeveloperOrMore(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("PRIVATE-TOKEN") != "tok" {
			t.Errorf("missing PRIVATE-TOKEN: %q", r.Header.Get("PRIVATE-TOKEN"))
		}
		if r.URL.EscapedPath() != inspectedProject+"/members/all" {
			http.NotFound(w, r)
			return
		}
		switch r.URL.Query().Get("query") {
		case "dev":
			_, _ = w.Write([]byte(`[{"username":"developer","access_level":30,"state":"active"},{"username":"dev","access_level":30,"state":"active"}]`))
		case "rep":
			_, _ = w.Write([]byte(`[{"username":"reporter","access_level":20,"state":"active"},{"username":"rep","access_level":20,"state":"active"}]`))
		case "maint":
			_, _ = w.Write([]byte(`[{"username":"maintainer","access_level":40,"state":"active"}]`))
		case "blocked":
			_, _ = w.Write([]byte(`[{"username":"blocked","access_level":40,"state":"blocked"}]`))
		case "broken":
			w.WriteHeader(http.StatusForbidden)
		default:
			_, _ = w.Write([]byte(`[]`))
		}
	}))
	t.Cleanup(srv.Close)
	p := &Provider{BaseURL: srv.URL, Token: "tok"}
	ctx := context.Background()
	for login, want := range map[string]bool{"dev": true, "rep": false, "maint": false, "blocked": false, "stranger": false} {
		got, err := provider.HasWriteAccess(ctx, p, "acme/internal/widgets", login)
		if err != nil {
			t.Fatalf("%s: %v", login, err)
		}
		if got != want {
			t.Errorf("%s: write access = %v, want %v", login, got, want)
		}
	}
	if _, err := provider.HasWriteAccess(ctx, p, "acme/internal/widgets", "broken"); err == nil {
		t.Error("a member list GitLab refused to show read as an answer")
	}
}
