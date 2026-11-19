package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/connection"
	"github.com/ploeg-hq/ploeg/pkg/provider"
	"github.com/ploeg-hq/ploeg/pkg/provider/gitlab"
)

const glOwnerToken = "owner-pat-7"

// fakeGroupGitLab is GitLab's group, service account, member and personal
// access token API for one Owner of the top-level group acme.
type fakeGroupGitLab struct {
	mu           sync.Mutex
	srv          *httptest.Server
	accounts     map[int64]string
	members      map[string]int
	tokens       map[string]int64
	revoked      map[string]bool
	expires      map[string]string
	next         int64
	failTokens   bool
	failSelfDrop bool
	rotations    int
}

func newFakeGroupGitLab(t *testing.T) *fakeGroupGitLab {
	f := &fakeGroupGitLab{accounts: map[int64]string{}, members: map[string]int{}, tokens: map[string]int64{glOwnerToken: 1},
		revoked: map[string]bool{}, expires: map[string]string{}, next: 300}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeGroupGitLab) serve(w http.ResponseWriter, r *http.Request) {
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
	user, ok := f.tokens[bearer]
	if !ok || f.revoked[bearer] {
		answer(401, map[string]string{"message": "401 Unauthorized"})
		return
	}
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	path := strings.TrimPrefix(r.URL.EscapedPath(), "/api/v4")
	parts := strings.Split(strings.Trim(path, "/"), "/")
	switch {
	case r.Method == "GET" && path == "/user":
		name := f.accounts[user]
		if user == 1 {
			name = "owner"
		}
		answer(200, map[string]any{"id": user, "username": name})
	case r.Method == "GET" && path == "/personal_access_tokens/self":
		scopes := []string{"api", "self_rotate"}
		answer(200, map[string]any{"id": 9, "scopes": scopes, "active": true, "user_id": user, "expires_at": f.expires[bearer]})
	case r.Method == "DELETE" && path == "/personal_access_tokens/self":
		if f.failSelfDrop && bearer == glOwnerToken {
			answer(500, map[string]string{"message": "500 Internal Server Error"})
			return
		}
		f.revoked[bearer] = true
		answer(204, nil)
	case r.Method == "POST" && path == "/personal_access_tokens/self/rotate":
		f.rotations++
		f.revoked[bearer] = true
		f.next++
		value := fmt.Sprintf("sa-pat-%d", f.next)
		f.tokens[value], f.expires[value] = user, body["expires_at"].(string)
		answer(200, map[string]any{"id": f.next, "token": value, "expires_at": body["expires_at"]})
	case r.Method == "GET" && path == "/projects":
		if f.members["11/"+strconv.FormatInt(user, 10)] >= 40 {
			answer(200, []map[string]any{{"id": 500}})
			return
		}
		answer(200, []map[string]any{})
	case r.Method == "GET" && len(parts) == 2 && parts[0] == "groups":
		ids := map[string]int64{"acme": 10, "acme%2Fteam": 11}
		id, ok := ids[parts[1]]
		if !ok {
			answer(404, map[string]string{"message": "404 Group Not Found"})
			return
		}
		answer(200, map[string]any{"id": id, "full_path": strings.ReplaceAll(parts[1], "%2F", "/")})
	case len(parts) == 3 && parts[1] == "10" && parts[2] == "service_accounts":
		if r.Method == "GET" {
			out := []map[string]any{}
			for id, name := range f.accounts {
				out = append(out, map[string]any{"id": id, "username": name})
			}
			answer(200, out)
			return
		}
		f.next++
		f.accounts[f.next] = body["username"].(string)
		answer(201, map[string]any{"id": f.next, "username": body["username"]})
	case len(parts) == 4 && parts[2] == "service_accounts" && r.Method == "DELETE":
		id, _ := strconv.ParseInt(parts[3], 10, 64)
		delete(f.accounts, id)
		delete(f.members, "11/"+parts[3])
		answer(204, nil)
	case len(parts) == 5 && parts[4] == "personal_access_tokens" && r.Method == "POST":
		if f.failTokens {
			answer(400, map[string]string{"message": "scopes are invalid for this account"})
			return
		}
		id, _ := strconv.ParseInt(parts[3], 10, 64)
		f.next++
		value := fmt.Sprintf("sa-pat-%d", f.next)
		f.tokens[value], f.expires[value] = id, body["expires_at"].(string)
		answer(201, map[string]any{"id": f.next, "token": value, "expires_at": body["expires_at"]})
	case len(parts) == 6 && parts[4] == "personal_access_tokens" && r.Method == "DELETE":
		answer(204, nil)
	case len(parts) == 3 && parts[2] == "members" && r.Method == "POST":
		key := parts[1] + "/" + fmt.Sprint(body["user_id"])
		if _, ok := f.members[key]; ok {
			answer(409, map[string]string{"message": "Member already exists"})
			return
		}
		f.members[key] = int(body["access_level"].(float64))
		answer(201, map[string]any{})
	case len(parts) == 4 && parts[2] == "members" && r.Method == "GET":
		answer(200, map[string]any{"access_level": f.members[parts[1]+"/"+parts[3]]})
	case len(parts) == 4 && parts[2] == "members" && r.Method == "DELETE":
		delete(f.members, parts[1]+"/"+parts[3])
		answer(204, nil)
	default:
		answer(404, map[string]string{"message": "404 Not Found"})
	}
}

func (f *fakeGroupGitLab) isRevoked(token string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.revoked[token]
}

func (rig *connRig) gitlabServiceAccounts(t *testing.T) *fakeGroupGitLab {
	t.Helper()
	gl := newFakeGroupGitLab(t)
	rig.svc.Instances["gitlab-main"] = provider.ConnectionInstance{ID: "gitlab-main", Kind: "gitlab", URL: gl.srv.URL}
	rig.svc.Config.Allow = append(rig.svc.Config.Allow, connection.Allow{Port: "forge", Kind: "gitlab", Instance: "gitlab-main",
		Methods: []string{connection.MethodGitLabServiceAccount, connection.MethodToken}})
	c := gitlab.Connector{}
	rig.svc.Connectors["gitlab"] = provider.Connector{Prober: c, Repos: c, Grants: c, ServiceAccounts: c, Rotator: c, Hooks: c}
	return gl
}

func (rig *connRig) startServiceAccount(t *testing.T, label string) string {
	t.Helper()
	code, out := rig.call("POST", "/api/v1/operator/connection-flows", rig.me(), map[string]any{"port": "forge", "kind": "gitlab",
		"instance": "gitlab-main", "method": "gitlab_service_account", "label": label})
	if code != 201 {
		t.Fatalf("start = %d %v", code, out)
	}
	return out["flow"].(map[string]any)["state"].(string)
}

func (rig *connRig) auditCount(t *testing.T, action string) int {
	t.Helper()
	var n int
	if err := testPool.QueryRow(context.Background(), `SELECT count(*) FROM audit_log WHERE action = $1`, action).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestAGitLabServiceAccountConnectionKeepsOnlyItsOwnTokenAndRevokesTheOwners(t *testing.T) {
	rig := newConnRig(t)
	gl := rig.gitlabServiceAccounts(t)
	state := rig.startServiceAccount(t, "Team")
	code, out := rig.call("POST", "/api/v1/operator/connection-flows/complete", rig.me(), map[string]any{"state": state, "token": glOwnerToken, "group": "acme/team"})
	if code != 201 {
		t.Fatalf("complete = %d %v", code, out)
	}
	conn := out["connection"].(map[string]any)
	sa := out["serviceAccount"].(map[string]any)
	if conn["method"] != "gitlab_service_account" || conn["canMint"] != true || conn["state"] != "active" || conn["id"] != "gitlab-ploeg-team" {
		t.Fatalf("connection = %v", conn)
	}
	if sa["username"] != "ploeg-team" || sa["group"] != "acme/team" || sa["topGroup"] != "acme" || sa["created"] != true || out["warnings"] != nil {
		t.Fatalf("serviceAccount = %v, warnings %v", sa, out["warnings"])
	}
	if !gl.isRevoked(glOwnerToken) {
		t.Fatal("the Owner's token is still alive at GitLab")
	}
	id := conn["id"].(string)
	cred, err := rig.svc.Credential(context.Background(), id)
	if err != nil || !strings.HasPrefix(cred.Token, "sa-pat-") || cred.Method != "gitlab_service_account" {
		t.Fatalf("stored credential = %v, %v", cred.Method, err)
	}
	hook, held, err := rig.svc.WebhookSecret(context.Background(), id)
	if err != nil || !held || !strings.HasPrefix(hook, gitlab.SigningTokenPrefix) {
		t.Fatalf("webhook secret held %v, %v", held, err)
	}
	validateOperatorSchema(t, rig.bodies[len(rig.bodies)-1])
	rig.assertNoSecrets(glOwnerToken, cred.Token, hook)

	rig.now = rig.now.AddDate(0, 0, 340)
	rig.svc.Reconcile(context.Background())
	rotated, err := rig.svc.Credential(context.Background(), id)
	if err != nil || rotated.Token == cred.Token || gl.rotations != 1 || !gl.isRevoked(cred.Token) {
		t.Fatalf("after the hourly pass near expiry: rotations %d, err %v", gl.rotations, err)
	}
	if again, _, _ := rig.svc.WebhookSecret(context.Background(), id); again != hook {
		t.Fatal("a rotation changed the webhook secret; every registered hook would stop verifying")
	}
	rig.svc.Reconcile(context.Background())
	if gl.rotations != 1 {
		t.Fatalf("a fresh token was rotated again: %d rotations", gl.rotations)
	}
	rig.assertNoSecrets(rotated.Token)

	code, out = rig.call("DELETE", "/api/v1/operator/forge-connections/"+id, rig.me(), nil)
	if code != 200 {
		t.Fatalf("remove = %d %v", code, out)
	}
	validateOperatorSchema(t, rig.bodies[len(rig.bodies)-1])
	left, _ := out["leftAtVendor"].([]any)
	if len(left) != 1 || left[0].(map[string]any)["kind"] != "service_account" || left[0].(map[string]any)["id"] != "ploeg-team" {
		t.Fatalf("leftAtVendor = %v; the account and its membership stay at GitLab", out["leftAtVendor"])
	}
	if !gl.isRevoked(rotated.Token) {
		t.Fatal("removing the connection left its token alive")
	}
}

func TestASecondServiceAccountFlowReusesTheAccountAndKeepsNoSecondToken(t *testing.T) {
	rig := newConnRig(t)
	gl := rig.gitlabServiceAccounts(t)
	state := rig.startServiceAccount(t, "Team")
	if code, out := rig.call("POST", "/api/v1/operator/connection-flows/complete", rig.me(), map[string]any{"state": state, "token": glOwnerToken, "group": "acme/team"}); code != 201 {
		t.Fatalf("first = %d %v", code, out)
	}
	gl.mu.Lock()
	gl.tokens["owner-pat-8"] = 1
	gl.mu.Unlock()
	state = rig.startServiceAccount(t, "Team")
	code, out := rig.call("POST", "/api/v1/operator/connection-flows/complete", rig.me(), map[string]any{"state": state, "token": "owner-pat-8", "group": "acme/team"})
	if code != 409 || errCode(out) != "already_connected" {
		t.Fatalf("second = %d %v", code, out)
	}
	if len(gl.accounts) != 1 || gl.members["11/"+strconv.FormatInt(301, 10)] != 40 {
		t.Fatalf("accounts %v members %v; the second flow must reuse the account and keep its membership", gl.accounts, gl.members)
	}
	if !gl.isRevoked("owner-pat-8") {
		t.Fatal("a refused flow left the Owner's token alive")
	}
}

func TestAServiceAccountBootstrapThatFailsUndoesItsAccountAndStoresNothing(t *testing.T) {
	rig := newConnRig(t)
	gl := rig.gitlabServiceAccounts(t)
	gl.failTokens = true
	state := rig.startServiceAccount(t, "Team")
	code, out := rig.call("POST", "/api/v1/operator/connection-flows/complete", rig.me(), map[string]any{"state": state, "token": glOwnerToken, "group": "acme/team"})
	if code != 422 || errCode(out) != "probe_failed" {
		t.Fatalf("complete = %d %v", code, out)
	}
	if len(gl.accounts) != 0 || len(gl.members) != 0 {
		t.Fatalf("left at GitLab: accounts %v members %v", gl.accounts, gl.members)
	}
	if len(rig.secrets.Values()) != 0 {
		t.Fatal("a failed bootstrap stored a value")
	}
	if !gl.isRevoked(glOwnerToken) || rig.auditCount(t, "connection.refused") != 1 {
		t.Fatal("a failed bootstrap must still revoke the Owner's token and audit the refusal")
	}
	rig.assertNoSecrets(glOwnerToken)
}

func TestAnOwnerTokenThatCannotBeRevokedIsAWarningAndAnAuditRow(t *testing.T) {
	rig := newConnRig(t)
	gl := rig.gitlabServiceAccounts(t)
	gl.failSelfDrop = true
	state := rig.startServiceAccount(t, "Team")
	code, out := rig.call("POST", "/api/v1/operator/connection-flows/complete", rig.me(), map[string]any{"state": state, "token": glOwnerToken, "group": "acme/team"})
	if code != 201 {
		t.Fatalf("complete = %d %v", code, out)
	}
	warnings, _ := out["warnings"].([]any)
	if len(warnings) != 1 || warnings[0].(map[string]any)["code"] != "owner_token_not_revoked" {
		t.Fatalf("warnings = %v", out["warnings"])
	}
	if rig.auditCount(t, "connection.owner_token_not_revoked") != 1 {
		t.Fatal("the failed revocation was not audited")
	}
	validateOperatorSchema(t, rig.bodies[len(rig.bodies)-1])
	rig.assertNoSecrets(glOwnerToken)
}

func TestAServiceAccountFlowRefusesAMissingGroupOrAnotherMethodsBody(t *testing.T) {
	rig := newConnRig(t)
	rig.gitlabServiceAccounts(t)
	state := rig.startServiceAccount(t, "Team")
	for _, body := range []map[string]any{{"state": state, "token": glOwnerToken}, {"state": state, "code": "x", "group": "acme/team"},
		{"state": state, "token": glOwnerToken, "group": "https://gitlab.example.com/acme"}} {
		if code, out := rig.call("POST", "/api/v1/operator/connection-flows/complete", rig.me(), body); code != 400 {
			t.Fatalf("%v = %d %v", body, code, out)
		}
	}
	code, out := rig.call("POST", "/api/v1/operator/connection-flows", rig.me(), map[string]any{"port": "tracker", "kind": "clickup",
		"instance": "clickup-cloud", "method": "gitlab_service_account"})
	if code != 422 || errCode(out) != "method_not_allowed" {
		t.Fatalf("another kind = %d %v", code, out)
	}
}
