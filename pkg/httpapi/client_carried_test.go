package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/shiftengine"
	"github.com/ploeg-hq/ploeg/pkg/store"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

const (
	clientPerson      = "oidc-0123456789abcdef0123456789abcdef"
	clientEnvironment = "ccpool_team"
)

func clientCarriedHarnesses() TeamHarnesses {
	return TeamHarnesses{"bronze": {"builder": {
		OperatorRoleHarness: store.OperatorRoleHarness{Name: "claude-environment"},
		Carrier:             store.CarrierClient, EnvironmentID: clientEnvironment,
	}}}
}

func clientCarriedServer(t *testing.T) (*Server, string) {
	t.Helper()
	consumers, token := operatorTestConsumers(t, []string{"bronze"}, true)
	return &Server{
		Store: testStore, LeaseTTL: time.Minute, Log: slog.New(slog.DiscardHandler),
		WorkerSecurity: &WorkerSecurity{AllowLegacy: true},
		OperatorConfig: OperatorConfig{Consumers: consumers, TeamHarnesses: clientCarriedHarnesses()},
		ClaudeSessions: &ClaudeSessions{Verifier: fakeSessionVerifier{}, GitUser: "agent-builder", ForgeURL: "https://forge.example/"},
	}, token
}

// clientCarriedWork is a tracker Work Item that trackerUser handed to Ploeg,
// mapped to person, with an open Shift whose writer a client carries.
func clientCarriedWork(t *testing.T, s *Server, externalID, trackerUser, person string) int64 {
	t.Helper()
	ctx := context.Background()
	id, _, err := testStore.IngestAssigned(ctx, work.WorkItem{
		Provider: "vikunja", ExternalID: externalID, Team: "bronze", Title: "Add a health check",
		Description: "Add a /healthz route that answers 200.",
		Target:      &work.Target{Forge: "forgejo", Owner: "webgrip", Repo: "fixture", BaseBranch: "development"},
	})
	if err != nil {
		t.Fatalf("IngestAssigned: %v", err)
	}
	if err := testStore.RecordHandedBy(ctx, id, trackerUser, person); err != nil {
		t.Fatalf("RecordHandedBy: %v", err)
	}
	shiftID, err := testStore.OpenShift(ctx, id, "bronze", "agent/vik-"+externalID, 10)
	if err != nil {
		t.Fatalf("OpenShift: %v", err)
	}
	selection := RunSelections{Harnesses: s.OperatorConfig.TeamHarnesses}.RoleSelection("bronze", "builder")
	if _, err := testStore.OpenRound(ctx, shiftID, 0, []store.Role{{Name: "builder", Writes: true, Cap: 1, Selection: selection}}); err != nil {
		t.Fatalf("OpenRound: %v", err)
	}
	return id
}

func clientClaim(s *Server, token, actor, actingUser string, id int64) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/v1/operator/work-items/%d/claim", id), nil)
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("X-Ploeg-Actor", actor)
	if actingUser != "" {
		r.Header.Set("X-Ploeg-Acting-User", actingUser)
	}
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	return w
}

func runStates(t *testing.T, id int64) string {
	t.Helper()
	var states string
	if err := testPool.QueryRow(context.Background(),
		`SELECT string_agg(state, ',' ORDER BY id) FROM agent_runs WHERE work_item_id = $1`, id).Scan(&states); err != nil {
		t.Fatal(err)
	}
	return states
}

func operatorErrorCode(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	validOperatorResponse(t, w)
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	return body.Error.Code
}

func TestClientCarriedRunIsNeverClaimedByAWorker(t *testing.T) {
	reset(t)
	s, _ := clientCarriedServer(t)
	id := clientCarriedWork(t, s, "1301", "ryan", clientPerson)
	h := s.Handler()
	for _, body := range []string{
		`{"team":"bronze","role":"builder"}`,
		`{"team":"bronze","role":"builder","harness":"claude-environment"}`,
		`{"team":"bronze"}`,
	} {
		if code, _ := postClaim(t, h, body); code != http.StatusNoContent {
			t.Errorf("worker claim %s answered %d, want 204", body, code)
		}
	}
	if got := runStates(t, id); got != "pending" {
		t.Fatalf("runs = %q, want the client-carried Run still pending", got)
	}
	for _, key := range []string{"", "claude-environment"} {
		if n, err := testStore.PendingRunsFor(context.Background(), "bronze", "builder", key); err != nil || n != 0 {
			t.Errorf("PendingRunsFor(%q) = %d, %v; KEDA must not start a worker for a client-carried Run", key, n, err)
		}
	}
}

func TestClaudeEnvironmentWorkItemWithoutAClientWaitsAsNeedsHumanWithItsPersonNamed(t *testing.T) {
	reset(t)
	s, token := clientCarriedServer(t)
	mapped := clientCarriedWork(t, s, "1302", "ryan", clientPerson)
	unmapped := clientCarriedWork(t, s, "1303", "jake", "")

	for _, c := range []struct {
		id       int64
		actsFor  string
		handedBy string
		names    string
	}{{mapped, clientPerson, "ryan", clientPerson}, {unmapped, "", "jake", "tracker user jake"}} {
		var detail struct {
			Item store.OperatorItem `json:"item"`
		}
		if err := json.Unmarshal(operatorSchemaGET(t, s, token, fmt.Sprintf("work-items/%d", c.id)), &detail); err != nil {
			t.Fatal(err)
		}
		if detail.Item.State != "needs_human" {
			t.Errorf("item %d state = %q, want needs_human", c.id, detail.Item.State)
		}
		wait := detail.Item.WaitingForClient
		if wait == nil {
			t.Fatalf("item %d has no waitingForClient", c.id)
		}
		person := ""
		if wait.ActsFor != nil {
			person = *wait.ActsFor
		}
		if wait.Reason != store.NeedsHumanStartFromClient || wait.Role != "builder" || !wait.Writes ||
			wait.HandedBy == nil || *wait.HandedBy != c.handedBy || person != c.actsFor {
			t.Errorf("item %d waitingForClient = %+v", c.id, wait)
		}
		if want := store.ClientWaitMessage(c.actsFor, c.handedBy); wait.Message != want || !strings.Contains(wait.Message, c.names) ||
			!strings.HasPrefix(wait.Message, "Start this from your Claude Code") {
			t.Errorf("item %d message = %q, want %q naming %s", c.id, wait.Message, want, c.names)
		}
		var reason string
		if err := testPool.QueryRow(context.Background(), `SELECT detail->>'reason' FROM audit_log
			WHERE work_item_id = $1 AND action = 'work_item.needs_human'`, c.id).Scan(&reason); err != nil || reason != store.NeedsHumanStartFromClient {
			t.Errorf("item %d needs_human event reason = %q, %v", c.id, reason, err)
		}
	}

	var listed struct {
		Items []store.OperatorItem `json:"items"`
	}
	if err := json.Unmarshal(operatorSchemaGET(t, s, token, "work-items?needsHuman=true"), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Items) != 2 {
		t.Fatalf("needsHuman=true lists %d items, want both waiting items", len(listed.Items))
	}
}

func TestOnlyThePersonAClientCarriedRunActsForCanClaimIt(t *testing.T) {
	reset(t)
	s, token := clientCarriedServer(t)
	id := clientCarriedWork(t, s, "1304", "ryan", clientPerson)

	if w := clientClaim(s, token, "oidc-ffffffffffffffffffffffffffffffff", "", id); w.Code != http.StatusForbidden || operatorErrorCode(t, w) != "not_your_run" {
		t.Fatalf("a colleague's claim answered %d %s, want 403 not_your_run", w.Code, w.Body)
	}
	if got := runStates(t, id); got != "pending" {
		t.Fatalf("after the refused claim runs = %q, want pending", got)
	}

	w := clientClaim(s, token, clientPerson, "", id)
	if w.Code != http.StatusOK {
		t.Fatalf("the person's claim answered %d %s", w.Code, w.Body)
	}
	if got := w.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
	validOperatorResponse(t, w)
	var resp clientClaimResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	d := resp.Dispatch
	if resp.Run.ActsFor != clientPerson || resp.Run.Role != "builder" || !resp.Run.Writes ||
		d.EnvironmentID != clientEnvironment || d.Ref != "development" || d.Branch != "agent/vik-1304" || d.BaseBranch != "development" ||
		d.Repository != (clientClaimRepository{Forge: "forgejo", Owner: "webgrip", Repo: "fixture"}) || d.Reference != "VIK-1304" {
		t.Errorf("claim response = %+v", resp)
	}
	for _, want := range []string{"# Work Item VIK-1304: Add a health check", "Add a /healthz route", "## Where you run", "agent/vik-1304"} {
		if !strings.Contains(d.Prompt, want) {
			t.Errorf("prompt lacks %q:\n%s", want, d.Prompt)
		}
	}
	if resp.Credential.RunToken == "" || resp.Credential.ControlToken != "" {
		t.Errorf("legacy-mode credential = %+v, want a run token and no control token", resp.Credential)
	}
	if got := runStates(t, id); got != "running" {
		t.Errorf("after the claim runs = %q, want running", got)
	}
	var state, actor string
	if err := testPool.QueryRow(context.Background(), `SELECT w.state, a.actor FROM work_items w
		JOIN audit_log a ON a.work_item_id = w.id AND a.action = 'run.claimed' AND a.detail->>'carrier' = 'client'
		WHERE w.id = $1`, id).Scan(&state, &actor); err != nil || state != "active" || actor != "operator:workbench:"+clientPerson {
		t.Errorf("work item %q claimed by %q, %v; want active, audited as the person", state, actor, err)
	}

	if w := clientClaim(s, token, clientPerson, "", id); w.Code != http.StatusConflict || operatorErrorCode(t, w) != "already_claimed" {
		t.Fatalf("a second claim answered %d %s, want 409 already_claimed", w.Code, w.Body)
	}
	if code := recordSession(t, s.Handler(), resp.Credential.RunToken, clientEnvironment, "session_client1"); code != http.StatusNoContent {
		t.Fatalf("recording the client's session answered %d, want 204", code)
	}
}

func TestAClientCarriedRunOfAnAdmissionActsForItsActingPerson(t *testing.T) {
	f := newAdmissionFixture(t)
	f.server.OperatorConfig.TeamHarnesses = clientCarriedHarnesses()
	f.engine.Selections = RunSelections{Harnesses: f.server.OperatorConfig.TeamHarnesses}
	w := operatorExecutionRequest(f.server, "POST", admissionPath, f.token, "agent-service", admissionBody("req-client"))
	if w.Code != http.StatusCreated {
		t.Fatalf("admission: %d %s", w.Code, w.Body)
	}
	var id int64
	if err := testPool.QueryRow(context.Background(), `SELECT id FROM work_items WHERE provider = 'manual'`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if got := runStates(t, id); got != "pending" {
		t.Fatalf("runs = %q, want the client-carried writer pending", got)
	}
	if w := clientClaim(f.server, f.token, "agent-service", clientPerson, id); w.Code != http.StatusForbidden {
		t.Fatalf("a claim for another person answered %d %s, want 403", w.Code, w.Body)
	}
	if w := clientClaim(f.server, f.token, "agent-service", "", id); w.Code != http.StatusOK {
		t.Fatalf("the admission's person claim answered %d %s", w.Code, w.Body)
	}
}

func TestAClientClaimCredentialWorksOnTheManagedRunAPI(t *testing.T) {
	reset(t)
	s, token := clientCarriedServer(t)
	security, err := NewWorkerSecurity(`[]`, strings.Repeat("s", 32), false)
	if err != nil {
		t.Fatal(err)
	}
	s.WorkerSecurity = security
	id := clientCarriedWork(t, s, "1305", "ryan", clientPerson)
	w := clientClaim(s, token, clientPerson, "", id)
	if w.Code != http.StatusOK {
		t.Fatalf("claim: %d %s", w.Code, w.Body)
	}
	var resp clientClaimResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Credential.ControlToken == "" || resp.Credential.WorkerID != "client:"+clientPerson {
		t.Fatalf("managed credential = %+v", resp.Credential)
	}
	renew := func(control, worker string) int {
		r := httptest.NewRequest(http.MethodPost, "/api/v1/runs/"+resp.Credential.RunToken+"/renew", nil)
		r.Header.Set("Authorization", "Bearer "+control)
		r.Header.Set("X-Ploeg-Worker-ID", worker)
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, r)
		return rec.Code
	}
	if code := renew(resp.Credential.ControlToken, resp.Credential.WorkerID); code != http.StatusOK {
		t.Fatalf("renew with the client's credential answered %d, want 200", code)
	}
	if code := renew(resp.Credential.ControlToken, "pod"); code == http.StatusOK {
		t.Fatal("renew under another worker id was accepted")
	}
}

func TestParseTeamHarnessesReadsAClientCarrier(t *testing.T) {
	got, err := ParseTeamHarnesses(`{"bronze":{"builder":{"name":"claude-environment","carrier":"client","environmentId":"ccpool_team"}}}`)
	if err != nil {
		t.Fatal(err)
	}
	if h := got["bronze"]["builder"]; !h.ClientCarried() || h.EnvironmentID != clientEnvironment {
		t.Fatalf("parsed %+v, want a client-carried Role with its environment", h)
	}
	if sel := (RunSelections{Harnesses: got}).RoleSelection("bronze", "builder"); len(sel.ClientCarried) != 1 || sel.ClientCarried[0] != "claude-environment" {
		t.Fatalf("selection = %+v, want claude-environment carried by a client", sel)
	}
	for _, raw := range []string{
		`{"bronze":{"builder":{"name":"claude-environment","carrier":"worker","environmentId":"ccpool_team"}}}`,
		`{"bronze":{"builder":{"name":"claude-code","carrier":"client","environmentId":"ccpool_team"}}}`,
		`{"bronze":{"builder":{"name":"claude-environment","carrier":"client"}}}`,
	} {
		if _, err := ParseTeamHarnesses(raw); err == nil {
			t.Errorf("ParseTeamHarnesses(%s) accepted it", raw)
		}
	}
}

func TestATrackerWorkItemActsForThePersonItsHandingUserMapsTo(t *testing.T) {
	reset(t)
	harnesses := TeamHarnesses{"bronze": {"": {OperatorRoleHarness: store.OperatorRoleHarness{Name: "claude-environment"},
		Carrier: store.CarrierClient, EnvironmentID: clientEnvironment}}}
	engine := &shiftengine.Engine{Store: testStore, Log: slog.New(slog.DiscardHandler), Uniform: true,
		Selections: RunSelections{Harnesses: harnesses}}
	s := withdrawalServer(engine)
	s.OperatorConfig.TeamHarnesses = harnesses
	body := `{"event_name":"task.assignee.created","data":{"task":{"id":4401,"title":"handed fixture","project_id":7},"assignee":{"username":"builder"},"doer":{"username":"Ryan"}}}`
	assign := func() {
		t.Helper()
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, signedTrackerHook("vikunja", body))
		if w.Code != http.StatusAccepted {
			t.Fatalf("assignment: %d %s", w.Code, w.Body)
		}
	}
	item := func() (state, actsFor, handedBy string) {
		t.Helper()
		if err := testPool.QueryRow(context.Background(), `SELECT state, acts_for, handed_by FROM work_items
			WHERE provider = 'vikunja' AND external_id = '4401'`).Scan(&state, &actsFor, &handedBy); err != nil {
			t.Fatal(err)
		}
		return state, actsFor, handedBy
	}
	assign()
	if state, actsFor, handedBy := item(); state != "needs_human" || actsFor != "" || handedBy != "Ryan" {
		t.Fatalf("unmapped: work item = %s acting for %q handed by %q, want needs_human naming the tracker user", state, actsFor, handedBy)
	}
	s.TrackerPeople = map[string]map[string]string{"vikunja": {"ryan": clientPerson}}
	assign()
	if state, actsFor, _ := item(); state != "needs_human" || actsFor != clientPerson {
		t.Fatalf("assigned again after mapping: work item = %s acting for %q, want still waiting, for the mapped person", state, actsFor)
	}
	if got := countRows(t, `SELECT count(*) FROM agent_runs WHERE carrier = 'client' AND state = 'pending'`); got != 1 {
		t.Fatalf("pending client-carried Runs = %d, want the one Run still waiting", got)
	}
	if code, _ := postClaim(t, s.Handler(), `{"team":"bronze"}`); code != http.StatusNoContent {
		t.Fatalf("a worker claimed the client-carried Run: %d", code)
	}
}
