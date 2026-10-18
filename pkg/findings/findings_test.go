package findings

import (
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/harness"
)

func reader(id int64, round int, items ...harness.ReviewFinding) Run {
	return Run{ID: id, ShiftID: 1, Role: "reviewer", Round: round, Finished: true, FindingItems: items}
}

func writer(id int64, round int, answers ...harness.FindingAnswer) Run {
	return Run{ID: id, ShiftID: 1, Role: "builder", Round: round, Writes: true, Finished: true, FindingAnswers: answers}
}

func TestAssignFindingIDs_KeepsKnownAndNumbersNew(t *testing.T) {
	got := AssignFindingIDs(9, []harness.ReviewFinding{
		{ID: "f3-1", Title: "again"}, {Title: "new"}, {ID: "f99-1", Title: "unknown id"}, {ID: "f3-1", Title: "duplicate"},
	}, map[string]bool{"f3-1": true})
	want := []string{"f3-1", "f9-1", "f9-2", "f9-3"}
	for i, f := range got {
		if f.ID != want[i] {
			t.Fatalf("ids = %v, want %v", got, want)
		}
	}
}

func TestBrief_WriterGetsLastReviewsFindingsAndReaderSeesAnswers(t *testing.T) {
	runs := []Run{
		writer(1, 1),
		reader(2, 2, harness.ReviewFinding{ID: "f2-1", Title: "nil check"}, harness.ReviewFinding{ID: "f2-2", Title: "naming"}),
	}
	got, _ := Brief(runs, 1, 3, true, nil)
	if len(got) != 2 || got[0].ID != "f2-1" || got[0].Answer != nil || got[0].Unanswered {
		t.Fatalf("writer brief = %+v", got)
	}

	runs = append(runs, writer(3, 3, harness.FindingAnswer{FindingID: "f2-1", Status: harness.AnswerFixed}))
	got, _ = Brief(runs, 1, 4, false, nil)
	if len(got) != 2 {
		t.Fatalf("reader brief = %+v", got)
	}
	if got[0].Answer == nil || got[0].Answer.Status != harness.AnswerFixed || got[0].Answer.Round != 3 {
		t.Fatalf("answered finding = %+v", got[0])
	}
	if got[1].Answer != nil || !got[1].Unanswered {
		t.Fatalf("the finding the writer skipped must be flagged unanswered: %+v", got[1])
	}
}

func TestBrief_RaisingAgainClearsTheAnswerAndOldFindingsDropOut(t *testing.T) {
	runs := []Run{
		reader(2, 2, harness.ReviewFinding{ID: "f2-1", Title: "a"}, harness.ReviewFinding{ID: "f2-2", Title: "b"}),
		writer(3, 3, harness.FindingAnswer{FindingID: "f2-1", Status: harness.AnswerFixed},
			harness.FindingAnswer{FindingID: "f2-2", Status: harness.AnswerDisagree, Reason: "intended"}),
		reader(4, 4, harness.ReviewFinding{ID: "f2-1", Title: "a, still"}),
	}
	got, _ := Brief(runs, 1, 5, true, nil)
	if len(got) != 1 || got[0].ID != "f2-1" || got[0].Title != "a, still" || got[0].Answer != nil || got[0].Round != 4 {
		t.Fatalf("a finding not raised again is settled; the raised one is open again: %+v", got)
	}
}

func TestBrief_ShadowFindingsNeverBriefed(t *testing.T) {
	shadow := reader(3, 2, harness.ReviewFinding{ID: "f3-1", Title: "shadow"})
	shadow.Shadow, shadow.Role = true, "shadow-reviewer"
	runs := []Run{reader(2, 2), shadow}
	if got, _ := Brief(runs, 1, 3, true, nil); len(got) != 0 {
		t.Fatalf("shadow finding briefed: %+v", got)
	}
}

func TestBrief_Questions(t *testing.T) {
	runs := []Run{
		{ID: 1, ShiftID: 1, Role: "builder", Round: 1, Writes: true, Finished: true, Questions: []harness.Question{
			{ID: "q1-1", To: harness.AskPerson, Text: "which locale?"},
			{ID: "q1-2", To: harness.AskReviewer, Text: "is the cache needed?"},
			{ID: "q1-3", To: harness.AskNextRun, Text: "did the migration run?"},
		}},
		{ID: 2, ShiftID: 1, Role: "reviewer", Round: 2, Finished: true, QuestionAnswers: []harness.QuestionAnswer{{QuestionID: "q1-2", Answer: "no"}}},
	}
	persons := map[string]PersonAnswer{"q1-1": {Answer: "nl-NL", Actor: "operator:x:ryan"}}
	_, writerQs := Brief(runs, 1, 3, true, persons)
	if len(writerQs) != 2 || writerQs[0].Answer != "nl-NL" || writerQs[0].AnsweredBy != "person" || writerQs[1].ID != "q1-3" {
		t.Fatalf("writer questions = %+v", writerQs)
	}
	_, readerQs := Brief(runs, 1, 3, false, persons)
	if len(readerQs) != 3 || readerQs[1].Answer != "no" || readerQs[1].AnsweredBy != "reviewer" {
		t.Fatalf("reader questions = %+v", readerQs)
	}
	_, nextShift := Brief(runs, 2, 1, true, persons)
	if len(nextShift) != 1 || nextShift[0].ID != "q1-1" {
		t.Fatalf("only a person's questions outlive the Shift: %+v", nextShift)
	}
	if w := WaitingForPerson(runs, 1, 1, nil); len(w) != 1 || w[0].Text != "which locale?" {
		t.Fatalf("waiting = %+v", w)
	}
	if w := WaitingForPerson(runs, 1, 1, persons); len(w) != 0 {
		t.Fatalf("an answered question still waits: %+v", w)
	}
}

func TestBuildLedger_Disagreements(t *testing.T) {
	acting := reader(2, 2)
	acting.Verdict = harness.VerdictApprove
	shadow := reader(3, 2)
	shadow.Shadow, shadow.Role, shadow.Verdict = true, "shadow-reviewer", harness.VerdictRequestChanges
	agreeing := reader(5, 4)
	agreeing.Verdict = harness.VerdictApprove
	shadowAgrees := reader(6, 4)
	shadowAgrees.Shadow, shadowAgrees.Verdict = true, harness.VerdictApprove
	l := BuildLedger([]Run{acting, shadow, agreeing, shadowAgrees}, nil)
	if len(l.Disagreements) != 1 {
		t.Fatalf("disagreements = %+v", l.Disagreements)
	}
	d := l.Disagreements[0]
	if d.Round != 2 || d.ShadowRunID != 3 || d.PrimaryVerdict != "approve" || d.ShadowVerdict != "request_changes" {
		t.Fatalf("disagreement = %+v", d)
	}
}
