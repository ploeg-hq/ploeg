package boardview

import (
	"strings"
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/harness"
	"github.com/ploeg-hq/ploeg/pkg/untrusted"
)

func TestFindings_AReviewCutAtTheBudgetStillClosesItsFrameBeforePloegsTruncationNote(t *testing.T) {
	review := strings.Repeat("rename the handler\n", FindingsBudget/10)
	text, _ := Findings([]harness.Finding{{Role: "human review by maintainer", Round: 1, Findings: untrusted.Frame(untrusted.SourceForgeReview, review)}})

	closing := "</untrusted boundary=\"" + untrusted.Boundary(untrusted.SourceForgeReview, strings.TrimRight(review, "\n")) + "\">\n"
	closed, note := strings.Index(text, closing), strings.Index(text, "_(truncated)_")
	if closed < 0 || note < 0 || closed > note {
		t.Fatalf("the cut review's frame is not closed before the truncation note (close at %d, note at %d):\n%s", closed, note, text[len(text)-300:])
	}
}
