package forgejo

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/provider"
)

func repositoryForge(t *testing.T, repo string, agentsOn map[string]bool) *Provider {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "token tok" {
			t.Errorf("missing token auth: %q", r.Header.Get("Authorization"))
		}
		switch r.URL.Path {
		case "/api/v1/repos/webgrip/app":
			_, _ = w.Write([]byte(repo))
		case "/api/v1/repos/webgrip/app/contents/AGENTS.md":
			if !agentsOn[r.URL.Query().Get("ref")] {
				http.NotFound(w, r)
				return
			}
			_, _ = w.Write([]byte(`{"type":"file","name":"AGENTS.md"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return &Provider{BaseURL: srv.URL, Token: "tok"}
}

func TestInspectRepositoryReadsArchiveMirrorAndAgentsFile(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name   string
		repo   string
		agents map[string]bool
		branch string
		want   provider.RepositoryState
	}{
		{"ready on the requested branch", `{"default_branch":"main"}`, map[string]bool{"development": true}, "development",
			provider.RepositoryState{Branch: "development", AgentsFile: true}},
		{"default branch when none is requested", `{"default_branch":"main"}`, map[string]bool{"main": true}, "",
			provider.RepositoryState{Branch: "main", AgentsFile: true}},
		{"archived", `{"archived":true,"default_branch":"main"}`, map[string]bool{"main": true}, "main",
			provider.RepositoryState{Archived: true, Branch: "main", AgentsFile: true}},
		{"mirror", `{"mirror":true,"default_branch":"main"}`, map[string]bool{"main": true}, "main",
			provider.RepositoryState{Mirror: true, Branch: "main", AgentsFile: true}},
		{"no AGENTS.md on the base branch", `{"default_branch":"main"}`, map[string]bool{"main": true}, "development",
			provider.RepositoryState{Branch: "development"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := repositoryForge(t, c.repo, c.agents).InspectRepository(ctx, "webgrip", "app", c.branch)
			if err != nil {
				t.Fatal(err)
			}
			if got != c.want {
				t.Errorf("state = %+v, want %+v", got, c.want)
			}
		})
	}
}

func TestInspectRepositoryThatTheForgeDoesNotShowIsAnError(t *testing.T) {
	p := repositoryForge(t, `{}`, nil)
	if _, err := p.InspectRepository(context.Background(), "webgrip", "missing", "main"); err == nil {
		t.Error("a missing repository must be an error, not a verdict")
	}
}
