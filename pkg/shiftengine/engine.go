// Package shiftengine owns the Shift lifecycle: when a Shift opens, how a
// Round advances, when a Shift closes, and what happens when either is left
// half-done (run-multi-agent-shifts, shift-orchestration spec).
//
// Every rule lives here and is called from two places with the same
// idempotent semantics — the fast path (webhook ingest opens, an outcome
// report evaluates) and the sweeper tick (EvaluateAll repairs whatever a
// crash left behind). That is the same claim/sweeper split the lease manager
// uses: the pipeline must never depend on a request handler surviving to the
// end of its function (R2). Advancement is derived from Run state, never
// reported by an agent.
package shiftengine

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/harness"
	"github.com/ploeg-hq/ploeg/pkg/plan"
	"github.com/ploeg-hq/ploeg/pkg/provider"
	"github.com/ploeg-hq/ploeg/pkg/store"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

// Engine drives Shifts for planned teams. Teams without a plan are invisible
// to it: their dispatch path is untouched.
type Engine struct {
	Store *store.Store
	Plans plan.Plans
	Log   *slog.Logger
	// Forges publishes findings to the pull request (ADR-0011). Keyed by
	// forge id, matching the Work Target's Forge. Nil or missing = findings
	// stay in the database and the run is otherwise unaffected.
	Forges map[string]provider.ForgeProvider
	// DefaultForge resolves a Target that names no forge, which is what
	// pkg/work.Target documents ("empty = the default forge") and what every
	// routing rule that omits `forge:` produces. It is the id an operator
	// configured, never an inference from len(Forges) — with two forges
	// registered, guessing would post an internal review onto whichever
	// instance happened to answer.
	DefaultForge string
	// Trackers asks a person to merge when a Shift closes. Keyed by provider
	// name, matching the Work Item's. Nil = no write-back.
	Trackers map[string]provider.TrackerProvider
	// Uniform gives a team with NO configured plan a synthesized one-writer
	// plan, so every queued item gets a Shift and "what is happening with
	// this item" has one answer instead of two.
	//
	// This is the one setting that changes behaviour for teams nobody
	// reconfigured, which is why it is a switch: PLOEG_SHIFTS_UNIFORM=false
	// restores the pre-Shift dispatch path without a rollback or a redeploy
	// of anything but ploegd.
	Uniform bool
	// UsageReport publishes the per-Shift usage and evidence report on the
	// pull request (PLOEG_USAGE_REPORT). Default on; false is the kill switch
	// and the report path does nothing else.
	UsageReport bool
	// ReportLinks are the optional link templates the usage report offers
	// (PLOEG_REPORT_*_URL). None configured omits the links section and the
	// rest of the report still renders.
	ReportLinks ReportLinks
	// TrackerRecheck is how often the sweep re-reads the tracker task of one
	// stopped Work Item (needs_human, awaiting_review) to settle it when the
	// task was closed. Zero means DefaultTrackerRecheck.
	TrackerRecheck time.Duration
}

// DefaultTrackerRecheck is the per-item interval between tracker re-reads of
// a stopped Work Item when Engine.TrackerRecheck is zero.
const DefaultTrackerRecheck = 15 * time.Minute

const (
	trackerRecheckBatch   = 20
	trackerRecheckTimeout = 10 * time.Second
)

// EnsureShift opens a Shift for a queued Work Item of a planned team, then
// evaluates it so the first Round materialises. Idempotent: an existing live
// Shift is left alone, and the shifts_one_live_per_item index settles the
// race two openers can still run into.
func (e *Engine) EnsureShift(ctx context.Context, workItemID int64, item work.WorkItem) error {
	tp, planned, _ := e.planFor(item.Team)
	if !planned {
		return nil
	}
	live, err := e.Store.LiveShiftForItem(ctx, workItemID)
	if err != nil {
		return err
	}
	if live == nil {
		branch := work.Branch(item)
		if _, err := e.Store.OpenShift(ctx, workItemID, item.Team, branch, float64(tp.Pool)); err != nil {
			// Unique-index violation = someone else opened it between our read
			// and our insert. That is the race behaving correctly; re-read.
			live, rerr := e.Store.LiveShiftForItem(ctx, workItemID)
			if rerr != nil || live == nil {
				return fmt.Errorf("open shift: %w", err)
			}
		}
		e.Log.Info("shift opened", "work_item", workItemID, "team", item.Team,
			"pool", float64(tp.Pool), "rounds", len(tp.Rounds))
	}
	return e.EvaluateItem(ctx, workItemID)
}

// planFor resolves a team's plan, synthesizing one when Uniform is set and
// the team has none.
//
// A synthesized plan is one Round with one writing Role, no name and no cap:
// exactly the engagement the pre-Shift path always was, now expressed as a
// Shift so that EVERY item has one. That is what makes the Shift the single
// answer to "what is happening with this item" — a bookkeeping change, not a
// behavioural one.
//
// The Role's name is empty on purpose. It makes the synthesized Run claimable
// by a role-less worker, which is what every plan-less team's pod still is,
// so no chart change is needed to adopt this. The pool is zero, which
// ClaimRole reads as unmetered, so the worker keeps minting against its env
// budget exactly as before.
// The second result reports whether the team is in scope at all; the third
// reports whether the plan was SYNTHESIZED, which decides how its Shift ends
// (see close).
func (e *Engine) planFor(team string) (tp plan.TeamPlan, ok, synthesized bool) {
	if tp, ok := e.Plans[team]; ok {
		return tp, true, false
	}
	if !e.Uniform {
		return plan.TeamPlan{}, false, false
	}
	return plan.TeamPlan{
		Rounds: []plan.Round{{Roles: []plan.Role{{Writes: true}}}},
	}, true, true
}

// EvaluateItem advances the live Shift on a Work Item, if any — the fast path
// the outcome handler calls. A missing live Shift is not an error: the other
// evaluator may have closed it first.
func (e *Engine) EvaluateItem(ctx context.Context, workItemID int64) error {
	si, err := e.Store.LiveShiftForItem(ctx, workItemID)
	if err != nil {
		return err
	}
	if si == nil {
		return nil
	}
	return e.evaluate(ctx, *si)
}

// evaluate is the one advancement rule. Rounds advance only when every Run in
// them has finished; the next Round comes from the plan; a stuck Run freezes
// the plan; an exhausted plan closes the Shift and asks a person to merge.
func (e *Engine) evaluate(ctx context.Context, si store.ShiftInfo) error {
	tp, planned, synthesized := e.planFor(si.Team)
	if !planned {
		// The plan was removed while the Shift ran (or uniform dispatch was
		// switched off under it). Freezing silently would strand the item;
		// close loudly and hand it to a person.
		return e.close(ctx, si, "plan removed from configuration",
			"team "+si.Team+" no longer has a plan; shift closed", false, nil)
	}

	complete, err := e.Store.RoundComplete(ctx, si.ID)
	if err != nil {
		return err
	}
	if !complete {
		return nil
	}

	// A stuck Run anywhere freezes the plan (R4, shift-orchestration spec):
	// retrying cannot fix what the agent said needs a human.
	reports, err := e.Store.RoundReports(ctx, si.ID)
	if err != nil {
		return err
	}
	// Findings reach the pull request as soon as their Round finishes, not at
	// close: a human watching the thread sees the review while the writer is
	// still working on it (ADR-0011).
	e.publishRound(ctx, si, reports, si.Round)
	// The usage report rides behind the findings: a Round just completed, so
	// its numbers and evidence moved. It publishes even when this Round
	// produced no findings.
	e.publishUsageReport(ctx, si)

	for _, r := range reports {
		if r.Outcome == string(work.OutcomeStuck) {
			// A stuck Outcome parks the item under BOTH dispatch shapes: R4
			// says so, and it is what ReportOutcome would have done anyway.
			return e.close(ctx, si,
				fmt.Sprintf("run stuck: %s round %d", r.Role, r.Round),
				fmt.Sprintf("shift stopped: %s reported stuck in round %d — %s", r.Role, r.Round, r.Summary),
				false, reports)
		}
	}

	// A Run that failed re-opens its Round in place, capped: a WRITING Run
	// because the plan must not step over an unwritten branch (ADR-0019), a
	// READING Run because a review that never happened must not read as one
	// (ADR-0043).
	//
	// `failed` is the sweeper's verdict on a pod that stopped renewing, so it
	// is retryable by construction, and retrying is what R2 promises: a node
	// eviction should not need a person.
	// Planned Shifts only. Under uniform dispatch a single-writer Shift
	// already does the right thing: the plan exhausts, close() settles by the
	// run's own Outcome, and `failed` maps to queued — the attempt-capped
	// requeue R5 requires. There is no later Round there to step over.
	if !synthesized {
		handled, err := e.retryFailedRuns(ctx, si, reports)
		if err != nil || handled {
			return err
		}
	}

	// The plan's next Round, or the end of the plan. si.Round counts opened
	// Rounds, so it doubles as the index of the next one.
	var next plan.Round
	if si.Round < len(tp.Rounds) {
		next = tp.Rounds[si.Round]
	} else {
		// Past the plan: the review loop decides whether anything more runs.
		loopRound, reason, ok := e.nextFixRound(ctx, si, tp, reports)
		if !ok {
			message := closeMessage(reason)
			if missing, found := missingReview(reports); found && reason == reasonPlanExhausted {
				reason, message = reasonReviewFailed, missing.message()
			}
			if checksHoldBackReview(reports) {
				reason, message = checksCloseReason(reason, message, reports)
			}
			return e.close(ctx, si, reason, message, synthesized, reports)
		}
		next = loopRound
	}
	round, err := e.Store.OpenRound(ctx, si.ID, si.Round, storeRoles(next))
	if err != nil {
		// The CAS lost to the other evaluator, or the Shift closed under us.
		// Both mean somebody else did the job; log at debug fidelity and move on.
		e.Log.Debug("round open skipped", "shift", si.ID, "from_round", si.Round, "err", err)
		return nil
	}
	e.Log.Info("round opened", "shift", si.ID, "work_item", si.WorkItemID,
		"team", si.Team, "round", round, "roles", len(next.Roles))
	return nil
}

// storeRoles converts a plan Round's roles into the store's claim shape.
func storeRoles(r plan.Round) []store.Role {
	out := make([]store.Role, 0, len(r.Roles))
	for _, role := range r.Roles {
		out = append(out, store.Role{Name: role.Name, Writes: role.Writes, Cap: role.CapUSD()})
	}
	return out
}

// close ends a Shift and moves its Work Item — the engine owns that
// transition for Shift runs (design.md D3), and it happens exactly once
// thanks to CloseShiftAndSettle's idempotency.
//
// Where the item lands depends on whose plan it was. A CONFIGURED plan that
// completes or is approved with a pull request lands at awaiting_review; any
// other close parks at needs_human (shift-orchestration spec).
//
// A SYNTHESIZED plan — uniform dispatch giving a plan-less team a Shift —
// takes the outcome-derived state instead, which is exactly what
// ReportOutcome writes for the same outcome.
//
// Neither reaches awaiting_review while the pull request's checks did not
// pass on its pushed commit (ADR-0070).
func (e *Engine) close(ctx context.Context, si store.ShiftInfo, closeReason, humanReason string,
	synthesized bool, reports []store.RunReport) error {
	next := work.StateNeedsHuman
	if synthesized {
		if outcome, ok := terminalOutcome(reports); ok {
			next = work.StateForOutcome(outcome)
		}
	} else if readyForReview(closeReason, reports) {
		next = work.StateAwaitingReview
	}
	if next == work.StateAwaitingReview && checksHoldBackReview(reports) {
		next = work.StateNeedsHuman
	}
	closed, settled, err := e.Store.CloseShiftAndSettle(ctx, si.ID, closeReason, next, humanReason)
	if err != nil {
		return err
	}
	if !closed {
		e.Log.Debug("shift close skipped: already closed", "shift", si.ID, "work_item", si.WorkItemID,
			"item_state", string(settled))
		return nil
	}
	e.Log.Info("shift closed", "shift", si.ID, "work_item", si.WorkItemID,
		"team", si.Team, "reason", closeReason, "item_state", string(settled))
	// After the state is durable, never before: a tracker outage must not be
	// able to leave a Shift open or an item un-transitioned.
	//
	// Every TERMINAL settle notifies, not just needs_human. The blackboard
	// spec's "a person is asked to merge" says a Shift closing without a
	// further Round writes back — it does not say "only when it went badly".
	// Gating on needs_human meant the happy path (a plan-less team's
	// pr_opened settles done) told the board nothing at all: the pull request
	// existed and nobody was informed.
	//
	// Returning early on a lost close keeps it to one comment when the outcome
	// fast-path and the sweeper both conclude the same Shift is over. The
	// notify decision lives HERE, not inside CloseShiftAndSettle, so that cancellation
	// (backlog #8) can close a Shift WITHOUT notifying — a human who
	// unassigned a ticket does not need to be told Ploeg finished it.
	if settled == work.StateAwaitingReview && e.remandForReview(ctx, si.WorkItemID) {
		return nil
	}
	if work.Terminal(settled) {
		var budget *store.ShiftLedger
		if budgetExhausted(closeReason) {
			if l, err := e.Store.Ledger(ctx, si.ID); err != nil {
				e.Log.Error("budget notice: ledger read failed", "shift", si.ID, "err", err)
			} else {
				budget = &l
			}
		}
		e.notifyTracker(ctx, si, settled, closeReason, humanReason, budget)
		if budget != nil {
			e.publishBudgetExhausted(ctx, si, *budget)
		}
		// The Shift is over, so its totals are final enough to show. The report
		// loads its own reports: this branch's `reports` may be nil (a floor
		// close passes nil), and the evidence section degrades to "not
		// recorded" when there are none.
		e.publishUsageReport(ctx, si)
	}
	return nil
}

func (e *Engine) remandForReview(ctx context.Context, workItemID int64) bool {
	requeued, err := e.Store.RequeueForPendingReview(ctx, workItemID)
	if err != nil {
		e.Log.Error("review remand failed", "work_item", workItemID, "err", err)
		return false
	}
	if !requeued {
		return false
	}
	item, err := e.Store.WorkItem(ctx, workItemID)
	if err != nil {
		e.Log.Error("review remand: work item read failed; sweeper will open the shift", "work_item", workItemID, "err", err)
		return true
	}
	e.Log.Info("work item queued again for a pending review", "work_item", workItemID, "team", item.Team)
	if err := e.EnsureShift(ctx, workItemID, item); err != nil {
		e.Log.Error("review remand: shift open failed; sweeper will repair", "work_item", workItemID, "err", err)
	}
	return true
}

// readyForReview reports whether a configured plan closed with a pull request
// a person can review: its reviewer approved, or it ran to completion, with or
// without an agent review, without the last review asking for changes, and a
// writer opened or updated it. A writer whose stored delivery record did not
// observe that delivery does not count (ADR-0059).
func readyForReview(closeReason string, reports []store.RunReport) bool {
	if closeReason != reasonApproved && closeReason != reasonPlanExhausted && closeReason != reasonReviewFailed {
		return false
	}
	if closeReason != reasonApproved && lastReviewRequestsChanges(reports) {
		return false
	}
	for _, r := range reports {
		if r.Writes && work.Outcome(r.Outcome).AssertsDelivery() && (r.Delivery == nil || r.Delivery.Observed.Delivered()) {
			return true
		}
	}
	return false
}

// lastReviewRequestsChanges reports whether the last reading Run after the
// last writing Run asked for changes. A reader that ran before the last
// writer never saw its pull request.
func lastReviewRequestsChanges(reports []store.RunReport) bool {
	for i := len(reports) - 1; i >= 0; i-- {
		if reports[i].Writes {
			return false
		}
		if reports[i].Outcome != "" {
			return reports[i].Verdict == harness.VerdictRequestChanges
		}
	}
	return false
}

// terminalOutcome is the last Outcome any Run of the Shift reported — what a
// single-writer Shift's whole engagement amounts to.
func terminalOutcome(reports []store.RunReport) (work.Outcome, bool) {
	for i := len(reports) - 1; i >= 0; i-- {
		if reports[i].Outcome != "" {
			return work.Outcome(reports[i].Outcome), true
		}
	}
	return "", false
}

// EvaluateAll is the sweeper's repair pass: open the Shifts crashes left
// unopened, advance the Rounds crashes left unadvanced, park the pools that
// ran dry. Errors are logged, never returned — one broken Shift must not
// stall the sweep of the rest (R2).
func (e *Engine) EvaluateAll(ctx context.Context) {
	e.settleClosedInTracker(ctx)

	// Queued items with no live Shift: the crash window between
	// IngestAssigned committing and EnsureShift running. Asked of the
	// database rather than iterated per configured team, so uniform dispatch
	// repairs items of teams that have no plan too. EnsureShift itself
	// decides whether a given team is in scope.
	ids, err := e.Store.QueuedWithoutShift(ctx)
	if err != nil {
		e.Log.Error("shift sweep: queued-without-shift read failed", "err", err)
	}
	for _, id := range ids {
		item, err := e.Store.WorkItem(ctx, id)
		if err != nil {
			e.Log.Error("shift sweep: work item read failed", "work_item", id, "err", err)
			continue
		}
		if err := e.EnsureShift(ctx, id, item); err != nil {
			e.Log.Error("shift sweep: ensure failed", "work_item", id, "err", err)
		}
	}

	// Live Shifts whose evaluation was lost.
	shifts, err := e.Store.LiveShifts(ctx)
	if err != nil {
		e.Log.Error("shift sweep: live shifts read failed", "err", err)
		return
	}
	for _, si := range shifts {
		if err := e.evaluate(ctx, si); err != nil {
			e.Log.Error("shift sweep: evaluate failed", "shift", si.ID, "err", err)
		}
	}

	// Pools that can no longer fund their pending work: park, don't retry —
	// retrying cannot fix running out of money (shift-orchestration spec).
	broke, err := e.Store.ShiftsBelowFloor(ctx, unsettledHoldPatience)
	if err != nil {
		e.Log.Error("shift sweep: floor read failed", "err", err)
		return
	}
	for _, b := range broke {
		reason, kind := floorCloseReason(b)
		si := store.ShiftInfo{ID: b.ShiftID, WorkItemID: b.WorkItemID, Team: b.Team}
		// Always needs_human: running out of money is never retryable, under
		// either dispatch shape (shift-orchestration spec).
		if err := e.close(ctx, si, reason, closeMessage(kind), false, nil); err != nil {
			e.Log.Error("shift sweep: park failed", "shift", b.ShiftID, "err", err)
		}
	}
}

func (e *Engine) settleClosedInTracker(ctx context.Context) {
	if len(e.Trackers) == 0 {
		return
	}
	providers := make([]string, 0, len(e.Trackers))
	for name := range e.Trackers {
		providers = append(providers, name)
	}
	every := e.TrackerRecheck
	if every <= 0 {
		every = DefaultTrackerRecheck
	}
	due, err := e.Store.ClaimStoppedTrackerChecks(ctx, providers, every, trackerRecheckBatch)
	if err != nil {
		e.Log.Error("shift sweep: stopped tracker items read failed", "err", err)
		return
	}
	for _, d := range due {
		tp, ok := e.Trackers[d.Provider]
		if !ok {
			continue
		}
		readCtx, cancel := context.WithTimeout(ctx, trackerRecheckTimeout)
		item, err := tp.FetchItem(readCtx, d.ExternalID)
		cancel()
		if err != nil {
			e.Log.Warn("shift sweep: tracker task read failed", "work_item", d.WorkItemID, "provider", d.Provider, "err", err)
			continue
		}
		if !item.Closed {
			continue
		}
		wd, err := e.Store.SettleClosedInTracker(ctx, d.WorkItemID, "sweep:"+d.Provider)
		if err != nil {
			e.Log.Error("shift sweep: closed tracker task settle failed", "work_item", d.WorkItemID, "err", err)
			continue
		}
		if wd.Withdrawn {
			e.Log.Info("work item settled: its task was closed in the tracker", "work_item", d.WorkItemID, "provider", d.Provider)
		}
	}
}

const unsettledHoldPatience = 24 * time.Hour

func floorCloseReason(b store.ShiftLedgerEntry) (reason, kind string) {
	if b.SettlementCouldFund() {
		return fmt.Sprintf("%s: pool %.2f, spent %.2f, held %.2f",
			reasonPoolHeld, b.Ledger.Budget, b.Ledger.Spent, b.Unsettled), reasonPoolHeld
	}
	return fmt.Sprintf("%s: pool %.2f, spent %.2f, reserved %.2f",
		reasonPoolExhausted, b.Ledger.Budget, b.Ledger.Spent, b.Ledger.Reserved), reasonPoolExhausted
}
