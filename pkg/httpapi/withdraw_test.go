package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/ploeg-hq/ploeg/pkg/provider"
	"github.com/ploeg-hq/ploeg/pkg/provider/vikunja"
	"github.com/ploeg-hq/ploeg/pkg/shiftengine"
	"github.com/ploeg-hq/ploeg/pkg/store"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

func vikunjaWebhook(t *testing.T, h http.Handler, event, taskID, assignee string) int {
	t.Helper()
	body := fmt.Sprintf(`{"event_name":%q,"data":{"task":{"id":%s,"title":"withdrawal fixture","project_id":7},"assignee":{"username":%q}}}`, event, taskID, assignee)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, signedTrackerHook("vikunja", body))
	return w.Code
}

func withdrawalServer(engine *shiftengine.Engine) *Server {
	s := &Server{
		Store:          testStore,
		LeaseTTL:       time.Minute,
		Log:            slog.New(slog.DiscardHandler),
		WorkerSecurity: &WorkerSecurity{AllowLegacy: true},
		Trackers: map[string]provider.TrackerProvider{"vikunja": &vikunja.Provider{Secret: testTrackerSecret,
			DefaultTeam: "bronze", TeamMap: map[string]string{"builder": "bronze", "reviewer-bot": "silver"},
			Log: slog.New(slog.DiscardHandler),
		}},
	}
	if engine != nil {
		s.Engine = engine
	}
	return s
}

type itemSnapshot struct {
	state       string
	attempts    int
	liveShifts  int
	closeReason string
	unfinished  int
}

func snapshotItem(t *testing.T, id int64) itemSnapshot {
	t.Helper()
	ctx := context.Background()
	var s itemSnapshot
	if err := testPool.QueryRow(ctx, `SELECT state, attempts FROM work_items WHERE id=$1`, id).Scan(&s.state, &s.attempts); err != nil {
		t.Fatal(err)
	}
	if err := testPool.QueryRow(ctx, `SELECT count(*) FILTER (WHERE closed_at IS NULL),
		COALESCE((SELECT close_reason FROM shifts WHERE work_item_id=$1 AND closed_at IS NOT NULL ORDER BY id DESC LIMIT 1), '')
		FROM shifts WHERE work_item_id=$1`, id).Scan(&s.liveShifts, &s.closeReason); err != nil {
		t.Fatal(err)
	}
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM agent_runs WHERE work_item_id=$1 AND state<>'finished'`, id).Scan(&s.unfinished); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestUnassignWebhookCancelsPendingShift(t *testing.T) {
	reset(t)
	ctx := context.Background()
	shiftFixture(t, "4101", 5, []store.Role{{Name: "security"}, {Name: "reviewer"}})
	id, _, err := testStore.TrackerWorkItemID(ctx, "vikunja", "4101")
	if err != nil {
		t.Fatal(err)
	}
	engine := &shiftengine.Engine{Store: testStore, Log: slog.New(slog.DiscardHandler), Uniform: true}
	h := withdrawalServer(engine).Handler()

	if code := vikunjaWebhook(t, h, "task.assignee.deleted", "4101", "reviewer-bot"); code != http.StatusAccepted {
		t.Fatalf("other team's unassignment: %d", code)
	}
	if got := snapshotItem(t, id); got.state != "queued" || got.liveShifts != 1 || got.unfinished != 2 {
		t.Fatalf("unassigning another team's assignee withdrew the item: %+v", got)
	}

	if code := vikunjaWebhook(t, h, "task.assignee.deleted", "4101", "builder"); code != http.StatusAccepted {
		t.Fatalf("unassignment: %d", code)
	}
	got := snapshotItem(t, id)
	if got.state != string(work.StateWithdrawn) || got.liveShifts != 0 || got.closeReason != store.CloseReasonWithdrawnUnassigned || got.unfinished != 0 {
		t.Fatalf("unassignment did not withdraw the item: %+v", got)
	}
	for _, role := range []string{"security", "reviewer"} {
		if n, err := testStore.PendingRuns(ctx, "bronze", role); err != nil || n != 0 {
			t.Fatalf("pending %s runs after withdrawal: %d %v", role, n, err)
		}
	}
	var audited int
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE work_item_id=$1 AND action='work_item.withdrawn' AND actor='webhook:vikunja'`, id).Scan(&audited); err != nil || audited != 1 {
		t.Fatalf("withdrawal audit rows=%d err=%v", audited, err)
	}

	engine.EvaluateAll(ctx)
	if _, err := testStore.ExpireLeases(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := testStore.ExpireRuns(ctx); err != nil {
		t.Fatal(err)
	}
	if settled, err := testStore.SettleItem(ctx, id, work.StateQueued, "late evaluator"); err != nil || settled != work.StateWithdrawn {
		t.Fatalf("late settle overrode the withdrawal: %s %v", settled, err)
	}
	if got := snapshotItem(t, id); got.state != string(work.StateWithdrawn) || got.liveShifts != 0 {
		t.Fatalf("a sweep retried withdrawn work: %+v", got)
	}

	if code := vikunjaWebhook(t, h, "task.assignee.deleted", "4101", "builder"); code != http.StatusAccepted {
		t.Fatalf("repeated unassignment: %d", code)
	}
	if code := vikunjaWebhook(t, h, "task.assignee.deleted", "9999", "builder"); code != http.StatusAccepted {
		t.Fatalf("unknown item unassignment: %d", code)
	}
}

func TestOperatorCancelBlocksRunningRunKey(t *testing.T) {
	s, bootstrap := secureWorkerFixture(t, &managedBrokerFixture{})
	consumers, token := operatorTestConsumers(t, []string{"bronze"}, true)
	readOnly, readToken := operatorTestConsumers(t, []string{"bronze"}, false)
	readOnly[0].Principal.Name = "reader"
	s.OperatorConfig = OperatorConfig{Consumers: append(consumers, readOnly...)}
	ctx := context.Background()

	w := workerRequest(s, "POST", "/api/v1/claim", bootstrap, "pod", `{"team":"bronze","role":"reviewer"}`)
	var claim struct {
		RunToken     string        `json:"runToken"`
		ControlToken string        `json:"controlToken"`
		WorkItem     work.WorkItem `json:"workItem"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &claim) != nil {
		t.Fatalf("claim failed: %d %s", w.Code, w.Body)
	}
	if w := workerRequest(s, "POST", "/api/v1/runs/"+claim.RunToken+"/llm/credential", claim.ControlToken, "pod", ""); w.Code != 200 {
		t.Fatalf("credential: %d %s", w.Code, w.Body)
	}
	path := "/api/v1/operator/work-items/" + claim.WorkItem.ID + "/cancel"

	if w := operatorExecutionRequest(s, "POST", path, readToken, "alice", nil); w.Code != 403 {
		t.Fatalf("read-only consumer cancelled: %d %s", w.Code, w.Body)
	}
	if w := operatorExecutionRequest(s, "POST", path, token, "", nil); w.Code != 400 {
		t.Fatalf("cancel without actor: %d %s", w.Code, w.Body)
	}

	w = operatorExecutionRequest(s, "POST", path, token, "alice", nil)
	if w.Code != 200 {
		t.Fatalf("cancel: %d %s", w.Code, w.Body)
	}
	schemaPath, err := filepath.Abs("../../docs/contracts/operator-api.v1.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	schema, err := jsonschema.NewCompiler().Compile(schemaPath)
	if err != nil {
		t.Fatal(err)
	}
	instance, err := jsonschema.UnmarshalJSON(bytes.NewReader(w.Body.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	if err := schema.Validate(instance); err != nil {
		t.Fatalf("cancel violates published schema: %v\n%s", err, w.Body)
	}
	var body struct {
		Cancellation struct {
			State       string  `json:"state"`
			Withdrawn   bool    `json:"withdrawn"`
			ShiftID     *string `json:"shiftId"`
			StoppedRuns int     `json:"stoppedRuns"`
			KeysBlocked bool    `json:"keysBlocked"`
		} `json:"cancellation"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	c := body.Cancellation
	if c.State != "withdrawn" || !c.Withdrawn || c.ShiftID == nil || c.StoppedRuns != 1 || !c.KeysBlocked {
		t.Fatalf("cancellation: %s", w.Body)
	}
	account, err := testStore.LLMAccount(ctx, claim.RunToken)
	if err != nil || account.State != "blocked" {
		t.Fatalf("running run key not blocked: %+v %v", account, err)
	}
	if got := workerRequest(s, "POST", "/api/v1/runs/"+claim.RunToken+"/renew", claim.ControlToken, "pod", "").Code; got != 401 {
		t.Fatalf("cancelled run renewed: %d", got)
	}
	if got := workerRequest(s, "POST", "/api/v1/runs/"+claim.RunToken+"/llm/credential", claim.ControlToken, "pod", "").Code; got != 401 {
		t.Fatalf("cancelled run minted: %d", got)
	}
	id, err := store.OperatorCursor(claim.WorkItem.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := snapshotItem(t, id); got.state != "withdrawn" || got.liveShifts != 0 || got.closeReason != store.CloseReasonWithdrawnByOperator || got.unfinished != 0 {
		t.Fatalf("cancel left live work: %+v", got)
	}
	var actor string
	if err := testPool.QueryRow(ctx, `SELECT actor FROM audit_log WHERE work_item_id=$1 AND action='work_item.withdrawn'`, id).Scan(&actor); err != nil || actor != "operator:workbench:alice" {
		t.Fatalf("withdrawal actor=%q err=%v", actor, err)
	}

	w = operatorExecutionRequest(s, "POST", path, token, "alice", nil)
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || w.Code != 200 || body.Cancellation.Withdrawn || body.Cancellation.State != "withdrawn" {
		t.Fatalf("repeated cancel: %d %s", w.Code, w.Body)
	}
}

func TestOperatorCancelRespectsScopeAndExecutionOwnership(t *testing.T) {
	reset(t)
	ctx := context.Background()
	consumers, token := operatorTestConsumers(t, []string{"silver"}, true)
	s := &Server{Store: testStore, Log: slog.New(slog.DiscardHandler), OperatorConfig: OperatorConfig{Consumers: consumers}}
	other, _, err := testStore.IngestAssigned(ctx, work.WorkItem{Provider: "vikunja", ExternalID: "4301", Team: "gold", Title: "outside scope"})
	if err != nil {
		t.Fatal(err)
	}
	if w := operatorExecutionRequest(s, "POST", fmt.Sprintf("/api/v1/operator/work-items/%d/cancel", other), token, "alice", nil); w.Code != 404 {
		t.Fatalf("cross-team cancel: %d %s", w.Code, w.Body)
	}
	e, _, err := testStore.AdmitOperatorExecution(ctx, "workbench", "alice", operatorHTTPInput("cancel-owned"), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if w := operatorExecutionRequest(s, "POST", "/api/v1/operator/work-items/"+e.WorkItemID+"/cancel", token, "alice", nil); w.Code != 409 {
		t.Fatalf("operator-owned cancel: %d %s", w.Code, w.Body)
	}
}

func TestReassignmentAfterWithdrawalRequeuesCleanly(t *testing.T) {
	reset(t)
	ctx := context.Background()
	engine := &shiftengine.Engine{Store: testStore, Log: slog.New(slog.DiscardHandler), Uniform: true}
	h := withdrawalServer(engine).Handler()
	if code := vikunjaWebhook(t, h, "task.assignee.created", "4201", "builder"); code != http.StatusAccepted {
		t.Fatalf("assignment: %d", code)
	}
	id, _, err := testStore.TrackerWorkItemID(ctx, "vikunja", "4201")
	if err != nil {
		t.Fatal(err)
	}
	status, claimed := postClaim(t, h, `{"team":"bronze"}`)
	if status != http.StatusOK {
		t.Fatalf("claim: %d", status)
	}
	if code := vikunjaWebhook(t, h, "task.assignee.deleted", "4201", "builder"); code != http.StatusAccepted {
		t.Fatalf("unassignment: %d", code)
	}
	if got := snapshotItem(t, id); got.state != "withdrawn" || got.unfinished != 0 {
		t.Fatalf("withdrawal: %+v", got)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/runs/"+claimed.RunToken+"/renew", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("withdrawn run renew: %d", rec.Code)
	}

	if code := vikunjaWebhook(t, h, "task.assignee.created", "4201", "builder"); code != http.StatusAccepted {
		t.Fatalf("re-assignment: %d", code)
	}
	got := snapshotItem(t, id)
	if got.state != "queued" || got.attempts != 0 || got.liveShifts != 1 || got.unfinished != 1 {
		t.Fatalf("re-assignment did not re-queue cleanly: %+v", got)
	}
	status, again := postClaim(t, h, `{"team":"bronze"}`)
	if status != http.StatusOK || again.WorkItem.ID != fmt.Sprint(id) || again.RunToken == claimed.RunToken {
		t.Fatalf("re-queued item not claimable: %d %+v", status, again)
	}
}

func closeWebhook(t *testing.T, h http.Handler, taskID string) int {
	t.Helper()
	body := fmt.Sprintf(`{"event_name":"task.updated","data":{"task":{"id":%s,"title":"withdrawal fixture","project_id":7,"done":true}}}`, taskID)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, signedTrackerHook("vikunja", body))
	return w.Code
}

func TestClosingATaskWithdrawsOnlyWorkThatHasNotStarted(t *testing.T) {
	reset(t)
	ctx := context.Background()
	shiftFixture(t, "4201", 5, []store.Role{{Name: "builder"}})
	shiftFixture(t, "4202", 5, []store.Role{{Name: "builder"}})
	unstarted, _, err := testStore.TrackerWorkItemID(ctx, "vikunja", "4201")
	if err != nil {
		t.Fatal(err)
	}
	started, _, err := testStore.TrackerWorkItemID(ctx, "vikunja", "4202")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := testPool.Exec(ctx, `UPDATE agent_runs SET started_at = now() WHERE work_item_id = $1`, started); err != nil {
		t.Fatal(err)
	}
	h := withdrawalServer(&shiftengine.Engine{Store: testStore, Log: slog.New(slog.DiscardHandler), Uniform: true}).Handler()

	for _, task := range []string{"4201", "4202", "9999"} {
		if code := closeWebhook(t, h, task); code != http.StatusAccepted {
			t.Fatalf("close of task %s: %d", task, code)
		}
	}
	if got := snapshotItem(t, unstarted); got.state != string(work.StateWithdrawn) || got.liveShifts != 0 || got.closeReason != store.CloseReasonWithdrawnClosed {
		t.Fatalf("closing an unstarted item did not withdraw it: %+v", got)
	}
	if got := snapshotItem(t, started); got.state != "queued" || got.liveShifts != 1 {
		t.Fatalf("closing a started item withdrew it: %+v", got)
	}
}

func reopenWebhook(t *testing.T, h http.Handler, taskID string) int {
	t.Helper()
	body := fmt.Sprintf(`{"event_name":"task.updated","data":{"task":{"id":%s,"title":"withdrawal fixture","project_id":7,"done":false}}}`, taskID)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, signedTrackerHook("vikunja", body))
	return w.Code
}

func stoppedFixture(t *testing.T, externalID, state string) int64 {
	t.Helper()
	ctx := context.Background()
	shiftFixture(t, externalID, 5, []store.Role{{Name: "builder"}})
	id, _, err := testStore.TrackerWorkItemID(ctx, "vikunja", externalID)
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`UPDATE agent_runs SET state='finished', started_at=now(), finished_at=now() WHERE work_item_id=$1`,
		`UPDATE shifts SET closed_at=now(), close_reason='pool_exhausted' WHERE work_item_id=$1`,
	} {
		if _, err := testPool.Exec(ctx, stmt, id); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := testPool.Exec(ctx, `UPDATE work_items SET state=$2 WHERE id=$1`, id, state); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestClosingATaskSettlesStoppedWorkWithoutAComment(t *testing.T) {
	reset(t)
	ctx := context.Background()
	var writes atomic.Int32
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writes.Add(1)
		}
		http.NotFound(w, r)
	}))
	defer api.Close()

	stopped := map[string]string{"4301": "needs_human", "4302": "awaiting_review"}
	ids := map[string]int64{}
	for task, state := range stopped {
		ids[task] = stoppedFixture(t, task, state)
	}
	shiftFixture(t, "4303", 5, []store.Role{{Name: "builder"}})
	running, _, err := testStore.TrackerWorkItemID(ctx, "vikunja", "4303")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := testStore.ClaimRole(ctx, "bronze", "builder", time.Minute, 3); err != nil {
		t.Fatal(err)
	}
	if _, err := testPool.Exec(ctx, `UPDATE work_items SET state='needs_human' WHERE id=$1`, running); err != nil {
		t.Fatal(err)
	}

	s := withdrawalServer(&shiftengine.Engine{Store: testStore, Log: slog.New(slog.DiscardHandler), Uniform: true})
	tp := s.Trackers["vikunja"].(*vikunja.Provider)
	tp.BaseURL, tp.Token = api.URL, "fixture"
	h := s.Handler()

	for _, task := range []string{"4301", "4302", "4303"} {
		if code := closeWebhook(t, h, task); code != http.StatusAccepted {
			t.Fatalf("close of task %s: %d", task, code)
		}
	}
	for task, id := range ids {
		if got := snapshotItem(t, id); got.state != string(work.StateWithdrawn) {
			t.Fatalf("closing a %s item left it %+v", stopped[task], got)
		}
		var audited int
		if err := testPool.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE work_item_id=$1 AND action='work_item.withdrawn'
			AND actor='webhook:vikunja' AND detail->>'reason'=$2`, id, store.CloseReasonWithdrawnClosed).Scan(&audited); err != nil || audited != 1 {
			t.Fatalf("%s settle audit rows=%d err=%v", stopped[task], audited, err)
		}
	}
	if got := snapshotItem(t, running); got.state != "needs_human" || got.unfinished != 1 {
		t.Fatalf("closing an item with a running Run changed it: %+v", got)
	}
	if n := writes.Load(); n != 0 {
		t.Fatalf("Ploeg wrote to the tracker %d times for a person's own close", n)
	}

	if code := reopenWebhook(t, h, "4301"); code != http.StatusAccepted {
		t.Fatalf("re-open: %d", code)
	}
	if got := snapshotItem(t, ids["4301"]); got.state != string(work.StateWithdrawn) {
		t.Fatalf("re-opening the task resurrected the item: %+v", got)
	}
	if code := vikunjaWebhook(t, h, "task.assignee.created", "4301", "builder"); code != http.StatusAccepted {
		t.Fatalf("re-assignment: %d", code)
	}
	if got := snapshotItem(t, ids["4301"]); got.state != "queued" {
		t.Fatalf("re-assignment did not start new work: %+v", got)
	}
}
