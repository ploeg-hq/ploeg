package githubissues

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/provider"
)

func TestListScopesNamesOnlyTheConfiguredRepositories(t *testing.T) {
	p := &Provider{Repositories: map[string]bool{"acme:web": true, "acme:api": true, "acme:old": false}}
	nodes, err := p.ListScopes(context.Background(), provider.Scope{})
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 2 || nodes[0].ID != "acme:api" || nodes[0].Name != "acme/api" || !nodes[0].Bindable || nodes[0].Browsable || nodes[0].Kind != KindRepository {
		t.Fatalf("nodes = %+v", nodes)
	}
	if _, err := p.ListScopes(context.Background(), provider.Scope{Kind: KindRepository, ID: "acme:web"}); !errors.Is(err, provider.ErrNotFound) {
		t.Fatalf("a repository listed children: %v", err)
	}
}

func TestListOpenItemsReadsOpenIssuesAndLeavesPullRequestsOut(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/acme/web/issues" || r.URL.Query().Get("state") != "open" || r.Header.Get("Authorization") != "Bearer installation" {
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, `[{"number":3,"title":"Bug","state":"open","updated_at":"2026-11-01T09:00:00Z"},{"number":4,"title":"A PR","state":"open","pull_request":{"url":"x"}}]`)
	}))
	t.Cleanup(srv.Close)
	p := &Provider{APIURL: srv.URL, WebURL: "https://github.com", Tokens: &fixedToken{token: "installation"}, Repositories: map[string]bool{"acme:web": true}}
	page, err := p.ListOpenItems(context.Background(), "acme:web", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].ID != "acme:web:3" || page.Items[0].URL != "https://github.com/acme/web/issues/3" || page.NextPage != 0 {
		t.Fatalf("page = %+v", page)
	}
	if _, err := p.ListOpenItems(context.Background(), "acme:secret", 1); !errors.Is(err, provider.ErrNotFound) {
		t.Fatalf("a repository outside the configuration was read: %v", err)
	}
}
