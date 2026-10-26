package forgebroker

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

const refusalsAfterRevokeToObserve = 5

type liveForgejo struct {
	baseURL string
	hc      *http.Client
}

func (f liveForgejo) statusAs(ctx context.Context, t *testing.T, path string, authenticate func(*http.Request)) int {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(f.baseURL, "/")+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	authenticate(req)
	resp, err := f.hc.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

func asAPIToken(token string) func(*http.Request) {
	return func(r *http.Request) { r.Header.Set("Authorization", "token "+token) }
}

func asGitPassword(bot, token string) func(*http.Request) {
	return func(r *http.Request) { r.SetBasicAuth(bot, token) }
}

func splitRepository(t *testing.T, variable string) (string, string) {
	t.Helper()
	owner, repo, ok := strings.Cut(os.Getenv(variable), "/")
	if !ok || owner == "" || repo == "" {
		t.Fatalf("%s %q is not owner/repo", variable, os.Getenv(variable))
	}
	return owner, repo
}

func TestQualificationARevokedForgejoRunTokenIsRefusedAtOnce(t *testing.T) {
	baseURL := os.Getenv("PLOEG_FORGEJO_E2E_URL")
	bot := os.Getenv("PLOEG_FORGEJO_E2E_BOT")
	if baseURL == "" || bot == "" || os.Getenv("PLOEG_FORGEJO_E2E_REPO") == "" {
		t.Skip("set PLOEG_FORGEJO_E2E_URL, PLOEG_FORGEJO_E2E_BOT, PLOEG_FORGEJO_E2E_PASSWORD_FILE and PLOEG_FORGEJO_E2E_REPO (owner/repo the bot can write), and optionally PLOEG_FORGEJO_E2E_OTHER_REPO (owner/repo the bot can read), to qualify per-Run token revocation on a live Forgejo")
	}
	password, err := os.ReadFile(os.Getenv("PLOEG_FORGEJO_E2E_PASSWORD_FILE"))
	if err != nil {
		t.Fatalf("PLOEG_FORGEJO_E2E_PASSWORD_FILE: %v", err)
	}
	owner, repo := splitRepository(t, "PLOEG_FORGEJO_E2E_REPO")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	broker := &Forgejo{BaseURL: baseURL, Bot: bot, Password: strings.TrimSpace(string(password))}
	forge := liveForgejo{baseURL: baseURL, hc: broker.client()}
	runToken := fmt.Sprintf("e2e%013d", time.Now().UnixNano()%1e13)
	cred, err := broker.Mint(ctx, MintRequest{RunToken: runToken, Owner: owner, Repo: repo})
	if err != nil {
		t.Fatalf("mint a per-Run token: %v", err)
	}
	defer func() { _ = broker.Revoke(context.Background(), cred) }()

	repositoryAPI := "/api/v1/repos/" + owner + "/" + repo
	gitPush := "/" + owner + "/" + repo + ".git/info/refs?service=git-receive-pack"
	if status := forge.statusAs(ctx, t, repositoryAPI, asAPIToken(cred.Token)); status != http.StatusOK {
		t.Fatalf("before revocation the repository API answered %d, want 200", status)
	}
	if status := forge.statusAs(ctx, t, gitPush, asGitPassword(bot, cred.Token)); status != http.StatusOK {
		t.Fatalf("before revocation git push discovery answered %d, want 200", status)
	}
	if os.Getenv("PLOEG_FORGEJO_E2E_OTHER_REPO") != "" {
		otherOwner, otherRepo := splitRepository(t, "PLOEG_FORGEJO_E2E_OTHER_REPO")
		status := forge.statusAs(ctx, t, "/api/v1/repos/"+otherOwner+"/"+otherRepo, asAPIToken(cred.Token))
		if status != http.StatusForbidden && status != http.StatusNotFound {
			t.Fatalf("the per-Run token read another repository with %d, want 403 or 404", status)
		}
	}

	revokedAt := time.Now()
	if err := broker.Revoke(ctx, cred); err != nil {
		t.Fatalf("revoke the per-Run token: %v", err)
	}
	revokeTook := time.Since(revokedAt)
	firstRefusalAfterRevoke := time.Duration(-1)
	for attempt := range refusalsAfterRevokeToObserve {
		apiStatus := forge.statusAs(ctx, t, repositoryAPI, asAPIToken(cred.Token))
		gitStatus := forge.statusAs(ctx, t, gitPush, asGitPassword(bot, cred.Token))
		if apiStatus != http.StatusUnauthorized || gitStatus != http.StatusUnauthorized {
			t.Fatalf("replay %d after revocation: repository API %d and git push discovery %d, want 401 for both", attempt+1, apiStatus, gitStatus)
		}
		if firstRefusalAfterRevoke < 0 {
			firstRefusalAfterRevoke = time.Since(revokedAt)
		}
	}
	tokens, err := broker.listTokens(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, token := range tokens {
		if token.Name == cred.Name {
			t.Fatalf("the revoked token %s is still listed for %s", cred.Name, bot)
		}
	}
	t.Logf("forge %s: revoke returned in %s; the first replay %s after the revoke call began was refused, and all %d replays of the API and git routes answered 401",
		baseURL, revokeTook.Round(time.Millisecond), firstRefusalAfterRevoke.Round(time.Millisecond), refusalsAfterRevokeToObserve)
}
