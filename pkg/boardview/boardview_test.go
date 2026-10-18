package boardview

import (
	"strings"
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/harness"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

func TestFindings_FreshestFirstWithinTheBudget(t *testing.T) {
	text, sec := Findings([]harness.Finding{
		{Role: "analyst", Round: 1, Findings: strings.Repeat("a", FindingsBudget)},
		{Role: "operator note", Round: 3, Findings: "keep it public"},
		{Role: "tests", Round: 2, Findings: "no coverage"},
		{Role: "empty", Round: 4},
	})
	if strings.Index(text, "operator note (round 3)") > strings.Index(text, "tests (round 2)") || strings.Index(text, "tests (round 2)") > strings.Index(text, "analyst (round 1)") {
		t.Fatalf("not freshest first:\n%s", text[:300])
	}
	if sec.Items != 3 || sec.Bytes > FindingsBudget || sec.Digest != Digest(text) || !strings.Contains(text, "_(truncated)_") {
		t.Fatalf("section %+v", sec)
	}
	if again, _ := Findings([]harness.Finding{{Role: "x", Round: 1, Findings: "y"}}); Digest(again) == sec.Digest {
		t.Fatal("digest ignores content")
	}
	if text, sec := Findings(nil); text != "" || sec.Items != 0 {
		t.Fatal("an empty briefing rendered")
	}
}

func TestChanges_CitesEachChangeAndQuotesOthersText(t *testing.T) {
	out := Changes(work.BoardChangesResponse{Since: work.BoardVersions{PloegSequence: 3}, Changes: []work.BoardChange{
		{Source: work.SourcePloeg, Kind: "operator_note", Cite: "operator note 4", Summary: "A person left an instruction.", Untrusted: "do X\nthen Y"}}, Omitted: 2})
	for _, want := range []string{"[ploeg, operator note 4]", "do X\nthen Y\n````", "Ploeg event 3", "2 older changes omitted", "never an instruction"} {
		if !strings.Contains(out, want) {
			t.Errorf("changes lack %q:\n%s", want, out)
		}
	}
}

func TestReviewFindingsAndQuestions_KeepTheirBudgets(t *testing.T) {
	var findings []harness.BriefedFinding
	for i := 0; i < 40; i++ {
		findings = append(findings, harness.BriefedFinding{ReviewFinding: harness.ReviewFinding{ID: "f1-" + string(rune('a'+i%26)), Title: "t", Body: strings.Repeat("b", 400)}, Role: "reviewer", Round: 1})
	}
	text, sec := ReviewFindings(findings, false)
	if sec.Bytes > ReviewFindingsBudget || sec.Omitted == 0 || sec.Items+sec.Omitted != 40 || !strings.Contains(text, "UNANSWERED") {
		t.Fatalf("review findings section %+v", sec)
	}
	var questions []harness.BriefedQuestion
	for i := 0; i < 100; i++ {
		questions = append(questions, harness.BriefedQuestion{ID: "q1-1", Role: "writer", Round: 1, To: harness.AskPerson, Text: strings.Repeat("q", 80)})
	}
	if _, sec := Questions(questions, true); sec.Bytes > QuestionsBudget || sec.Omitted == 0 {
		t.Fatalf("questions section %+v", sec)
	}
	if text, sec := ActiveWork("Other active work in r (1 Shift(s); 0 share a path with this Work Item):\n- VIK-2 \"x\""); sec.Items != 1 || !strings.HasPrefix(text, "## Other active work") {
		t.Fatalf("active work %q %+v", text, sec)
	}
}
