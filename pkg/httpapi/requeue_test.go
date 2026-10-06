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
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/ploeg-hq/ploeg/pkg/plan"
	"github.com/ploeg-hq/ploeg/pkg/provider"
	"github.com/ploeg-hq/ploeg/pkg/provider/vikunja"
	"github.com/ploeg-hq/ploeg/pkg/shiftengine"
	"github.com/ploeg-hq/ploeg/pkg/store"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

type taskComments struct {
	mu     sync.Mutex
	bodies []string
}

func (c *taskComments) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Comment string `json:"comment"`
	}
	data, _ := io.ReadAll(r.Body)
	_ = json.Unmarshal(data, &body)
	if r.Method == http.MethodPut && strings.HasSuffix(r.URL.Path, "/comments") {
		c.mu.Lock()
		c.bodies = append(c.bodies, body.Comment)
		c.mu.Unlock()
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{}`))
}

func (c *taskComments) matching(text string) []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []string
	for _, b := range c.bodies {
		if strings.Contains(b, text) {
			out = append(out, b)
		}
	}
	return out
}

type requeueFixture struct {
	server   *Server
	engine   *shiftengine.Engine
	token    string
	readOnly string
	comments *taskComments
}

func newRequeueFixture(t *testing.T) requeueFixture {
	t.Helper()
	reset(t)
	plans, err := plan.Parse(`{"bronze": {"pool": 10, "rounds": [
		{"roles": [{"name": "builder", "writes": true, "cap": 3}]},
		{"roles": [{"name": "reviewer", "writes": false, "cap": 1}]}
	]}}`)
	if err != nil {
		t.Fatal(err)
	}
	comments := &taskComments{}
	api := httptest.NewServer(comments)
	t.Cleanup(api.Close)
	log := slog.New(slog.DiscardHandler)
	tracker := &vikunja.Provider{Secret: testTrackerSecret, BaseURL: api.URL, Token: "test-token", DefaultTeam: "bronze", Log: log}
	engine := &shiftengine.Engine{Store: testStore, Plans: plans, Log: log}
	consumers, token := operatorTestConsumers(t, []string{"bronze"}, true)
	readOnly, readToken := operatorTestConsumers(t, []string{"bronze"}, false)
	readOnly[0].Principal.Name = "reader"
	s := &Server{
		Store: testStore, LeaseTTL: time.Minute, Log: log, RoleCaps: plans, Engine: engine,
		WorkerSecurity: &WorkerSecurity{AllowLegacy: true},
		Trackers:       map[string]provider.TrackerProvider{"vikunja": tracker},
		OperatorConfig: OperatorConfig{Consumers: append(consumers, readOnly...)},
	}
	return requeueFixture{server: s, engine: engine, token: token, readOnly: readToken, comments: comments}
}

func (f requeueFixture) item(t *testing.T, externalID string) int64 {
	t.Helper()
	ctx := context.Background()
	item := work.WorkItem{Provider: "vikunja", ExternalID: externalID, Team: "bronze", Title: "t"}
	id, _, err := testStore.IngestAssigned(ctx, item)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.engine.EnsureShift(ctx, id, item); err != nil {
		t.Fatal(err)
	}
	return id
}

func (f requeueFixture) run(t *testing.T, id int64, role string, outcome work.Outcome, summary string, links []string, failure *string) {
	t.Helper()
	ctx := context.Background()
	rep := store.Report(outcome, summary, "", links, nil, failure)
	if outcome == work.OutcomeStuck {
		rep = store.Report(outcome, summary, "needs a person", links, nil, failure)
	}
	run, err := testStore.ClaimRole(ctx, "bronze", role, time.Minute, 0)
	if err != nil || run == nil {
		t.Fatalf("claim %s: %v", role, err)
	}
	if _, err := testStore.ReportOutcome(ctx, run.RunToken, rep); err != nil {
		t.Fatal(err)
	}
	if err := f.engine.EvaluateItem(ctx, id); err != nil {
		t.Fatal(err)
	}
}

func (f requeueFixture) reviewFailed(t *testing.T, externalID string) int64 {
	t.Helper()
	id := f.item(t, externalID)
	f.run(t, id, "builder", work.OutcomePROpened, "opened", []string{"https://forgejo/o/r/pulls/3"}, nil)
	reason := string(work.FailureAgentError)
	for attempt := 1; attempt <= store.MaxRunAttempts; attempt++ {
		f.run(t, id, "reviewer", work.OutcomeFailed, "acp agent stopped responding", nil, &reason)
	}
	if state := snapshotItem(t, id).state; state != string(work.StateAwaitingReview) {
		t.Fatalf("fixture state = %q, want awaiting_review", state)
	}
	return id
}

func (f requeueFixture) stuckBuilder(t *testing.T, externalID string) int64 {
	t.Helper()
	id := f.item(t, externalID)
	f.run(t, id, "builder", work.OutcomeStuck, "blocked", nil, nil)
	if state := snapshotItem(t, id).state; state != string(work.StateNeedsHuman) {
		t.Fatalf("fixture state = %q, want needs_human", state)
	}
	return id
}

func requeuePath(id int64) string {
	return fmt.Sprintf("/api/v1/operator/work-items/%d/requeue", id)
}

func validOperatorResponse(t *testing.T, w *httptest.ResponseRecorder) {
	t.Helper()
	path, err := filepath.Abs("../../docs/contracts/operator-api.v1.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	schema, err := jsonschema.NewCompiler().Compile(path)
	if err != nil {
		t.Fatal(err)
	}
	instance, err := jsonschema.UnmarshalJSON(bytes.NewReader(w.Body.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	if err := schema.Validate(instance); err != nil {
		t.Fatalf("response violates published schema: %v\n%s", err, w.Body)
	}
}

type requeueResult struct {
	WorkItemID      string  `json:"workItemId"`
	State           string  `json:"state"`
	FromRound       int     `json:"fromRound"`
	PoolUSD         float64 `json:"poolUsd"`
	ShiftID         *string `json:"shiftId"`
	PreviousShiftID *string `json:"previousShiftId"`
	CommandID       *string `json:"commandId"`
	Replayed        bool    `json:"replayed"`
}

func decodeRequeue(t *testing.T, w *httptest.ResponseRecorder) requeueResult {
	t.Helper()
	validOperatorResponse(t, w)
	var body struct {
		Requeue requeueResult `json:"requeue"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	return body.Requeue
}

func countRows(t *testing.T, query string, args ...any) int {
	t.Helper()
	var n int
	if err := testPool.QueryRow(context.Background(), query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestOperatorRequeueReRunsTheReviewWithABriefing(t *testing.T) {
	f := newRequeueFixture(t)
	id := f.reviewFailed(t, "1701")
	body := map[string]any{"fromRound": 2, "poolUsd": 0.4, "note": "check the migration order",
		"commandId": "rerun-1701", "expectedState": "awaiting_review"}

	w := operatorExecutionRequest(f.server, "POST", requeuePath(id), f.token, "alice", body)
	if w.Code != 200 {
		t.Fatalf("requeue: %d %s", w.Code, w.Body)
	}
	got := decodeRequeue(t, w)
	if got.State != "queued" || got.FromRound != 2 || got.PoolUSD != 0.4 || got.ShiftID == nil || got.PreviousShiftID == nil || got.Replayed {
		t.Fatalf("requeue = %+v", got)
	}
	if n := countRows(t, `SELECT count(*) FROM audit_log WHERE work_item_id = $1 AND action = 'work_item.requeued'
		AND actor = 'operator:workbench:alice' AND detail->>'note' = 'check the migration order'
		AND (detail->>'from_round')::int = 2 AND detail->>'command_id' = 'rerun-1701'`, id); n != 1 {
		t.Fatalf("requeue audit rows = %d, want 1", n)
	}
	if comments := f.comments.matching("Restarted from Round 2 by alice."); len(comments) != 1 {
		t.Fatalf("tracker restart comments = %v, want 1", comments)
	}

	code, claim := postClaim(t, f.server.Handler(), `{"team":"bronze","role":"reviewer"}`)
	if code != 200 || claim.Round != 2 || fmt.Sprint(claim.Shift) != *got.ShiftID {
		t.Fatalf("reviewer claim: %d %+v", code, claim)
	}
	var brief string
	for _, finding := range claim.Briefing {
		if finding.Role == "operator restart" {
			brief = finding.Findings
		}
	}
	for _, want := range []string{"check the migration order", "review_failed", "reviewer, round 2, failed: agent_error", "acp agent stopped responding"} {
		if !strings.Contains(brief, want) {
			t.Errorf("briefing does not carry %q:\n%s", want, brief)
		}
	}
	if code, _ := postClaim(t, f.server.Handler(), `{"team":"bronze","role":"builder"}`); code != http.StatusNoContent {
		t.Errorf("builder claim = %d, want 204: the writing Round is not run again", code)
	}

	replay := operatorExecutionRequest(f.server, "POST", requeuePath(id), f.token, "alice", body)
	if replay.Code != 200 {
		t.Fatalf("replay: %d %s", replay.Code, replay.Body)
	}
	again := decodeRequeue(t, replay)
	if !again.Replayed || *again.ShiftID != *got.ShiftID {
		t.Fatalf("replay = %+v, want the first result", again)
	}
	if n := countRows(t, `SELECT count(*) FROM shifts WHERE work_item_id = $1`, id); n != 2 {
		t.Errorf("shifts = %d, want 2: a replay opens no Shift", n)
	}
	if n := countRows(t, `SELECT count(*) FROM audit_log WHERE work_item_id = $1 AND action = 'work_item.requeued'`, id); n != 1 {
		t.Errorf("requeue audit rows after replay = %d, want 1", n)
	}
	if comments := f.comments.matching("Restarted from Round"); len(comments) != 1 {
		t.Errorf("tracker restart comments after replay = %d, want 1", len(comments))
	}
}

func TestOperatorRequeueWithoutABodyStartsAgainFromRoundOne(t *testing.T) {
	f := newRequeueFixture(t)
	id := f.stuckBuilder(t, "1702")

	w := operatorExecutionRequest(f.server, "POST", requeuePath(id), f.token, "alice", nil)
	if w.Code != 200 {
		t.Fatalf("requeue: %d %s", w.Code, w.Body)
	}
	got := decodeRequeue(t, w)
	if got.FromRound != 1 || got.PoolUSD != 10 || got.CommandID != nil || got.ShiftID == nil {
		t.Fatalf("requeue = %+v, want Round 1 with the plan's pool", got)
	}
	code, claim := postClaim(t, f.server.Handler(), `{"team":"bronze","role":"builder"}`)
	if code != 200 || claim.Round != 1 {
		t.Fatalf("builder claim: %d %+v", code, claim)
	}
	var brief string
	for _, finding := range claim.Briefing {
		if finding.Role == "operator restart" {
			brief = finding.Findings
		}
	}
	if !strings.Contains(brief, "builder, round 1, stuck") || !strings.Contains(brief, "run stuck") {
		t.Errorf("briefing does not carry the previous stuck Run and close reason:\n%s", brief)
	}
}

func TestOperatorRequeueRefusals(t *testing.T) {
	type refusal struct {
		name    string
		prepare func(t *testing.T, f requeueFixture) int64
		token   func(f requeueFixture) string
		body    any
		status  int
		code    string
	}
	execute := func(f requeueFixture) string { return f.token }
	cases := []refusal{
		{name: "without execute", prepare: func(t *testing.T, f requeueFixture) int64 { return f.reviewFailed(t, "1710") },
			token: func(f requeueFixture) string { return f.readOnly }, status: 403, code: "execution_forbidden"},
		{name: "live item", prepare: func(t *testing.T, f requeueFixture) int64 { return f.item(t, "1711") },
			token: execute, status: 409, code: "not_requeueable"},
		{name: "done", prepare: func(t *testing.T, f requeueFixture) int64 {
			id := f.reviewFailed(t, "1712")
			forceState(t, id, "done")
			return id
		}, token: execute, status: 409, code: "not_requeueable"},
		{name: "withdrawn", prepare: func(t *testing.T, f requeueFixture) int64 {
			id := f.reviewFailed(t, "1713")
			forceState(t, id, "withdrawn")
			return id
		}, token: execute, status: 409, code: "not_requeueable"},
		{name: "operator owned", prepare: func(t *testing.T, f requeueFixture) int64 {
			id := f.reviewFailed(t, "1714")
			if _, err := testPool.Exec(context.Background(), `UPDATE work_items SET operator_owned = true WHERE id = $1`, id); err != nil {
				t.Fatal(err)
			}
			return id
		}, token: execute, status: 409, code: "operator_owned"},
		{name: "expected state mismatch", prepare: func(t *testing.T, f requeueFixture) int64 { return f.reviewFailed(t, "1715") },
			token: execute, body: map[string]any{"commandId": "c-1715", "expectedState": "needs_human"}, status: 409, code: "state_changed"},
		{name: "nothing to review", prepare: func(t *testing.T, f requeueFixture) int64 { return f.stuckBuilder(t, "1716") },
			token: execute, body: map[string]any{"commandId": "c-1716", "fromRound": 2}, status: 422, code: "nothing_to_review"},
		{name: "round not in plan", prepare: func(t *testing.T, f requeueFixture) int64 { return f.reviewFailed(t, "1717") },
			token: execute, body: map[string]any{"commandId": "c-1717", "fromRound": 3}, status: 422, code: "round_not_in_plan"},
		{name: "pool above maxBudgetUsd", prepare: func(t *testing.T, f requeueFixture) int64 { return f.reviewFailed(t, "1718") },
			token: execute, body: map[string]any{"commandId": "c-1718", "fromRound": 2, "poolUsd": 25.01}, status: 400, code: "pool_above_limit"},
		{name: "body without commandId", prepare: func(t *testing.T, f requeueFixture) int64 { return f.reviewFailed(t, "1719") },
			token: execute, body: map[string]any{"fromRound": 2}, status: 400, code: "command_required"},
		{name: "commandId reused for another requeue", prepare: func(t *testing.T, f requeueFixture) int64 {
			id := f.reviewFailed(t, "1720")
			if w := operatorExecutionRequest(f.server, "POST", requeuePath(id), f.token, "alice", map[string]any{"commandId": "c-1720", "fromRound": 2}); w.Code != 200 {
				t.Fatalf("first requeue: %d %s", w.Code, w.Body)
			}
			return id
		}, token: execute, body: map[string]any{"commandId": "c-1720", "fromRound": 1}, status: 409, code: "command_conflict"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newRequeueFixture(t)
			id := tc.prepare(t, f)
			before := snapshotItem(t, id)
			audits := countRows(t, `SELECT count(*) FROM audit_log WHERE work_item_id = $1 AND action = 'work_item.requeued'`, id)

			w := operatorExecutionRequest(f.server, "POST", requeuePath(id), tc.token(f), "alice", tc.body)
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
			if after := snapshotItem(t, id); after != before {
				t.Errorf("a refused requeue changed the item: %+v -> %+v", before, after)
			}
			if n := countRows(t, `SELECT count(*) FROM audit_log WHERE work_item_id = $1 AND action = 'work_item.requeued'`, id); n != audits {
				t.Errorf("requeue audit rows = %d, want %d", n, audits)
			}
		})
	}
}
