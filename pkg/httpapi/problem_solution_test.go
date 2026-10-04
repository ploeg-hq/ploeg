package httpapi

import (
	"context"
	"net/http"
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/harness"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

// ADR-0042: the writer's account travels from the outcome report to the
// operator detail Console renders, unchanged.
func TestOutcome_WriterProblemAndSolutionReachTheOperatorDetail(t *testing.T) {
	reset(t)
	h := createdServer(nil).Handler()
	source := ingestSource(t, "6001", "bronze")
	c := claimLegacy(t, h, "bronze")

	const problem, solution = "Refunds over **€500** fail with a 500.", "- `refund.go` checks the limit first.\n- A test covers €500.01."
	rec := postOutcome(t, h, c.RunToken, harness.OutcomeReport{
		Outcome: work.OutcomePROpened, Summary: "opened a PR", Links: []string{"https://forge.example/webgrip/ploeg/pulls/9"},
		Problem: problem, Solution: solution,
	})
	if rec.Code != http.StatusNoContent {
		t.Fatalf("outcome: %d %s", rec.Code, rec.Body)
	}
	detail, err := testStore.OperatorItem(context.Background(), source, []string{"bronze"})
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Runs) != 1 || detail.Runs[0].Problem != problem || detail.Runs[0].Solution != solution {
		t.Fatalf("the writer's account did not reach the operator detail: %+v", detail.Runs)
	}
}
