package github

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/forgebroker"
	"github.com/ploeg-hq/ploeg/pkg/githubapi"
	"github.com/ploeg-hq/ploeg/pkg/githubapp"
	"github.com/ploeg-hq/ploeg/pkg/provider"
)

func TestQualificationAGitHubRunDeliversAPullRequest(t *testing.T) {
	full := os.Getenv("PLOEG_GITHUB_E2E_REPO")
	appID := os.Getenv("PLOEG_GITHUB_E2E_APP_ID")
	if full == "" || appID == "" {
		t.Skip("set PLOEG_GITHUB_E2E_REPO (owner/repo of a dedicated test repository), PLOEG_GITHUB_E2E_APP_ID and PLOEG_GITHUB_E2E_APP_PRIVATE_KEY_FILE to run the GitHub forge qualification")
	}
	owner, repo, ok := strings.Cut(full, "/")
	if !ok {
		t.Fatalf("PLOEG_GITHUB_E2E_REPO %q is not owner/repo", full)
	}
	pemBytes, err := os.ReadFile(os.Getenv("PLOEG_GITHUB_E2E_APP_PRIVATE_KEY_FILE"))
	if err != nil {
		t.Fatalf("PLOEG_GITHUB_E2E_APP_PRIVATE_KEY_FILE: %v", err)
	}
	key, err := githubapp.ParsePrivateKey(pemBytes)
	if err != nil {
		t.Fatal(err)
	}
	web := os.Getenv("PLOEG_GITHUB_E2E_URL")
	if web == "" {
		web = githubapi.PublicWebURL
	}
	api := os.Getenv("PLOEG_GITHUB_E2E_API_URL")
	if api == "" {
		api = githubapi.APIURL(web)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	broker := &forgebroker.GitHubApp{App: &githubapp.Minter{AppID: appID, Key: key, APIURL: api}}
	runToken := fmt.Sprintf("e2e%013d", time.Now().UnixNano()%1e13)
	cred, err := broker.Mint(ctx, forgebroker.MintRequest{RunToken: runToken, Owner: owner, Repo: repo})
	if err != nil {
		t.Fatalf("mint a per-Run token: %v", err)
	}
	defer func() { _ = broker.Revoke(context.Background(), cred) }()
	if time.Until(cred.ExpiresAt) > time.Hour+time.Minute {
		t.Fatalf("the per-Run token expires at %s, later than GitHub's one hour", cred.ExpiresAt)
	}

	runner := &githubapi.Client{}
	asRun := func(method, path string, body, out any) error {
		return runner.JSON(ctx, method, api+"/repos/"+owner+"/"+repo+path, "Bearer "+cred.Token, body, out)
	}
	var meta struct {
		DefaultBranch string `json:"default_branch"`
	}
	if err := asRun(http.MethodGet, "", nil, &meta); err != nil {
		t.Fatal(err)
	}
	var base struct {
		Object struct {
			SHA string `json:"sha"`
		} `json:"object"`
	}
	if err := asRun(http.MethodGet, "/git/ref/heads/"+meta.DefaultBranch, nil, &base); err != nil {
		t.Fatal(err)
	}
	branch := "agent/e2e-" + runToken
	if err := asRun(http.MethodPost, "/git/refs", map[string]string{"ref": "refs/heads/" + branch, "sha": base.Object.SHA}, nil); err != nil {
		t.Fatalf("the per-Run token cannot create the Run's branch: %v", err)
	}
	defer func() { _ = asRun(http.MethodDelete, "/git/refs/heads/"+branch, nil, nil) }()
	content := base64.StdEncoding.EncodeToString([]byte("qualification " + runToken + "\n"))
	if err := asRun(http.MethodPut, "/contents/ploeg-e2e/"+runToken+".txt",
		map[string]string{"message": "test: ploeg forge qualification", "content": content, "branch": branch}, nil); err != nil {
		t.Fatalf("the per-Run token cannot commit to the Run's branch: %v", err)
	}
	if err := asRun(http.MethodPut, "/contents/.github/workflows/ploeg-e2e.yml",
		map[string]string{"message": "test: must be refused", "content": content, "branch": branch}, nil); err == nil {
		t.Error("the per-Run token changed a GitHub Actions workflow; it must not hold the workflows permission")
	}
	var opened struct {
		Number int `json:"number"`
	}
	if err := asRun(http.MethodPost, "/pulls", map[string]string{"title": "test: ploeg forge qualification", "head": branch,
		"base": meta.DefaultBranch, "body": "Opened and closed by Ploeg's opt-in qualification test."}, &opened); err != nil {
		t.Fatalf("the per-Run token cannot open the pull request: %v", err)
	}
	defer func() {
		_ = asRun(http.MethodPatch, fmt.Sprintf("/pulls/%d", opened.Number), map[string]string{"state": "closed"}, nil)
	}()

	p := &Provider{BaseURL: web, APIURL: api, Tokens: &githubapp.Minter{AppID: appID, Key: key, APIURL: api,
		Permissions: githubapp.ForgePermissions()}}
	m, err := p.PullRequestMergeability(ctx, full, opened.Number)
	if err != nil {
		t.Fatal(err)
	}
	if m.Facts.State != provider.PullRequestOpen || m.Facts.HeadSHA == "" || m.BaseBranch != meta.DefaultBranch {
		t.Fatalf("pull request read %+v", m)
	}
	if err := p.Comment(ctx, full, opened.Number, "ploeg qualification "+runToken); err != nil {
		t.Fatal(err)
	}
	comments, err := p.Comments(ctx, full, opened.Number)
	if err != nil || len(comments) == 0 || !strings.Contains(comments[len(comments)-1].Body, runToken) {
		t.Fatalf("comments %+v err %v", comments, err)
	}
	paths, err := p.ChangedPaths(ctx, full, opened.Number)
	if err != nil || len(paths.Paths) != 1 || paths.Paths[0].Status != provider.PathAdded {
		t.Fatalf("changed paths %+v err %v", paths, err)
	}
	if _, _, err := p.CommitStatus(ctx, full, m.Facts.HeadSHA); err != nil {
		t.Fatalf("commit status: %v", err)
	}

	if err := asRun(http.MethodPatch, fmt.Sprintf("/pulls/%d", opened.Number), map[string]string{"state": "closed"}, nil); err != nil {
		t.Errorf("close the pull request: %v", err)
	}
	if err := asRun(http.MethodDelete, "/git/refs/heads/"+branch, nil, nil); err != nil {
		t.Errorf("delete the branch: %v", err)
	}
	if err := broker.Revoke(ctx, cred); err != nil {
		t.Fatal(err)
	}
	if err := asRun(http.MethodGet, "", nil, &meta); githubapi.StatusOf(err) != http.StatusUnauthorized {
		t.Fatalf("a revoked per-Run token still reads the repository: %v", err)
	}
}
