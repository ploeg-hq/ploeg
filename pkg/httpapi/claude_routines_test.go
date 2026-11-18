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
)

const (
	routineTriggerToken      = "sk-ant-oat01-routine-trigger-fixture-0123456789"
	routineEnvironmentSecret = "sk-ant-envsecret-fixture-should-never-move"
	routineClaudeLogin       = "sk-ant-oat01-claude-login-fixture-should-never-move"
	routineTriggerPath       = "/v1/claude_code/routines/trig_01ABCDEFGHJKLMNOP/fire"
)

type fakeRoutineAPI struct {
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
	consumers, _ := operatorTestConsumers(t, []string{"bronze"}, true)
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
	logs := &bytes.Buffer{}
	secrets := secretstoretest.NewFake()
	s := &Server{
		Store: testStore, LeaseTTL: time.Minute, Log: slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
		WorkerSecurity: &WorkerSecurity{AllowLegacy: true},
		OperatorConfig: OperatorConfig{Consumers: consumers, PersonTokens: tokens, TeamHarnesses: harnesses},
		ClaudeSessions: &ClaudeSessions{Verifier: fakeSessionVerifier{}, GitUser: "agent-builder", ForgeURL: "https://forge.example/"},
		ClaudeRoutines: &ClaudeRoutines{Client: &clauderoutine.Client{BaseURL: srv.URL}},
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
