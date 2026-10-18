package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

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

// ADR-0121: Work Item detail offers the resume with its Round's cap, and the
// requeue endpoint resumes once every question is answered.
func TestResumeOverTheOperatorAPI(t *testing.T) {
	ctx := context.Background()
	f := newRequeueFixture(t)
	s, token := f.server, f.token
	id := f.item(t, "5701")
	f.run(t, id, "builder", work.OutcomePROpened, "opened", []string{"https://forgejo/o/r/pulls/3"}, nil)
	run, err := testStore.ClaimRole(ctx, "bronze", "reviewer", time.Minute, 0)
	if err != nil || run == nil {
		t.Fatalf("claim reviewer: %v", err)
	}
	if _, err := testStore.ReportOutcome(ctx, run.RunToken, store.Report(work.OutcomeNoChangeNeeded, "read", "", nil, nil, nil).
		WithBlackboard(nil, nil, []harness.Question{{To: harness.AskPerson, Text: "Is the old API still public?"}}, nil)); err != nil {
		t.Fatal(err)
	}
	if err := f.engine.EvaluateItem(ctx, id); err != nil {
		t.Fatal(err)
	}

	detail := func() map[string]any {
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
	offer, _ := detail()["resume"].(map[string]any)
	if offer == nil || offer["fromRound"] != float64(2) || offer["roundCapUsd"] != float64(1) || offer["ready"] != false {
		t.Fatalf("resume offer = %+v", offer)
	}
	waiting := offer["waitingQuestionIds"].([]any)
	qid := waiting[0].(string)

	if w := operatorExecutionRequest(s, "POST", requeuePath(id), token, "alice", map[string]any{"commandId": "r0", "resume": true, "fromRound": 2}); w.Code != 400 {
		t.Fatalf("resume with fromRound: %d %s", w.Code, w.Body)
	}
	if w := operatorExecutionRequest(s, "POST", requeuePath(id), token, "alice", map[string]any{"commandId": "r1", "resume": true}); w.Code != 409 || errorCode(t, w) != "question_unanswered" {
		t.Fatalf("resume before the answer: %d %s", w.Code, w.Body)
	}
	if w := operatorExecutionRequest(s, "POST", fmt.Sprintf("/api/v1/operator/work-items/%d/questions/%s/answer", id, qid), token, "alice",
		map[string]any{"commandId": "a1", "answer": "No, internal only."}); w.Code != 201 {
		t.Fatalf("answer: %d %s", w.Code, w.Body)
	}
	if offer, _ := detail()["resume"].(map[string]any); offer == nil || offer["ready"] != true {
		t.Fatalf("resume offer after the answer = %+v", offer)
	}
	w := operatorExecutionRequest(s, "POST", requeuePath(id), token, "alice", map[string]any{"commandId": "r2", "resume": true})
	if w.Code != 200 {
		t.Fatalf("resume: %d %s", w.Code, w.Body)
	}
	validOperatorResponse(t, w)
	var body struct {
		Requeue struct {
			FromRound int  `json:"fromRound"`
			Resume    bool `json:"resume"`
		} `json:"requeue"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || body.Requeue.FromRound != 2 || !body.Requeue.Resume {
		t.Fatalf("resume response = %s", w.Body)
	}
	if d := detail(); d["resume"] != nil {
		t.Fatalf("a resumed Work Item still offers a resume: %v", d["resume"])
	}
	if w := operatorExecutionRequest(s, "POST", requeuePath(id), token, "alice", map[string]any{"commandId": "r2", "resume": true}); w.Code != 200 {
		t.Fatalf("replayed resume: %d %s", w.Code, w.Body)
	}
}
