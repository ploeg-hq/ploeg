package forgebroker

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func restrictedTokenRepositories(owner, repo string) []map[string]any {
	return []map[string]any{{"id": 7, "owner": owner, "name": repo, "full_name": owner + "/" + repo}}
}

type mintedTokenForge struct {
	mu            sync.Mutex
	repositories  any
	refuseDelete  bool
	deletedTokens []string
}

func (f *mintedTokenForge) deleted() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.deletedTokens...)
}

func (f *mintedTokenForge) broker(t *testing.T) *Forgejo {
	t.Helper()
	srv := forgejoTokensAPI(t, "agent-builder", "bot-password", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		switch r.Method {
		case http.MethodPost:
			var body struct {
				Name string `json:"name"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": 42, "name": body.Name, "sha1": "unrestricted-secret", "scopes": []string{"write:repository"},
				"repositories": f.repositories,
			})
		case http.MethodDelete:
			if f.refuseDelete {
				w.WriteHeader(http.StatusBadGateway)
				return
			}
			parts := strings.Split(r.URL.Path, "/")
			f.deletedTokens = append(f.deletedTokens, parts[len(parts)-1])
			w.WriteHeader(http.StatusNoContent)
		}
	})
	return &Forgejo{BaseURL: srv.URL, Bot: "agent-builder", Password: "bot-password"}
}

var runRepository = MintRequest{RunToken: "1cd43e1dfd6c9a8b7f6e5d4c", Owner: "webgrip", Repo: "ploeg"}

func TestMint_RefusesAndRevokesATokenTheForgeDidNotRestrict(t *testing.T) {
	cases := map[string]any{
		"no repositories field, as Forgejo before 15 answers": nil,
		"an empty repository list":                            []map[string]any{},
		"another repository":                                  restrictedTokenRepositories("webgrip", "homelab-cluster"),
		"another owner's repository of the same name":         restrictedTokenRepositories("someone-else", "ploeg"),
		"the Run's repository and one more": append(restrictedTokenRepositories("webgrip", "ploeg"),
			restrictedTokenRepositories("webgrip", "homelab-cluster")...),
	}
	for name, repositories := range cases {
		t.Run(name, func(t *testing.T) {
			forge := &mintedTokenForge{repositories: repositories}
			cred, err := forge.broker(t).Mint(context.Background(), runRepository)
			if !errors.Is(err, ErrUnrestrictedToken) {
				t.Fatalf("Mint = %v, want ErrUnrestrictedToken", err)
			}
			if cred != (Credential{}) {
				t.Errorf("Mint handed out %+v alongside the refusal", cred)
			}
			if strings.Contains(err.Error(), "unrestricted-secret") {
				t.Errorf("the refusal carries the token: %v", err)
			}
			want := tokenName(runRepository.RunToken, runRepository.Owner, runRepository.Repo)
			if deleted := forge.deleted(); len(deleted) != 1 || deleted[0] != want {
				t.Errorf("deleted %v, want exactly the minted token %s", deleted, want)
			}
		})
	}
}

func TestMint_AcceptsATokenRestrictedToTheRunsRepositoryWhateverItsCase(t *testing.T) {
	forge := &mintedTokenForge{repositories: restrictedTokenRepositories("WebGrip", "Ploeg")}
	cred, err := forge.broker(t).Mint(context.Background(), runRepository)
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	if cred.Token != "unrestricted-secret" || cred.ID != "42" {
		t.Errorf("credential = %+v", cred)
	}
	if deleted := forge.deleted(); len(deleted) != 0 {
		t.Errorf("a restricted token was deleted: %v", deleted)
	}
}

func TestMint_AnUnrestrictedTokenThatCannotBeDeletedIsStillRefused(t *testing.T) {
	forge := &mintedTokenForge{refuseDelete: true}
	cred, err := forge.broker(t).Mint(context.Background(), runRepository)
	if !errors.Is(err, ErrUnrestrictedToken) || !strings.Contains(err.Error(), "HTTP 502") {
		t.Fatalf("Mint = %v, want ErrUnrestrictedToken joined with the failed delete", err)
	}
	if cred != (Credential{}) {
		t.Errorf("Mint handed out %+v when the delete failed", cred)
	}
}

func versionForge(t *testing.T, status int, body string) *Forgejo {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/version" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return &Forgejo{BaseURL: srv.URL, Bot: "agent-builder", Password: "bot-password"}
}

func TestRequireRepositoryTokens_RefusesAForgeOlderThanForgejo15AndNamesItsVersion(t *testing.T) {
	for _, version := range []string{"14.0.2+gitea-1.22.0", "11.0.6+gitea-1.22.0", "1.22.0", "dev", ""} {
		t.Run(version, func(t *testing.T) {
			b := versionForge(t, http.StatusOK, `{"version":"`+version+`"}`)
			found, err := b.RequireRepositoryTokens(context.Background())
			if !errors.Is(err, ErrForgeTooOld) {
				t.Fatalf("RequireRepositoryTokens = %v, want ErrForgeTooOld", err)
			}
			if found != version || !strings.Contains(err.Error(), `"`+version+`"`) {
				t.Errorf("found %q, error %q; want both to name %q", found, err, version)
			}
		})
	}
}

func TestRequireRepositoryTokens_AcceptsForgejo15AndLater(t *testing.T) {
	for _, version := range []string{"15.0.0+gitea-1.22.0", "15.0.10+gitea-1.22.0", "16.1.0+gitea-1.22.0", "v15.0.0"} {
		t.Run(version, func(t *testing.T) {
			found, err := versionForge(t, http.StatusOK, `{"version":"`+version+`"}`).RequireRepositoryTokens(context.Background())
			if err != nil || found != version {
				t.Errorf("RequireRepositoryTokens = %q, %v; want %q and no error", found, err, version)
			}
		})
	}
}

func TestRequireRepositoryTokens_AnUnreadableVersionIsAnError(t *testing.T) {
	found, err := versionForge(t, http.StatusBadGateway, `bad gateway`).RequireRepositoryTokens(context.Background())
	if err == nil || !strings.Contains(err.Error(), "HTTP 502") {
		t.Errorf("RequireRepositoryTokens = %q, %v; want an HTTP 502 error", found, err)
	}
}
