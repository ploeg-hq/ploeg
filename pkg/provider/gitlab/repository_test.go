package gitlab

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/provider"
)

const inspectedProject = "/api/v4/projects/acme%2Finternal%2Fwidgets"

func repositoryForge(t *testing.T, project string, agentsOn map[string]bool) *Provider {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("PRIVATE-TOKEN") != "tok" {
			t.Errorf("missing PRIVATE-TOKEN: %q", r.Header.Get("PRIVATE-TOKEN"))
		}
		switch r.URL.EscapedPath() {
		case inspectedProject:
			_, _ = w.Write([]byte(project))
		case inspectedProject + "/repository/files/AGENTS.md":
			if r.Method != http.MethodHead {
				t.Errorf("method = %s, want HEAD for a presence check", r.Method)
			}
			if !agentsOn[r.URL.Query().Get("ref")] {
				http.NotFound(w, r)
				return
			}
			w.WriteHeader(http.StatusOK)
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
		name    string
		project string
		agents  map[string]bool
		branch  string
		want    provider.RepositoryState
	}{
		{"ready on the requested branch", `{"default_branch":"main"}`, map[string]bool{"development": true}, "development",
			provider.RepositoryState{Branch: "development", AgentsFile: true}},
		{"default branch when none is requested", `{"default_branch":"main"}`, map[string]bool{"main": true}, "",
			provider.RepositoryState{Branch: "main", AgentsFile: true}},
		{"archived", `{"archived":true,"default_branch":"main"}`, map[string]bool{"main": true}, "main",
			provider.RepositoryState{Archived: true, Branch: "main", AgentsFile: true}},
		{"pull mirror", `{"mirror":true,"default_branch":"main"}`, map[string]bool{"main": true}, "main",
			provider.RepositoryState{Mirror: true, Branch: "main", AgentsFile: true}},
		{"no AGENTS.md on the base branch", `{"default_branch":"main"}`, map[string]bool{"main": true}, "development",
			provider.RepositoryState{Branch: "development"}},
		{"empty project without a default branch", `{"default_branch":null}`, nil, "",
			provider.RepositoryState{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := repositoryForge(t, c.project, c.agents).InspectRepository(ctx, "acme", "internal/widgets", c.branch)
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
	if _, err := p.InspectRepository(context.Background(), "acme", "missing", "main"); err == nil {
		t.Error("a missing project must be an error, not a verdict")
	}
}

func TestInspectRepositoryOutageIsAnErrorNotAVerdict(t *testing.T) {
	for _, failing := range []string{"project", "file"} {
		t.Run(failing, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				isFile := strings.HasSuffix(r.URL.EscapedPath(), "/repository/files/AGENTS.md")
				if (failing == "file") == isFile {
					w.WriteHeader(http.StatusServiceUnavailable)
					return
				}
				_, _ = w.Write([]byte(`{"default_branch":"main"}`))
			}))
			defer srv.Close()
			_, err := (&Provider{BaseURL: srv.URL}).InspectRepository(context.Background(), "acme", "widgets", "")
			if err == nil {
				t.Fatal("an outage must be an error, so readiness is unknown rather than refused or admitted")
			}
		})
	}
}

func TestInspectRepositoryRejectsAnUnaddressablePath(t *testing.T) {
	p := &Provider{BaseURL: "http://unused"}
	for _, c := range [][2]string{{"", "widgets"}, {"acme", ""}, {"acme", "sub//widgets"}} {
		if _, err := p.InspectRepository(context.Background(), c[0], c[1], "main"); err == nil {
			t.Errorf("InspectRepository(%q, %q) = nil error", c[0], c[1])
		}
	}
}

func TestProviderIsARepositoryInspector(t *testing.T) {
	var _ provider.RepositoryInspector = (*Provider)(nil)
}
