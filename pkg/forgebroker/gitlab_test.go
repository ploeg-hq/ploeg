package forgebroker

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type gitLabMinted struct {
	ID          int64    `json:"id"`
	Name        string   `json:"name"`
	Scopes      []string `json:"scopes"`
	AccessLevel int      `json:"access_level"`
	ExpiresAt   string   `json:"expires_at"`
	project     string
	revoked     bool
}

// projectTokensAPI answers like GitLab's /projects/:id/access_tokens for one
// service account token.
type projectTokensAPI struct {
	mu      sync.Mutex
	srv     *httptest.Server
	tokens  []*gitLabMinted
	next    int64
	onList  func()
	refuse  int
	account string
}

func newProjectTokensAPI(t *testing.T) *projectTokensAPI {
	api := &projectTokensAPI{next: 900, account: "sa-pat-1"}
	api.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		api.mu.Lock()
		defer api.mu.Unlock()
		if r.Header.Get("PRIVATE-TOKEN") != api.account {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		path := strings.TrimPrefix(r.URL.EscapedPath(), "/api/v4")
		parts := strings.Split(strings.Trim(path, "/"), "/")
		if len(parts) < 3 || parts[0] != "projects" || parts[2] != "access_tokens" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		switch {
		case r.Method == http.MethodPost:
			if api.refuse != 0 {
				w.WriteHeader(api.refuse)
				_, _ = w.Write([]byte(`{"message":"403 Forbidden"}`))
				return
			}
			var m gitLabMinted
			_ = json.NewDecoder(r.Body).Decode(&m)
			api.next++
			m.ID, m.project = api.next, parts[1]
			api.tokens = append(api.tokens, &m)
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{"id": m.ID, "name": m.Name, "token": fmt.Sprintf("run-pat-%d", m.ID), "expires_at": m.ExpiresAt})
		case r.Method == http.MethodGet:
			if api.onList != nil {
				api.onList()
			}
			out := []*gitLabMinted{}
			for _, m := range api.tokens {
				if m.project == parts[1] && !m.revoked {
					out = append(out, m)
				}
			}
			_ = json.NewEncoder(w).Encode(out)
		case r.Method == http.MethodDelete && len(parts) == 4:
			for _, m := range api.tokens {
				if m.project == parts[1] && fmt.Sprint(m.ID) == parts[3] && !m.revoked {
					m.revoked = true
					w.WriteHeader(http.StatusNoContent)
					return
				}
			}
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(api.srv.Close)
	return api
}

func (api *projectTokensAPI) broker(now time.Time) *GitLab {
	return &GitLab{APIURL: api.srv.URL + "/api/v4", Token: api.account, Now: func() time.Time { return now }}
}

func TestAGitLabMintIsAProjectTokenOfTheRunsProjectByIdWithTheRunsRole(t *testing.T) {
	api := newProjectTokensAPI(t)
	now := time.Date(2026, 11, 19, 23, 59, 0, 0, time.UTC)
	g := api.broker(now)
	cred, err := g.Mint(context.Background(), MintRequest{RunToken: "aaaabbbbcccc1234", Owner: "acme/team", Repo: "app", RepositoryID: "500"})
	if err != nil {
		t.Fatal(err)
	}
	writer := api.tokens[0]
	if writer.project != "500" || writer.Name != "ploeg-run-aaaabbbbcccc" || writer.AccessLevel != 30 ||
		strings.Join(writer.Scopes, ",") != "api,write_repository" || writer.ExpiresAt != "2026-11-21" {
		t.Fatalf("writer token = %+v", writer)
	}
	if cred.ID != "gitlab:500/"+fmt.Sprint(writer.ID) || cred.Token == "" || !cred.ExpiresAt.Equal(time.Date(2026, 11, 21, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("credential = %+v", cred)
	}
	if d := cred.ExpiresAt.Sub(now); d < 24*time.Hour || d > 48*time.Hour {
		t.Fatalf("a token minted a minute before midnight lives %v; it must live between one and two days", d)
	}
	if _, err := g.Mint(context.Background(), MintRequest{RunToken: "runreader", Owner: "acme/team", Repo: "app", RepositoryID: "500", Read: true}); err != nil {
		t.Fatal(err)
	}
	reader := api.tokens[1]
	if reader.AccessLevel != 20 || strings.Join(reader.Scopes, ",") != "read_api,read_repository" {
		t.Fatalf("reader token = %+v; a reading Run is a Reporter that only reads", reader)
	}
	if _, err := g.Mint(context.Background(), MintRequest{RunToken: "runstatic", Owner: "acme/team", Repo: "static"}); err != nil {
		t.Fatal(err)
	}
	if api.tokens[2].project != "acme%2Fteam%2Fstatic" {
		t.Fatalf("a static target without an id was minted on %q", api.tokens[2].project)
	}
	if _, err := g.Mint(context.Background(), MintRequest{RunToken: "runbad", RepositoryID: "acme"}); err == nil {
		t.Fatal("a non-numeric repository id was minted on")
	}
}

func TestAGitLabMintRefusalSaysWhatTheProjectNeeds(t *testing.T) {
	api := newProjectTokensAPI(t)
	api.refuse = http.StatusForbidden
	_, err := api.broker(time.Now()).Mint(context.Background(), MintRequest{RunToken: "r", RepositoryID: "500"})
	if err == nil || !strings.Contains(err.Error(), "Maintainer") || !strings.Contains(err.Error(), "Premium") {
		t.Fatalf("err = %v", err)
	}
}

func TestAGitLabRevokeIsIdempotent(t *testing.T) {
	api := newProjectTokensAPI(t)
	g := api.broker(time.Now())
	cred, err := g.Mint(context.Background(), MintRequest{RunToken: "abc", RepositoryID: "500"})
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := g.Revoke(context.Background(), cred); err != nil {
			t.Fatalf("revoke = %v", err)
		}
	}
	if !api.tokens[0].revoked {
		t.Fatal("the token was not revoked")
	}
	if err := g.Revoke(context.Background(), Credential{}); err != nil {
		t.Fatal(err)
	}
}

func TestAGitLabSweepRevokesOnlyOrphansAndListsBeforeItReadsLeases(t *testing.T) {
	api := newProjectTokensAPI(t)
	g := api.broker(time.Now())
	g.Projects = func(context.Context) ([]string, error) { return []string{"500", "acme/other"}, nil }
	ctx := context.Background()
	orphan, alive := strings.Repeat("a", 12)+"1", strings.Repeat("b", 12)+"2"
	for _, run := range []string{orphan, alive} {
		if _, err := g.Mint(ctx, MintRequest{RunToken: run, RepositoryID: "500"}); err != nil {
			t.Fatal(err)
		}
	}
	api.tokens = append(api.tokens, &gitLabMinted{ID: 1, Name: "a person's deploy token", project: "500"},
		&gitLabMinted{ID: 2, Name: "ploeg-run-" + strings.Repeat("c", 12), project: "acme%2Fother"})
	listed := false
	api.onList = func() { listed = true }
	leased := func(context.Context) ([]string, error) {
		if !listed {
			t.Fatal("the sweep read the Leases before it listed the tokens")
		}
		return []string{alive}, nil
	}
	n, err := g.SweepOrphans(ctx, leased)
	if err != nil || n != 2 {
		t.Fatalf("sweep = %d, %v", n, err)
	}
	for _, m := range api.tokens {
		want := m.Name == "ploeg-run-"+strings.Repeat("a", 12) || m.Name == "ploeg-run-"+strings.Repeat("c", 12)
		if m.revoked != want {
			t.Errorf("%s on %s revoked = %v, want %v", m.Name, m.project, m.revoked, want)
		}
	}
}

func TestARouterSendsAGitLabForgesMintAndRevocationToTheGitLabBroker(t *testing.T) {
	api := newProjectTokensAPI(t)
	g := api.broker(time.Now())
	base := &countingBroker{name: "forgejo"}
	r := &Router{Forgejo: base, ForgejoForges: []string{"forgejo"}, GitLab: g, GitLabForges: []string{"gitlab"}, DefaultForge: "forgejo"}
	cred, err := r.Mint(context.Background(), MintRequest{RunToken: "r1", Forge: "gitlab", Owner: "acme", Repo: "app"})
	if err != nil || !strings.HasPrefix(cred.ID, GitLabIDPrefix) || base.mints != 0 {
		t.Fatalf("mint = %+v, %v, forgejo mints %d", cred, err, base.mints)
	}
	if err := r.Revoke(context.Background(), cred); err != nil || !api.tokens[0].revoked || len(base.revoked) != 0 {
		t.Fatalf("revoke = %v, forgejo revoked %v", err, base.revoked)
	}
}
