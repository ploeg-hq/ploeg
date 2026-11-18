package httpapi

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/config"
	"github.com/ploeg-hq/ploeg/pkg/forgebroker"
	"github.com/ploeg-hq/ploeg/pkg/provider"
	"github.com/ploeg-hq/ploeg/pkg/provider/forgejo"
	"github.com/ploeg-hq/ploeg/pkg/provider/github"
	"github.com/ploeg-hq/ploeg/pkg/store"
	"github.com/ploeg-hq/ploeg/pkg/target"
	"github.com/ploeg-hq/ploeg/pkg/work"
	"github.com/ploeg-hq/ploeg/pkg/worktarget"
)

type grantRepo struct {
	ID       int64
	FullName string
	Archived bool
	Mirror   bool
	Agents   bool
	Push     bool
}

// grantForge is a GitHub App API and a Forgejo API in one: installations
// 77 (write) and 78 (read-only) and an OAuth token for Forgejo.
type grantForge struct {
	mu      sync.Mutex
	repos   map[string]*grantRepo
	granted map[string]map[string]bool
	perms   map[string]map[string]string
	moved   map[string]string
	mints   []string
}

func newGrantForge() *grantForge {
	return &grantForge{repos: map[string]*grantRepo{}, granted: map[string]map[string]bool{"77": {}, "78": {}},
		perms: map[string]map[string]string{
			"77": {"contents": "write", "pull_requests": "write", "metadata": "read", "checks": "read", "statuses": "read"},
			"78": {"contents": "read", "pull_requests": "read", "metadata": "read"},
		}, moved: map[string]string{}}
}

func (f *grantForge) add(installations []string, r grantRepo) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.repos[strings.ToLower(r.FullName)] = &r
	for _, i := range installations {
		f.granted[i][strings.ToLower(r.FullName)] = true
	}
}

func (f *grantForge) grant(installation, repo string, on bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.granted[installation][strings.ToLower(repo)] = on
}

func (f *grantForge) lookup(path string) (*grantRepo, bool) {
	if to, ok := f.moved[strings.ToLower(path)]; ok {
		path = to
	}
	r, ok := f.repos[strings.ToLower(path)]
	return r, ok
}

func (f *grantForge) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	answer := func(v any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(v)
	}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	auth := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	switch {
	case r.Method == http.MethodGet && len(parts) == 3 && parts[0] == "app" && parts[1] == "installations":
		answer(map[string]any{"id": parts[2], "permissions": f.perms[parts[2]]})
	case r.Method == http.MethodPost && len(parts) == 4 && parts[0] == "app" && parts[3] == "access_tokens":
		var body struct {
			Repositories []string          `json:"repositories"`
			Permissions  map[string]string `json:"permissions"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		for _, name := range body.Repositories {
			found := false
			for full, on := range f.granted[parts[2]] {
				if on && strings.HasSuffix(full, "/"+strings.ToLower(name)) {
					found = true
				}
			}
			if !found {
				w.WriteHeader(http.StatusUnprocessableEntity)
				return
			}
		}
		f.mints = append(f.mints, parts[2]+":"+strings.Join(body.Repositories, ","))
		w.WriteHeader(http.StatusCreated)
		answer(map[string]any{"token": "ghs_installation_" + parts[2], "expires_at": "2099-01-01T00:00:00Z", "permissions": body.Permissions})
	case r.Method == http.MethodDelete && r.URL.Path == "/installation/token":
		w.WriteHeader(http.StatusNoContent)
	case r.Method == http.MethodGet && len(parts) >= 3 && parts[0] == "repos":
		f.github(w, parts, auth, answer)
	case r.Method == http.MethodGet && len(parts) >= 5 && parts[0] == "api" && parts[2] == "repos":
		f.forgejo(w, parts[2:], auth, answer)
	default:
		http.NotFound(w, r)
	}
}

func (f *grantForge) github(w http.ResponseWriter, parts []string, auth string, answer func(any)) {
	installation, ok := strings.CutPrefix(auth, "ghs_installation_")
	path := strings.ToLower(parts[1] + "/" + parts[2])
	repo, exists := f.lookup(path)
	if !ok || !exists || !f.granted[installation][strings.ToLower(repo.FullName)] {
		http.NotFound(w, nil)
		return
	}
	if len(parts) == 5 && parts[3] == "contents" {
		if !repo.Agents {
			http.NotFound(w, nil)
			return
		}
		answer(map[string]any{"name": "AGENTS.md"})
		return
	}
	var mirror any
	if repo.Mirror {
		mirror = "https://elsewhere.example/x.git"
	}
	answer(map[string]any{"id": repo.ID, "full_name": repo.FullName, "default_branch": "main", "archived": repo.Archived, "mirror_url": mirror})
}

func (f *grantForge) forgejo(w http.ResponseWriter, parts []string, auth string, answer func(any)) {
	if auth != "forgejo-oauth-token" {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	repo, exists := f.lookup(parts[1] + "/" + parts[2])
	if !exists {
		http.NotFound(w, nil)
		return
	}
	if len(parts) == 5 && parts[3] == "contents" {
		if !repo.Agents {
			http.NotFound(w, nil)
			return
		}
		answer(map[string]any{"name": "AGENTS.md"})
		return
	}
	answer(map[string]any{"id": repo.ID, "full_name": repo.FullName, "default_branch": "main", "archived": repo.Archived, "mirror": repo.Mirror,
		"permissions": map[string]bool{"pull": true, "push": repo.Push}})
}

type fakeTargetConnections struct {
	infos    map[string]worktarget.ConnectionInfo
	creds    map[string]provider.ConnectionCredential
	installs map[string][]string
}

func (c *fakeTargetConnections) ForgeConnection(_ context.Context, id string) (worktarget.ConnectionInfo, bool, error) {
	info, ok := c.infos[id]
	return info, ok, nil
}

func (c *fakeTargetConnections) Credential(_ context.Context, id string) (provider.ConnectionCredential, error) {
	return c.creds[id], nil
}

func (c *fakeTargetConnections) ConnectionsOfInstallation(_ context.Context, appID, installationID string) ([]string, error) {
	return c.installs[appID+"/"+installationID], nil
}

type targetRig struct {
	t        *testing.T
	forge    *grantForge
	conns    *fakeTargetConnections
	svc      *worktarget.Service
	server   *Server
	handler  http.Handler
	issuer   *personIssuer
	consumer string
	reader   string
	key      string
	bodies   [][]byte
}

func appKeyPEM(t *testing.T) string {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
}

func resetTargets(t *testing.T) {
	t.Helper()
	reset(t)
	for _, table := range []string{"work_targets", "tracker_bindings"} {
		if _, err := testPool.Exec(context.Background(), "DELETE FROM "+table); err != nil {
			t.Fatal(err)
		}
	}
}

func newTargetRig(t *testing.T, registration *config.TargetRegistration, mode AttributionMode) *targetRig {
	t.Helper()
	resetTargets(t)
	forge := newGrantForge()
	srv := httptest.NewServer(forge)
	t.Cleanup(srv.Close)
	forge.add([]string{"77", "78"}, grantRepo{ID: 101, FullName: "acme/app", Agents: true})
	forge.add([]string{"77"}, grantRepo{ID: 102, FullName: "acme/archived", Archived: true, Agents: true})
	forge.add([]string{"77"}, grantRepo{ID: 103, FullName: "acme/mirror", Mirror: true, Agents: true})
	forge.add([]string{"77"}, grantRepo{ID: 104, FullName: "acme/bare"})
	forge.add(nil, grantRepo{ID: 105, FullName: "acme/elsewhere", Agents: true})
	forge.add([]string{"77"}, grantRepo{ID: 106, FullName: "acme/static", Agents: true})
	forge.add(nil, grantRepo{ID: 201, FullName: "team/service", Agents: true, Push: true})
	forge.add(nil, grantRepo{ID: 202, FullName: "team/readonly", Agents: true})

	key := appKeyPEM(t)
	conns := &fakeTargetConnections{
		infos: map[string]worktarget.ConnectionInfo{
			"gh-acme":  {ID: "gh-acme", Kind: "github", Instance: "github", State: "active", Runtime: true},
			"gh-ro":    {ID: "gh-ro", Kind: "github", Instance: "github", State: "active", Runtime: true},
			"fj-team":  {ID: "fj-team", Kind: "forgejo", Instance: "forgejo-local", State: "active", Runtime: true},
			"plain":    {ID: "plain", Kind: "plain", Instance: "plain", State: "active", Runtime: true},
			"github":   {ID: "github", Kind: "github", Instance: "github", State: "active"},
			"gh-ended": {ID: "gh-ended", Kind: "github", Instance: "github", State: "invalid", Runtime: true},
		},
		creds: map[string]provider.ConnectionCredential{
			"gh-acme": {Port: "forge", AppID: "1001", PrivateKey: key, InstallationID: "77"},
			"gh-ro":   {Port: "forge", AppID: "1001", PrivateKey: key, InstallationID: "78"},
			"fj-team": {Port: "forge", Token: "forgejo-oauth-token"},
		},
		installs: map[string][]string{"1001/77": {"gh-acme"}, "1001/78": {"gh-ro"}},
	}
	if registration == nil {
		registration = &config.TargetRegistration{Enabled: true, Connections: []string{"*"}}
	}
	static := map[string]work.Target{"app-static": {Owner: "acme", Repo: "static"}}
	gate := target.NewGate(map[string]work.Target{}, nil)
	svc := &worktarget.Service{Store: testStore, Config: registration, Connections: conns,
		Connectors: map[string]provider.Connector{"github": {Grants: github.Connector{}}, "forgejo": {Grants: forgejo.Connector{}}, "plain": {}},
		Instances: map[string]provider.ConnectionInstance{
			"github":        {ID: "github", Kind: "github", URL: srv.URL, APIURL: srv.URL},
			"forgejo-local": {ID: "forgejo-local", Kind: "forgejo", URL: srv.URL, APIURL: srv.URL + "/api/v1"},
		},
		Static: static, DefaultForge: "github", Gate: gate, Log: slog.New(slog.DiscardHandler)}
	if err := svc.Rebuild(context.Background()); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal([]map[string]any{
		{"name": "workbench", "tokenEnv": "T_ONE", "teams": []string{"*"}, "registerTargets": true, "clientIds": []string{personClient}},
		{"name": "reader", "tokenEnv": "T_TWO", "teams": []string{"*"}, "clientIds": []string{"other-app"}},
	})
	tokens := map[string]string{"T_ONE": strings.Repeat("a", 40), "T_TWO": strings.Repeat("b", 40)}
	consumers, err := ParseOperatorConsumers(string(raw), func(k string) (string, bool) { v, ok := tokens[k]; return v, ok })
	if err != nil {
		t.Fatal(err)
	}
	is := newPersonIssuer(t)
	personTokens, err := NewPersonTokens(PersonTokenConfig{Issuer: is.url, AllowLoopbackHTTP: true, Audience: personAudience, Now: func() time.Time { return personNow }}, consumers)
	if err != nil {
		t.Fatal(err)
	}
	server := &Server{Store: testStore, Log: slog.New(slog.DiscardHandler), Readiness: gate,
		OperatorConfig: OperatorConfig{Consumers: consumers, PersonTokens: personTokens, Attribution: mode},
		WorkTargets:    &WorkTargets{Service: svc}}
	return &targetRig{t: t, forge: forge, conns: conns, svc: svc, server: server, handler: server.operatorHandler(), issuer: is,
		consumer: tokens["T_ONE"], reader: tokens["T_TWO"], key: key}
}

func (rig *targetRig) person(client string) string {
	c := rig.issuer.claims()
	c["azp"] = client
	return rig.issuer.sign(c)
}

func (rig *targetRig) call(method, path, bearer, body string, headers ...string) (int, map[string]any) {
	rig.t.Helper()
	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	r := httptest.NewRequest(method, path, rdr)
	r.Header.Set("Authorization", "Bearer "+bearer)
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	for i := 0; i+1 < len(headers); i += 2 {
		r.Header.Set(headers[i], headers[i+1])
	}
	w := httptest.NewRecorder()
	rig.handler.ServeHTTP(w, r)
	rig.bodies = append(rig.bodies, w.Body.Bytes())
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	if w.Body.Len() > 0 {
		validateOperatorSchema(rig.t, w.Body.Bytes())
	}
	return w.Code, out
}

func (rig *targetRig) me() string { return rig.person(personClient) }

func (rig *targetRig) rows(t *testing.T, query string, args ...any) int {
	t.Helper()
	var n int
	if err := testPool.QueryRow(context.Background(), query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

const targetsPath = "/api/v1/operator/targets"

func TestRuntimeTargetRegisterRepeatConflictReplaceAndRemove(t *testing.T) {
	rig := newTargetRig(t, nil, AttributionProven)
	body := `{"connection":"gh-acme","repository":"acme/app","key":"acme-app","label":"App"}`

	code, out := rig.call("POST", targetsPath+"?dryRun=true", rig.me(), body)
	if code != 200 || out["wouldRegister"] != true || out["prepareAvailable"] != false {
		t.Fatalf("dry run = %d %v", code, out)
	}
	if n := rig.rows(t, `SELECT count(*) FROM work_targets`); n != 0 {
		t.Fatalf("a dry run stored %d targets", n)
	}
	if n := rig.rows(t, `SELECT count(*) FROM audit_log WHERE action LIKE 'work_target.%'`); n != 0 {
		t.Fatalf("a dry run wrote %d audit rows", n)
	}

	code, out = rig.call("POST", targetsPath, rig.me(), body)
	if code != 201 || out["created"] != true {
		t.Fatalf("register = %d %v", code, out)
	}
	created := out["target"].(map[string]any)
	if created["source"] != "runtime" || created["state"] != "active" || created["forge"] != "github" || created["branch"] != "main" ||
		created["repositoryId"] != "101" || created["readiness"].(map[string]any)["state"] != "ready" {
		t.Fatalf("registered target = %v", created)
	}
	if _, ok := rig.server.Readiness.Target("acme-app"); !ok {
		t.Fatal("the registered target is not in the readiness gate")
	}
	if _, ok := rig.svc.ActiveRuntimeTargets()["acme-app"]; !ok {
		t.Fatal("the registered target is not in the effective registry")
	}

	code, out = rig.call("POST", targetsPath, rig.me(), body)
	if code != 200 || out["created"] != false {
		t.Fatalf("repeat = %d %v", code, out)
	}
	if n := rig.rows(t, `SELECT count(*) FROM work_targets`); n != 1 {
		t.Fatalf("rows = %d after a repeated registration, want 1", n)
	}

	code, out = rig.call("POST", targetsPath, rig.me(), `{"connection":"gh-acme","repository":"acme/app","key":"acme-app","label":"Other"}`)
	if code != 409 || errCode(out) != "target_exists" || out["target"] == nil {
		t.Fatalf("same identity, other body = %d %v", code, out)
	}

	revision := strconv.FormatFloat(created["revision"].(float64), 'f', 0, 64)
	code, out = rig.call("PUT", targetsPath+"/acme-app", rig.me(), `{"label":"Renamed"}`, "If-Match", `"999999"`)
	if code != 412 || errCode(out) != "revision_stale" {
		t.Fatalf("stale If-Match = %d %v", code, out)
	}
	code, out = rig.call("PUT", targetsPath+"/acme-app", rig.me(), `{"label":"Renamed"}`)
	if code != 428 {
		t.Fatalf("no If-Match = %d %v", code, out)
	}
	code, out = rig.call("PUT", targetsPath+"/acme-app", rig.me(), `{"label":"Renamed"}`, "If-Match", `"`+revision+`"`)
	if code != 200 || out["target"].(map[string]any)["label"] != "Renamed" {
		t.Fatalf("replace = %d %v", code, out)
	}

	code, out = rig.call("GET", targetsPath, rig.consumer, "")
	if code != 200 || len(out["targets"].([]any)) != 2 {
		t.Fatalf("list = %d %v", code, out)
	}

	if code, out = rig.call("DELETE", targetsPath+"/acme-app", rig.me(), ""); code != 204 {
		t.Fatalf("remove = %d %v", code, out)
	}
	if code, out = rig.call("DELETE", targetsPath+"/acme-app", rig.me(), ""); code != 204 {
		t.Fatalf("remove again = %d %v", code, out)
	}
	if _, ok := rig.server.Readiness.Target("acme-app"); ok {
		t.Fatal("a removed target stays in the readiness gate")
	}
	if n := rig.rows(t, `SELECT count(*) FROM work_targets WHERE removed_at IS NOT NULL`); n != 1 {
		t.Fatalf("removed rows = %d; a removal never deletes the row", n)
	}
	for _, action := range []string{"work_target.registered", "work_target.updated", "work_target.removed"} {
		if n := rig.rows(t, `SELECT count(*) FROM audit_log WHERE action = $1 AND actor = $2 AND detail->>'consumer' = 'workbench'`, action, rig.issuer.personID()); n != 1 {
			t.Errorf("%s audited %d times for the proven person, want 1", action, n)
		}
	}
}

func TestRuntimeTargetRefusals(t *testing.T) {
	rig := newTargetRig(t, nil, AttributionProven)
	cases := []struct {
		name, body string
		status     int
		code       string
	}{
		{"outside the grant", `{"connection":"gh-acme","repository":"acme/elsewhere"}`, 422, "not_granted"},
		{"read-only grant", `{"connection":"gh-ro","repository":"acme/app"}`, 422, "grant_insufficient"},
		{"archived", `{"connection":"gh-acme","repository":"acme/archived"}`, 422, "target_not_ready"},
		{"mirror", `{"connection":"gh-acme","repository":"acme/mirror"}`, 422, "target_not_ready"},
		{"forgejo read-only", `{"connection":"fj-team","repository":"team/readonly"}`, 422, "grant_insufficient"},
		{"forgejo unknown repository", `{"connection":"fj-team","repository":"team/nothing"}`, 422, "repository_not_found"},
		{"adapter without RepositoryGrantProber", `{"connection":"plain","repository":"acme/app"}`, 422, "capability_missing"},
		{"static connection", `{"connection":"github","repository":"acme/app"}`, 422, "registration_not_accepted"},
		{"unusable connection", `{"connection":"gh-ended","repository":"acme/app"}`, 422, "connection_unusable"},
		{"unknown connection", `{"connection":"nope","repository":"acme/app"}`, 404, "unknown_connection"},
		{"key outside the name pattern", `{"connection":"gh-acme","repository":"acme/app","key":"-bad key"}`, 422, "probe_failed"},
		{"key with capitals", `{"connection":"gh-acme","repository":"acme/app","key":"Acme-App"}`, 422, "probe_failed"},
		{"key equal to a static key", `{"connection":"gh-acme","repository":"acme/app","key":"app-static"}`, 409, "key_taken"},
		{"repository a static target registers", `{"connection":"gh-acme","repository":"acme/static"}`, 409, "conflicts_with_static"},
		{"raw URL", `{"connection":"gh-acme","repository":"https://github.com/acme/app"}`, 400, "invalid_request"},
		{"clone address", `{"connection":"gh-acme","repository":"git@github.com:acme/app.git"}`, 400, "invalid_request"},
		{"token-shaped label", `{"connection":"gh-acme","repository":"acme/app","label":"ghp_abcdef"}`, 400, "invalid_request"},
		{"unknown field", `{"connection":"gh-acme","repository":"acme/app","forge":"github"}`, 400, "invalid_request"},
	}
	for _, c := range cases {
		code, out := rig.call("POST", targetsPath, rig.me(), c.body)
		if code != c.status || errCode(out) != c.code {
			t.Errorf("%s: %d %v, want %d %s", c.name, code, out, c.status, c.code)
		}
	}
	if n := rig.rows(t, `SELECT count(*) FROM work_targets`); n != 0 {
		t.Fatalf("a refused registration stored %d targets", n)
	}
	if n := rig.rows(t, `SELECT count(*) FROM audit_log WHERE action = 'work_target.refused'`); n == 0 {
		t.Fatal("refusals after the gates are not audited")
	}

	code, out := rig.call("POST", targetsPath, rig.me(), `{"connection":"gh-acme","repository":"acme/bare"}`)
	if code != 201 {
		t.Fatalf("missing only its instruction file = %d %v; ADR-0194 registers it not ready", code, out)
	}
	readiness := out["target"].(map[string]any)["readiness"].(map[string]any)
	if readiness["state"] != "not_ready" || readiness["prepareAvailable"] != true {
		t.Fatalf("readiness = %v, want not_ready with prepareAvailable", readiness)
	}
	code, out = rig.call("POST", targetsPath+"?dryRun=true", rig.me(), `{"connection":"fj-team","repository":"team/service","key":"forgejo:team:service"}`)
	if code != 200 || out["wouldRegister"] != true {
		t.Fatalf("forgejo dry run = %d %v", code, out)
	}
	code, out = rig.call("POST", targetsPath, rig.me(), `{"connection":"gh-acme","repository":"acme/app","key":"github:acme:bare"}`)
	if code != 409 || errCode(out) != "key_taken" {
		t.Fatalf("a key another runtime target holds = %d %v", code, out)
	}
}

func TestRuntimeTargetGatesRefuseWithoutPermissionOptInOrProvenPerson(t *testing.T) {
	body := `{"connection":"gh-acme","repository":"acme/app"}`
	for _, mode := range []AttributionMode{AttributionAsserted, AttributionProven} {
		rig := newTargetRig(t, nil, mode)
		code, out := rig.call("POST", targetsPath, rig.consumer, body, "X-Ploeg-Actor", "person-1")
		if code != 403 || errCode(out) != reasonPersonTokenRequired {
			t.Errorf("%s: asserted attribution = %d %v, want 403 %s", mode, code, out, reasonPersonTokenRequired)
		}
		code, out = rig.call("POST", targetsPath, rig.person("other-app"), body)
		if code != 403 || errCode(out) != "permission_required" {
			t.Errorf("%s: a consumer without registerTargets = %d %v", mode, code, out)
		}
	}
	rig := newTargetRig(t, &config.TargetRegistration{}, AttributionProven)
	if code, out := rig.call("POST", targetsPath, rig.me(), body); code != 422 || errCode(out) != "registration_not_accepted" {
		t.Errorf("an installation that did not opt in = %d %v", code, out)
	}
	rig = newTargetRig(t, &config.TargetRegistration{Enabled: true, Connections: []string{"fj-team"}}, AttributionProven)
	if code, out := rig.call("POST", targetsPath, rig.me(), body); code != 422 || errCode(out) != "registration_not_accepted" {
		t.Errorf("a connection targetRegistration.connections does not name = %d %v", code, out)
	}
	rig = newTargetRig(t, &config.TargetRegistration{Enabled: true, Connections: []string{"*"}, RequireClaims: map[string][]string{"groups": {"ploeg-operators"}}}, AttributionProven)
	if code, out := rig.call("POST", targetsPath, rig.me(), body); code != 403 || errCode(out) != "person_claim_required" {
		t.Errorf("a person without operator authority = %d %v", code, out)
	}
	if n := rig.rows(t, `SELECT count(*) FROM work_targets`); n != 0 {
		t.Fatalf("a refused caller stored %d targets", n)
	}
}

func TestRuntimeTargetShadowedByALaterStaticEntryAndRestoredWhenItGoes(t *testing.T) {
	rig := newTargetRig(t, nil, AttributionProven)
	if code, out := rig.call("POST", targetsPath, rig.me(), `{"connection":"gh-acme","repository":"acme/app","key":"acme-app"}`); code != 201 {
		t.Fatalf("register = %d %v", code, out)
	}
	ctx := context.Background()
	rig.svc.Static = map[string]work.Target{"app-static": {Owner: "acme", Repo: "static"}, "app": {Owner: "acme", Repo: "app"}}
	for range 2 {
		if err := rig.svc.Rebuild(ctx); err != nil {
			t.Fatal(err)
		}
	}
	live := rig.svc.Live()
	if len(live) != 1 || live[0].State != store.WorkTargetShadowed || live[0].ShadowedBy == nil || live[0].ShadowedBy.Entry != "targets.app" {
		t.Fatalf("live = %+v, want shadowed by targets.app", live)
	}
	if _, ok := rig.svc.ActiveRuntimeTargets()["acme-app"]; ok {
		t.Fatal("a shadowed target stays in the registry")
	}
	if n := rig.rows(t, `SELECT count(*) FROM audit_log WHERE action = 'work_target.shadowed'`); n != 1 {
		t.Fatalf("shadowed events = %d, want 1", n)
	}
	rig.svc.Static = map[string]work.Target{"app-static": {Owner: "acme", Repo: "static"}}
	if err := rig.svc.Rebuild(ctx); err != nil {
		t.Fatal(err)
	}
	if _, ok := rig.svc.ActiveRuntimeTargets()["acme-app"]; !ok {
		t.Fatal("the target did not return when the static entry went away")
	}
	if n := rig.rows(t, `SELECT count(*) FROM audit_log WHERE action = 'work_target.unshadowed'`); n != 1 {
		t.Fatalf("unshadowed events = %d, want 1", n)
	}
}

func TestAGrantRemovalInvalidatesTheTargetAndItsClaimMintsNothing(t *testing.T) {
	rig := newTargetRig(t, nil, AttributionProven)
	if code, out := rig.call("POST", targetsPath, rig.me(), `{"connection":"gh-acme","repository":"acme/app","key":"acme-app"}`); code != 201 {
		t.Fatalf("register = %d %v", code, out)
	}
	ctx := context.Background()
	pinned := &work.Target{Forge: "github", Owner: "acme", Repo: "app", BaseBranch: "main"}
	minted := 0
	rig.server.ForgeCreds = &forgebroker.WithConnections{Resolve: func(_ context.Context, connection string) (forgebroker.Broker, error) {
		minted++
		if connection != "gh-acme" {
			t.Errorf("minted through %q, want the target's own connection", connection)
		}
		return forgebroker.Static{Token: "x"}, nil
	}}
	if conn, err := rig.server.mintConnection(ctx, pinned); err != nil || conn != "gh-acme" {
		t.Fatalf("an active target mints through %q, %v", conn, err)
	}

	rig.forge.grant("77", "acme/app", false)
	if err := rig.svc.HandleGrant(ctx, provider.GrantEvent{AppID: "1001", InstallationID: "77", Event: "installation_repositories", Action: "removed",
		Removed: []string{"acme/app"}, RemovedIDs: []string{"101"}}); err != nil {
		t.Fatal(err)
	}
	live := rig.svc.Live()
	if len(live) != 1 || live[0].State != store.WorkTargetInvalid || live[0].InvalidReason != store.WorkTargetGrantRevoked {
		t.Fatalf("after removed = %+v, want invalid grant_revoked", live)
	}
	claim, err := rig.server.runtimeTargetClaim(ctx, pinned)
	if err != nil || claim.Refusal != "target_invalid" {
		t.Fatalf("claim = %+v, %v; want target_invalid", claim, err)
	}
	if _, err := rig.server.mintConnection(ctx, pinned); err == nil {
		t.Fatal("an invalid target's mint was not refused")
	}
	cred, ok := rig.server.mintForgeCredential(ctx, "silver", &store.ClaimedRun{Item: work.WorkItem{Target: pinned}, RunToken: "run-none", Writes: true})
	if ok || cred.Token != "" || minted != 0 {
		t.Fatalf("an invalid target's run got a credential (%v, %d mints)", ok, minted)
	}

	if err := rig.svc.HandleGrant(ctx, provider.GrantEvent{AppID: "1001", InstallationID: "77", Event: "installation_repositories", Action: "added",
		Added: []string{"acme/app"}, AddedIDs: []string{"101"}}); err != nil {
		t.Fatal(err)
	}
	if live := rig.svc.Live(); live[0].State != store.WorkTargetInvalid {
		t.Fatalf("an added delivery restored %+v before the forge grants it again", live[0])
	}
	rig.forge.grant("77", "acme/app", true)
	if err := rig.svc.HandleGrant(ctx, provider.GrantEvent{AppID: "1001", InstallationID: "77", Event: "installation_repositories", Action: "added",
		Added: []string{"acme/app"}, AddedIDs: []string{"101"}}); err != nil {
		t.Fatal(err)
	}
	if live := rig.svc.Live(); live[0].State != store.WorkTargetActive {
		t.Fatalf("after a passing probe = %+v, want active", live[0])
	}
	if n := rig.rows(t, `SELECT count(*) FROM audit_log WHERE action = 'work_target.restored' AND actor = 'connection:gh-acme'`); n != 1 {
		t.Fatalf("restored events = %d, want 1 attributed to the connection", n)
	}
	if err := rig.svc.HandleGrant(ctx, provider.GrantEvent{AppID: "1001", InstallationID: "77", Event: "installation", Action: "suspend"}); err != nil {
		t.Fatal(err)
	}
	if live := rig.svc.Live(); live[0].InvalidReason != store.WorkTargetInstallationSuspended {
		t.Fatalf("after suspend = %+v", live[0])
	}
}

func TestTheReconcilerCatchesAWithdrawnGrantAndNeverFollowsARename(t *testing.T) {
	rig := newTargetRig(t, nil, AttributionProven)
	ctx := context.Background()
	rig.forge.repos["team/service"].Push = true
	if code, out := rig.call("POST", targetsPath, rig.me(), `{"connection":"fj-team","repository":"team/service","key":"service"}`); code != 201 {
		t.Fatalf("register forgejo = %d %v", code, out)
	}
	if code, out := rig.call("POST", targetsPath, rig.me(), `{"connection":"gh-acme","repository":"acme/app","key":"acme-app"}`); code != 201 {
		t.Fatalf("register github = %d %v", code, out)
	}
	rig.forge.mu.Lock()
	rig.forge.repos["team/service"].Push = false
	rig.forge.repos["acme/app"].FullName = "acme/app-renamed"
	rig.forge.repos["acme/app-renamed"] = rig.forge.repos["acme/app"]
	rig.forge.granted["77"]["acme/app-renamed"] = true
	rig.forge.mu.Unlock()
	rig.svc.Reconcile(ctx)
	got := map[string]string{}
	for _, tg := range rig.svc.Live() {
		got[tg.Key] = tg.State + "/" + tg.InvalidReason
	}
	if got["service"] != "invalid/grant_revoked" || got["acme-app"] != "invalid/repository_moved" {
		t.Fatalf("after reconcile = %v", got)
	}
}

func TestAReplicaThatHasNotRebuiltRefusesTheClaimOfARemovedTarget(t *testing.T) {
	rig := newTargetRig(t, nil, AttributionProven)
	if code, out := rig.call("POST", targetsPath, rig.me(), `{"connection":"gh-acme","repository":"acme/app","key":"acme-app"}`); code != 201 {
		t.Fatalf("register = %d %v", code, out)
	}
	other := &worktarget.Service{Store: testStore, Config: rig.svc.Config, Connections: rig.conns, Static: rig.svc.Static, DefaultForge: "github", Log: slog.New(slog.DiscardHandler)}
	if err := other.Rebuild(context.Background()); err != nil {
		t.Fatal(err)
	}
	if code, out := rig.call("DELETE", targetsPath+"/acme-app", rig.me(), ""); code != 204 {
		t.Fatalf("remove = %d %v", code, out)
	}
	if _, cached := other.ActiveRuntimeTargets()["acme-app"]; !cached {
		t.Fatal("the other replica rebuilt already; the test needs a stale cache")
	}
	claim, err := other.ClaimTarget(context.Background(), work.Target{Forge: "github", Owner: "acme", Repo: "app"})
	if err != nil || claim.Refusal != "target_not_registered" {
		t.Fatalf("stale replica claim = %+v, %v; want target_not_registered from the row", claim, err)
	}
}

func TestRuntimeTargetRowsAnswersAndEventsCarryNoSecret(t *testing.T) {
	rig := newTargetRig(t, nil, AttributionProven)
	rig.call("POST", targetsPath, rig.me(), `{"connection":"gh-acme","repository":"acme/app","key":"acme-app"}`)
	rig.call("POST", targetsPath, rig.me(), `{"connection":"gh-acme","repository":"acme/elsewhere"}`)
	rig.call("GET", targetsPath, rig.consumer, "")
	secrets := []string{"ghs_installation_77", "forgejo-oauth-token", "PRIVATE KEY", strings.Split(rig.key, "\n")[1]}
	var rows []string
	for _, q := range []string{`SELECT row_to_json(t)::text FROM work_targets t`, `SELECT row_to_json(l)::text FROM audit_log l`} {
		res, err := testPool.Query(context.Background(), q)
		if err != nil {
			t.Fatal(err)
		}
		for res.Next() {
			var s string
			_ = res.Scan(&s)
			rows = append(rows, s)
		}
		res.Close()
	}
	for _, secret := range secrets {
		for _, row := range rows {
			if strings.Contains(row, secret) {
				t.Errorf("a row carries a secret: %s", row)
			}
		}
		for _, b := range rig.bodies {
			if bytes.Contains(b, []byte(secret)) {
				t.Errorf("an answer carries a secret: %s", b)
			}
		}
	}
}
