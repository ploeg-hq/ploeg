package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/harness"
	"github.com/ploeg-hq/ploeg/pkg/store"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

// ADR-0121 over HTTP: a reader's findings and questions arrive on the outcome
// endpoint, the operator API shows them, a person answers, and the next
// claim is briefed with both.
func TestFindingsAndQuestionsOverTheAPIs(t *testing.T) {
	ctx := context.Background()
	s, token := notesServer(t)
	id, shift := shiftFixtureItem(t, "5601", []store.Role{{Name: "reviewer", Cap: 1}})
	h := s.Handler()
	status, claim := postClaim(t, h, `{"team":"bronze","role":"reviewer"}`)
	if status != http.StatusOK {
		t.Fatalf("claim: %d", status)
	}
	bad := harness.OutcomeReport{Outcome: work.OutcomeNoChangeNeeded, Summary: "x",
		FindingAnswers: []harness.FindingAnswer{{FindingID: "f1-1", Status: harness.AnswerDisagree}}}
	if w := postOutcome(t, h, claim.RunToken, bad); w.Code != http.StatusBadRequest {
		t.Fatalf("a disagree answer without a reason: %d %s", w.Code, w.Body)
	}
	rep := harness.OutcomeReport{Outcome: work.OutcomeNoChangeNeeded, Summary: "read it", Findings: "prose",
		FindingItems: []harness.ReviewFinding{{Severity: harness.SeverityBlocking, Title: "no tests", Path: "a.go", Line: 4}},
		Questions:    []harness.Question{{To: harness.AskPerson, Text: "Is the old endpoint still used?"}}}
	if w := postOutcome(t, h, claim.RunToken, rep); w.Code != http.StatusNoContent {
		t.Fatalf("outcome: %d %s", w.Code, w.Body)
	}

	w := operatorGet(s, token, fmt.Sprintf("/api/v1/operator/work-items/%d", id))
	if w.Code != 200 {
		t.Fatalf("detail: %d %s", w.Code, w.Body)
	}
	validOperatorResponse(t, w)
	var detail struct {
		Findings  []store.OperatorFinding  `json:"findings"`
		Questions []store.OperatorQuestion `json:"questions"`
		Runs      []store.OperatorRun      `json:"runs"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	if len(detail.Findings) != 1 || len(detail.Questions) != 1 || !detail.Questions[0].WaitingForPerson || len(detail.Runs[0].Questions) != 1 {
		t.Fatalf("detail = %s", w.Body)
	}
	qid := detail.Questions[0].ID

	answerPath := fmt.Sprintf("/api/v1/operator/work-items/%d/questions/%s/answer", id, qid)
	if w := operatorExecutionRequest(s, "POST", answerPath, token, "alice", map[string]any{"commandId": "a1", "answer": "  Yes, by the mobile app.  "}); w.Code != 201 {
		t.Fatalf("answer: %d %s", w.Code, w.Body)
	} else {
		validOperatorResponse(t, w)
	}
	if w := operatorExecutionRequest(s, "POST", answerPath, token, "alice", map[string]any{"commandId": "a2", "answer": "No."}); w.Code != 409 || errorCode(t, w) != "already_answered" {
		t.Fatalf("second answer: %d %s", w.Code, w.Body)
	}
	missing := fmt.Sprintf("/api/v1/operator/work-items/%d/questions/q1-9/answer", id)
	if w := operatorExecutionRequest(s, "POST", missing, token, "alice", map[string]any{"commandId": "a3", "answer": "x"}); w.Code != 404 {
		t.Fatalf("unknown question: %d %s", w.Code, w.Body)
	}
	if w := operatorExecutionRequest(s, "POST", fmt.Sprintf("/api/v1/operator/work-items/%d/questions/nope/answer", id), token, "alice",
		map[string]any{"commandId": "a4", "answer": "x"}); w.Code != 400 {
		t.Fatalf("malformed question id: %d %s", w.Code, w.Body)
	}
	if n := countRows(t, `SELECT count(*) FROM audit_log WHERE work_item_id=$1 AND action='question.answered'`, id); n != 1 {
		t.Fatalf("question.answered events = %d", n)
	}

	if _, err := testStore.OpenRound(ctx, shift, 1, []store.Role{{Name: "builder", Writes: true, Cap: 1}}); err != nil {
		t.Fatal(err)
	}
	status, next := postClaim(t, h, `{"team":"bronze","role":"builder"}`)
	if status != http.StatusOK {
		t.Fatalf("second claim: %d", status)
	}
	if len(next.ReviewFindings) != 1 || next.ReviewFindings[0].Title != "no tests" || next.ReviewFindings[0].ID != detail.Findings[0].ID {
		t.Fatalf("writer review findings = %+v", next.ReviewFindings)
	}
	if len(next.Questions) != 1 || next.Questions[0].Answer != "Yes, by the mobile app." || next.Questions[0].AnsweredBy != "person" {
		t.Fatalf("writer questions = %+v", next.Questions)
	}
}
