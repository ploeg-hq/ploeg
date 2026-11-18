package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/clauderoutine"
	"github.com/ploeg-hq/ploeg/pkg/claudesession"
	"github.com/ploeg-hq/ploeg/pkg/secretstore/secretstoretest"
	"github.com/ploeg-hq/ploeg/pkg/store"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

const (
	routineTriggerToken      = "sk-ant-oat01-routine-trigger-fixture-0123456789"
	routineEnvironmentSecret = "sk-ant-envsecret-fixture-should-never-move"
	routineClaudeLogin       = "sk-ant-oat01-claude-login-fixture-should-never-move"
	routineTriggerPath       = "/v1/claude_code/routines/trig_01ABCDEFGHJKLMNOP/fire"
)

type fakeRoutineAPI struct {
	onFire func()
	mu     sync.Mutex
	status int
	auth   []string
	betas  []string
	texts  []string
	raw    [][]byte
}

func (f *fakeRoutineAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	body, _ := io.ReadAll(r.Body)
	var req struct {
		Text string `json:"text"`
	}
	_ = json.Unmarshal(body, &req)
	f.auth = append(f.auth, r.Header.Get("Authorization"))
	f.betas = append(f.betas, r.Header.Get("anthropic-beta"))
	f.texts = append(f.texts, req.Text)
	f.raw = append(f.raw, body)
	if f.onFire != nil {
		f.onFire()
		time.Sleep(20 * time.Millisecond)
	}
	if f.status != 0 && f.status != 200 {
		w.WriteHeader(f.status)
		return
	}
	n := len(f.texts)
	_ = json.NewEncoder(w).Encode(map[string]string{"type": "routine_fire",
		"claude_code_session_id":  fmt.Sprintf("session_01ROUTINE%08d", n),
		"claude_code_session_url": fmt.Sprintf("https://claude.ai/code/session_01ROUTINE%08d", n)})
}

func (f *fakeRoutineAPI) fires() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.texts)
}

type routineRig struct {
	t       *testing.T
	server  *Server
	handler http.Handler
	issuer  *personIssuer
	secrets *secretstoretest.Fake
	api     *fakeRoutineAPI
	apiURL  string
	logs    *bytes.Buffer
	bodies  [][]byte
}

func newRoutineRig(t *testing.T) *routineRig {
	t.Helper()
	reset(t)
	for _, table := range []string{"claude_routine_fires", "claude_routines", "claude_sessions"} {
		if _, err := testPool.Exec(context.Background(), "DELETE FROM "+table); err != nil {
			t.Fatal(err)
		}
	}
	consumers, _ := operatorTestConsumers(t, []string{"bronze", "brass"}, true)
	consumers[0].clientIDs = []string{personClient}
	is := newPersonIssuer(t)
	tokens, err := NewPersonTokens(PersonTokenConfig{Issuer: is.url, AllowLoopbackHTTP: true, Audience: personAudience, Now: func() time.Time { return personNow }}, consumers)
	if err != nil {
		t.Fatal(err)
	}
	api := &fakeRoutineAPI{}
	srv := httptest.NewServer(api)
	t.Cleanup(srv.Close)
	harnesses := clientCarriedHarnesses()
	builder := harnesses["bronze"]["builder"]
	builder.Carrier = store.CarrierRoutine
	harnesses["bronze"]["builder"] = builder
	harnesses["brass"] = map[string]RoleHarnesses{"builder": builder}
	logs := &bytes.Buffer{}
	secrets := secretstoretest.NewFake()
	s := &Server{
		Store: testStore, LeaseTTL: time.Minute, Log: slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
		WorkerSecurity: &WorkerSecurity{AllowLegacy: true},
		OperatorConfig: OperatorConfig{Consumers: consumers, PersonTokens: tokens, TeamHarnesses: harnesses},
		ClaudeSessions: &ClaudeSessions{Verifier: fakeSessionVerifier{}, GitUser: "agent-builder", ForgeURL: "https://forge.example/"},
		ClaudeRoutines: &ClaudeRoutines{Client: &clauderoutine.Client{BaseURL: srv.URL}, GatewayMode: true},
		SecretStore:    secrets,
	}
	return &routineRig{t: t, server: s, handler: s.Handler(), issuer: is, secrets: secrets, api: api, apiURL: srv.URL, logs: logs}
}

func (rig *routineRig) person() string { return rig.issuer.sign(rig.issuer.claims()) }

func (rig *routineRig) call(method, path, bearer, body string) (int, map[string]any) {
	rig.t.Helper()
	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	r := httptest.NewRequest(method, path, rdr)
	if bearer != "" {
		r.Header.Set("Authorization", "Bearer "+bearer)
	}
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	rig.handler.ServeHTTP(w, r)
	rig.bodies = append(rig.bodies, w.Body.Bytes())
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	if w.Body.Len() > 0 && strings.HasPrefix(path, "/api/v1/operator/") {
		validateOperatorSchema(rig.t, w.Body.Bytes())
	}
	return w.Code, out
}

func (rig *routineRig) register() {
	rig.t.Helper()
	body := fmt.Sprintf(`{"fireUrl":%q,"token":%q}`, rig.apiURL+routineTriggerPath, routineTriggerToken)
	if code, out := rig.call("PUT", "/api/v1/operator/claude-routines/bronze", rig.person(), body); code != 200 {
		rig.t.Fatalf("register = %d %v", code, out)
	}
}

func (rig *routineRig) work(externalID string) int64 {
	rig.t.Helper()
	return clientCarriedWork(rig.t, rig.server, externalID, "ryan", rig.issuer.personID())
}

func (rig *routineRig) workFor(team, externalID string) int64 {
	rig.t.Helper()
	ctx := context.Background()
	id, _, err := testStore.IngestAssigned(ctx, work.WorkItem{
		Provider: "vikunja", ExternalID: externalID, Team: team, Title: "Add a health check",
		Description: "Add a /healthz route that answers 200.",
		Target:      &work.Target{Forge: "forgejo", Owner: "webgrip", Repo: "fixture", BaseBranch: "development"},
	})
	if err != nil {
		rig.t.Fatal(err)
	}
	if err := testStore.RecordHandedBy(ctx, id, "ryan", rig.issuer.personID()); err != nil {
		rig.t.Fatal(err)
	}
	shiftID, err := testStore.OpenShift(ctx, id, team, "agent/vik-"+externalID, 10)
	if err != nil {
		rig.t.Fatal(err)
	}
	selection := RunSelections{Harnesses: rig.server.OperatorConfig.TeamHarnesses}.RoleSelection(team, "builder")
	if _, err := testStore.OpenRound(ctx, shiftID, 0, []store.Role{{Name: "builder", Writes: true, Cap: 1, Selection: selection}}); err != nil {
		rig.t.Fatal(err)
	}
	return id
}

func (rig *routineRig) fire(id int64) (int, map[string]any) {
	rig.t.Helper()
	return rig.call("POST", fmt.Sprintf("/api/v1/operator/work-items/%d/fire-routine", id), rig.person(), "")
}

func routineErrorCode(out map[string]any) string {
	e, _ := out["error"].(map[string]any)
	code, _ := e["code"].(string)
	return code
}

func TestARoutineIsRegisteredOnlyByAProvenPersonAndItsTokenIsWriteOnly(t *testing.T) {
	rig := newRoutineRig(t)
	consumerToken := ""
	consumers, token := operatorTestConsumers(t, []string{"bronze"}, true)
	rig.server.OperatorConfig.Consumers = append(rig.server.OperatorConfig.Consumers, consumers...)
	consumerToken = token
	body := fmt.Sprintf(`{"fireUrl":%q,"token":%q}`, rig.apiURL+routineTriggerPath, routineTriggerToken)
	r := httptest.NewRequest("PUT", "/api/v1/operator/claude-routines/bronze", strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer "+consumerToken)
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-Ploeg-Actor", "someone")
	w := httptest.NewRecorder()
	rig.handler.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatalf("a consumer without a proven person registered a routine: %d %s", w.Code, w.Body)
	}
	if code, out := rig.call("PUT", "/api/v1/operator/claude-routines/bronze", rig.person(),
		`{"fireUrl":"https://evil.example/v1/claude_code/routines/trig_01ABCDEFGHJK/fire","token":"`+routineTriggerToken+`"}`); code != 400 || routineErrorCode(out) != "invalid_fire_url" {
		t.Fatalf("a fire URL off Anthropic's host = %d %v", code, out)
	}
	if code, out := rig.call("PUT", "/api/v1/operator/claude-routines/silver", rig.person(), body); code != 404 {
		t.Fatalf("a Team outside the consumer's scope = %d %v", code, out)
	}
	rig.register()
	if rig.secrets.Puts != 1 {
		t.Fatalf("secret store writes = %d, want 1", rig.secrets.Puts)
	}
	code, out := rig.call("GET", "/api/v1/operator/claude-routines", rig.person(), "")
	if routines, _ := out["routines"].([]any); code != 200 || len(routines) != 1 {
		t.Fatalf("list = %d %v", code, out)
	}
	if n := countRows(t, `SELECT count(*) FROM claude_routines WHERE fire_url LIKE '%' || $1 || '%' OR secret_path LIKE '%' || $1 || '%'`, routineTriggerToken); n != 0 {
		t.Fatal("the trigger token reached the database")
	}
	rig.assertNoSecretLeft(t, "registration")
	if code, out := rig.call("DELETE", "/api/v1/operator/claude-routines/bronze", rig.person(), ""); code != 200 {
		t.Fatalf("remove = %d %v", code, out)
	}
	if len(rig.secrets.Values()) != 0 {
		t.Fatal("the removed routine's token is still in the secret store")
	}
}

func TestFiringARoutineStartsTheRunForItsProvenPersonAndRecordsTheSession(t *testing.T) {
	rig := newRoutineRig(t)
	rig.register()
	id := rig.work("2101")
	code, out := rig.fire(id)
	if code != 200 {
		t.Fatalf("fire = %d %v", code, out)
	}
	if rig.api.auth[0] != "Bearer "+routineTriggerToken || rig.api.betas[0] != clauderoutine.Beta {
		t.Fatalf("Anthropic saw auth %q beta %q", rig.api.auth[0], rig.api.betas[0])
	}
	text := rig.api.texts[0]
	for _, want := range []string{"VIK-2101", "Add a health check", "agent/vik-2101"} {
		if !strings.Contains(text, want) {
			t.Errorf("the fire text lacks %q:\n%s", want, text)
		}
	}
	if got := runStates(t, id); got != "running" {
		t.Fatalf("run = %q, want running", got)
	}
	if n := countRows(t, `SELECT count(*) FROM claude_sessions c JOIN agent_runs a ON a.id = c.run_id
		WHERE a.work_item_id = $1 AND c.session_id = '01ROUTINE00000001'`, id); n != 1 {
		t.Fatalf("recorded sessions = %d, want the fired one", n)
	}
	if n := countRows(t, `SELECT count(*) FROM audit_log WHERE detail->>'via' = 'routine' AND detail->>'sub' = $1
		AND detail->'act'->>'sub' = 'team:bronze/role:builder'`, rig.issuer.personID()); n != 1 {
		t.Fatalf("audit rows naming the person and the Team on their behalf = %d, want 1", n)
	}
	session, _ := out["session"].(map[string]any)
	if session["id"] != "session_01ROUTINE00000001" {
		t.Fatalf("session = %v", session)
	}
	if _, has := out["credential"]; has {
		t.Fatal("a routine fire handed out the Run credential")
	}
}

func TestARoutineFireIsRefusedWithANamedReasonBeforeTheClaim(t *testing.T) {
	rig := newRoutineRig(t)
	id := rig.work("2102")
	if code, out := rig.fire(id); code != 409 || routineErrorCode(out) != "routine_not_registered" {
		t.Fatalf("no routine = %d %v", code, out)
	}
	rig.register()
	other := clientCarriedWork(t, rig.server, "2103", "ryan", "someone-else")
	if code, out := rig.fire(other); code != 403 || routineErrorCode(out) != "not_your_run" {
		t.Fatalf("a Run of another person = %d %v", code, out)
	}
	rig.server.ClaudeRoutines.Limits = store.RoutineFireLimits{PerPerson: 90, PerRoutine: 1}
	if code, out := rig.fire(id); code != 200 {
		t.Fatalf("first fire = %d %v", code, out)
	}
	third := rig.work("2104")
	if code, out := rig.fire(third); code != 429 || routineErrorCode(out) != "routine_rate_limited" {
		t.Fatalf("over the routine's hourly count = %d %v", code, out)
	}
	if got := runStates(t, third) + "/" + runStates(t, other); got != "pending/pending" {
		t.Fatalf("refused runs = %s, want still waiting", got)
	}
	if rig.api.fires() != 1 {
		t.Fatalf("Anthropic saw %d fires, want only the allowed one", rig.api.fires())
	}
	rig.server.SecretStore = secretstoretest.NewFake()
	rig.server.ClaudeRoutines.Limits = store.RoutineFireLimits{}
	if code, out := rig.fire(third); code != 503 || routineErrorCode(out) != "secret_store_unavailable" {
		t.Fatalf("a lost trigger token = %d %v", code, out)
	}
}

func TestAFailedFireEndsTheRunStuckAndNeverFallsBackToALogin(t *testing.T) {
	for status, reason := range map[int]string{401: "routine_token_refused", 404: "routine_not_found", 429: "routine_rate_limited", 502: "routines_unavailable"} {
		t.Run(reason, func(t *testing.T) {
			rig := newRoutineRig(t)
			rig.register()
			rig.api.status = status
			id := rig.work("2110")
			code, out := rig.fire(id)
			if routineErrorCode(out) != reason || code < 400 {
				t.Fatalf("fire = %d %v, want %s", code, out, reason)
			}
			var state, outcome, stuck string
			if err := testPool.QueryRow(context.Background(), `SELECT state, COALESCE(outcome, ''), COALESCE(stuck_reason, '') FROM agent_runs WHERE work_item_id = $1`,
				id).Scan(&state, &outcome, &stuck); err != nil {
				t.Fatal(err)
			}
			if state != "finished" || outcome != "stuck" || !strings.HasPrefix(stuck, reason) {
				t.Fatalf("run = %s %s %q, want stuck with %s", state, outcome, stuck, reason)
			}
			if n := countRows(t, `SELECT count(*) FROM claude_sessions`); n != 0 {
				t.Fatal("a failed fire recorded a session")
			}
		})
	}
}

func TestASessionCreatedByAnotherAccountThanTheRoutinesIsRefused(t *testing.T) {
	rig := newRoutineRig(t)
	rig.register()
	first := rig.work("2120")
	if code, out := rig.fire(first); code != 200 {
		t.Fatalf("fire = %d %v", code, out)
	}
	second := rig.work("2121")
	if code, out := rig.fire(second); code != 200 {
		t.Fatalf("fire = %d %v", code, out)
	}
	owner := claudesession.Claims{EnvironmentID: clientEnvironment, SessionID: "01ROUTINE00000001", Creator: "user:owner", ExpiresAt: time.Now().Add(time.Hour)}
	rig.server.ClaudeSessions.Verifier = fakeSessionVerifier{claims: owner}
	if code := runnerReport(t, rig.server, `{"exitReason":"completed","finalMessage":"{\"outcome\":\"no_change_needed\",\"summary\":\"x\"}"}`); code != http.StatusNoContent {
		t.Fatalf("the routine owner's session report = %d", code)
	}
	intruder := claudesession.Claims{EnvironmentID: clientEnvironment, SessionID: "01ROUTINE00000002", Creator: "user:intruder", ExpiresAt: time.Now().Add(time.Hour)}
	rig.server.ClaudeSessions.Verifier = fakeSessionVerifier{claims: intruder}
	if code := runnerReport(t, rig.server, `{"exitReason":"completed"}`); code != http.StatusForbidden {
		t.Fatalf("a report from another account's session = %d, want 403", code)
	}
	r := httptest.NewRequest(http.MethodPost, "/api/v1/claude-sessions/forge-credential", nil)
	r.Header.Set("Authorization", "Bearer sk-ant-cc-anything")
	w := httptest.NewRecorder()
	rig.handler.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("a push credential for another account's session = %d, want 403", w.Code)
	}
	if got := runStates(t, second); got != "running" {
		t.Fatalf("the intruded Run = %q, want untouched", got)
	}
}

func TestClaudeRunnerPodHoldsNoClaudeCredential(t *testing.T) {
	rig := newRoutineRig(t)
	rig.register()
	id := rig.work("2130")
	if code, out := rig.fire(id); code != 200 {
		t.Fatalf("fire = %d %v", code, out)
	}
	runnerSees := map[string][]byte{"the session's fire payload": []byte(strings.Join(rig.api.texts, "\n"))}
	rig.server.ClaudeSessions.Verifier = fakeSessionVerifier{claims: claudesession.Claims{EnvironmentID: clientEnvironment,
		SessionID: "01ROUTINE00000001", Creator: "user:owner", ExpiresAt: time.Now().Add(time.Hour)}}
	for _, path := range []string{"/api/v1/claude-sessions/forge-credential"} {
		r := httptest.NewRequest(http.MethodPost, path, nil)
		r.Header.Set("Authorization", "Bearer sk-ant-cc-anything")
		w := httptest.NewRecorder()
		rig.handler.ServeHTTP(w, r)
		runnerSees[path] = w.Body.Bytes()
	}
	state := httptest.NewRecorder()
	var runToken string
	if err := testPool.QueryRow(context.Background(), `SELECT run_token FROM agent_runs WHERE work_item_id = $1`, id).Scan(&runToken); err != nil {
		t.Fatal(err)
	}
	rig.handler.ServeHTTP(state, httptest.NewRequest(http.MethodGet, "/api/v1/runs/"+runToken+"/claude-session", nil))
	runnerSees["the session state"] = state.Body.Bytes()
	for what, seen := range runnerSees {
		for _, secret := range []string{routineTriggerToken, routineEnvironmentSecret, routineClaudeLogin, "sk-ant-oat01"} {
			if bytes.Contains(seen, []byte(secret)) {
				t.Errorf("%s carries %q", what, secret)
			}
		}
	}
	rig.assertNoSecretLeft(t, "a fired Run")
}

func (rig *routineRig) assertNoSecretLeft(t *testing.T, when string) {
	t.Helper()
	for i, body := range rig.bodies {
		if bytes.Contains(body, []byte(routineTriggerToken)) {
			t.Errorf("%s: response %d carries the trigger token", when, i)
		}
	}
	if strings.Contains(rig.logs.String(), routineTriggerToken) {
		t.Errorf("%s: a log line carries the trigger token", when)
	}
	for _, query := range []string{
		`SELECT count(*) FROM audit_log WHERE detail::text LIKE '%' || $1 || '%'`,
		`SELECT count(*) FROM claude_routine_fires WHERE session_id LIKE '%' || $1 || '%'`,
		`SELECT count(*) FROM claude_sessions WHERE title LIKE '%' || $1 || '%' OR reference LIKE '%' || $1 || '%'`,
	} {
		if n := countRows(t, query, routineTriggerToken); n != 0 {
			t.Errorf("%s: %d rows carry the trigger token (%s)", when, n, query)
		}
	}
}

func fireCtx(rig *routineRig, ctx context.Context, id int64) *httptest.ResponseRecorder {
	r := httptest.NewRequestWithContext(ctx, "POST", fmt.Sprintf("/api/v1/operator/work-items/%d/fire-routine", id), nil)
	r.Header.Set("Authorization", "Bearer "+rig.person())
	w := httptest.NewRecorder()
	rig.handler.ServeHTTP(w, r)
	return w
}

func (rig *routineRig) sessionCall(path string, claims claudesession.Claims, body string) *httptest.ResponseRecorder {
	rig.server.ClaudeSessions.Verifier = fakeSessionVerifier{claims: claims}
	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	r := httptest.NewRequest(http.MethodPost, path, rdr)
	r.Header.Set("Authorization", "Bearer sk-ant-cc-anything")
	w := httptest.NewRecorder()
	rig.handler.ServeHTTP(w, r)
	return w
}

func routineSession(n int, creator string) claudesession.Claims {
	return claudesession.Claims{EnvironmentID: clientEnvironment, SessionID: fmt.Sprintf("01ROUTINE%08d", n), Creator: creator, ExpiresAt: time.Now().Add(time.Hour)}
}

func TestARoutineRunIsStoredWithItsOwnCarrierAndItsSessionIsNeverHandedAPushCredential(t *testing.T) {
	rig := newRoutineRig(t)
	rig.register()
	id := rig.work("2140")
	if n := countRows(t, `SELECT count(*) FROM agent_runs WHERE work_item_id = $1 AND carrier = 'routine'`, id); n != 1 {
		t.Fatalf("routine-carried runs = %d, want the Run stored with carrier routine", n)
	}
	if code, out := rig.fire(id); code != 200 {
		t.Fatalf("fire = %d %v", code, out)
	}
	w := rig.sessionCall("/api/v1/claude-sessions/forge-credential", routineSession(1, "user:owner"), "")
	if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "credential_gateway_mode") {
		t.Fatalf("push credential for a routine session = %d %s, want 403 credential_gateway_mode", w.Code, w.Body)
	}
}

func TestFireRoutineRefusesUntilTheCredentialGatewayIsConfigured(t *testing.T) {
	rig := newRoutineRig(t)
	rig.register()
	rig.server.ClaudeRoutines.GatewayMode = false
	id := rig.work("2141")
	if code, out := rig.fire(id); code != 409 || routineErrorCode(out) != "credential_gateway_required" {
		t.Fatalf("fire without the gateway = %d %v", code, out)
	}
	if rig.api.fires() != 0 || runStates(t, id) != "pending" {
		t.Fatalf("fires %d, run %s: want nothing fired and the Run waiting", rig.api.fires(), runStates(t, id))
	}
}

func TestAFireThatCannotBeRecordedEndsTheRunStuckAndBindsNoSession(t *testing.T) {
	rig := newRoutineRig(t)
	rig.register()
	ctx := context.Background()
	if _, err := testPool.Exec(ctx, `CREATE OR REPLACE FUNCTION refuse_fired() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN IF NEW.outcome = 'fired' THEN RAISE EXCEPTION 'fire record refused'; END IF; RETURN NEW; END $$;
		CREATE TRIGGER refuse_fired BEFORE UPDATE ON claude_routine_fires FOR EACH ROW EXECUTE FUNCTION refuse_fired();`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DROP TRIGGER IF EXISTS refuse_fired ON claude_routine_fires; DROP FUNCTION IF EXISTS refuse_fired();`)
	})
	id := rig.work("2142")
	if code, out := rig.fire(id); code != 503 || routineErrorCode(out) != "routine_fire_unrecorded" {
		t.Fatalf("fire whose record failed = %d %v", code, out)
	}
	if n := countRows(t, `SELECT count(*) FROM claude_sessions`); n != 0 {
		t.Fatal("a session was bound without its fire, so without the creator pin")
	}
	if n := countRows(t, `SELECT count(*) FROM agent_runs WHERE work_item_id = $1 AND state = 'finished' AND outcome = 'stuck'`, id); n != 1 {
		t.Fatalf("run = %s, want it ended stuck", runStates(t, id))
	}
}

func TestARoutineSessionWithoutARecordedFireFailsClosed(t *testing.T) {
	rig := newRoutineRig(t)
	rig.register()
	id := rig.work("2143")
	if code, out := rig.fire(id); code != 200 {
		t.Fatalf("fire = %d %v", code, out)
	}
	if _, err := testPool.Exec(context.Background(), `DELETE FROM claude_routine_fires`); err != nil {
		t.Fatal(err)
	}
	if w := rig.sessionCall("/api/v1/claude-sessions/report", routineSession(1, "user:owner"), `{"exitReason":"completed"}`); w.Code != http.StatusForbidden {
		t.Fatalf("report of a routine session whose fire is gone = %d, want 403", w.Code)
	}
}

func TestTheClientClaimNeverTakesARoutineRole(t *testing.T) {
	rig := newRoutineRig(t)
	id := rig.work("2144")
	r := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/v1/operator/work-items/%d/claim", id), nil)
	r.Header.Set("Authorization", "Bearer "+rig.person())
	w := httptest.NewRecorder()
	rig.handler.ServeHTTP(w, r)
	if w.Code != 409 || runStates(t, id) != "pending" {
		t.Fatalf("client claim of a routine Role = %d %s, run %s; want 409 and the Run waiting", w.Code, w.Body, runStates(t, id))
	}
}

func TestARoutineFireOutlivesTheCallersCancellation(t *testing.T) {
	rig := newRoutineRig(t)
	rig.register()
	id := rig.work("2145")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rig.api.onFire = cancel
	w := fireCtx(rig, ctx, id)
	if n := countRows(t, `SELECT count(*) FROM claude_sessions c JOIN agent_runs a ON a.id = c.run_id WHERE a.work_item_id = $1`, id); n != 1 {
		t.Fatalf("a fire whose caller went away = %d %s, sessions %d: want the session Anthropic started recorded", w.Code, w.Body, n)
	}
}

func TestTheRoutineLimitCountsOneTriggerAcrossTeams(t *testing.T) {
	rig := newRoutineRig(t)
	rig.register()
	body := fmt.Sprintf(`{"fireUrl":%q,"token":%q}`, rig.apiURL+routineTriggerPath, routineTriggerToken)
	if code, out := rig.call("PUT", "/api/v1/operator/claude-routines/brass", rig.person(), body); code != 200 {
		t.Fatalf("register brass = %d %v", code, out)
	}
	rig.server.ClaudeRoutines.Limits = store.RoutineFireLimits{PerPerson: 90, PerRoutine: 1}
	if code, out := rig.fire(rig.workFor("bronze", "2146")); code != 200 {
		t.Fatalf("first fire = %d %v", code, out)
	}
	brass := rig.workFor("brass", "2147")
	if code, out := rig.fire(brass); code != 429 || routineErrorCode(out) != "routine_rate_limited" {
		t.Fatalf("the same trigger through another Team = %d %v, want 429", code, out)
	}
}

func TestAPostClaimFailureEndsTheRoutineRunStuck(t *testing.T) {
	rig := newRoutineRig(t)
	rig.register()
	other := rig.work("2148")
	if code, out := rig.fire(other); code != 200 {
		t.Fatalf("fire = %d %v", code, out)
	}
	rig.api.mu.Lock()
	rig.api.texts = nil
	rig.api.mu.Unlock()
	id := rig.work("2149")
	if code, out := rig.fire(id); code != 503 || routineErrorCode(out) != "routine_session_unrecorded" {
		t.Fatalf("fire whose session cannot be recorded = %d %v", code, out)
	}
	if n := countRows(t, `SELECT count(*) FROM agent_runs WHERE work_item_id = $1 AND state = 'finished' AND outcome = 'stuck'`, id); n != 1 {
		t.Fatalf("run = %s, want it ended stuck", runStates(t, id))
	}
}

func TestARoutineIsRemovedOnlyOnceItsTokenIsDeleted(t *testing.T) {
	rig := newRoutineRig(t)
	rig.register()
	rig.secrets.Fail = true
	if code, out := rig.call("DELETE", "/api/v1/operator/claude-routines/bronze", rig.person(), ""); code != 503 {
		t.Fatalf("remove while the secret store fails = %d %v, want 503", code, out)
	}
	if n := countRows(t, `SELECT count(*) FROM claude_routines`); n != 1 {
		t.Fatal("the routine was forgotten while its token is still stored")
	}
	rig.secrets.Fail = false
	if code, out := rig.call("DELETE", "/api/v1/operator/claude-routines/bronze", rig.person(), ""); code != 200 {
		t.Fatalf("retried remove = %d %v", code, out)
	}
}
