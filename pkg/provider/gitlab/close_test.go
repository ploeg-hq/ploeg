package gitlab

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

const closedProject = "/api/v4/projects/acme%2Finternal%2Fwidgets/merge_requests"

func TestOpenPullRequests_ListsOnlyTheProjectsOwnSourceBranch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if r.URL.EscapedPath() != closedProject || q.Get("state") != "opened" || q.Get("source_branch") != "agent/vikunja-1" {
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, `[{"iid":5,"source_branch":"agent/vikunja-1","source_project_id":9,"target_project_id":9},
		                {"iid":6,"source_branch":"agent/vikunja-1","source_project_id":12,"target_project_id":9}]`)
	}))
	defer srv.Close()
	got, err := (&Provider{BaseURL: srv.URL, Token: "tok"}).OpenPullRequests(context.Background(), "acme/internal/widgets", "agent/vikunja-1")
	if err != nil {
		t.Fatal(err)
	}
	if want := []int{5}; !reflect.DeepEqual(got, want) {
		t.Fatalf("open merge requests = %v, want %v", got, want)
	}
}

func TestClosePullRequest_SendsTheCloseEvent(t *testing.T) {
	var body map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut || r.URL.EscapedPath() != closedProject+"/5" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		fmt.Fprint(w, `{"iid":5,"state":"closed"}`)
	}))
	defer srv.Close()
	if err := (&Provider{BaseURL: srv.URL}).ClosePullRequest(context.Background(), "acme/internal/widgets", 5); err != nil {
		t.Fatal(err)
	}
	if want := map[string]string{"state_event": "close"}; !reflect.DeepEqual(body, want) {
		t.Fatalf("body = %v, want %v", body, want)
	}
}

func TestClosePullRequest_ReportsGitLabsRefusal(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "403 Forbidden", http.StatusForbidden)
	}))
	defer srv.Close()
	if err := (&Provider{BaseURL: srv.URL}).ClosePullRequest(context.Background(), "acme/internal/widgets", 5); err == nil {
		t.Fatal("a refused close reported success")
	}
}
