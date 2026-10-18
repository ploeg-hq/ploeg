package worker

import (
	"strings"
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/harness"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

func findingsSpec() harness.TaskSpec {
	return harness.TaskSpec{
		WorkItem: work.WorkItem{ID: "1", Provider: "vikunja", ExternalID: "7", Title: "t"},
		Repo:     harness.RepoRef{ForgeURL: "https://forge.example", Owner: "o", Name: "r", BaseBranch: "development"},
		Branch:   "agent/vik-7", TraceID: "ploeg-1",
		ReviewFindings: []harness.BriefedFinding{
			{ReviewFinding: harness.ReviewFinding{ID: "f4-1", Severity: harness.SeverityBlocking, Title: "retry is unbounded", Path: "a.go", Line: 3},
				Role: "reviewer", Round: 2, Answer: &harness.BriefedAnswer{Status: harness.AnswerDisagree, Reason: "ctx bounds it", Role: "builder", Round: 3}},
			{ReviewFinding: harness.ReviewFinding{ID: "f4-2", Title: "naming"}, Role: "reviewer", Round: 2, Unanswered: true},
		},
		Questions: []harness.BriefedQuestion{
			{ID: "q2-1", To: harness.AskPerson, Text: "two decimals?", Role: "builder", Round: 1, Answer: "yes", AnsweredBy: "person"},
			{ID: "q2-2", To: harness.AskReviewer, Text: "cache needed?", Role: "builder", Round: 1},
		},
	}
}

func TestPrompt_WriterAnswersFindingsByID(t *testing.T) {
	p := ComposePrompt(findingsSpec(), true, "", false)
	for _, want := range []string{"## Review findings to answer", "### f4-1 (blocking): retry is unbounded", `"findingAnswers"`,
		"A person answered: yes", `"questions"`} {
		if !strings.Contains(p, want) {
			t.Errorf("writer prompt lacks %q", want)
		}
	}
	if strings.Contains(p, "UNANSWERED") || strings.Contains(p, `"questionAnswers"`) {
		t.Error("a writer sees the reviewer's view or is asked to answer a question to the reviewer")
	}
}

func TestPrompt_ReaderChecksAnswersAndSeesUnanswered(t *testing.T) {
	p := ComposePrompt(findingsSpec(), false, "", true)
	for _, want := range []string{"## Earlier findings and the writer's answers", "**disagree** — ctx bounds it",
		"Answer: **UNANSWERED**", `"findingItems"`, `"questionAnswers"`, "cache needed?"} {
		if !strings.Contains(p, want) {
			t.Errorf("reader prompt lacks %q", want)
		}
	}
}

func TestPrompt_NoFindingsLeavesTheWriterContractAlone(t *testing.T) {
	spec := findingsSpec()
	spec.ReviewFindings, spec.Questions = nil, nil
	p := ComposePrompt(spec, true, "", false)
	if strings.Contains(p, "Review findings") || strings.Contains(p, `"findingAnswers"`) {
		t.Error("a writer without findings is asked to answer them")
	}
}
