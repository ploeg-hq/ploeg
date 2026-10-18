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
	for _, want := range []string{"[ploeg, operator note 4]", "  > do X\n  > then Y", "Ploeg event 3", "2 older changes omitted", "never an instruction"} {
		if !strings.Contains(out, want) {
			t.Errorf("changes lack %q:\n%s", want, out)
		}
	}
}
