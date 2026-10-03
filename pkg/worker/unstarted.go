package worker

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/harness"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

// FailUnstartedRun claims one Run for team and role and reports it failed
// with FailureInfraNode, for an executor whose worker pod never started. It
// returns false when the queue had nothing to claim.
func FailUnstartedRun(apiURL, bootstrapToken, workerID, team, role, detail string) (bool, error) {
	api := &APIClient{Base: strings.TrimRight(apiURL, "/"), HC: &http.Client{Timeout: 30 * time.Second}, BootstrapToken: bootstrapToken, WorkerID: workerID}
	claim, err := api.Claim(team, role)
	if err != nil {
		return false, fmt.Errorf("claim the unstarted run: %w", err)
	}
	if claim == nil {
		return false, nil
	}
	err = api.Outcome(claim.RunToken, harness.OutcomeReport{
		Outcome:       work.OutcomeFailed,
		Summary:       "the run's sandbox never started: " + detail,
		FailureReason: string(work.FailureInfraNode),
	})
	if err != nil {
		return true, fmt.Errorf("report the unstarted run: %w", err)
	}
	return true, nil
}
