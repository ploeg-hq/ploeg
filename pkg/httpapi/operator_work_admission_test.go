package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/ploeg-hq/ploeg/pkg/config"
	"github.com/ploeg-hq/ploeg/pkg/plan"
	"github.com/ploeg-hq/ploeg/pkg/provider"
	"github.com/ploeg-hq/ploeg/pkg/provider/vikunja"
	"github.com/ploeg-hq/ploeg/pkg/shiftengine"
	"github.com/ploeg-hq/ploeg/pkg/store"
	"github.com/ploeg-hq/ploeg/pkg/target"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

const admissionPath = "/api/v1/operator/work-items"

type admissionFixture struct {
	server   *Server
	engine   *shiftengine.Engine
	token    string
	readOnly string
	comments *taskComments
}

func newAdmissionFixture(t *testing.T) admissionFixture {
	t.Helper()
	reset(t)
	plans, err := plan.Parse(`{"bronze": {"pool": 50, "rounds": [
		{"roles": [{"name": "builder", "writes": true, "cap": 3}]},
		{"roles": [{"name": "reviewer", "writes": false, "cap": 1}]}
	]}}`)
	if err != nil {
		t.Fatal(err)
	}
	forge := &fakeForge{states: map[string]provider.RepositoryState{
		"webgrip/app":             {AgentsFile: true},
		"webgrip/homelab-cluster": {AgentsFile: false},
	}}
	gate := target.NewGate(map[string]work.Target{"app": appTarget, "homelab-cluster": homelabTarget},
		map[string]provider.RepositoryInspector{"forgejo": forge})
	comments := &taskComments{}
	api := httptest.NewServer(comments)
	t.Cleanup(api.Close)
	log := slog.New(slog.DiscardHandler)
	engine := &shiftengine.Engine{Store: testStore, Plans: plans, Log: log}
	consumers, token := operatorTestConsumers(t, []string{"bronze", "silver"}, true)
	readOnly, readToken := operatorTestConsumers(t, []string{"bronze"}, false)
	readOnly[0].Principal.Name = "reader"
	s := &Server{
		Store: testStore, LeaseTTL: time.Minute, Log: log, RoleCaps: plans, Engine: engine, Readiness: gate,
		WorkerSecurity: &WorkerSecurity{AllowLegacy: true},
		Trackers: map[string]provider.TrackerProvider{"vikunja": &vikunja.Provider{Secret: testTrackerSecret,
			BaseURL: api.URL, Token: "test-token", DefaultTeam: "bronze", Log: log}},
		OperatorConfig: OperatorConfig{Consumers: append(consumers, readOnly...)},
	}
	return admissionFixture{server: s, engine: engine, token: token, readOnly: readToken, comments: comments}
}

func admissionBody(requestID string) map[string]any {
	return map[string]any{"requestId": requestID, "team": "bronze", "target": "app",
		"title": "Add a health check", "description": "Add a /healthz route that answers 200."}
}

type admissionResult struct {
	WorkItem struct {
		ID        string   `json:"id"`
		Team      string   `json:"team"`
		State     string   `json:"state"`
		ShiftID   *string  `json:"shiftId"`
		BudgetUSD float64  `json:"budgetUsd"`
		PoolUSD   *float64 `json:"poolUsd"`
	} `json:"workItem"`
	Created  bool `json:"created"`
	Replayed bool `json:"replayed"`
}

func (f admissionFixture) admit(t *testing.T, body map[string]any, wantStatus int) (admissionResult, int64) {
	t.Helper()
	w := operatorExecutionRequest(f.server, "POST", admissionPath, f.token, "alice", body)
	if w.Code != wantStatus {
		t.Fatalf("admission: %d %s, want %d", w.Code, w.Body, wantStatus)
	}
	validOperatorResponse(t, w)
	var got admissionResult
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	id, err := strconv.ParseInt(got.WorkItem.ID, 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	return got, id
}

func admissionEvents(t *testing.T, f admissionFixture, id int64) []store.OperatorEvent {
	t.Helper()
	var page struct {
		Events []store.OperatorEvent `json:"events"`
	}
	if err := json.Unmarshal(operatorSchemaGET(t, f.server, f.token, fmt.Sprintf("events?workItemId=%d", id)), &page); err != nil {
		t.Fatal(err)
	}
	return page.Events
}

func TestOperatorWorkAdmissionQueuesWorkThatAWorkerClaims(t *testing.T) {
	f := newAdmissionFixture(t)
	got, id := f.admit(t, admissionBody("req-1"), http.StatusCreated)
	if !got.Created || got.Replayed || got.WorkItem.Team != "bronze" || got.WorkItem.ShiftID == nil || got.WorkItem.BudgetUSD != 25 || got.WorkItem.PoolUSD == nil || *got.WorkItem.PoolUSD != 25 {
		t.Fatalf("admission = %+v, want a Shift capped at maxBudgetUsd 25", got)
	}
	if n := countRows(t, `SELECT count(*) FROM work_items WHERE operator_owned OR provider<>'manual'`); n != 0 {
		t.Fatalf("operator-owned or non-manual rows = %d", n)
	}
	if n := countRows(t, `SELECT count(*) FROM agent_runs WHERE role='operator' OR state<>'pending'`); n != 0 {
		t.Fatalf("Runs that are not pending planned Runs = %d", n)
	}

	replay := operatorExecutionRequest(f.server, "POST", admissionPath, f.token, "alice", admissionBody("req-1"))
	if replay.Code != http.StatusOK {
		t.Fatalf("replay: %d %s", replay.Code, replay.Body)
	}
	validOperatorResponse(t, replay)
	var again admissionResult
	if err := json.Unmarshal(replay.Body.Bytes(), &again); err != nil {
		t.Fatal(err)
	}
	if again.Created || !again.Replayed || again.WorkItem.ID != got.WorkItem.ID || *again.WorkItem.ShiftID != *got.WorkItem.ShiftID {
		t.Fatalf("replay = %+v, want the first Work Item", again)
	}
	if n := countRows(t, `SELECT count(*) FROM shifts WHERE work_item_id=$1`, id); n != 1 {
		t.Fatalf("shifts = %d, want 1: a replay opens no Shift", n)
	}
	if n := countRows(t, `SELECT count(*) FROM agent_runs WHERE work_item_id=$1`, id); n != 1 {
		t.Fatalf("runs = %d, want 1: a replay creates no Run", n)
	}

	code, claim := postClaim(t, f.server.Handler(), `{"team":"bronze","role":"builder"}`)
	if code != 200 || fmt.Sprint(claim.Shift) != *got.WorkItem.ShiftID || claim.WorkItem.Target == nil || *claim.WorkItem.Target != appTarget {
		t.Fatalf("builder claim: %d %+v", code, claim)
	}

	events := admissionEvents(t, f, id)
	if len(events) == 0 || events[0].Action != "operator.work_item.admitted" || events[0].Actor != "operator:workbench:alice" || events[0].Detail["requestId"] != "req-1" {
		t.Fatalf("first event = %+v, want the Admission", events)
	}
	admitted := 0
	for _, e := range events {
		if e.Action == "operator.work_item.admitted" {
			admitted++
		}
	}
	if admitted != 1 {
		t.Fatalf("admission events = %d, want 1: a replay is silent", admitted)
	}
}

func TestOperatorWorkAdmissionBudgetBelowThePlanPoolCapsIt(t *testing.T) {
	f := newAdmissionFixture(t)
	body := admissionBody("req-budget")
	body["budgetUsd"] = 10
	got, _ := f.admit(t, body, http.StatusCreated)
	if got.WorkItem.BudgetUSD != 10 || got.WorkItem.PoolUSD == nil || *got.WorkItem.PoolUSD != 10 {
		t.Fatalf("admission = %+v, want a pool of 10", got)
	}
}

func TestOperatorWorkAdmissionRefusals(t *testing.T) {
	with := func(key string, value any) map[string]any {
		body := admissionBody("req-refused")
		body[key] = value
		return body
	}
	cases := []struct {
		name    string
		token   func(f admissionFixture) string
		actors  []string
		body    map[string]any
		prepare func(f admissionFixture)
		status  int
		code    string
	}{
		{name: "read-only consumer", token: func(f admissionFixture) string { return f.readOnly }, body: admissionBody("req-refused"), status: 403, code: "execution_forbidden"},
		{name: "team outside scope", body: with("team", "gold"), status: 403, code: "team_forbidden"},
		{name: "missing actor", actors: []string{}, body: admissionBody("req-refused"), status: 400, code: "actor_required"},
		{name: "two actors", actors: []string{"alice", "bob"}, body: admissionBody("req-refused"), status: 400, code: "actor_required"},
		{name: "unknown target", body: with("target", "nowhere"), status: 422, code: "unknown_target"},
		{name: "target not ready", body: with("target", "homelab-cluster"), status: 422, code: "target_not_ready"},
		{name: "budget above the ceiling", body: with("budgetUsd", 40), status: 400, code: "budget_above_limit"},
		{name: "budget zero", body: with("budgetUsd", 0), status: 400, code: "invalid_admission"},
		{name: "empty description", body: with("description", "  "), status: 400, code: "invalid_admission"},
		{name: "repository url", body: with("repositoryUrl", "https://forge.example/o/r.git"), status: 400, code: "invalid_command"},
		{name: "field of a later change", body: with("attended", true), status: 400, code: "invalid_command"},
		{name: "harness without an allowlist", body: with("harness", "claude-code"), status: 422, code: "harness_not_allowed"},
		{name: "malformed harness", body: with("harness", "Claude Code"), status: 400, code: "invalid_admission"},
		{name: "model without managed inference", body: with("model", "kimi-k3"), status: 422, code: "model_requires_managed_inference"},
		{name: "team without a plan", body: with("team", "silver"), status: 422, code: "team_not_dispatched"},
		{name: "no Shift dispatch", body: admissionBody("req-refused"), prepare: func(f admissionFixture) { f.server.Engine = nil }, status: 503, code: "dispatch_unavailable"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newAdmissionFixture(t)
			if tc.prepare != nil {
				tc.prepare(f)
			}
			token := f.token
			if tc.token != nil {
				token = tc.token(f)
			}
			data, _ := json.Marshal(tc.body)
			r := httptest.NewRequest("POST", admissionPath, bytes.NewReader(data))
			r.Header.Set("Authorization", "Bearer "+token)
			r.Header.Set("Content-Type", "application/json")
			actors := tc.actors
			if actors == nil {
				actors = []string{"alice"}
			}
			for _, actor := range actors {
				r.Header.Add("X-Ploeg-Actor", actor)
			}
			w := httptest.NewRecorder()
			f.server.Handler().ServeHTTP(w, r)
			if w.Code != tc.status {
				t.Fatalf("status = %d, want %d: %s", w.Code, tc.status, w.Body)
			}
			validOperatorResponse(t, w)
			var body struct {
				Error struct {
					Code string `json:"code"`
				} `json:"error"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || body.Error.Code != tc.code {
				t.Fatalf("error code = %q (%v), want %q", body.Error.Code, err, tc.code)
			}
			for _, table := range []string{"work_items", "shifts", "agent_runs", "leases", "audit_log"} {
				if n := countRows(t, "SELECT count(*) FROM "+table); n != 0 {
					t.Fatalf("a refused Admission left %d rows in %s", n, table)
				}
			}
		})
	}
}

func TestOperatorWorkAdmissionConflictsOnAChangedReplay(t *testing.T) {
	f := newAdmissionFixture(t)
	_, id := f.admit(t, admissionBody("req-conflict"), http.StatusCreated)
	changed := admissionBody("req-conflict")
	changed["title"] = "Another title"
	for actor, body := range map[string]map[string]any{"alice": changed, "bob": admissionBody("req-conflict")} {
		w := operatorExecutionRequest(f.server, "POST", admissionPath, f.token, actor, body)
		if w.Code != 409 || !strings.Contains(w.Body.String(), `"request_conflict"`) {
			t.Fatalf("replay by %s: %d %s, want 409 request_conflict", actor, w.Code, w.Body)
		}
		validOperatorResponse(t, w)
	}
	if n := countRows(t, `SELECT count(*) FROM work_items WHERE id=$1 AND title='Add a health check'`, id); n != 1 {
		t.Fatal("a conflicting replay changed the first Work Item")
	}
}

func TestOperatorWorkAdmissionRefusesARequestIdOfAnOperatorExecution(t *testing.T) {
	f := newAdmissionFixture(t)
	execution := operatorExecutionRequest(f.server, "POST", "/api/v1/operator/executions", f.token, "alice", map[string]any{
		"sessionId": "shared-id", "team": "bronze", "title": "Operator execution", "objective": "Implement the bounded example and verify it.",
		"repositoryId": "example", "repositoryUrl": "https://forge.example/webgrip/example.git", "baseBranch": "development",
		"crewId": "delivery", "budgetUsd": 0, "demo": true})
	if execution.Code != http.StatusCreated {
		t.Fatalf("execution: %d %s", execution.Code, execution.Body)
	}
	w := operatorExecutionRequest(f.server, "POST", admissionPath, f.token, "alice", admissionBody("shared-id"))
	if w.Code != 409 || !strings.Contains(w.Body.String(), `"request_conflict"`) {
		t.Fatalf("admission over an execution: %d %s", w.Code, w.Body)
	}
	if n := countRows(t, `SELECT count(*) FROM work_items WHERE NOT operator_owned`); n != 0 {
		t.Fatalf("admitted rows = %d, want 0", n)
	}
}

func TestOperatorWorkAdmissionCancelWithdrawsWithoutATrackerAndStaysWithdrawn(t *testing.T) {
	f := newAdmissionFixture(t)
	ctx := context.Background()
	_, id := f.admit(t, admissionBody("req-cancel"), http.StatusCreated)
	code, claim := postClaim(t, f.server.Handler(), `{"team":"bronze","role":"builder"}`)
	if code != 200 {
		t.Fatalf("builder claim: %d", code)
	}

	w := operatorExecutionRequest(f.server, "POST", fmt.Sprintf("%s/%d/cancel", admissionPath, id), f.token, "alice", nil)
	if w.Code != 200 {
		t.Fatalf("cancel: %d %s", w.Code, w.Body)
	}
	validOperatorResponse(t, w)
	if got := snapshotItem(t, id); got.state != "withdrawn" || got.liveShifts != 0 || got.closeReason != store.CloseReasonWithdrawnByOperator || got.unfinished != 0 {
		t.Fatalf("cancel left live work: %+v", got)
	}
	if renew := workerRequestLegacy(f.server, "/api/v1/runs/"+claim.RunToken+"/renew"); renew == 200 {
		t.Fatal("the cancelled Run renewed its Lease")
	}
	if len(f.comments.matching("")) != 0 {
		t.Fatalf("a tracker was called: %v", f.comments.matching(""))
	}

	reason := string(work.FailureAgentError)
	_, _ = testStore.ReportOutcome(ctx, claim.RunToken, store.Report(work.OutcomeFailed, "stopped", "", nil, nil, &reason))
	if err := f.engine.EvaluateItem(ctx, id); err != nil {
		t.Fatal(err)
	}
	f.engine.EvaluateAll(ctx)
	if got := snapshotItem(t, id); got.state != "withdrawn" || got.liveShifts != 0 || got.unfinished != 0 {
		t.Fatalf("a failed writer after the cancel re-opened work: %+v", got)
	}

	replay, _ := f.admit(t, admissionBody("req-cancel"), http.StatusOK)
	if !replay.Replayed || replay.WorkItem.State != "withdrawn" {
		t.Fatalf("replay after cancel = %+v, want withdrawn", replay)
	}
	if got := snapshotItem(t, id); got.state != "withdrawn" || got.liveShifts != 0 {
		t.Fatalf("a replay after cancel queued the item again: %+v", got)
	}
}

func workerRequestLegacy(s *Server, path string) int {
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodPost, path, nil))
	return w.Code
}

func TestOperatorWorkAdmissionNoteReachesOnlyTheNextRun(t *testing.T) {
	f := newAdmissionFixture(t)
	ctx := context.Background()
	_, id := f.admit(t, admissionBody("req-note"), http.StatusCreated)
	code, builder := postClaim(t, f.server.Handler(), `{"team":"bronze","role":"builder"}`)
	if code != 200 {
		t.Fatalf("builder claim: %d", code)
	}
	w := operatorExecutionRequest(f.server, "POST", notesPath(id), f.token, "alice", map[string]any{"commandId": "note-1", "text": "keep the route unauthenticated"})
	if w.Code != http.StatusCreated {
		t.Fatalf("note: %d %s", w.Code, w.Body)
	}
	if len(operatorNotes(builder.Briefing)) != 0 {
		t.Fatal("the running Run was briefed with a later note")
	}
	if _, err := testStore.ReportOutcome(ctx, builder.RunToken, store.Report(work.OutcomePROpened, "opened", "", []string{"https://forgejo/webgrip/app/pulls/7"}, nil, nil)); err != nil {
		t.Fatal(err)
	}
	if err := f.engine.EvaluateItem(ctx, id); err != nil {
		t.Fatal(err)
	}
	code, reviewer := postClaim(t, f.server.Handler(), `{"team":"bronze","role":"reviewer"}`)
	if code != 200 {
		t.Fatalf("reviewer claim: %d", code)
	}
	notes := operatorNotes(reviewer.Briefing)
	if len(notes) != 1 || !strings.Contains(notes[0].Findings, "keep the route unauthenticated") {
		t.Fatalf("reviewer briefing notes = %+v", notes)
	}
}

func TestOperatorWorkAdmissionRequeueAfterNeedsHumanStaysWithinTheCeiling(t *testing.T) {
	f := newAdmissionFixture(t)
	ctx := context.Background()
	_, id := f.admit(t, admissionBody("req-requeue"), http.StatusCreated)
	code, builder := postClaim(t, f.server.Handler(), `{"team":"bronze","role":"builder"}`)
	if code != 200 {
		t.Fatalf("builder claim: %d", code)
	}
	if _, err := testStore.ReportOutcome(ctx, builder.RunToken, store.Report(work.OutcomeStuck, "blocked", "needs a person", nil, nil, nil)); err != nil {
		t.Fatal(err)
	}
	if err := f.engine.EvaluateItem(ctx, id); err != nil {
		t.Fatal(err)
	}
	if state := snapshotItem(t, id).state; state != string(work.StateNeedsHuman) {
		t.Fatalf("state = %q, want needs_human", state)
	}

	over := operatorExecutionRequest(f.server, "POST", requeuePath(id), f.token, "alice", nil)
	if over.Code != 400 || !strings.Contains(over.Body.String(), `"pool_above_limit"`) {
		t.Fatalf("requeue at the plan pool of 50: %d %s, want 400 pool_above_limit", over.Code, over.Body)
	}
	w := operatorExecutionRequest(f.server, "POST", requeuePath(id), f.token, "alice", map[string]any{"commandId": "again-1", "poolUsd": 20})
	if w.Code != 200 {
		t.Fatalf("requeue: %d %s", w.Code, w.Body)
	}
	got := decodeRequeue(t, w)
	if got.State != "queued" || got.FromRound != 1 || got.PoolUSD != 20 || got.ShiftID == nil {
		t.Fatalf("requeue = %+v", got)
	}
	if code, _ := postClaim(t, f.server.Handler(), `{"team":"bronze","role":"builder"}`); code != 200 {
		t.Fatalf("builder claim after requeue: %d", code)
	}
	if len(f.comments.matching("")) != 0 {
		t.Fatal("a requeue of manual work called a tracker")
	}
}

func TestOperatorWorkAdmissionRunWaitsBehindTheTeamCap(t *testing.T) {
	f := newAdmissionFixture(t)
	f.server.TeamCaps = config.RunCaps{"bronze": 1}
	f.admit(t, admissionBody("req-cap-1"), http.StatusCreated)
	f.admit(t, admissionBody("req-cap-2"), http.StatusCreated)
	if code, _ := postClaim(t, f.server.Handler(), `{"team":"bronze","role":"builder"}`); code != 200 {
		t.Fatalf("first claim: %d", code)
	}
	if code, _ := postClaim(t, f.server.Handler(), `{"team":"bronze","role":"builder"}`); code != http.StatusNoContent {
		t.Fatalf("claim over the cap: %d, want 204", code)
	}
	if n, _ := testStore.PendingRuns(context.Background(), "bronze", "builder"); n != 1 {
		t.Fatalf("pending = %d, want the admitted Run waiting", n)
	}
}

func TestOperatorWorkAdmissionSweeperOpensTheShiftAfterACrash(t *testing.T) {
	f := newAdmissionFixture(t)
	ctx := context.Background()
	engine := f.server.Engine
	f.server.Engine = crashBeforeShift{engine}
	_, id := f.admit(t, admissionBody("req-crash"), http.StatusCreated)
	f.server.Engine = engine
	if got := snapshotItem(t, id); got.state != "queued" || got.liveShifts != 0 {
		t.Fatalf("after the crash = %+v, want queued without a Shift", got)
	}
	f.engine.EvaluateAll(ctx)
	if got := snapshotItem(t, id); got.liveShifts != 1 {
		t.Fatalf("the sweeper did not open the Shift: %+v", got)
	}
	replay, _ := f.admit(t, admissionBody("req-crash"), http.StatusOK)
	if !replay.Replayed || replay.WorkItem.ShiftID == nil {
		t.Fatalf("replay = %+v", replay)
	}
	if n := countRows(t, `SELECT count(*) FROM shifts WHERE work_item_id=$1`, id); n != 1 {
		t.Fatalf("shifts = %d, want 1", n)
	}
}

type crashBeforeShift struct{ ShiftEngine }

func (crashBeforeShift) EnsureShift(context.Context, int64, work.WorkItem) error {
	return context.Canceled
}

func TestOperatorWorkAdmissionRequestMatchesThePublishedSchema(t *testing.T) {
	path, err := filepath.Abs("../../docs/contracts/operator-api.v1.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	schema, err := jsonschema.NewCompiler().Compile(path + "#/$defs/workItemAdmissionRequest")
	if err != nil {
		t.Fatal(err)
	}
	budget := 10.0
	data, _ := json.Marshal(store.AdmitOperatorWorkItem{RequestID: "req-1", Team: "bronze", Target: "app", Title: "t", Description: "d", BudgetUSD: &budget})
	instance, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if err := schema.Validate(instance); err != nil {
		t.Fatalf("Go request violates the schema: %v", err)
	}
	var raw struct {
		Defs map[string]struct {
			Properties map[string]any `json:"properties"`
		} `json:"$defs"`
	}
	file, err := filepath.Abs("../../docs/contracts/operator-api.v1.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(content, &raw); err != nil {
		t.Fatal(err)
	}
	var published []string
	for name := range raw.Defs["workItemAdmissionRequest"].Properties {
		published = append(published, name)
	}
	var fields []string
	typ := reflect.TypeOf(store.AdmitOperatorWorkItem{})
	for i := 0; i < typ.NumField(); i++ {
		fields = append(fields, strings.Split(typ.Field(i).Tag.Get("json"), ",")[0])
	}
	sort.Strings(published)
	sort.Strings(fields)
	if !reflect.DeepEqual(published, fields) {
		t.Fatalf("schema properties %v differ from Go fields %v", published, fields)
	}
}
