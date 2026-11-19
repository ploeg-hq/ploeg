package httpapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/forgebroker"
	"github.com/ploeg-hq/ploeg/pkg/provider"
	"github.com/ploeg-hq/ploeg/pkg/provider/gitlab"
	"github.com/ploeg-hq/ploeg/pkg/store"
	"github.com/ploeg-hq/ploeg/pkg/work"
	"github.com/ploeg-hq/ploeg/pkg/worktarget"
)

const glServiceToken = "sa-pat-41"

var glHookKey = gitlab.SigningTokenPrefix + base64.StdEncoding.EncodeToString([]byte(strings.Repeat("h", 32)))

func (c *fakeTargetConnections) WebhookSecret(_ context.Context, id string) (string, bool, error) {
	if strings.HasPrefix(id, "gl-") {
		return glHookKey, true, nil
	}
	return "", false, nil
}

// projectGitLab is one GitLab project, acme/team/app with id 500, as a
// service account that is its Maintainer sees it.
type projectGitLab struct {
	mu     sync.Mutex
	hooks  map[int]map[string]any
	nextID int
	mints  []map[string]any
	level  int
}

func (f *projectGitLab) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	answer := func(status int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		if v != nil {
			_ = json.NewEncoder(w).Encode(v)
		}
	}
	auth := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if auth == "" {
		auth = r.Header.Get("PRIVATE-TOKEN")
	}
	if auth != glServiceToken {
		answer(401, map[string]string{"message": "401 Unauthorized"})
		return
	}
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	path := strings.TrimPrefix(r.URL.EscapedPath(), "/api/v4")
	switch {
	case path == "/version":
		answer(200, map[string]string{"version": "19.1.0"})
	case path == "/personal_access_tokens/self":
		answer(200, map[string]any{"id": 41, "scopes": []string{"api", "self_rotate"}, "active": true})
	case path == "/projects/acme%2Fteam%2Fapp":
		answer(200, map[string]any{"id": 500, "path_with_namespace": "acme/team/app", "default_branch": "main",
			"permissions": map[string]any{"group_access": map[string]int{"access_level": f.level}}})
	case path == "/projects/acme%2Fteam%2Fapp/repository/files/AGENTS.md":
		answer(200, map[string]string{"file_name": "AGENTS.md"})
	case path == "/projects/500/hooks" && r.Method == "GET":
		out := []map[string]any{}
		for id, h := range f.hooks {
			out = append(out, map[string]any{"id": id, "url": h["url"]})
		}
		answer(200, out)
	case path == "/projects/500/hooks" && r.Method == "POST":
		f.nextID++
		f.hooks[f.nextID] = body
		answer(201, map[string]any{"id": f.nextID})
	case strings.HasPrefix(path, "/projects/500/hooks/") && r.Method == "DELETE":
		for id := range f.hooks {
			if path == "/projects/500/hooks/"+itoa(int64(id)) {
				delete(f.hooks, id)
			}
		}
		answer(204, nil)
	case path == "/projects/500/access_tokens" && r.Method == "POST":
		f.mints = append(f.mints, body)
		answer(201, map[string]any{"id": 77, "token": "run-pat-77", "expires_at": body["expires_at"]})
	default:
		answer(404, map[string]string{"message": "404 Not Found"})
	}
}

func (rig *targetRig) withGitLab(t *testing.T) *projectGitLab {
	t.Helper()
	gl := &projectGitLab{hooks: map[int]map[string]any{}, level: 40}
	srv := httptest.NewServer(gl)
	t.Cleanup(srv.Close)
	rig.conns.infos["gl-team"] = worktarget.ConnectionInfo{ID: "gl-team", Kind: "gitlab", Instance: "gitlab-main", State: "active", Runtime: true, CanMint: true}
	rig.conns.creds["gl-team"] = provider.ConnectionCredential{Port: "forge", Method: "gitlab_service_account", Token: glServiceToken}
	rig.conns.infos["gl-person"] = worktarget.ConnectionInfo{ID: "gl-person", Kind: "gitlab", Instance: "gitlab-main", State: "active", Runtime: true}
	rig.conns.creds["gl-person"] = provider.ConnectionCredential{Port: "forge", Method: "oauth", Token: glServiceToken}
	rig.svc.Connectors["gitlab"] = provider.Connector{Grants: gitlab.Connector{}, Hooks: gitlab.Connector{}}
	rig.svc.Instances["gitlab-main"] = provider.ConnectionInstance{ID: "gitlab-main", Kind: "gitlab", URL: srv.URL}
	rig.svc.PublicWebhookBase = "https://ploeg.example"
	return gl
}

func TestAGitLabServiceAccountRegistersATargetWithItsWebhookAndMintsByProjectID(t *testing.T) {
	rig := newTargetRig(t, nil, AttributionProven)
	gl := rig.withGitLab(t)
	body := `{"connection":"gl-team","repository":"acme/team/app","key":"team-app"}`
	code, out := rig.call("POST", targetsPath+"?dryRun=true", rig.me(), body)
	if code != 200 || out["wouldRegister"] != true || len(gl.hooks) != 0 {
		t.Fatalf("dry run = %d %v, hooks %d; a dry run registers nothing", code, out, len(gl.hooks))
	}
	target := rig.register(t, body)
	if target["repositoryId"] != "500" || target["forge"] != "gitlab-main" {
		t.Fatalf("target = %v", target)
	}
	if len(gl.hooks) != 1 {
		t.Fatalf("hooks = %v", gl.hooks)
	}
	for _, h := range gl.hooks {
		if h["url"] != "https://ploeg.example/webhooks/forge/gitlab?connection=gl-team" || h["signing_token"] != glHookKey ||
			h["merge_requests_events"] != true || h["note_events"] != true || h["pipeline_events"] != true || h["push_events"] != true {
			t.Fatalf("hook = %v", h)
		}
	}
	rig.server.ForgeCreds = &forgebroker.WithConnections{Resolve: func(context.Context, string) (forgebroker.Broker, error) {
		return &forgebroker.GitLab{APIURL: rig.svc.Instances["gitlab-main"].URL + "/api/v4", Token: glServiceToken}, nil
	}}
	pinned := &work.Target{Forge: "gitlab-main", Owner: "acme/team", Repo: "app", BaseBranch: "main"}
	rig.server.mintForgeCredential(context.Background(), "silver", &store.ClaimedRun{Item: work.WorkItem{Target: pinned}, RunToken: "run-team-app-1", Writes: true})
	gl.mu.Lock()
	mints := append([]map[string]any{}, gl.mints...)
	gl.mu.Unlock()
	if len(mints) != 1 || mints[0]["access_level"] != float64(30) || mints[0]["name"] != "ploeg-run-run-team-app" {
		t.Fatalf("mints = %v; a Run on a GitLab target is minted a Developer token on project 500", mints)
	}
	if code, out := rig.call("DELETE", targetsPath+"/team-app", rig.me(), ""); code != 204 {
		t.Fatalf("remove = %d %v", code, out)
	}
	if len(gl.hooks) != 0 {
		t.Fatalf("hooks after removal = %v", gl.hooks)
	}
	for _, b := range rig.bodies {
		if strings.Contains(string(b), glServiceToken) || strings.Contains(string(b), glHookKey) {
			t.Fatalf("an answer carries a secret: %s", b)
		}
	}
}

func TestAGitLabTargetIsRefusedForAPersonsConnectionADeveloperOrNoWebhookBase(t *testing.T) {
	rig := newTargetRig(t, nil, AttributionProven)
	gl := rig.withGitLab(t)
	code, out := rig.call("POST", targetsPath, rig.me(), `{"connection":"gl-person","repository":"acme/team/app"}`)
	if code != 422 || errCode(out) != "capability_missing" {
		t.Fatalf("an OAuth connection = %d %v", code, out)
	}
	gl.level = 30
	code, out = rig.call("POST", targetsPath, rig.me(), `{"connection":"gl-team","repository":"acme/team/app"}`)
	if code != 422 || errCode(out) != "grant_insufficient" {
		t.Fatalf("a Developer service account = %d %v; it cannot create project access tokens", code, out)
	}
	gl.level = 40
	rig.svc.PublicWebhookBase = ""
	code, out = rig.call("POST", targetsPath, rig.me(), `{"connection":"gl-team","repository":"acme/team/app"}`)
	if code != 422 || errCode(out) != "webhook_base_missing" || len(gl.hooks) != 0 {
		t.Fatalf("no webhook base = %d %v", code, out)
	}
}
