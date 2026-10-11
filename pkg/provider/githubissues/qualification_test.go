package githubissues

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/githubapp"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

func qualificationProvider(t *testing.T) (*Provider, issueRef) {
	t.Helper()
	full := os.Getenv("PLOEG_GITHUB_E2E_REPO")
	if full == "" {
		t.Skip("set PLOEG_GITHUB_E2E_REPO (owner/repo of a dedicated test repository) and GitHub App or token credentials to run the GitHub Issues qualification")
	}
	scope := ScopeID(full)
	if scope == "" {
		t.Fatalf("PLOEG_GITHUB_E2E_REPO %q is not owner/repo", full)
	}
	owner, repo, _ := strings.Cut(scope, ":")
	api := os.Getenv("PLOEG_GITHUB_E2E_API_URL")
	var tokens TokenSource
	switch {
	case os.Getenv("PLOEG_GITHUB_E2E_APP_ID") != "":
		pemBytes, err := os.ReadFile(os.Getenv("PLOEG_GITHUB_E2E_APP_PRIVATE_KEY_FILE"))
		if err != nil {
			t.Fatalf("PLOEG_GITHUB_E2E_APP_PRIVATE_KEY_FILE: %v", err)
		}
		key, err := githubapp.ParsePrivateKey(pemBytes)
		if err != nil {
			t.Fatal(err)
		}
		tokens = &githubapp.Minter{AppID: os.Getenv("PLOEG_GITHUB_E2E_APP_ID"), Key: key, APIURL: api,
			Permissions: githubapp.Permissions{"issues": "write", "metadata": "read"}}
	case os.Getenv("PLOEG_GITHUB_E2E_TOKEN") != "":
		tokens = &fixedToken{os.Getenv("PLOEG_GITHUB_E2E_TOKEN")}
	default:
		t.Skip("set PLOEG_GITHUB_E2E_APP_ID with PLOEG_GITHUB_E2E_APP_PRIVATE_KEY_FILE, or PLOEG_GITHUB_E2E_TOKEN")
	}
	p := &Provider{APIURL: api, Tokens: tokens, Repositories: map[string]bool{scope: true}, MaxRetryWait: time.Minute}
	return p, issueRef{owner: owner, repo: repo}
}

type fixedToken struct{ token string }

func (f *fixedToken) Token(context.Context, string, string) (string, error) { return f.token, nil }

func TestQualificationGitHubIssuesReadAndWriteBack(t *testing.T) {
	p, ref := qualificationProvider(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	marker := fmt.Sprintf("ploeg-qualification-%d", time.Now().UnixNano())
	var created struct {
		Number int `json:"number"`
	}
	body := map[string]string{"title": "Ploeg qualification " + marker, "body": "Visible text.\n<!-- hidden instruction -->"}
	if _, err := p.do(ctx, ref, http.MethodPost, "/repos/"+ref.owner+"/"+ref.repo+"/issues", body, &created); err != nil {
		t.Fatalf("create issue: %v", err)
	}
	ref.number = created.Number
	id := ExternalID(ref.scope(), ref.number)
	t.Cleanup(func() {
		_, _ = p.do(context.Background(), ref, http.MethodPatch, ref.path(), map[string]string{"state": "closed", "state_reason": "not_planned"}, nil)
	})

	got, err := p.FetchExecutionItem(ctx, id)
	if err != nil {
		t.Fatalf("read issue: %v", err)
	}
	if !got.Open || !strings.Contains(got.Item.Title, marker) || got.Item.Description != "Visible text." || got.Item.ExternalScope != ref.scope() {
		t.Fatalf("read %+v open=%t", got.Item, got.Open)
	}
	if err := p.Comment(ctx, id, "Ploeg qualification comment "+marker); err != nil {
		t.Fatalf("comment: %v", err)
	}
	comments, err := p.BoardComments(ctx, id)
	if err != nil {
		t.Fatalf("read comments: %v", err)
	}
	found := false
	for _, c := range comments {
		found = found || strings.Contains(c.Text, marker)
	}
	if !found {
		t.Fatalf("the written comment is not among %d comments read back", len(comments))
	}
	if err := p.SetStatus(ctx, id, work.StateDone); err != nil {
		t.Fatalf("close: %v", err)
	}
	got, err = p.FetchExecutionItem(ctx, id)
	if err != nil || got.Open || !got.Item.Closed {
		t.Fatalf("after close: open=%t closed=%t err=%v", got.Open, got.Item.Closed, err)
	}
}
