package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/harness"
	"github.com/ploeg-hq/ploeg/pkg/store"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

const (
	yieldedAt  = "1111111111111111111111111111111111111111"
	wipCommit  = "2222222222222222222222222222222222222222"
	yieldClaim = `{"team":"bronze","role":"builder","harness":"acp","harnessProfile":"opencode","yields":true}`
)

func takeOverPath(id int64) string {
	return fmt.Sprintf("/api/v1/operator/work-items/%d/take-over", id)
}

func renewRun(t *testing.T, h http.Handler, token string) map[string]any {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/runs/"+token+"/renew", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("renew: %d %s", rec.Code, rec.Body)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	return body
}

func itemDetail(t *testing.T, s *Server, token string, id int64) map[string]any {
	t.Helper()
	w := operatorGet(s, token, fmt.Sprintf("/api/v1/operator/work-items/%d", id))
	if w.Code != 200 {
		t.Fatalf("detail: %d %s", w.Code, w.Body)
	}
	validOperatorResponse(t, w)
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	return body
}

// ADR-0142 over HTTP: a person takes over a running writer, the lease
// renewal asks it to yield, its yielded outcome parks the Work Item for that
// person with the branch and commit in its detail, only they hand it back,
// and the hand back's writer is told where to continue.
func TestTakeOverAndHandBackOverTheAPIs(t *testing.T) {
	f := newRequeueFixture(t)
	s, token := f.server, f.token
	h := s.Handler()
	id := f.item(t, "5801")
	status, claim := postClaim(t, h, yieldClaim)
	if status != http.StatusOK {
		t.Fatalf("claim: %d", status)
	}
	if body := renewRun(t, h, claim.RunToken); body["yield"] != nil {
		t.Fatalf("a Run nobody took over was asked to yield: %v", body)
	}

	if w := operatorExecutionRequest(s, "POST", takeOverPath(id), token, "alice", map[string]any{}); w.Code != 400 || errorCode(t, w) != "command_required" {
		t.Fatalf("take-over without a commandId: %d %s", w.Code, w.Body)
	}
	if w := operatorExecutionRequest(s, "POST", takeOverPath(id), f.readOnly, "alice", map[string]any{"commandId": "t1"}); w.Code != 403 {
		t.Fatalf("take-over from a read-only consumer: %d %s", w.Code, w.Body)
	}
	if w := operatorExecutionRequest(s, "POST", takeOverPath(id), token, "alice", map[string]any{"commandId": "t1", "expectedShiftId": "999999"}); w.Code != 409 || errorCode(t, w) != "shift_changed" {
		t.Fatalf("take-over of another Shift: %d %s", w.Code, w.Body)
	}
	w := operatorExecutionRequest(s, "POST", takeOverPath(id), token, "alice", map[string]any{"commandId": "t1"})
	if w.Code != 202 {
		t.Fatalf("take-over: %d %s", w.Code, w.Body)
	}
	validOperatorResponse(t, w)
	var accepted struct {
		TakeOver struct {
			State    string           `json:"state"`
			Runs     []map[string]any `json:"runs"`
			Replayed bool             `json:"replayed"`
			Branch   string           `json:"branch"`
		} `json:"takeOver"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &accepted); err != nil || accepted.TakeOver.State != store.TakeOverYielding ||
		len(accepted.TakeOver.Runs) != 1 || accepted.TakeOver.Runs[0]["role"] != "builder" || accepted.TakeOver.Branch == "" {
		t.Fatalf("take-over response = %s", w.Body)
	}
	if w := operatorExecutionRequest(s, "POST", takeOverPath(id), token, "alice", map[string]any{"commandId": "t1"}); w.Code != 202 || !strings.Contains(w.Body.String(), `"replayed":true`) {
		t.Fatalf("replayed take-over: %d %s", w.Code, w.Body)
	}
	if w := operatorExecutionRequest(s, "POST", takeOverPath(id), token, "bob", map[string]any{"commandId": "t1"}); w.Code != 409 || errorCode(t, w) != "command_conflict" {
		t.Fatalf("the same commandId for another person: %d %s", w.Code, w.Body)
	}
	if w := operatorExecutionRequest(s, "POST", takeOverPath(id), token, "bob", map[string]any{"commandId": "t2"}); w.Code != 409 || errorCode(t, w) != "take_over_pending" {
		t.Fatalf("a second take-over: %d %s", w.Code, w.Body)
	}
	if n := countRows(t, `SELECT count(*) FROM audit_log WHERE work_item_id=$1 AND action=$2 AND detail->'attribution'->>'actor'='alice'`, id, store.AuditWorkItemTakenOver); n != 1 {
		t.Fatalf("attributed take-over events = %d", n)
	}
	if body := renewRun(t, h, claim.RunToken); body["yield"] != true {
		t.Fatalf("renewal after the take-over = %v, want yield", body)
	}
	if to, _ := itemDetail(t, s, token, id)["takeOver"].(map[string]any); to == nil || to["state"] != store.TakeOverYielding || to["by"] != "alice" || to["commit"] != nil {
		t.Fatalf("detail takeOver while yielding = %v", to)
	}

	if _, err := testPool.Exec(context.Background(), `INSERT INTO run_llm_accounts (run_token, alias, authorized, models, ttl_seconds, state, observed_spend)
		VALUES ($1, 'a', 3, '[]', 60, 'issued', 0.5)`, claim.RunToken); err != nil {
		t.Fatal(err)
	}
	rep := harness.OutcomeReport{Outcome: work.OutcomeYielded, Summary: "a person took over the Run",
		Yield: &harness.YieldReport{Branch: accepted.TakeOver.Branch, Commit: wipCommit, WorkInProgress: wipCommit, Pushed: true}}
	if w := postOutcome(t, h, claim.RunToken, rep); w.Code != http.StatusNoContent {
		t.Fatalf("yielded outcome: %d %s", w.Code, w.Body)
	}
	detail := itemDetail(t, s, token, id)
	item := detail["item"].(map[string]any)
	to, _ := detail["takeOver"].(map[string]any)
	if item["state"] != string(work.StateNeedsHuman) || to == nil || to["state"] != store.TakeOverTakenOver ||
		to["commit"] != wipCommit || to["workInProgress"] != wipCommit || to["pushed"] != true || to["yieldedAt"] == nil {
		t.Fatalf("detail after the yield: item %v takeOver %v", item, to)
	}
	offer, _ := detail["resume"].(map[string]any)
	if offer == nil || offer["reason"] != "taken_over" || offer["fromRound"] != float64(1) || offer["ready"] != true || offer["poolUsd"] != 9.5 {
		t.Fatalf("resume offer = %v", offer)
	}
	if n := countRows(t, `SELECT count(*) FROM shifts WHERE work_item_id=$1 AND close_reason=$2`, id, work.CloseReasonTakenOver); n != 1 {
		t.Fatalf("taken_over Shifts = %d", n)
	}

	if w := operatorExecutionRequest(s, "POST", requeuePath(id), token, "bob", map[string]any{"commandId": "r1", "resume": true}); w.Code != 409 || errorCode(t, w) != "taken_over_by_another" {
		t.Fatalf("a hand back by another person: %d %s", w.Code, w.Body)
	}
	if w := operatorExecutionRequest(s, "POST", requeuePath(id), token, "alice", map[string]any{"commandId": "r2", "resume": true,
		"note": "I renamed the handler; keep the new name."}); w.Code != 200 {
		t.Fatalf("hand back: %d %s", w.Code, w.Body)
	}
	to, _ = itemDetail(t, s, token, id)["takeOver"].(map[string]any)
	if to == nil || to["state"] != store.TakeOverHandedBack || to["handedBackShiftId"] == nil || to["handedBackAt"] == nil {
		t.Fatalf("detail takeOver after the hand back = %v", to)
	}

	status, next := postClaim(t, h, yieldClaim)
	if status != http.StatusOK {
		t.Fatalf("claim after the hand back: %d", status)
	}
	if next.Handback == nil || next.Handback.Branch != accepted.TakeOver.Branch || next.Handback.YieldedCommit != wipCommit {
		t.Fatalf("hand back on the claim = %+v", next.Handback)
	}
	var briefing strings.Builder
	for _, b := range next.Briefing {
		briefing.WriteString(b.Findings)
	}
	for _, want := range []string{"alice had taken over a Run", "I renamed the handler"} {
		if !strings.Contains(briefing.String(), want) {
			t.Fatalf("briefing lacks %q: %s", want, briefing.String())
		}
	}
}

// ADR-0142: a take-over refuses before anything changes when nothing runs,
// a running Run's Harness cannot yield, or the Work Item acts for another
// person; a Run nobody asked to yield cannot report yielded.
func TestTakeOverRefusals(t *testing.T) {
	f := newRequeueFixture(t)
	s, token := f.server, f.token
	h := s.Handler()

	idle := f.item(t, "5811")
	if w := operatorExecutionRequest(s, "POST", takeOverPath(idle), token, "alice", map[string]any{"commandId": "n1"}); w.Code != 409 || errorCode(t, w) != "nothing_running" {
		t.Fatalf("take-over of an unclaimed Work Item: %d %s", w.Code, w.Body)
	}
	status, claim := postClaim(t, h, `{"team":"bronze","role":"builder","harness":"claude-code"}`)
	if status != http.StatusOK {
		t.Fatalf("claim: %d", status)
	}
	w := operatorExecutionRequest(s, "POST", takeOverPath(idle), token, "alice", map[string]any{"commandId": "n2"})
	if w.Code != 409 || errorCode(t, w) != "yield_unsupported" || !strings.Contains(w.Body.String(), "claude-code") {
		t.Fatalf("take-over of a Harness that cannot yield: %d %s", w.Code, w.Body)
	}
	if n := countRows(t, `SELECT count(*) FROM agent_runs WHERE work_item_id=$1 AND yield_requested_at IS NOT NULL`, idle); n != 0 {
		t.Fatalf("a refused take-over asked %d Runs to yield", n)
	}
	if body := renewRun(t, h, claim.RunToken); body["yield"] != nil {
		t.Fatalf("a refused take-over reached the Run: %v", body)
	}
	unasked := harness.OutcomeReport{Outcome: work.OutcomeYielded, Summary: "x", Yield: &harness.YieldReport{Branch: "b", Pushed: true}}
	if w := postOutcome(t, h, claim.RunToken, unasked); w.Code != http.StatusBadRequest {
		t.Fatalf("a yielded outcome nobody asked for: %d %s", w.Code, w.Body)
	}
	bad := harness.OutcomeReport{Outcome: work.OutcomeYielded, Summary: "x", Yield: &harness.YieldReport{Branch: "b", Commit: "abc", Pushed: true}}
	if w := postOutcome(t, h, claim.RunToken, bad); w.Code != http.StatusBadRequest {
		t.Fatalf("a yield with a malformed commit: %d %s", w.Code, w.Body)
	}

	owned := f.item(t, "5812")
	if _, err := testPool.Exec(context.Background(), `UPDATE work_items SET acts_for='carol' WHERE id=$1`, owned); err != nil {
		t.Fatal(err)
	}
	if status, _ := postClaim(t, h, yieldClaim); status != http.StatusOK {
		t.Fatalf("claim: %d", status)
	}
	if w := operatorExecutionRequest(s, "POST", takeOverPath(owned), token, "alice", map[string]any{"commandId": "p1"}); w.Code != 403 || errorCode(t, w) != "person_forbidden" {
		t.Fatalf("take-over of another person's Work Item: %d %s", w.Code, w.Body)
	}
	if w := operatorExecutionRequest(s, "POST", takeOverPath(owned), token, "carol", map[string]any{"commandId": "p2"}); w.Code != 202 {
		t.Fatalf("take-over by the Work Item's person: %d %s", w.Code, w.Body)
	}
}

// A reader asked to yield that finished another way still parks the Work
// Item for the person, ahead of its stuck outcome, and the Round's unclaimed
// Runs were cancelled at the take-over, so the Round ends with it.
func TestTakeOverParksAWorkItemWhoseRunFinishedFirst(t *testing.T) {
	ctx := context.Background()
	f := newRequeueFixture(t)
	s, token := f.server, f.token
	h := s.Handler()
	id, shift := shiftFixtureItem(t, "5821", []store.Role{{Name: "reviewer", Cap: 1}, {Name: "reviewer", Cap: 1}})
	status, claim := postClaim(t, h, strings.Replace(yieldClaim, "builder", "reviewer", 1))
	if status != http.StatusOK {
		t.Fatalf("claim: %d", status)
	}
	w := operatorExecutionRequest(s, "POST", takeOverPath(id), token, "alice", map[string]any{"commandId": "f1"})
	if w.Code != 202 || !strings.Contains(w.Body.String(), `"cancelledPendingRuns":1`) {
		t.Fatalf("take-over: %d %s", w.Code, w.Body)
	}
	if w := postOutcome(t, h, claim.RunToken, harness.OutcomeReport{Outcome: work.OutcomeStuck, Summary: "gave up", StuckReason: "unclear"}); w.Code != http.StatusNoContent {
		t.Fatalf("outcome: %d %s", w.Code, w.Body)
	}
	if err := f.engine.EvaluateItem(ctx, id); err != nil {
		t.Fatal(err)
	}
	var reason string
	if err := testPool.QueryRow(ctx, `SELECT close_reason FROM shifts WHERE id=$1`, shift).Scan(&reason); err != nil {
		t.Fatal(err)
	}
	if reason != work.CloseReasonTakenOver {
		t.Fatalf("close reason = %q, want taken_over over the stuck Run", reason)
	}
	to, _ := itemDetail(t, s, token, id)["takeOver"].(map[string]any)
	if to == nil || to["state"] != store.TakeOverTakenOver || to["yieldedAt"] != nil {
		t.Fatalf("takeOver = %v", to)
	}
	if n := countRows(t, `SELECT count(*) FROM audit_log WHERE work_item_id=$1 AND action='work_item.needs_human'
		AND detail->>'reason' LIKE '%alice%finished before it reached a safe point%'`, id); n != 1 {
		t.Fatal("the settle reason does not say who holds the Work Item and that its Run finished first")
	}
}
