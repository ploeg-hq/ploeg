package clickup

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/provider"
)

func hierarchyAPI(t *testing.T) (*Provider, *[]string) {
	t.Helper()
	var mu sync.Mutex
	seen := []string{}
	routes := map[string]string{
		"/team":                           `{"teams":[{"id":"1","name":"Agency"}]}`,
		"/team/1/space?archived=false":    `{"spaces":[{"id":"20","name":"Clients"}]}`,
		"/space/20/folder?archived=false": `{"folders":[{"id":"300","name":"Acme"}]}`,
		"/space/20/list?archived=false":   `{"lists":[{"id":"901","name":"Inbox"}]}`,
		"/folder/300/list?archived=false": `{"lists":[{"id":"902","name":"Sprint"},{"id":"","name":"broken"}]}`,
		"/list/902/task?archived=false&include_closed=false&page=0&subtasks=true": `{"tasks":[{"id":"a1","name":"Open one","url":"https://app.clickup.com/t/a1","status":{"status":"to do","type":"open"},"date_updated":"1759310400000"},{"id":"a2","name":"Done one","status":{"status":"complete","type":"closed"}}],"last_page":false}`,
		"/list/902/task?archived=false&include_closed=false&page=1&subtasks=true": `{"tasks":[{"id":"a3","name":"Last","status":{"status":"to do","type":"custom"}}],"last_page":true}`,
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "pk_scopes" {
			http.Error(w, `{"err":"Token invalid"}`, http.StatusUnauthorized)
			return
		}
		key := r.URL.Path
		if r.URL.RawQuery != "" {
			key += "?" + r.URL.Query().Encode()
		}
		mu.Lock()
		seen = append(seen, key)
		mu.Unlock()
		body, ok := routes[key]
		if !ok {
			http.Error(w, `{"err":"not found"}`, http.StatusNotFound)
			return
		}
		fmt.Fprint(w, body)
	}))
	t.Cleanup(srv.Close)
	return &Provider{BaseURL: srv.URL, Token: "pk_scopes"}, &seen
}

func TestListScopesWalksWorkspaceSpaceFolderAndListOneLevelAtATime(t *testing.T) {
	p, _ := hierarchyAPI(t)
	ctx := context.Background()
	refs := func(nodes []provider.ScopeNode) string {
		out := []string{}
		for _, n := range nodes {
			out = append(out, fmt.Sprintf("%s:%s:%s:%t:%t", n.Kind, n.ID, n.Name, n.Bindable, n.Browsable))
		}
		return strings.Join(out, ",")
	}
	for _, step := range []struct {
		parent provider.Scope
		want   string
	}{
		{provider.Scope{}, "workspace:1:Agency:false:true"},
		{provider.Scope{Kind: KindWorkspace, ID: "1"}, "space:20:Clients:false:true"},
		{provider.Scope{Kind: KindSpace, ID: "20"}, "folder:300:Acme:false:true,list:901:Inbox:true:false"},
		{provider.Scope{Kind: KindFolder, ID: "300"}, "list:902:Sprint:true:false"},
		{provider.Scope{Kind: KindList, ID: "902"}, ""},
	} {
		nodes, err := p.ListScopes(ctx, step.parent)
		if err != nil {
			t.Fatalf("%+v: %v", step.parent, err)
		}
		if got := refs(nodes); got != step.want {
			t.Fatalf("%+v: got %s, want %s", step.parent, got, step.want)
		}
	}
	if _, err := p.ListScopes(ctx, provider.Scope{Kind: KindFolder, ID: "404"}); !errors.Is(err, provider.ErrNotFound) {
		t.Fatalf("an unknown Folder: %v, want ErrNotFound", err)
	}
	if _, err := p.ListScopes(ctx, provider.Scope{Kind: "board", ID: "1"}); !errors.Is(err, provider.ErrNotFound) {
		t.Fatalf("an unknown kind: %v, want ErrNotFound", err)
	}
	if _, err := (&Provider{}).ListScopes(ctx, provider.Scope{}); err == nil {
		t.Fatal("an unconfigured adapter listed Scopes")
	}
}

func TestListOpenItemsPagesAListAndLeavesClosedTasksOut(t *testing.T) {
	p, _ := hierarchyAPI(t)
	first, err := p.ListOpenItems(context.Background(), "902", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Items) != 1 || first.Items[0].ID != "a1" || first.Items[0].Status != "to do" || first.Items[0].UpdatedAt.IsZero() || first.NextPage != 2 {
		t.Fatalf("first page = %+v", first)
	}
	second, err := p.ListOpenItems(context.Background(), "902", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Items) != 1 || second.Items[0].ID != "a3" || second.Items[0].URL != "https://app.clickup.com/t/a3" || second.NextPage != 0 {
		t.Fatalf("second page = %+v", second)
	}
}
