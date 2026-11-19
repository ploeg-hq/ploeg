package gitlab

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/provider"
)

type fakeToken struct {
	id      int64
	user    int64
	scopes  []string
	revoked bool
	expires string
}

type fakeHook struct {
	ID           int64  `json:"id"`
	URL          string `json:"url"`
	Token        string `json:"-"`
	SigningToken string `json:"-"`
	Events       map[string]any
}

// serviceAccountGitLab answers like GitLab's groups, service accounts,
// members, personal access tokens and project hooks APIs, with one Owner
// token on the top-level group acme.
type serviceAccountGitLab struct {
	t            *testing.T
	mu           sync.Mutex
	srv          *httptest.Server
	groups       map[string]int64
	accounts     map[int64]string
	members      map[string]int
	tokens       map[string]*fakeToken
	hooks        map[string][]*fakeHook
	nextID       int64
	failTokens   bool
	refuseCreate bool
	version      string
}

const ownerToken = "owner-pat-1"

func fakeKey(fill string) string {
	return SigningTokenPrefix + base64.StdEncoding.EncodeToString([]byte(strings.Repeat(fill, 32)))
}

func newServiceAccountGitLab(t *testing.T) *serviceAccountGitLab {
	f := &serviceAccountGitLab{t: t, groups: map[string]int64{"acme": 10, "acme/team": 11}, accounts: map[int64]string{},
		members: map[string]int{}, tokens: map[string]*fakeToken{ownerToken: {id: 1, user: 1, scopes: []string{"api"}}},
		hooks: map[string][]*fakeHook{}, nextID: 100, version: "19.2.0-ee"}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *serviceAccountGitLab) inst() provider.ConnectionInstance {
	return provider.ConnectionInstance{ID: "gitlab", Kind: "gitlab", URL: f.srv.URL}
}

func (f *serviceAccountGitLab) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	answer := func(status int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		if v != nil {
			_ = json.NewEncoder(w).Encode(v)
		}
	}
	bearer := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	tok, ok := f.tokens[bearer]
	if !ok || tok.revoked {
		answer(401, map[string]string{"message": "401 Unauthorized"})
		return
	}
	path := strings.TrimPrefix(r.URL.EscapedPath(), "/api/v4")
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	parts := strings.Split(strings.Trim(path, "/"), "/")
	switch {
	case r.Method == "GET" && path == "/version":
		answer(200, map[string]string{"version": f.version})
	case r.Method == "GET" && path == "/user":
		answer(200, map[string]any{"id": tok.user, "username": f.username(tok.user)})
	case r.Method == "GET" && path == "/personal_access_tokens/self":
		answer(200, map[string]any{"id": tok.id, "scopes": tok.scopes, "active": true, "revoked": false, "user_id": tok.user, "expires_at": tok.expires})
	case r.Method == "DELETE" && path == "/personal_access_tokens/self":
		tok.revoked = true
		answer(204, nil)
	case r.Method == "POST" && path == "/personal_access_tokens/self/rotate":
		tok.revoked = true
		f.nextID++
		value := fmt.Sprintf("sa-rotated-%d", f.nextID)
		f.tokens[value] = &fakeToken{id: f.nextID, user: tok.user, scopes: tok.scopes, expires: body["expires_at"].(string)}
		answer(200, map[string]any{"id": f.nextID, "token": value, "expires_at": body["expires_at"]})
	case r.Method == "GET" && path == "/projects":
		if tok.user != 1 && f.members["11/"+strconv.FormatInt(tok.user, 10)] >= 40 {
			answer(200, []map[string]any{{"id": 500}})
			return
		}
		answer(200, []map[string]any{})
	case len(parts) == 2 && parts[0] == "groups" && r.Method == "GET":
		name := strings.ReplaceAll(parts[1], "%2F", "/")
		id, ok := f.groups[name]
		if !ok {
			answer(404, map[string]string{"message": "404 Group Not Found"})
			return
		}
		answer(200, map[string]any{"id": id, "full_path": name})
	case len(parts) == 3 && parts[0] == "groups" && parts[2] == "service_accounts":
		if tok.user != 1 || parts[1] != "10" {
			answer(403, map[string]string{"message": "403 Forbidden"})
			return
		}
		if r.Method == "GET" {
			out := []map[string]any{}
			for id, name := range f.accounts {
				out = append(out, map[string]any{"id": id, "username": name})
			}
			answer(200, out)
			return
		}
		if f.refuseCreate {
			answer(400, map[string]string{"message": "Username has already been taken"})
			return
		}
		f.nextID++
		f.accounts[f.nextID] = body["username"].(string)
		answer(201, map[string]any{"id": f.nextID, "username": body["username"], "name": body["name"]})
	case len(parts) == 4 && parts[0] == "groups" && parts[2] == "service_accounts" && r.Method == "DELETE":
		id, _ := strconv.ParseInt(parts[3], 10, 64)
		delete(f.accounts, id)
		for k := range f.members {
			if strings.HasSuffix(k, "/"+parts[3]) {
				delete(f.members, k)
			}
		}
		answer(204, nil)
	case len(parts) == 5 && parts[2] == "service_accounts" && parts[4] == "personal_access_tokens" && r.Method == "POST":
		if f.failTokens {
			answer(400, map[string]string{"message": "expires_at must be before 2027-11-19"})
			return
		}
		user, _ := strconv.ParseInt(parts[3], 10, 64)
		f.nextID++
		value := fmt.Sprintf("sa-pat-%d", f.nextID)
		scopes := []string{}
		for _, s := range body["scopes"].([]any) {
			scopes = append(scopes, s.(string))
		}
		f.tokens[value] = &fakeToken{id: f.nextID, user: user, scopes: scopes, expires: body["expires_at"].(string)}
		answer(201, map[string]any{"id": f.nextID, "token": value, "scopes": scopes, "expires_at": body["expires_at"], "name": body["name"]})
	case len(parts) == 6 && parts[2] == "service_accounts" && parts[4] == "personal_access_tokens" && r.Method == "DELETE":
		id, _ := strconv.ParseInt(parts[5], 10, 64)
		for _, t := range f.tokens {
			if t.id == id {
				t.revoked = true
			}
		}
		answer(204, nil)
	case len(parts) == 3 && parts[0] == "groups" && parts[2] == "members" && r.Method == "POST":
		key := parts[1] + "/" + fmt.Sprint(body["user_id"])
		if _, ok := f.members[key]; ok {
			answer(409, map[string]string{"message": "Member already exists"})
			return
		}
		f.members[key] = int(body["access_level"].(float64))
		answer(201, map[string]any{"id": body["user_id"]})
	case len(parts) == 4 && parts[0] == "groups" && parts[2] == "members":
		key := parts[1] + "/" + parts[3]
		switch r.Method {
		case "GET":
			answer(200, map[string]any{"access_level": f.members[key]})
		case "PUT":
			f.members[key] = int(body["access_level"].(float64))
			answer(200, map[string]any{})
		case "DELETE":
			delete(f.members, key)
			answer(204, nil)
		}
	case len(parts) >= 3 && parts[0] == "projects" && parts[2] == "hooks":
		f.serveHooks(parts, r.Method, body, answer)
	default:
		answer(404, map[string]string{"message": "404 Not Found " + r.Method + " " + path})
	}
}

func (f *serviceAccountGitLab) serveHooks(parts []string, method string, body map[string]any, answer func(int, any)) {
	project := parts[1]
	switch {
	case len(parts) == 3 && method == "GET":
		out := f.hooks[project]
		if out == nil {
			out = []*fakeHook{}
		}
		answer(200, out)
	case len(parts) == 3 && method == "POST":
		f.nextID++
		h := &fakeHook{ID: f.nextID, URL: body["url"].(string), Events: body}
		h.Token, _ = body["token"].(string)
		h.SigningToken, _ = body["signing_token"].(string)
		f.hooks[project] = append(f.hooks[project], h)
		answer(201, h)
	case len(parts) == 4 && method == "PUT":
		for _, h := range f.hooks[project] {
			if strconv.FormatInt(h.ID, 10) == parts[3] {
				h.Events = body
				h.Token, _ = body["token"].(string)
				h.SigningToken, _ = body["signing_token"].(string)
			}
		}
		answer(200, map[string]any{})
	case len(parts) == 4 && method == "DELETE":
		kept := []*fakeHook{}
		for _, h := range f.hooks[project] {
			if strconv.FormatInt(h.ID, 10) != parts[3] {
				kept = append(kept, h)
			}
		}
		f.hooks[project] = kept
		answer(204, nil)
	}
}

func (f *serviceAccountGitLab) username(id int64) string {
	if id == 1 {
		return "owner"
	}
	return f.accounts[id]
}

func bootstrap(t *testing.T, f *serviceAccountGitLab) (provider.ServiceAccountGrant, error) {
	t.Helper()
	return Connector{}.BootstrapServiceAccount(context.Background(), f.inst(), provider.ServiceAccountRequest{OwnerToken: ownerToken,
		Group: "acme/team", Username: "ploeg-team", Name: "Ploeg team", ExpiresAt: time.Date(2027, 11, 18, 0, 0, 0, 0, time.UTC)})
}

func TestABootstrapMakesAServiceAccountInTheTopGroupAMaintainerOfTheGroupWithItsOwnToken(t *testing.T) {
	f := newServiceAccountGitLab(t)
	grant, err := bootstrap(t, f)
	if err != nil {
		t.Fatal(err)
	}
	if !grant.CreatedUser || !grant.AddedMember || grant.TopGroup != "acme" || grant.Group != "acme/team" || grant.Account.Login != "ploeg-team" {
		t.Fatalf("grant = %+v", grant)
	}
	if level := f.members["11/"+grant.Account.ID]; level != 40 {
		t.Fatalf("membership on acme/team = %d, want Maintainer (40)", level)
	}
	if _, ok := f.members["10/"+grant.Account.ID]; ok {
		t.Fatal("the account was made a member of the top-level group; it must reach the named group alone")
	}
	tok := f.tokens[grant.Token]
	if tok == nil || strings.Join(tok.scopes, ",") != "api,self_rotate" || tok.expires != "2027-11-18" {
		t.Fatalf("service account token = %+v", tok)
	}
	if f.tokens[ownerToken].revoked {
		t.Fatal("the bootstrap revoked the Owner's token itself; the caller revokes it last")
	}
}

func TestASecondBootstrapReusesTheAccountAndItsMembership(t *testing.T) {
	f := newServiceAccountGitLab(t)
	first, err := bootstrap(t, f)
	if err != nil {
		t.Fatal(err)
	}
	second, err := bootstrap(t, f)
	if err != nil {
		t.Fatal(err)
	}
	if second.CreatedUser || second.AddedMember || second.Account.ID != first.Account.ID || len(f.accounts) != 1 {
		t.Fatalf("second = %+v, accounts %v", second, f.accounts)
	}
	if err := (Connector{}).UndoServiceAccount(context.Background(), f.inst(), ownerToken, second); err != nil {
		t.Fatal(err)
	}
	if len(f.accounts) != 1 || f.members["11/"+first.Account.ID] != 40 || !f.tokens[second.Token].revoked || f.tokens[first.Token].revoked {
		t.Fatal("undoing a reusing bootstrap removed more than the token it created")
	}
}

func TestABootstrapThatFailsAfterCreatingTheAccountDeletesIt(t *testing.T) {
	f := newServiceAccountGitLab(t)
	f.failTokens = true
	_, err := bootstrap(t, f)
	if !errors.Is(err, provider.ErrServiceAccountRefused) || !strings.Contains(err.Error(), "expires_at") {
		t.Fatalf("err = %v", err)
	}
	if len(f.accounts) != 0 || len(f.members) != 0 {
		t.Fatalf("left accounts %v members %v", f.accounts, f.members)
	}
}

func TestABootstrapNamesTheStepGitLabRefused(t *testing.T) {
	f := newServiceAccountGitLab(t)
	f.refuseCreate = true
	if _, err := bootstrap(t, f); !errors.Is(err, provider.ErrServiceAccountRefused) || !strings.Contains(err.Error(), "already been taken") {
		t.Fatalf("err = %v", err)
	}
	f.refuseCreate = false
	_, err := Connector{}.BootstrapServiceAccount(context.Background(), f.inst(), provider.ServiceAccountRequest{OwnerToken: ownerToken, Group: "acme/missing", Username: "x"})
	if !errors.Is(err, provider.ErrServiceAccountRefused) {
		t.Fatalf("missing group = %v", err)
	}
	_, err = Connector{}.BootstrapServiceAccount(context.Background(), f.inst(), provider.ServiceAccountRequest{OwnerToken: "wrong", Group: "acme/team", Username: "x"})
	if !errors.Is(err, provider.ErrCredentialRefused) {
		t.Fatalf("wrong token = %v", err)
	}
}

func TestAServiceAccountTokenRotatesAndRevokesItselfAndItsProbeCanMint(t *testing.T) {
	f := newServiceAccountGitLab(t)
	grant, err := bootstrap(t, f)
	if err != nil {
		t.Fatal(err)
	}
	probe, err := Connector{}.ProbeConnection(context.Background(), f.inst(), provider.ConnectionCredential{Method: "gitlab_service_account", Token: grant.Token})
	if err != nil || probe.CanMint == nil || !*probe.CanMint || probe.Account.ID != grant.Account.ID || len(probe.Missing) != 0 {
		t.Fatalf("probe = %+v, %v", probe, err)
	}
	owner, err := Connector{}.ProbeConnection(context.Background(), f.inst(), provider.ConnectionCredential{Method: "token", Token: ownerToken})
	if err != nil || *owner.CanMint {
		t.Fatalf("a token without Maintainer reach can mint: %+v, %v", owner, err)
	}
	rotated, err := Connector{}.RotateOwnToken(context.Background(), f.inst(), grant.Token, time.Date(2028, 1, 2, 0, 0, 0, 0, time.UTC))
	if err != nil || !f.tokens[grant.Token].revoked || rotated.ExpiresAt.Format(time.DateOnly) != "2028-01-02" || f.tokens[rotated.Token] == nil {
		t.Fatalf("rotate = %+v, %v", rotated, err)
	}
	if err := (Connector{}).RevokeOwnToken(context.Background(), f.inst(), rotated.Token); err != nil || !f.tokens[rotated.Token].revoked {
		t.Fatalf("revoke = %v", err)
	}
	if err := (Connector{}).RevokeOwnToken(context.Background(), f.inst(), rotated.Token); err != nil {
		t.Fatalf("revoking a dead token again = %v", err)
	}
}

func TestAProjectHookIsSignedOnGitLab19AndUpdatedInPlace(t *testing.T) {
	f := newServiceAccountGitLab(t)
	grant, _ := bootstrap(t, f)
	cred := provider.ConnectionCredential{Method: "gitlab_service_account", Token: grant.Token}
	hook := provider.RepositoryHook{URL: "https://ploeg.example/webhooks/forge/gitlab?connection=gl-team", Secret: fakeKey("a")}
	for range 2 {
		if err := (Connector{}).EnsureRepositoryHook(context.Background(), f.inst(), cred, "500", hook); err != nil {
			t.Fatal(err)
		}
	}
	hooks := f.hooks["500"]
	if len(hooks) != 1 || hooks[0].SigningToken != hook.Secret || hooks[0].Token != "" {
		t.Fatalf("hooks = %+v", hooks)
	}
	for _, event := range []string{"merge_requests_events", "note_events", "pipeline_events", "push_events"} {
		if hooks[0].Events[event] != true {
			t.Errorf("%s not subscribed", event)
		}
	}
	f.version = "18.11.3"
	if err := (Connector{}).EnsureRepositoryHook(context.Background(), f.inst(), cred, "500", hook); err != nil {
		t.Fatal(err)
	}
	if f.hooks["500"][0].Token != hook.Secret {
		t.Fatal("an older GitLab must get the secret as the hook's token")
	}
	if err := (Connector{}).RemoveRepositoryHook(context.Background(), f.inst(), cred, "500", hook.URL); err != nil || len(f.hooks["500"]) != 0 {
		t.Fatalf("remove = %v, left %v", err, f.hooks["500"])
	}
	if err := (Connector{}).RemoveRepositoryHook(context.Background(), f.inst(), cred, "500", hook.URL); err != nil {
		t.Fatalf("removing again = %v", err)
	}
}

func signedDelivery(key string, body []byte, at time.Time) *http.Request {
	raw, _ := base64.StdEncoding.DecodeString(strings.TrimPrefix(key, SigningTokenPrefix))
	stamp := strconv.FormatInt(at.Unix(), 10)
	mac := hmac.New(sha256.New, raw)
	mac.Write([]byte("msg-1." + stamp + "."))
	mac.Write(body)
	r := httptest.NewRequest("POST", "/webhooks/forge/gitlab?connection=gl-team", strings.NewReader(string(body)))
	r.Header.Set("webhook-id", "msg-1")
	r.Header.Set("webhook-timestamp", stamp)
	r.Header.Set("webhook-signature", "v1,"+base64.StdEncoding.EncodeToString(mac.Sum(nil)))
	return r
}

func TestARuntimeDeliveryIsProvedWithItsConnectionsSecret(t *testing.T) {
	key := fakeKey("k")
	static := "static-value"
	now := time.Unix(1_800_000_000, 0)
	p := &Provider{Secret: static, Now: func() time.Time { return now },
		HookSecret: func(_ context.Context, id string) (string, bool, error) { return key, id == "gl-team", nil }}
	body := []byte(`{"object_kind":"note","project":{"path_with_namespace":"acme/team/app"},"merge_request":{"iid":3},"object_attributes":{"note":"hi","id":9},"user":{"username":"project_500_bot_ab12","name":"ploeg-run-0123456789ab"}}`)
	events, err := p.ParseWebhook(signedDelivery(key, body, now))
	if err != nil || len(events) != 1 || events[0].Actor != "ploeg-run-0123456789ab" {
		t.Fatalf("signed delivery = %+v, %v", events, err)
	}
	if _, err := p.ParseWebhook(signedDelivery(key, body, now.Add(-10*time.Minute))); err == nil {
		t.Fatal("a stale signed delivery was accepted")
	}
	if _, err := p.ParseWebhook(signedDelivery(fakeKey("x"), body, now)); err == nil {
		t.Fatal("a delivery signed with another key was accepted")
	}
	withToken := func(path, token string) *http.Request {
		r := httptest.NewRequest("POST", path, strings.NewReader(string(body)))
		r.Header.Set("X-Gitlab-Token", token)
		return r
	}
	if _, err := p.ParseWebhook(withToken("/webhooks/forge/gitlab?connection=gl-team", static)); err == nil {
		t.Fatal("a runtime delivery was accepted with the static secret")
	}
	if _, err := p.ParseWebhook(withToken("/webhooks/forge/gitlab?connection=gl-team", key)); err != nil {
		t.Fatalf("an older GitLab's token delivery = %v", err)
	}
	if _, err := p.ParseWebhook(withToken("/webhooks/forge/gitlab?connection=gl-gone", key)); err == nil {
		t.Fatal("a delivery for a connection Ploeg does not hold was accepted")
	}
	if held, err := p.ForApp(withToken("/webhooks/forge/gitlab?connection=gl-team", key)); !held || err != nil {
		t.Fatalf("ForApp = %v, %v", held, err)
	}
	events, err = p.ParseWebhook(withToken("/webhooks/forge/gitlab", static))
	if err != nil || events[0].Actor != "ploeg-run-0123456789ab" {
		t.Fatalf("a static delivery = %+v, %v", events, err)
	}
	human := strings.Replace(string(body), `"username":"project_500_bot_ab12"`, `"username":"alice"`, 1)
	r := httptest.NewRequest("POST", "/webhooks/forge/gitlab", strings.NewReader(human))
	r.Header.Set("X-Gitlab-Token", static)
	if events, _ := p.ParseWebhook(r); events[0].Actor != "alice" {
		t.Fatalf("a person naming themselves like a Run's token was taken for Ploeg: %q", events[0].Actor)
	}
}
