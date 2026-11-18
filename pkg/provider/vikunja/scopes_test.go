package vikunja

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/provider"
)

func projectsAPI(t *testing.T, projects []map[string]any, tasks []map[string]any) (*Provider, *[]string) {
	t.Helper()
	filters := []string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tk_scopes" {
			http.Error(w, `{"message":"invalid token"}`, http.StatusUnauthorized)
			return
		}
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		perPage, _ := strconv.Atoi(r.URL.Query().Get("per_page"))
		switch r.URL.Path {
		case "/api/v1/projects":
			start := min((page-1)*perPage, len(projects))
			_ = json.NewEncoder(w).Encode(projects[start:min(start+perPage, len(projects))])
		case "/api/v1/tasks/all":
			filters = append(filters, r.URL.Query().Get("filter"))
			start := min((page-1)*perPage, len(tasks))
			_ = json.NewEncoder(w).Encode(tasks[start:min(start+perPage, len(tasks))])
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return &Provider{BaseURL: srv.URL + "/api/v1", Token: "tk_scopes"}, &filters
}

func TestListScopesNestsProjectsUnderTheirParent(t *testing.T) {
	projects := []map[string]any{
		{"id": 1, "title": "Agency", "parent_project_id": 0},
		{"id": 2, "title": "Client A", "parent_project_id": 1},
		{"id": 3, "title": "Archive", "parent_project_id": 0, "is_archived": true},
		{"id": 4, "title": "Inbox", "parent_project_id": 0},
	}
	for i := range 250 {
		projects = append(projects, map[string]any{"id": 100 + i, "title": fmt.Sprintf("Board %d", i), "parent_project_id": 4})
	}
	p, _ := projectsAPI(t, projects, nil)
	ctx := context.Background()
	top, err := p.ListScopes(ctx, provider.Scope{})
	if err != nil {
		t.Fatal(err)
	}
	if len(top) != 2 || top[0].ID != "1" || !top[0].Bindable || !top[0].Browsable || top[1].ID != "4" || !top[1].Browsable {
		t.Fatalf("top level = %+v", top)
	}
	children, err := p.ListScopes(ctx, provider.Scope{Kind: KindProject, ID: "4"})
	if err != nil {
		t.Fatal(err)
	}
	if len(children) != 250 || children[249].Name != "Board 249" || children[0].Browsable {
		t.Fatalf("a second page of projects was not read: %d children", len(children))
	}
	if _, err := p.ListScopes(ctx, provider.Scope{Kind: KindProject, ID: "999"}); !errors.Is(err, provider.ErrNotFound) {
		t.Fatalf("an unknown parent: %v", err)
	}
	if _, err := p.ListScopes(ctx, provider.Scope{Kind: "list", ID: "1"}); !errors.Is(err, provider.ErrNotFound) {
		t.Fatalf("an unknown kind: %v", err)
	}
}

func TestListOpenItemsFiltersOnTheServerAndAgainLocally(t *testing.T) {
	tasks := []map[string]any{
		{"id": 11, "title": "Open", "done": false, "project_id": 7, "updated": "2026-11-01T09:00:00Z", "bucket": map[string]any{"title": "Doing"}},
		{"id": 12, "title": "Done", "done": true, "project_id": 7},
		{"id": 13, "title": "Elsewhere", "done": false, "project_id": 8},
	}
	p, filters := projectsAPI(t, nil, tasks)
	page, err := p.ListOpenItems(context.Background(), "7", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].ID != "11" || page.Items[0].Status != "Doing" || page.Items[0].UpdatedAt.IsZero() || page.NextPage != 0 {
		t.Fatalf("page = %+v", page)
	}
	if !strings.Contains(page.Items[0].URL, "/tasks/11") {
		t.Fatalf("url = %q", page.Items[0].URL)
	}
	if len(*filters) != 1 || (*filters)[0] != "done = false && project = 7" {
		t.Fatalf("filters = %q", *filters)
	}
	if _, err := p.ListOpenItems(context.Background(), "not-a-number", 1); !errors.Is(err, provider.ErrNotFound) {
		t.Fatalf("a non-numeric project: %v", err)
	}
}
