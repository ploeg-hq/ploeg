package shiftengine

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/ploeg-hq/ploeg/pkg/provider"
	"github.com/ploeg-hq/ploeg/pkg/store"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

// The blackboard's transport half (ADR-0011). A reading Run returns findings
// in its OutcomeReport; Ploeg puts them where a human is already looking —
// the pull request — and injects them into the next Round's prompt. The agent
// gains no client, tool or credential to take part (R6).
//
// Everything here is best-effort by design: a forge that is down, a Target
// that never resolved, or a Shift with no pull request yet must not stall the
// pipeline or lose an Outcome. Failures are logged, never returned into the
// lifecycle, and never block a state transition (blackboard spec).

// pullRequest finds the Shift's pull request in repository ("owner/name")
// from its Runs' stored facts: a Run's admitted delivery record, or for a Run
// from an older worker its last link to a pull request of repository. The
// last Run that names one wins.
func pullRequest(reports []store.RunReport, repository string) (link string, number int) {
	for _, r := range reports {
		if l, n := store.RunPullRequest(r.Delivery, r.Links, repository); n > 0 {
			link, number = l, n
		}
	}
	return link, number
}

// publishRound posts one comment per reading Run of the round that just
// finished, attributed to its Role.
//
// At-least-once: two evaluators can both observe the same completed Round and
// both publish before one wins the round-advance CAS. A duplicated comment is
// visible and harmless; a missing one loses the review a human is waiting for.
func (e *Engine) publishRound(ctx context.Context, si store.ShiftInfo, reports []store.RunReport, round int) {
	if len(e.Forges) == 0 {
		return
	}
	var pending []store.RunReport
	for _, r := range reports {
		if r.Round == round && strings.TrimSpace(r.Findings) != "" {
			pending = append(pending, r)
		}
	}
	if len(pending) == 0 {
		return
	}

	fp, repo, pr, skip := e.pullRequestThread(ctx, si, reports)
	if skip != "" {
		e.Log.Info("findings not published: "+skip,
			"shift", si.ID, "work_item", si.WorkItemID, "round", round, "findings", len(pending))
		return
	}

	for _, r := range pending {
		if err := fp.Comment(ctx, repo, pr, findingsComment(r)); err != nil {
			e.Log.Error("findings comment failed", "shift", si.ID, "role", r.Role,
				"repo", repo, "pr", pr, "err", err)
			continue
		}
		e.Log.Info("findings published", "shift", si.ID, "role", r.Role,
			"round", r.Round, "repo", repo, "pr", pr)
	}
}

// pullRequestThread resolves the forge, repository and number of the Shift's
// pull request. A non-empty skip says why there is nowhere to comment.
func (e *Engine) pullRequestThread(ctx context.Context, si store.ShiftInfo, reports []store.RunReport) (fp provider.ForgeProvider, repo string, pr int, skip string) {
	item, err := e.Store.WorkItem(ctx, si.WorkItemID)
	if err != nil {
		return nil, "", 0, "work item read failed"
	}
	if item.Target == nil {
		return nil, "", 0, "work item has no resolved target"
	}
	repo = item.Target.Owner + "/" + item.Target.Repo
	if _, pr = pullRequest(reports, repo); pr == 0 {
		return nil, "", 0, "no pull request on this shift yet"
	}
	forgeID := item.Target.Forge
	if forgeID == "" {
		forgeID = e.DefaultForge
	}
	fp, ok := e.Forges[forgeID]
	if !ok {
		return nil, "", 0, "no provider for forge " + strconv.Quote(forgeID)
	}
	return fp, repo, pr, ""
}

// budgetExhausted reports whether a close reason says the Shift's pool could
// not fund another Round.
func budgetExhausted(closeReason string) bool {
	return closeReason == reasonLoopBudget || strings.HasPrefix(closeReason, reasonPoolExhausted)
}

// usd formats a dollar amount with two decimals.
func usd(v float64) string {
	return "$" + strconv.FormatFloat(math.Round(v*100)/100+0, 'f', 2, 64)
}

// budgetSummary states the pool's spent, reserved and total amounts.
func budgetSummary(l store.ShiftLedger) string {
	return fmt.Sprintf("the budget pool could not fund another Round. Spent %s, reserved %s, pool %s.",
		usd(l.Spent), usd(l.Reserved), usd(l.Budget))
}

// budgetComment is the pull request comment for a Shift that ran out of budget.
func budgetComment(l store.ShiftLedger) string {
	return "### Budget exhausted\n\nPloeg stopped working on this pull request: " + budgetSummary(l) +
		"\n\nA person is asked to take over.\n\n<sub>Posted by Ploeg.</sub>"
}

// publishBudgetExhausted posts budgetComment on the Shift's pull request, if
// one exists. Failures are logged, never returned.
func (e *Engine) publishBudgetExhausted(ctx context.Context, si store.ShiftInfo, l store.ShiftLedger) {
	if len(e.Forges) == 0 {
		return
	}
	reports, err := e.Store.RoundReports(ctx, si.ID)
	if err != nil {
		e.Log.Error("budget notice not published: reports read failed", "shift", si.ID, "err", err)
		return
	}
	fp, repo, pr, skip := e.pullRequestThread(ctx, si, reports)
	if skip != "" {
		e.Log.Info("budget notice not published: "+skip, "shift", si.ID, "work_item", si.WorkItemID)
		return
	}
	if err := fp.Comment(ctx, repo, pr, budgetComment(l)); err != nil {
		e.Log.Error("budget notice comment failed", "shift", si.ID, "repo", repo, "pr", pr, "err", err)
		return
	}
	e.Log.Info("budget notice published", "shift", si.ID, "repo", repo, "pr", pr)
}

// publishUsageReport maintains the one usage report comment on the Shift's
// pull request: it renders the Shift's usage and evidence, finds the comment
// by its marker and edits it, or creates it when there is none.
//
// Best-effort and outside the lifecycle (R2, R3): every failure is logged and
// skipped, and none changes an Outcome, a close reason or a Work Item state.
// A failed list is a skip, never a blind post, because a comment we cannot see
// is a duplicate waiting to happen on the next refresh (design D6).
func (e *Engine) publishUsageReport(ctx context.Context, si store.ShiftInfo) {
	if !e.UsageReport || len(e.Forges) == 0 {
		return
	}
	reports, err := e.Store.RoundReports(ctx, si.ID)
	if err != nil {
		e.Log.Error("usage report not published: reports read failed", "shift", si.ID, "err", err)
		return
	}
	fp, repo, pr, skip := e.pullRequestThread(ctx, si, reports)
	if skip != "" {
		e.Log.Info("usage report not published: "+skip, "shift", si.ID, "work_item", si.WorkItemID)
		return
	}
	usage, err := e.Store.ShiftUsage(ctx, si.ID)
	if err != nil {
		e.Log.Error("usage report not published: usage read failed", "shift", si.ID, "err", err)
		return
	}
	body := usageReport(usageReportInput{
		Shift:    usage,
		Ledger:   usage.Ledger,
		TraceID:  traceAlias(usage.Runs),
		Evidence: parseEvidence(reports),
		Links:    e.ReportLinks,
	})

	comments, err := fp.Comments(ctx, repo, pr)
	if err != nil {
		// Do not post blind: without the thread we cannot know whether the
		// report already exists, and a second copy is exactly what the marker
		// is for.
		e.Log.Error("usage report not published: comments list failed", "shift", si.ID, "repo", repo, "pr", pr, "err", err)
		return
	}
	for _, c := range comments {
		if strings.HasPrefix(strings.TrimSpace(c.Body), usageReportMarker) {
			if err := fp.EditComment(ctx, repo, pr, c.ID, body); err != nil {
				e.Log.Error("usage report edit failed", "shift", si.ID, "repo", repo, "pr", pr, "comment", c.ID, "err", err)
				return
			}
			e.Log.Info("usage report updated", "shift", si.ID, "repo", repo, "pr", pr, "comment", c.ID)
			return
		}
	}
	if err := fp.Comment(ctx, repo, pr, body); err != nil {
		e.Log.Error("usage report comment failed", "shift", si.ID, "repo", repo, "pr", pr, "err", err)
		return
	}
	e.Log.Info("usage report published", "shift", si.ID, "repo", repo, "pr", pr)
}

// RefreshUsageReport re-renders and updates the usage report after a late
// settlement has moved its numbers. It is a no-op for a Shift with no pull
// request and returns no error for one, so the sweep treats it as best-effort
// (R2, R3).
func (e *Engine) RefreshUsageReport(ctx context.Context, shiftID int64) error {
	if !e.UsageReport {
		return nil
	}
	si, err := e.Store.ShiftInfoByID(ctx, shiftID)
	if err != nil {
		return err
	}
	e.publishUsageReport(ctx, si)
	return nil
}

// findingsComment renders one Role's findings for the pull request thread.
// Attribution first: a human scanning the thread needs to know which
// specialist said what before they read the prose.
func findingsComment(r store.RunReport) string {
	var b strings.Builder
	fmt.Fprintf(&b, "### %s — round %d\n\n", r.Role, r.Round)
	if r.Summary != "" {
		fmt.Fprintf(&b, "_%s_\n\n", r.Summary)
	}
	b.WriteString(r.Findings)
	if r.Writes {
		b.WriteString("\n\n<sub>Posted by Ploeg for the writing Run that pushed this branch.</sub>")
	} else {
		b.WriteString("\n\n<sub>Posted by Ploeg on behalf of the reviewing agent. It could not push to this branch.</sub>")
	}
	return b.String()
}

// notifyTracker closes the loop the factory opens: a Shift that has stopped
// tells the board what happened, with the pull request to look at. Without
// this the handoff is something a person notices, not something they are told
// (blackboard spec, "a person is asked to merge").
//
// Called for every TERMINAL settle — see Engine.close. It was called only for
// needs_human, which meant the successful path said nothing.
func (e *Engine) notifyTracker(ctx context.Context, si store.ShiftInfo, settled work.State, closeReason, reason string, budget *store.ShiftLedger) {
	if len(e.Trackers) == 0 {
		return
	}
	item, err := e.Store.WorkItem(ctx, si.WorkItemID)
	if err != nil {
		e.Log.Error("tracker write-back skipped: work item read failed", "shift", si.ID, "err", err)
		return
	}
	if item.Provider == work.ProviderPloeg {
		e.Log.Info("tracker write-back skipped: the work item exists only in Ploeg", "shift", si.ID)
		return
	}
	tp, ok := e.Trackers[item.Provider]
	if !ok {
		e.Log.Warn("tracker write-back skipped: no provider", "provider", item.Provider, "shift", si.ID)
		return
	}

	reports, err := e.Store.RoundReports(ctx, si.ID)
	if err != nil {
		e.Log.Error("tracker write-back: reports read failed", "shift", si.ID, "err", err)
	}
	var repository string
	if item.Target != nil {
		repository = item.Target.Owner + "/" + item.Target.Repo
	}
	link, _ := pullRequest(reports, repository)

	body := trackerMessage(settled, reason, link, len(reports), si.Round, budget, closeReason == reasonReviewFailed)

	// Write-back failure is logged, never propagated: the Work Item state and
	// the audit rows are already correct, and losing them to a tracker outage
	// would be the worse trade (blackboard spec).
	if err := tp.Comment(ctx, item.ExternalID, body); err != nil {
		e.Log.Error("tracker comment failed", "shift", si.ID, "external_id", item.ExternalID, "err", err)
	}
	// Ploeg NEVER reports an item done, whatever its own state says.
	//
	// This deployment's Definition of Done is "in production, monitored,
	// first telemetry observed". Ploeg opens a pull request and stops — it
	// does not merge, deploy, or watch a graph, so it is never in a position
	// to know the item is finished. Closing the task here would hide work
	// that still owes a merge, a release and a look at the dashboards, and
	// the comment we just posted literally asks a person to merge it.
	//
	// So the tracker is told awaiting_review or needs_human. The provider drops anything
	// that is not `done`, which makes this a deliberate no-op today rather
	// than an accidental one — keep the call, because a provider whose
	// tracker HAS an in-review column should be free to use it.
	status := work.StateNeedsHuman
	if settled == work.StateAwaitingReview {
		status = work.StateAwaitingReview
	}
	if err := tp.SetStatus(ctx, item.ExternalID, status); err != nil {
		e.Log.Error("tracker status write failed", "shift", si.ID, "external_id", item.ExternalID, "err", err)
	}
}

// trackerMessage is what the board is told, per terminal state. Pure so the
// wording is table-testable without an embedded Postgres.
// A non-nil budget adds the budget-exhaustion notice with the pool's amounts.
func trackerMessage(settled work.State, reason, link string, runs, rounds int, budget *store.ShiftLedger, reviewMissing bool) string {
	var b strings.Builder
	switch {
	case settled == work.StateStale:
		b.WriteString("Ploeg gave up on this item after repeated failures.\n\n")
	case settled == work.StateAwaitingReview && reviewMissing:
		b.WriteString("Ploeg finished this item, but an agent review of its pull request is missing. Please review it yourself.\n\n")
	case settled == work.StateAwaitingReview:
		b.WriteString("Ploeg finished this item. Its pull request is ready for review.\n\n")
	case link != "":
		// A pull request exists, so the Shift produced work — say so, whatever
		// state the item settled in.
		//
		// This used to key on the state alone, and a CONFIGURED plan settles
		// needs_human on success by design (the last word is "a person is
		// asked to merge"). So every successful multi-Round Shift opened its
		// board comment with "Ploeg stopped working this item" directly above
		// a link to the pull request it had just produced. The state describes
		// who owes the next move; it does not describe how the run went.
		b.WriteString("Ploeg finished this item and opened a pull request.\n\n")
	case settled == work.StateDone:
		b.WriteString("Ploeg finished this item without needing to change anything.\n\n")
	default:
		b.WriteString("Ploeg stopped working this item without opening a pull request.\n\n")
	}
	fmt.Fprintf(&b, "**Outcome:** %s\n", reason)
	if budget != nil {
		fmt.Fprintf(&b, "**Budget exhausted:** %s\n", budgetSummary(*budget))
	}
	if link != "" {
		fmt.Fprintf(&b, "**Pull request:** %s\n", link)
		b.WriteString("\nPlease review and merge — the agents never merge their own work.\n")
	} else {
		b.WriteString("\nNo pull request was opened.\n")
	}
	// Left open on purpose: Ploeg's part is done, the item's is not. See the
	// SetStatus comment above.
	b.WriteString("\nThis task stays open until it is in production and its first telemetry has been seen.\n")
	if runs > 0 {
		fmt.Fprintf(&b, "\n<sub>%d agent run(s) across %d round(s).</sub>", runs, rounds)
	}
	return b.String()
}
