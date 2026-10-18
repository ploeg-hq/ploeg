package shiftengine

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/harness"
	"github.com/ploeg-hq/ploeg/pkg/store"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

// ADR-0121: findings by id, the writer's answers, questions and the shadow
// disagreement, through the review loop.

func resetFindingTables(t *testing.T) {
	t.Helper()
	if _, err := testPool.Exec(context.Background(), "DELETE FROM question_answers"); err != nil {
		t.Fatal(err)
	}
}

// claimAndReport claims one pending Run of role and lets report report it.
func claimAndReport(t *testing.T, role string, report func(store.ClaimedRun) error) store.ClaimedRun {
	t.Helper()
	run, err := testStore.ClaimRole(context.Background(), "bronze", role, time.Minute, 1)
	if err != nil || run == nil {
		t.Fatalf("claim %s: %v", role, err)
	}
	if err := report(*run); err != nil {
		t.Fatalf("report %s: %v", role, err)
	}
	return *run
}

func brief(t *testing.T, run store.ClaimedRun) ([]harness.BriefedFinding, []harness.BriefedQuestion) {
	t.Helper()
	f, q, err := testStore.FindingsBrief(context.Background(), run.ShiftID, run.Round, run.Writes)
	if err != nil {
		t.Fatal(err)
	}
	return f, q
}

func TestFindings_AnswersFlowAndUnansweredAreFlagged(t *testing.T) {
	ctx := context.Background()
	e := newEngine(shadowPlan(t))
	resetFindingTables(t)
	id := startShadowShift(t, e, "2101")
	buildAndOpenPR(t, e, id)

	claimAndReport(t, "reviewer", func(run store.ClaimedRun) error {
		_, err := testStore.ReportOutcome(context.Background(), run.RunToken, store.Report(work.OutcomeNoChangeNeeded, "read it", "", nil, nil, nil).
			WithVerdict(harness.VerdictRequestChanges).WithFindings("two things").
			WithBlackboard([]harness.ReviewFinding{
				{Severity: harness.SeverityBlocking, Title: "retry is unbounded", Path: "pkg/a.go", Line: 12},
				{Severity: harness.SeverityNit, Title: "naming"},
			}, nil, nil, nil))
		return err
	})
	claimAndReport(t, "shadow-reviewer", func(run store.ClaimedRun) error {
		_, err := testStore.ReportOutcome(context.Background(), run.RunToken, store.Report(work.OutcomeNoChangeNeeded, "read it", "", nil, nil, nil).WithVerdict(harness.VerdictApprove).
			WithBlackboard([]harness.ReviewFinding{{Title: "shadow only"}}, nil, nil, nil))
		return err
	})
	evaluate(t, e, id)

	claimAndReport(t, "builder", func(run store.ClaimedRun) error {
		got, _ := brief(t, run)
		if len(got) != 2 || got[0].Title != "retry is unbounded" || got[1].Title != "naming" {
			t.Fatalf("the fix round's writer is briefed with = %+v", got)
		}
		for _, f := range got {
			if f.Title == "shadow only" {
				t.Fatal("a shadow's finding reached the writer")
			}
		}
		_, err := testStore.ReportOutcome(context.Background(), run.RunToken, store.Report(work.OutcomePRUpdated, "fixed", "", []string{"https://forgejo.webgrip.dev/webgrip/ploeg/pulls/7"}, nil, nil).
			WithBlackboard(nil, []harness.FindingAnswer{{FindingID: got[0].ID, Status: harness.AnswerFixed}}, nil, nil))
		return err
	})
	evaluate(t, e, id)

	claimAndReport(t, "reviewer", func(run store.ClaimedRun) error {
		got, _ := brief(t, run)
		if len(got) != 2 {
			t.Fatalf("the next review is briefed with = %+v", got)
		}
		if got[0].Answer == nil || got[0].Answer.Status != harness.AnswerFixed || got[0].Answer.Role != "builder" {
			t.Fatalf("the answered finding = %+v", got[0])
		}
		if got[1].Answer != nil || !got[1].Unanswered {
			t.Fatalf("the finding the writer skipped must be flagged unanswered: %+v", got[1])
		}
		_, err := testStore.ReportOutcome(context.Background(), run.RunToken, store.Report(work.OutcomeNoChangeNeeded, "ok", "", nil, nil, nil).WithVerdict(harness.VerdictApprove))
		return err
	})
	claimAndReport(t, "shadow-reviewer", func(run store.ClaimedRun) error {
		_, err := testStore.ReportOutcome(context.Background(), run.RunToken, store.Report(work.OutcomeNoChangeNeeded, "ok", "", nil, nil, nil).WithVerdict(harness.VerdictApprove))
		return err
	})
	evaluate(t, e, id)

	detail, err := testStore.OperatorItem(ctx, id, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Findings) != 3 {
		t.Fatalf("operator findings = %+v", detail.Findings)
	}
	byTitle := map[string]store.OperatorFinding{}
	for _, f := range detail.Findings {
		byTitle[f.Title] = f
	}
	if f := byTitle["retry is unbounded"]; f.Answer == nil || f.Answer.Status != harness.AnswerFixed || f.Unanswered || f.Line != 12 {
		t.Fatalf("answered finding on the operator API = %+v", f)
	}
	if f := byTitle["naming"]; f.Answer != nil || !f.Unanswered {
		t.Fatalf("unanswered finding on the operator API = %+v", f)
	}
	if f := byTitle["shadow only"]; !f.Shadow || f.Unanswered {
		t.Fatalf("shadow finding on the operator API = %+v", f)
	}
	if len(detail.ReviewDisagreements) != 1 {
		t.Fatalf("disagreements = %+v", detail.ReviewDisagreements)
	}
	d := detail.ReviewDisagreements[0]
	if d.Round != 2 || d.PrimaryVerdict != harness.VerdictRequestChanges || d.ShadowVerdict != harness.VerdictApprove || d.ShadowRole != "shadow-reviewer" {
		t.Fatalf("disagreement = %+v", d)
	}
	var runFindings int
	for _, r := range detail.Runs {
		runFindings += len(r.FindingItems)
	}
	if runFindings != 3 {
		t.Fatalf("Run shapes carry %d findings, want 3", runFindings)
	}
}

// A writer that answers a finding nobody raised, or a reader that answers
// findings, has the entry dropped, and the outcome still stands.
func TestFindings_ForeignEntriesAreDropped(t *testing.T) {
	e := newEngine(shadowPlan(t))
	resetFindingTables(t)
	id := startShadowShift(t, e, "2102")
	claimAndReport(t, "builder", func(run store.ClaimedRun) error {
		_, err := testStore.ReportOutcome(context.Background(), run.RunToken, store.Report(work.OutcomePROpened, "opened", "", []string{"https://forgejo.webgrip.dev/webgrip/ploeg/pulls/7"}, nil, nil).
			WithBlackboard([]harness.ReviewFinding{{Title: "a writer cannot raise"}},
				[]harness.FindingAnswer{{FindingID: "f999-1", Status: harness.AnswerFixed}}, nil,
				[]harness.QuestionAnswer{{QuestionID: "q999-1", Answer: "x"}}))
		return err
	})
	detail, err := testStore.OperatorItem(context.Background(), id, nil)
	if err != nil {
		t.Fatal(err)
	}
	r := detail.Runs[0]
	if len(r.FindingItems)+len(r.FindingAnswers)+len(r.QuestionAnswers) != 0 || len(detail.Findings) != 0 {
		t.Fatalf("foreign entries were stored: %+v", r)
	}
}

func TestQuestions_PersonQuestionParksAndTheAnswerBriefsTheNextRun(t *testing.T) {
	ctx := context.Background()
	e := newEngine(shadowPlan(t))
	resetFindingTables(t)
	id := startShadowShift(t, e, "2103")
	asker := claimAndReport(t, "builder", func(run store.ClaimedRun) error {
		_, err := testStore.ReportOutcome(context.Background(), run.RunToken, store.Report(work.OutcomeNoChangeNeeded, "need a decision", "", nil, nil, nil).
			WithBlackboard(nil, nil, []harness.Question{
				{To: harness.AskPerson, Text: "Should prices show two decimals in nl-NL?"},
				{To: harness.AskNextRun, Text: "Was the migration applied?"},
			}, nil))
		return err
	})
	evaluate(t, e, id)

	item, err := testStore.WorkItem(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if item.State != work.StateNeedsHuman {
		t.Fatalf("state = %s, want needs_human", item.State)
	}
	var closeReason, reason string
	if err := testPool.QueryRow(ctx, `SELECT close_reason FROM shifts WHERE work_item_id = $1`, id).Scan(&closeReason); err != nil {
		t.Fatal(err)
	}
	if err := testPool.QueryRow(ctx, `SELECT detail->>'reason' FROM audit_log WHERE work_item_id = $1 AND action = 'work_item.needs_human'`, id).Scan(&reason); err != nil {
		t.Fatal(err)
	}
	if closeReason != reasonQuestionForPerson || !strings.Contains(reason, "Should prices show two decimals in nl-NL?") {
		t.Fatalf("close reason %q, message %q", closeReason, reason)
	}
	if strings.Contains(reason, "migration") {
		t.Fatalf("a question to the next Run does not stop the Work Item: %q", reason)
	}

	detail, err := testStore.OperatorItem(ctx, id, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Questions) != 2 || !detail.Questions[0].WaitingForPerson || detail.Questions[1].WaitingForPerson {
		t.Fatalf("operator questions = %+v", detail.Questions)
	}
	qid := detail.Questions[0].ID
	if qid != "q"+runID(asker)+"-1" {
		t.Fatalf("question id = %q", qid)
	}

	cmd := store.QuestionAnswerCommand{Consumer: "c", CommandID: "a1", Actor: "operator:c:ryan", QuestionID: qid, Answer: "Yes, two decimals."}
	if _, err := testStore.AnswerQuestion(ctx, id, nil, cmd); err != nil {
		t.Fatal(err)
	}
	if again, err := testStore.AnswerQuestion(ctx, id, nil, cmd); err != nil || !again.Replayed {
		t.Fatalf("replay = %+v, %v", again, err)
	}
	other := cmd
	other.CommandID, other.Answer = "a2", "No."
	if _, err := testStore.AnswerQuestion(ctx, id, nil, other); err != store.ErrQuestionAnswered {
		t.Fatalf("a second answer = %v, want ErrQuestionAnswered", err)
	}
	other.QuestionID = detail.Questions[1].ID
	if _, err := testStore.AnswerQuestion(ctx, id, nil, other); err != store.ErrQuestionNotFound {
		t.Fatalf("a person answering a question to a Run = %v, want ErrQuestionNotFound", err)
	}

	if _, err := testStore.RequeueWorkItem(ctx, store.RequeueRequest{WorkItemID: id, Actor: "operator:c:ryan", PoolUSD: 10, OpenShift: true,
		Team: "bronze", Branch: "agent/vik-2103", Command: store.RequeueCommand{FromRound: 1, CommandID: "r1"}}); err != nil {
		t.Fatal(err)
	}
	evaluate(t, e, id)
	run, err := testStore.ClaimRole(ctx, "bronze", "builder", time.Minute, 1)
	if err != nil || run == nil {
		t.Fatalf("claim after requeue: %v", err)
	}
	_, qs := brief(t, *run)
	if len(qs) != 1 || qs[0].ID != qid || qs[0].Answer != "Yes, two decimals." || qs[0].AnsweredBy != "person" {
		t.Fatalf("the next Shift's writer is briefed with = %+v", qs)
	}
}

func runID(run store.ClaimedRun) string {
	var id int64
	if err := testPool.QueryRow(context.Background(), `SELECT id FROM agent_runs WHERE run_token = $1`, run.RunToken).Scan(&id); err != nil {
		panic(err)
	}
	return strconv.FormatInt(id, 10)
}

func TestFindingsCommentListsFindingsAndAnswersByID(t *testing.T) {
	review := findingsComment(store.RunReport{Role: "reviewer", Round: 2, Findings: "prose",
		FindingItems: []harness.ReviewFinding{{ID: "f9-1", Severity: harness.SeverityBlocking, Title: "no\ntests", Path: "a.go", Line: 4, Body: "add one"}}})
	for _, want := range []string{"#### Findings", "- `f9-1` (blocking) **no tests** in `a.go:4`", "  add one"} {
		if !strings.Contains(review, want) {
			t.Errorf("review comment lacks %q:\n%s", want, review)
		}
	}
	answers := findingsComment(store.RunReport{Role: "builder", Round: 3, Writes: true,
		FindingAnswers: []harness.FindingAnswer{{FindingID: "f9-1", Status: harness.AnswerOutOfScope, Reason: "separate ticket"}}})
	if !strings.Contains(answers, "- `f9-1`: out of scope — separate ticket") {
		t.Errorf("writer comment:\n%s", answers)
	}
}
