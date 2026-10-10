package forgejo

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/provider"
)

var _ provider.PublishedPullRequestReader = (*Provider)(nil)

func TestPublishedPullRequest_ReadsBothSidesWithTheReadToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "token read" {
			t.Errorf("read went out as %q", r.Header.Get("Authorization"))
		}
		if r.URL.Path != "/api/v1/repos/webgrip/example/pulls/42" {
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, `{"state":"open","merged":false,"user":{"login":"publisher"},`+
			`"head":{"ref":"console/one","sha":"abc","repo":{"full_name":"webgrip/example"}},`+
			`"base":{"ref":"development","sha":"def","repo":{"full_name":"webgrip/example"}}}`)
	}))
	defer srv.Close()
	got, err := (&Provider{BaseURL: srv.URL, Token: "read"}).PublishedPullRequest(context.Background(), "webgrip/example", 42)
	if err != nil {
		t.Fatal(err)
	}
	want := provider.PublishedPullRequest{State: provider.PullRequestOpen, HeadSHA: "abc", HeadRef: "console/one", HeadRepository: "webgrip/example",
		BaseRef: "development", BaseRepository: "webgrip/example", Author: "publisher"}
	if got != want {
		t.Fatalf("pull request = %+v; want %+v", got, want)
	}
}

func TestPublishedPullRequest_ReportsMergedAndFailsOnMissing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/repos/webgrip/example/pulls/1" {
			fmt.Fprint(w, `{"state":"closed","merged":true}`)
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()
	p := &Provider{BaseURL: srv.URL}
	got, err := p.PublishedPullRequest(context.Background(), "webgrip/example", 1)
	if err != nil || got.State != provider.PullRequestMerged {
		t.Fatalf("merged pull request = %+v %v", got, err)
	}
	if _, err := p.PublishedPullRequest(context.Background(), "webgrip/example", 2); err == nil {
		t.Fatal("a missing pull request read as published")
	}
	if _, err := p.PublishedPullRequest(context.Background(), "example", 2); err == nil {
		t.Fatal("a repository without an owner was read")
	}
}
