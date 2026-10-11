package worker

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/harness"
)

func githubSpec(role string) harness.TaskSpec {
	spec := specFor(testCfg("main"), testItem(), "agent/vik-585", "ploeg-abc123def456")
	spec.Role = role
	spec.Repo = harness.RepoRef{Forge: harness.ForgeGitHub, ForgeURL: "https://github.com", Owner: "acme", Name: "widgets", BaseBranch: "main"}
	return spec
}

func TestComposePrompt_GitHubWriterOpensAPullRequestThroughTheRESTAPI(t *testing.T) {
	task := ComposePrompt(githubSpec("builder"), true, "", true)
	for _, want := range []string{
		"open a pull request",
		"https://api.github.com/repos/acme/widgets/pulls",
		"Authorization: Bearer",
		"Do NOT merge the pull request",
	} {
		if !strings.Contains(task, want) {
			t.Errorf("GitHub writer prompt missing %q:\n%s", want, task)
		}
	}
	for _, reject := range []string{"Forgejo", "/api/v1/repos/", "merge request"} {
		if strings.Contains(task, reject) {
			t.Errorf("GitHub writer prompt still says %q:\n%s", reject, task)
		}
	}
}

func TestFindOpenChangeRequestReadsGitHubPullsForTheRunBranch(t *testing.T) {
	var next string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v3/repos/acme/widgets/pulls" || r.Header.Get("Authorization") != "Bearer ghs_run" {
			t.Errorf("request %s %q", r.URL, r.Header.Get("Authorization"))
		}
		if r.URL.Query().Get("page") == "2" {
			_, _ = io.WriteString(w, `[{"html_url":"https://github.com/acme/widgets/pull/9","number":9,
				"head":{"ref":"agent/vik-1","sha":"h9","repo":{"full_name":"acme/widgets"}},
				"base":{"ref":"main","repo":{"full_name":"acme/widgets"}}}]`)
			return
		}
		if got := r.URL.Query().Get("head"); got != "acme:agent/vik-1" {
			t.Errorf("head filter = %q", got)
		}
		w.Header().Set("Link", "<"+next+">; rel=\"next\"")
		_, _ = io.WriteString(w, `[{"html_url":"https://github.com/acme/widgets/pull/8","number":8,
			"head":{"ref":"agent/vik-1","sha":"h8","repo":{"full_name":"fork/widgets"}},
			"base":{"ref":"main","repo":{"full_name":"acme/widgets"}}}]`)
	}))
	defer srv.Close()
	next = srv.URL + "/api/v3/repos/acme/widgets/pulls?page=2"
	ref := harness.RepoRef{Forge: harness.ForgeGitHub, ForgeURL: srv.URL, Owner: "acme", Name: "widgets", BaseBranch: "main"}
	cr, err := findOpenChangeRequest(ref, "ghs_run", "agent/vik-1")
	if err != nil {
		t.Fatal(err)
	}
	if cr.Number != 9 || cr.HeadSHA != "h9" || cr.URL != "https://github.com/acme/widgets/pull/9" {
		t.Fatalf("change request = %+v; the fork's pull request must not count", cr)
	}
}

func githubProxy(t *testing.T, forge *httptest.Server, token string) *forgeTokenProxy {
	t.Helper()
	p, err := startForgeTokenProxy(harness.RepoRef{Forge: harness.ForgeGitHub, ForgeURL: forge.URL, Owner: "acme", Name: "widgets"},
		staticForgeToken(token), forgeWriter, writerBranch, testLeakScope(newLeakWatch()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.close)
	return p
}

func TestForgeProxyScopesTheGitHubAPI(t *testing.T) {
	forge, seen := fakeForge(t)
	p := githubProxy(t, forge, "ghs_real")
	bearer := http.Header{"Authorization": {"Bearer " + p.placeholder}}
	for _, path := range []string{
		"/api/v3/repos/acme/widgets/pulls",
		"/api/v3/repos/acme/widgets/issues/3/comments",
	} {
		send(t, p, http.MethodPost, path, bearer)
		if got := seen.last(); got == nil || got.URL.Path != path || got.Header.Get("Authorization") != "Bearer ghs_real" {
			t.Fatalf("%s: forge saw %v, want the real installation token", path, got)
		}
	}
	send(t, p, http.MethodGet, "/api/v3/repos/acme/widgets/commits/abc/check-runs", bearer)
	if got := seen.last(); got == nil || got.URL.Path != "/api/v3/repos/acme/widgets/commits/abc/check-runs" {
		t.Fatalf("check runs were not forwarded: %v", got)
	}
	before := seen.count()
	for _, refused := range []struct{ method, path string }{
		{http.MethodPut, "/api/v3/repos/acme/widgets/pulls/3/merge"},
		{http.MethodPost, "/api/v3/repos/acme/other/pulls"},
		{http.MethodDelete, "/api/v3/repos/acme/widgets"},
		{http.MethodPut, "/api/v3/repos/acme/widgets/contents/README.md"},
		{http.MethodPost, "/api/v3/repos/acme/widgets/git/refs"},
		{http.MethodGet, "/api/v3/user"},
		{http.MethodPost, "/api/v3/repos/acme/widgets/dispatches"},
	} {
		if status := send(t, p, refused.method, refused.path, bearer); status != http.StatusForbidden {
			t.Errorf("%s %s: status %d, want 403", refused.method, refused.path, status)
		}
	}
	if seen.count() != before {
		t.Fatalf("a refused GitHub request reached the forge: %s", seen.last().URL)
	}
}

func TestForgeProxyAuthenticatesGitHubGitAsTheInstallation(t *testing.T) {
	forge, seen := fakeForge(t)
	p := githubProxy(t, forge, "ghs_real")
	send(t, p, http.MethodGet, "/acme/widgets.git/info/refs?service=git-receive-pack",
		http.Header{"Authorization": {"Basic " + base64.StdEncoding.EncodeToString([]byte("agent-builder:"+p.placeholder))}})
	want := "Basic " + base64.StdEncoding.EncodeToString([]byte("x-access-token:ghs_real"))
	if got := seen.last(); got == nil || got.Header.Get("Authorization") != want {
		t.Fatalf("forge saw %v, want the installation token as x-access-token", got)
	}
}

func TestPublicGitHubAPIRequestsLeaveTheAPIPrefixBehind(t *testing.T) {
	u, _ := url.Parse("https://github.com/api/v3/repos/acme/widgets/pulls")
	if host := toPublicGitHubAPI(u); host != "api.github.com" || u.Path != "/repos/acme/widgets/pulls" {
		t.Fatalf("host %q path %q", host, u.Path)
	}
	u, _ = url.Parse("https://github.com/API/V3/repos/acme/widgets/pulls")
	if toPublicGitHubAPI(u); u.Path != "/repos/acme/widgets/pulls" {
		t.Fatalf("path %q", u.Path)
	}
}

func TestGitBasicUserFollowsTheTokenFormat(t *testing.T) {
	for token, want := range map[string]string{
		"ghs_abc": "x-access-token", "github_pat_abc": "x-access-token", "ghp_abc": "x-access-token",
		"0123456789abcdef": "agent-builder", "glpat-abc": "agent-builder",
	} {
		if got := gitBasicUser(token); got != want {
			t.Errorf("gitBasicUser(%q) = %q, want %q", token, got, want)
		}
	}
}

func TestForgeCredentialRefreshesBeforeItExpires(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	var guarded []string
	refreshes := 0
	cred := &forgeCredential{token: "ghs_first", expiresAt: now.Add(time.Hour), now: func() time.Time { return now },
		guard: func(token string) { guarded = append(guarded, token) },
		refresh: func(context.Context) (string, time.Time, error) {
			refreshes++
			return "ghs_second", now.Add(time.Hour), nil
		}}
	if got := cred.current(context.Background()); got != "ghs_first" || refreshes != 0 {
		t.Fatalf("a fresh token was replaced: %q after %d refreshes", got, refreshes)
	}
	now = now.Add(50 * time.Minute)
	if got := cred.current(context.Background()); got != "ghs_second" || refreshes != 1 {
		t.Fatalf("token ten minutes before expiry = %q after %d refreshes", got, refreshes)
	}
	if len(guarded) != 1 || guarded[0] != "ghs_second" {
		t.Fatalf("the leak watch guards %v, want the refreshed token", guarded)
	}
	cred.refresh = func(context.Context) (string, time.Time, error) { return "", time.Time{}, errors.New("ploegd away") }
	now = now.Add(55 * time.Minute)
	if got := cred.current(context.Background()); got != "ghs_second" {
		t.Fatalf("a failed refresh dropped the current token: %q", got)
	}
}

func TestForgeProxySendsTheRefreshedToken(t *testing.T) {
	forge, seen := fakeForge(t)
	now := time.Unix(1_800_000_000, 0)
	cred := &forgeCredential{token: "ghs_first", expiresAt: now.Add(time.Hour), now: func() time.Time { return now },
		refresh: func(context.Context) (string, time.Time, error) { return "ghs_second", now.Add(2 * time.Hour), nil }}
	p, err := startForgeTokenProxy(harness.RepoRef{Forge: harness.ForgeGitHub, ForgeURL: forge.URL, Owner: "acme", Name: "widgets"},
		cred, forgeWriter, writerBranch, testLeakScope(newLeakWatch()))
	if err != nil {
		t.Fatal(err)
	}
	defer p.close()
	now = now.Add(55 * time.Minute)
	send(t, p, http.MethodGet, "/api/v3/repos/acme/widgets/pulls", http.Header{"Authorization": {"Bearer " + p.placeholder}})
	if got := seen.last(); got == nil || got.Header.Get("Authorization") != "Bearer ghs_second" {
		t.Fatalf("forge saw %v, want the refreshed token", got)
	}
}
