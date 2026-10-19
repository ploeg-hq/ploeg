package shiftengine

import (
	"context"
	"fmt"

	"github.com/ploeg-hq/ploeg/pkg/plan"
	"github.com/ploeg-hq/ploeg/pkg/store"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

// A failed writing Run re-opens its own Round, capped (ADR-0019). A failed
// reading Run does too, under the same budgets (ADR-0043, failedreader.go).
//
// A Round that loses its WRITER loses the work: the branch was never written,
// and every later Round is then reasoning about something that does not
// exist. Left alone, the reviewer reviews nothing, returns `approve`, and the
// Shift closes `review_approved` with no pull request — the most reassuring
// close reason there is, for a Shift that produced nothing.
//
// `failed` is not the agent's word. It is the sweeper's verdict on a pod that
// stopped renewing (ExpireRuns), which is precisely what makes it retryable
// and precisely what distinguishes it from `stuck` (R4: a stuck Outcome says a
// human is needed, and no retry fixes that).

const (
	// reasonWriterFailed is the close reason when a writing Role has used up
	// its attempts inside one Round.
	reasonWriterFailed = work.CloseWriterFailed
	// reasonInfraFailed is the close reason when the writing Role never got a
	// real attempt because the pods kept dying under it. A different reason
	// from reasonWriterFailed on purpose: it sends a person to the cluster,
	// not to the ticket.
	reasonInfraFailed = work.CloseWriterKilled
	// reasonCapReached is the close reason when a writing Run used its whole
	// cap and its Role's onCapReached is stop (ADR-0130).
	reasonCapReached = work.CloseRunCapReached
)

func (e *Engine) retryFailedRuns(ctx context.Context, si store.ShiftInfo, reports []store.RunReport) (bool, error) {
	failed, err := e.Store.FailedRunsInRound(ctx, si.ID, si.Round)
	if err != nil {
		return false, err
	}
	for i := range failed {
		if failed[i].Writes {
			return e.retryFailedWriter(ctx, si, &failed[i], reports)
		}
	}
	return e.retryFailedReaders(ctx, si, failed)
}

// retryFailedWriter re-opens the current Round when its writing Run failed and
// attempts remain, or closes the Shift when they do not.
//
// Returns handled=true when it has taken the decision — the caller must not go
// on to advance the plan.
func (e *Engine) retryFailedWriter(ctx context.Context, si store.ShiftInfo, writer *store.FailedRun, reports []store.RunReport) (bool, error) {
	// Infrastructure gets its own, larger budget. A pod the cluster killed
	// says nothing about the work, so spending the Round's three attempts on
	// evictions parks a ticket that was never actually tried — which is
	// exactly how shift 73 reached needs_human on 2026-08-13 having run one
	// real agent. ExpireLeases has always counted these apart on the pre-Shift
	// path (store.MaxInfraFailures); the Shift path now does too.
	if writer.InfraAttempts >= store.MaxInfraFailures {
		e.Log.Error("writing Run was killed by infrastructure too many times; parking the shift",
			"shift", si.ID, "round", si.Round, "role", writer.Role,
			"infra_attempts", writer.InfraAttempts, "agent_attempts", writer.AgentAttempts())
		return true, e.close(ctx, si, reasonInfraFailed,
			fmt.Sprintf("shift stopped: %s was killed %d times in round %d before it could finish — the runs never failed on the work, so look at the cluster (evictions, node pressure, image pulls), not the ticket",
				writer.Role, writer.InfraAttempts, si.Round),
			false, reports)
	}

	escalated := 0.0
	if writer.Last.Reason == work.FailureBudget {
		role, _ := e.planRole(si.Team, writer.Role)
		switch role.CapPolicy() {
		case plan.CapStop:
			e.Log.Warn("writing Run used its whole cap; its Role stops there",
				"shift", si.ID, "round", si.Round, "role", writer.Role,
				"authorized_usd", writer.Last.AuthorizedUSD, "spent_usd", writer.Last.SpentUSD)
			return true, e.close(ctx, si, reasonCapReached, capReachedMessage(writer, si.Round), false, reports)
		case plan.CapEscalate:
			escalated = escalatedCap(writer.Last.AuthorizedUSD, role)
		}
	}

	if writer.AgentAttempts() >= store.MaxRunAttempts {
		e.Log.Warn("writing Run failed too many times; parking the shift",
			"shift", si.ID, "round", si.Round, "role", writer.Role,
			"agent_attempts", writer.AgentAttempts(), "infra_attempts", writer.InfraAttempts)
		return true, e.close(ctx, si, reasonWriterFailed,
			fmt.Sprintf("shift stopped: %s failed %d times in round %d without writing the branch — a person should look at why the run keeps dying",
				writer.Role, writer.AgentAttempts(), si.Round),
			false, reports)
	}

	attempt, err := e.Store.ReopenRound(ctx, si.ID, si.Round,
		[]store.Role{{Name: writer.Role, Writes: true, Cap: e.capFor(si.Team, writer.Role), Selection: e.selection(si.Team, writer.Role), EscalatedCap: escalated}})
	if err != nil {
		// Losing the race to the other evaluator is not an error: the Round
		// has been reopened, which is the outcome we wanted.
		e.Log.Info("could not reopen the round for a failed writer; another evaluator may have",
			"shift", si.ID, "round", si.Round, "role", writer.Role, "err", err)
		return true, nil
	}
	e.Log.Info("reopened the round after its writing Run failed",
		"shift", si.ID, "round", si.Round, "role", writer.Role, "attempt", attempt,
		"max", store.MaxRunAttempts, "escalated_cap_usd", escalated)
	return true, nil
}

func capReachedMessage(writer *store.FailedRun, round int) string {
	spent := ""
	if writer.Last.SpentUSD > 0 {
		spent = fmt.Sprintf(" ($%.2f spent)", writer.Last.SpentUSD)
	}
	return fmt.Sprintf("shift stopped: the %s used its whole $%.2f cap%s in round %d before it finished, so Ploeg did not retry it at the same cap. "+
		"Raise the cap for this role or split the ticket, then restart the item",
		writer.Role, writer.Last.AuthorizedUSD, spent, round)
}

func escalatedCap(previous float64, role plan.Role) float64 {
	if previous <= 0 {
		previous = role.CapUSD()
	}
	return previous * role.Escalation()
}

func (e *Engine) planRole(team, role string) (plan.Role, bool) {
	tp, ok, _ := e.planFor(team)
	if !ok {
		return plan.Role{}, false
	}
	return plan.Plans{team: tp}.Role(team, role)
}

// capFor recovers a Role's per-Run spending cap from the Team's plan, so a
// retry is authorized on the same terms as the attempt it replaces.
func (e *Engine) capFor(team, role string) float64 {
	tp, ok, _ := e.planFor(team)
	if !ok {
		return 0
	}
	for _, rd := range tp.Rounds {
		for _, r := range rd.Roles {
			if r.Name == role {
				return float64(r.Cap)
			}
		}
	}
	return 0
}
