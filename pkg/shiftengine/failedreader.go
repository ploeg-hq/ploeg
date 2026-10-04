package shiftengine

import (
	"context"
	"fmt"

	"github.com/ploeg-hq/ploeg/pkg/store"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

func readerAttemptsSpent(f store.FailedRun) bool {
	return f.InfraAttempts >= store.MaxInfraFailures || f.AgentAttempts() >= store.MaxRunAttempts
}

func (e *Engine) retryFailedReaders(ctx context.Context, si store.ShiftInfo, failed []store.FailedRun) (bool, error) {
	var retry []store.Role
	for _, f := range failed {
		if readerAttemptsSpent(f) {
			e.Log.Warn("a reading Run failed and its attempts are spent; its review is missing from this Round",
				"shift", si.ID, "round", si.Round, "role", f.Role,
				"agent_attempts", f.AgentAttempts(), "infra_attempts", f.InfraAttempts)
			continue
		}
		retry = append(retry, store.Role{Name: f.Role, Writes: false, Cap: e.capFor(si.Team, f.Role)})
	}
	if len(retry) == 0 {
		return false, nil
	}
	attempt, err := e.Store.ReopenRound(ctx, si.ID, si.Round, retry)
	if err != nil {
		e.Log.Info("could not reopen the round for a failed reader; another evaluator may have",
			"shift", si.ID, "round", si.Round, "roles", len(retry), "err", err)
		return true, nil
	}
	e.Log.Info("reopened the round after a reading Run failed",
		"shift", si.ID, "round", si.Round, "roles", len(retry), "attempt", attempt, "max", store.MaxRunAttempts)
	return true, nil
}

type absentReview struct {
	role, failureReason string
	attempts            int
	othersReviewed      bool
}

func (a absentReview) message() string {
	reason := a.failureReason
	if reason == "" {
		reason = "no failure reason recorded"
	}
	if a.othersReviewed {
		return fmt.Sprintf("not reviewed by every agent: %s Run failed (%s) after %d attempt(s). Agent review incomplete; a person is asked to review and merge",
			a.role, reason, a.attempts)
	}
	return fmt.Sprintf("not reviewed by an agent: %s Run failed (%s) after %d attempt(s). Agent review unavailable; a person is asked to review and merge",
		a.role, reason, a.attempts)
}

func missingReview(reports []store.RunReport) (absentReview, bool) {
	lastWriterRound := 0
	for _, r := range reports {
		if r.Writes && r.Round > lastWriterRound {
			lastWriterRound = r.Round
		}
	}
	reviewRound := 0
	for _, r := range reports {
		if !r.Writes && r.Round > lastWriterRound && r.Round > reviewRound {
			reviewRound = r.Round
		}
	}
	if reviewRound == 0 {
		return absentReview{}, false
	}
	var roles []string
	byRole := map[string]*absentReview{}
	reviewed := map[string]bool{}
	for _, r := range reports {
		if r.Writes || r.Round != reviewRound {
			continue
		}
		if _, seen := byRole[r.Role]; !seen {
			roles = append(roles, r.Role)
			byRole[r.Role] = &absentReview{role: r.Role}
		}
		switch r.Outcome {
		case string(work.OutcomeFailed):
			byRole[r.Role].attempts++
			byRole[r.Role].failureReason = r.FailureReason
		case "":
		default:
			reviewed[r.Role] = true
		}
	}
	for _, role := range roles {
		if !reviewed[role] && byRole[role].attempts > 0 {
			missing := *byRole[role]
			missing.othersReviewed = len(reviewed) > 0
			return missing, true
		}
	}
	return absentReview{}, false
}
