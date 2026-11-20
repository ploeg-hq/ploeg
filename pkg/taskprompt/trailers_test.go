package taskprompt

import (
	"strings"
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/harness"
)

func TestAWriterIsToldToEndEveryCommitWithTheAttributionTrailers(t *testing.T) {
	spec := limitedSpec(harness.RunLimits{})
	spec.Trailers = []string{"Ploeg-Run: 42", "Work-Item: VIK-42 https://tracker.example/tasks/42", "Ploeg-Acts-For: oidc-0123456789abcdef0123456789abcdef (proven)"}
	got := Compose(spec, true, "", false)
	want := `- Every commit message ends with the trailers:
  VIK-42
  Agent-Trace-Id: ploeg-000000000000
  Ploeg-Run: 42
  Work-Item: VIK-42 https://tracker.example/tasks/42
  Ploeg-Acts-For: oidc-0123456789abcdef0123456789abcdef (proven)
`
	if !strings.Contains(got, want) {
		t.Fatalf("the delivery contract lacks the trailers:\n%s", got)
	}
}
