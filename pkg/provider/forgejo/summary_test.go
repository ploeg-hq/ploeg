package forgejo

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/provider"
)

var _ provider.PullRequestLister = (*Provider)(nil)

func TestListOpenPullRequests_ReadsTheSummaryAndLeavesForksOut(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/repos/webgrip/example/pulls" || r.URL.Query().Get("state") != "open" {
			http.NotFound(w, r)
			return
		}
		if r.URL.Query().Get("page") == "2" {
			fmt.Fprint(w, `[{"number":390,"title":"later","user":{"login":"renovate"},"head":{"ref":"renovate/x","sha":"s","repo":{"full_name":"webgrip/example"}},"base":{"ref":"development"},"html_url":"https://forgejo/webgrip/example/pulls/390"}]`)
			return
		}
		pulls := []map[string]any{
			{"number": 386, "title": "fix(agent): update opencode ( 1.19.0 ➔ 1.19.2 )", "body": "release notes", "draft": false,
				"user": map[string]any{"login": "renovate"},
				"head": map[string]any{"ref": "renovate/opencode", "sha": "69762db", "repo": map[string]any{"full_name": "WebGrip/Example"}},
				"base": map[string]any{"ref": "development"}, "html_url": "https://forgejo/webgrip/example/pulls/386"},
			{"number": 387, "title": "from a fork", "user": map[string]any{"login": "someone"},
				"head": map[string]any{"ref": "renovate/opencode", "repo": map[string]any{"full_name": "someone/fork"}}},
		}
		for len(pulls) < commentsPageSize {
			pulls = append(pulls, map[string]any{"number": 100 + len(pulls), "title": "t", "user": map[string]any{"login": "ryan"},
				"head": map[string]any{"ref": "feat/x", "repo": map[string]any{"full_name": "webgrip/example"}}})
		}
		_ = json.NewEncoder(w).Encode(pulls)
	}))
	defer srv.Close()
	got, err := (&Provider{BaseURL: srv.URL}).ListOpenPullRequests(context.Background(), "webgrip/example")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != commentsPageSize-1+1 {
		t.Fatalf("listed %d pull requests, want every own-branch one across both pages", len(got))
	}
	first := got[0]
	if first.Number != 386 || first.Author != "renovate" || first.HeadBranch != "renovate/opencode" || first.HeadSHA != "69762db" ||
		first.BaseBranch != "development" || first.Body != "release notes" || first.URL != "https://forgejo/webgrip/example/pulls/386" ||
		first.Title != "fix(agent): update opencode ( 1.19.0 ➔ 1.19.2 )" {
		t.Errorf("summary = %+v", first)
	}
	for _, pr := range got {
		if pr.Number == 387 {
			t.Error("a pull request from a fork was listed")
		}
	}
	if got[len(got)-1].Number != 390 {
		t.Errorf("last = %+v, want the second page's pull request", got[len(got)-1])
	}
}

func TestPullRequestSummary_ReadsOnePullRequest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/repos/webgrip/example/pulls/386" {
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, `{"number":386,"title":"t","body":"b","draft":true,"user":{"login":"renovate"},"head":{"ref":"renovate/opencode","sha":"abc"},"base":{"ref":"development"},"html_url":"u"}`)
	}))
	defer srv.Close()
	p := &Provider{BaseURL: srv.URL}
	got, err := p.PullRequestSummary(context.Background(), "webgrip/example", 386)
	if err != nil {
		t.Fatal(err)
	}
	if got.Number != 386 || !got.Draft || got.Author != "renovate" || got.HeadBranch != "renovate/opencode" || got.BaseBranch != "development" || got.Body != "b" {
		t.Errorf("summary = %+v", got)
	}
	if _, err := p.PullRequestSummary(context.Background(), "webgrip/example", 1); err == nil {
		t.Error("a missing pull request must be an error")
	}
}
