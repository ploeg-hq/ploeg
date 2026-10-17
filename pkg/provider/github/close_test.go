package github

import (
	"context"
	"net/http"
	"reflect"
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/provider"
)

var _ provider.PullRequestCloser = (*Provider)(nil)

func TestOpenPullRequests_AsksForTheRepositorysOwnHeadBranch(t *testing.T) {
	f, p := newFake(t)
	f.json("GET /repos/acme/widgets/pulls?head=acme%3Aagent%2Fvikunja-1&per_page=100&state=open", http.StatusOK,
		`[{"number":5,"head":{"ref":"agent/vikunja-1","repo":{"full_name":"acme/widgets"}}},
		  {"number":6,"head":{"ref":"agent/vikunja-1","repo":null}}]`)
	got, err := p.OpenPullRequests(context.Background(), "acme/widgets", "agent/vikunja-1")
	if err != nil {
		t.Fatal(err)
	}
	if want := []int{5}; !reflect.DeepEqual(got, want) {
		t.Fatalf("open pull requests = %v, want %v", got, want)
	}
}

func TestClosePullRequest_PatchesTheStateClosed(t *testing.T) {
	f, p := newFake(t)
	f.json("PATCH /repos/acme/widgets/pulls/5", http.StatusOK, `{"number":5,"state":"closed"}`)
	if err := p.ClosePullRequest(context.Background(), "acme/widgets", 5); err != nil {
		t.Fatal(err)
	}
	if got := f.bodies[len(f.bodies)-1]; got != `{"state":"closed"}` {
		t.Fatalf("body = %s", got)
	}
}

func TestClosePullRequest_ReportsGitHubsRefusal(t *testing.T) {
	f, p := newFake(t)
	f.json("PATCH /repos/acme/widgets/pulls/5", http.StatusForbidden, `{"message":"Resource not accessible by integration"}`)
	if err := p.ClosePullRequest(context.Background(), "acme/widgets", 5); err == nil {
		t.Fatal("a refused close reported success")
	}
}
