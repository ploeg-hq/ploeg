package forgebroker

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/githubapp"
)

type githubTokensAPI struct {
	mu      sync.Mutex
	minted  int
	revoked []string
	asked   []map[string]any
}

func fakeGitHubApp(t *testing.T, now func() time.Time) (*GitHubApp, *githubTokensAPI) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	api := &githubTokensAPI{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		api.mu.Lock()
		defer api.mu.Unlock()
		switch r.Method + " " + r.URL.Path {
		case "GET /repos/acme/widgets/installation":
			_, _ = io.WriteString(w, `{"id": 5}`)
		case "POST /app/installations/5/access_tokens":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			api.asked = append(api.asked, body)
			api.minted++
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{"token": "ghs_" + strings.Repeat("x", api.minted),
				"expires_at": now().Add(time.Hour).Format(time.RFC3339)})
		case "DELETE /installation/token":
			api.revoked = append(api.revoked, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
			w.WriteHeader(http.StatusNoContent)
		default:
			_, _ = io.Copy(io.Discard, r.Body)
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	app := &githubapp.Minter{AppID: "1", Key: key, APIURL: srv.URL, Now: now}
	return &GitHubApp{App: app}, api
}

func TestGitHubAppMintsOneRepositoryWithWriterPermissionsAndRevokesByID(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	b, api := fakeGitHubApp(t, func() time.Time { return now })
	cred, err := b.Mint(context.Background(), MintRequest{RunToken: "aaaabbbbcccc0123", Owner: "acme", Repo: "widgets"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(cred.ID, GitHubAppIDPrefix+"aaaabbbbcccc-") || cred.Token != "ghs_x" || !cred.ExpiresAt.Equal(now.Add(time.Hour)) {
		t.Fatalf("credential = %+v", cred)
	}
	if strings.Contains(cred.ID, cred.Token) {
		t.Fatal("the credential id carries the token, and the id is stored on the lease row")
	}
	asked := api.asked[0]
	perms, _ := asked["permissions"].(map[string]any)
	if repos, _ := asked["repositories"].([]any); len(repos) != 1 || repos[0] != "widgets" || perms["contents"] != "write" || perms["workflows"] != nil {
		t.Fatalf("token request = %v", asked)
	}
	if err := b.RevokeByID(context.Background(), cred.ID); err != nil {
		t.Fatal(err)
	}
	if err := b.RevokeByID(context.Background(), cred.ID); err != nil {
		t.Fatalf("a second revoke: %v", err)
	}
	if len(api.revoked) != 1 || api.revoked[0] != "ghs_x" {
		t.Fatalf("revoked = %v, want the minted token once", api.revoked)
	}
}

func TestGitHubAppSweepRevokesTokensOfRunsWithoutALeaseAndForgetsExpiredOnes(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	b, api := fakeGitHubApp(t, func() time.Time { return now })
	held, _ := b.Mint(context.Background(), MintRequest{RunToken: "111111111111aa", Owner: "acme", Repo: "widgets"})
	orphan, _ := b.Mint(context.Background(), MintRequest{RunToken: "222222222222bb", Owner: "acme", Repo: "widgets"})
	n, err := b.SweepOrphans(context.Background(), leasedRuns("111111111111aa"))
	if err != nil || n != 1 {
		t.Fatalf("sweep = %d, %v", n, err)
	}
	if len(api.revoked) != 1 || api.revoked[0] != orphan.Token {
		t.Fatalf("revoked = %v, want only the orphan", api.revoked)
	}
	now = now.Add(2 * time.Hour)
	if n, err := b.SweepOrphans(context.Background(), leasedRuns()); err != nil || n != 0 {
		t.Fatalf("sweep after expiry = %d, %v", n, err)
	}
	if len(b.minted) != 0 || len(api.revoked) != 1 {
		t.Fatalf("expired token %s was revoked or kept: minted %v revoked %v", held.ID, b.minted, api.revoked)
	}
}

type recordingBroker struct {
	minted  []MintRequest
	revoked []string
}

func (r *recordingBroker) Mint(_ context.Context, req MintRequest) (Credential, error) {
	r.minted = append(r.minted, req)
	return Credential{ID: "17", Token: "forgejo-token"}, nil
}

func (r *recordingBroker) Revoke(_ context.Context, cred Credential) error {
	r.revoked = append(r.revoked, cred.ID)
	return nil
}

func (r *recordingBroker) RevokeByID(_ context.Context, id string) error {
	r.revoked = append(r.revoked, id)
	return nil
}

func (r *recordingBroker) SweepOrphans(context.Context, func(context.Context) ([]string, error)) (int, error) {
	return 2, nil
}

func TestRouterSendsEachForgeToItsBroker(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	gh, api := fakeGitHubApp(t, func() time.Time { return now })
	fj := &recordingBroker{}
	r := &Router{Default: fj, DefaultSweeper: fj, GitHub: gh, GitHubForges: []string{"github", "gh-prod"}, DefaultForge: "gh-prod"}
	ghCred, err := r.Mint(context.Background(), MintRequest{RunToken: "aaaaaaaaaaaa1", Owner: "acme", Repo: "widgets"})
	if err != nil || !strings.HasPrefix(ghCred.ID, GitHubAppIDPrefix) {
		t.Fatalf("a target without a forge did not go to the default forge's GitHub broker: %+v %v", ghCred, err)
	}
	fjCred, _ := r.Mint(context.Background(), MintRequest{RunToken: "bbbbbbbbbbbb1", Owner: "acme", Repo: "widgets", Forge: "forgejo"})
	if fjCred.ID != "17" || len(fj.minted) != 1 {
		t.Fatalf("forgejo mint = %+v", fjCred)
	}
	if err := r.RevokeByID(context.Background(), ghCred.ID); err != nil {
		t.Fatal(err)
	}
	if err := r.Revoke(context.Background(), Credential{ID: "17"}); err != nil {
		t.Fatal(err)
	}
	if len(api.revoked) != 1 || len(fj.revoked) != 1 || fj.revoked[0] != "17" {
		t.Fatalf("github revoked %v, forgejo revoked %v", api.revoked, fj.revoked)
	}
	if n, err := r.SweepOrphans(context.Background(), leasedRuns()); err != nil || n != 2 {
		t.Fatalf("sweep = %d, %v", n, err)
	}
}
