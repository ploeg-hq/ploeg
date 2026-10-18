package worker

import (
	"strings"

	"github.com/ploeg-hq/ploeg/pkg/boardview"
	"github.com/ploeg-hq/ploeg/pkg/harness"
)

// writeReviewFindings and writeQuestions render the findings by id and the
// questions of ADR-0121 as sections of the board view, each within its own
// budget (ADR-0124).
func writeReviewFindings(b *strings.Builder, findings []harness.BriefedFinding, writes bool) {
	text, _ := boardview.ReviewFindings(findings, writes)
	b.WriteString(text)
}

func writeQuestions(b *strings.Builder, questions []harness.BriefedQuestion, writes bool) {
	text, _ := boardview.Questions(questions, writes)
	b.WriteString(text)
}

// writeBlackboardContract tells the agent how to report findings by id,
// answers and questions in the outcome file (ADR-0121).
func writeBlackboardContract(b *strings.Builder, spec harness.TaskSpec, writes bool) {
	if writes {
		if len(spec.ReviewFindings) > 0 {
			b.WriteString(`- Answer every finding under "Review findings to answer" in the same file:

      "findingAnswers": [
        {"findingId": "<id>", "status": "fixed"},
        {"findingId": "<id>", "status": "disagree", "reason": "<why the code is right as it is>"}
      ]

  status is one of: fixed (you changed the code so it no longer applies),
  disagree (you did not change it; the reason is required), out_of_scope
  (it belongs to other work), deferred (it should be done, later). The next
  reviewer reads your answers; a finding you leave out is shown as
  unanswered.
`)
		}
	} else {
		b.WriteString(`- Next to the findings prose, list each finding in "findingItems", so the
  writer can answer it by id:

      "findingItems": [
        {"severity": "blocking", "title": "<one line>", "body": "<what and why>",
         "path": "<file>", "line": 42}
      ]

  severity is blocking (must change before merge), should_fix or nit. Leave
  out "id" for a new finding; Ploeg assigns it. To raise an earlier finding
  again because its answer does not settle it, give its "id".
`)
	}
	b.WriteString(`- When you cannot go on without a decision you must not guess, ask instead
  in the same file, at most five questions:

      "questions": [{"to": "person", "text": "<the question, and the options you see>"}]

  "to" is person (a person must decide: the Work Item stops and waits for the
  answer), reviewer (the next reviewer answers it) or next_run (the next Run
  answers it). Ask only what the Work Item, the code and the briefing cannot
  tell you.
`)
	if answerable(spec.Questions, writes) {
		b.WriteString(`- Answer the unanswered questions under "Questions asked earlier" that you
  can, in the same file:

      "questionAnswers": [{"questionId": "<id>", "answer": "<your answer>"}]
`)
	}
}

func answerable(questions []harness.BriefedQuestion, writes bool) bool {
	for _, q := range questions {
		if q.AnsweredBy == "" && (q.To == harness.AskNextRun || (q.To == harness.AskReviewer && !writes)) {
			return true
		}
	}
	return false
}
