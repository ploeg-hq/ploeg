package worker

import (
	"strings"
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/work"
)

// A Run that explains a pull request someone else opened (ADR-0109) gets its
// own reading contract: the branch is the pull request's, Ploeg posts the
// findings on it, no fix Round follows, and follow-up work is created work.
func TestComposePrompt_ReadsAPullRequestWithoutAReviewLoop(t *testing.T) {
	spec := contextSpec()
	spec.WorkItem.Origin = work.OriginPullRequest
	spec.WorkItem.URL = "https://forgejo.example/webgrip/example/pulls/386"
	spec.WorkItem.SourcePR = 386
	spec.Branch = "renovate/opencode"
	prompt := ComposePrompt(spec, false, "", true)
	for _, want := range []string{
		"## Delivery contract (read a pull request)",
		"standing on branch renovate/opencode",
		"https://forgejo.example/webgrip/example/pulls/386",
		`"createdWorkItems"`,
		"No fix Round follows either",
		"Do NOT modify, commit or push anything",
		"## Repository instructions",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt lacks %q", want)
		}
	}
	if !strings.Contains(prompt, "This Run has no forge access, so do not try to fetch the pull request.") {
		t.Error("the pull request reader is not told the description is quoted and the forge is out of reach")
	}
	for _, absent := range []string{"Delivery contract (review only)", "sends the work back to the writer", "read the\n  pull request itself"} {
		if strings.Contains(prompt, absent) {
			t.Errorf("prompt carries the ordinary review contract: %q", absent)
		}
	}
	linked := ComposePrompt(spec, false, "https://forgejo.example/webgrip/example/pulls/386#issue", true)
	if !strings.Contains(linked, "pulls/386#issue") {
		t.Error("the pull request the worker found on the branch must be the one named")
	}
	ordinary := ComposePrompt(contextSpec(), false, "", true)
	if strings.Contains(ordinary, "read a pull request)") {
		t.Error("an ordinary reader got the pull request reader's contract")
	}
}
