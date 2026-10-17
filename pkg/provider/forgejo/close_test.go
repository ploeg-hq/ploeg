package forgejo

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/provider"
)

var _ provider.PullRequestCloser = (*Provider)(nil)

func TestOpenPullRequests_ListsOnlyTheRepositorysOwnBranchAcrossPages(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/repos/webgrip/example/pulls" || r.URL.Query().Get("state") != "open" {
			http.NotFound(w, r)
			return
		}
		if r.URL.Query().Get("page") == "2" {
			fmt.Fprint(w, `[{"number":77,"head":{"ref":"agent/vikunja-1","repo":{"full_name":"webgrip/example"}}}]`)
			return
		}
		pulls := make([]map[string]any, 0, commentsPageSize)
		pulls = append(pulls,
			map[string]any{"number": 5, "head": map[string]any{"ref": "agent/vikunja-1", "repo": map[string]any{"full_name": "WebGrip/Example"}}},
			map[string]any{"number": 6, "head": map[string]any{"ref": "agent/vikunja-1", "repo": map[string]any{"full_name": "someone/fork"}}},
		)
		for len(pulls) < commentsPageSize {
			pulls = append(pulls, map[string]any{"number": 100 + len(pulls), "head": map[string]any{"ref": "other", "repo": map[string]any{"full_name": "webgrip/example"}}})
		}
		_ = json.NewEncoder(w).Encode(pulls)
	}))
	defer srv.Close()
	got, err := (&Provider{BaseURL: srv.URL}).OpenPullRequests(context.Background(), "webgrip/example", "agent/vikunja-1")
	if err != nil {
		t.Fatal(err)
	}
	if want := []int{5, 77}; !reflect.DeepEqual(got, want) {
		t.Fatalf("open pull requests = %v, want %v", got, want)
	}
}

func TestClosePullRequest_PatchesTheStateWithPloegsToken(t *testing.T) {
	var body map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch || r.URL.Path != "/api/v1/repos/webgrip/example/pulls/5" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "token ploegd" {
			t.Errorf("close went out as %q", r.Header.Get("Authorization"))
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		fmt.Fprint(w, `{"number":5,"state":"closed"}`)
	}))
	defer srv.Close()
	if err := (&Provider{BaseURL: srv.URL, Token: "ploegd"}).ClosePullRequest(context.Background(), "webgrip/example", 5); err != nil {
		t.Fatal(err)
	}
	if want := map[string]string{"state": "closed"}; !reflect.DeepEqual(body, want) {
		t.Fatalf("body = %v, want %v", body, want)
	}
}

func TestClosePullRequest_ReportsTheForgesRefusal(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "forbidden", http.StatusForbidden)
	}))
	defer srv.Close()
	if err := (&Provider{BaseURL: srv.URL}).ClosePullRequest(context.Background(), "webgrip/example", 5); err == nil {
		t.Fatal("a refused close reported success")
	}
}
