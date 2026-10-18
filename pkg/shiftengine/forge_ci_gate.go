package shiftengine

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/forgefacts"
	"github.com/ploeg-hq/ploeg/pkg/plan"
	"github.com/ploeg-hq/ploeg/pkg/provider"
	"github.com/ploeg-hq/ploeg/pkg/store"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

// The forge CI gate (ADR-0119). A Shift that would hand its pull request to
// a person waits until the forge's CI on the pull request's head passed. The
// wait does not block anything: the Shift stays open, and the sweeper's next
// evaluation reads the CI again. A red check the change caused opens a fix
// round; a cancelled one, or one that also fails on the base commit, is not
// the writer's to fix and gets one re-run before a person is asked.

// DefaultCIWaitTimeout is how long a Shift waits for the forge's CI when its
// route sets no ci.waitTimeout.
const DefaultCIWaitTimeout = 60 * time.Minute

// CIWaits maps a route, as CIRoute names it, to how long a Shift on it waits
// for the forge's CI.
type CIWaits map[string]time.Duration

// CIRoute names the route of a repository on a forge instance. forge is the
// instance id as the routing configuration writes it, empty for the
// deployment's default forge.
func CIRoute(forge, repo string) string {
	return strings.ToLower(strings.TrimSpace(forge)) + " " + strings.ToLower(strings.TrimSpace(repo))
}

func (w CIWaits) timeout(t work.Target) time.Duration {
	if d, ok := w[CIRoute(t.Forge, t.Owner+"/"+t.Repo)]; ok && d > 0 {
		return d
	}
	return DefaultCIWaitTimeout
}

type ciVerdict int

const (
	ciGreen ciVerdict = iota
	ciPending
	ciWritersToFix
	ciNotWritersToFix
)

// excusedCheck is a failing check that is not the writer's to fix, and why.
type excusedCheck struct {
	check provider.CommitCheck
	why   string
}

// ciReading is the forge's CI on one pull request head, sorted by cause.
type ciReading struct {
	verdict ciVerdict
	headSHA string
	pending []provider.CommitCheck
	failing []provider.CommitCheck
	excused []excusedCheck
}

// readForgeCI reads the head of pull request pr from the forge, never from an
// agent (ADR-0059), and sorts its CI. A pull request that is no longer open
// reads green: the review watch settles it.
func readForgeCI(ctx context.Context, fp provider.ForgeProvider, repo string, pr int) (ciReading, error) {
	facts, err := fp.PullRequestFacts(ctx, repo, pr)
	if err != nil {
		return ciReading{}, fmt.Errorf("pull request facts: %w", err)
	}
	if facts.State != provider.PullRequestOpen {
		return ciReading{verdict: ciGreen, headSHA: facts.HeadSHA}, nil
	}
	head, err := forgefacts.CI(ctx, fp, repo, facts.HeadSHA)
	if err != nil {
		return ciReading{headSHA: facts.HeadSHA}, fmt.Errorf("commit status of the head: %w", err)
	}
	return sortForgeCI(ctx, fp, repo, facts, head)
}

// sortForgeCI sorts head, the CI read at facts.HeadSHA, by cause. The base
// commit is read only when a check failed that the provider did not mark
// cancelled.
func sortForgeCI(ctx context.Context, fp provider.ForgeProvider, repo string, facts provider.PullRequestFacts, head *provider.CommitStatus) (ciReading, error) {
	r := ciReading{headSHA: facts.HeadSHA}
	if head == nil || head.State == provider.CommitSuccess {
		return r, nil
	}
	failingOnBase, err := baseFailures(ctx, fp, repo, facts, head)
	if err != nil {
		return r, err
	}
	for _, c := range head.Checks {
		switch {
		case c.State == provider.CommitPending:
			r.pending = append(r.pending, c)
		case c.Failed() && c.Cancelled:
			r.excused = append(r.excused, excusedCheck{check: c, why: "the forge reports it cancelled"})
		case c.Failed() && failingOnBase[c.Context]:
			r.excused = append(r.excused, excusedCheck{check: c, why: "it also fails on the base commit"})
		case c.Failed():
			r.failing = append(r.failing, c)
		}
	}
	switch {
	case len(r.failing) > 0:
		r.verdict = ciWritersToFix
	case len(r.pending) > 0, head.State == provider.CommitPending && len(r.excused) == 0:
		r.verdict = ciPending
	default:
		r.verdict = ciNotWritersToFix
	}
	return r, nil
}

func baseFailures(ctx context.Context, fp provider.ForgeProvider, repo string, facts provider.PullRequestFacts, head *provider.CommitStatus) (map[string]bool, error) {
	suspect := false
	for _, c := range head.Checks {
		suspect = suspect || (c.Failed() && !c.Cancelled)
	}
	if !suspect || facts.BaseSHA == "" || strings.EqualFold(facts.BaseSHA, facts.HeadSHA) {
		return nil, nil
	}
	base, err := forgefacts.CI(ctx, fp, repo, facts.BaseSHA)
	if err != nil {
		return nil, fmt.Errorf("commit status of the base: %w", err)
	}
	failing := map[string]bool{}
	if base != nil {
		for _, c := range base.Checks {
			failing[c.Context] = failing[c.Context] || c.Failed()
		}
	}
	return failing, nil
}

type ciStepKind int

const (
	ciProceed ciStepKind = iota
	ciHold
	ciFix
	ciPark
)

// ciStep is what the gate decided for one pull request head: let the Shift
// settle, hold it, hand the failing checks to the writer, or park it with a
// close reason and a message naming the checks.
type ciStep struct {
	kind    ciStepKind
	reason  string
	message string
	reading ciReading
}

// ciGate decides rules 3 to 5 of ADR-0119 for one pull request, remembering
// its wait and its one re-run per head commit under shiftID.
type ciGate struct {
	store *store.Store
	log   *slog.Logger
}

func (g ciGate) decide(ctx context.Context, shiftID int64, fp provider.ForgeProvider, repo string, reading ciReading,
	readErr error, timeout time.Duration) (ciStep, error) {
	if readErr != nil {
		g.log.Warn("forge CI gate: CI unreadable; holding", "shift", shiftID, "repo", repo, "head", reading.headSHA, "err", readErr)
		return g.hold(ctx, shiftID, reading, timeout, "the forge's CI could not be read ("+readErr.Error()+")")
	}
	switch reading.verdict {
	case ciWritersToFix:
		return ciStep{kind: ciFix, reading: reading}, nil
	case ciPending:
		return g.hold(ctx, shiftID, reading, timeout, "CI is still running: "+checkList(reading.pending))
	case ciNotWritersToFix:
		return g.reRunOnce(ctx, shiftID, fp, repo, reading, timeout)
	}
	return ciStep{kind: ciProceed, reading: reading}, nil
}

func (g ciGate) hold(ctx context.Context, shiftID int64, reading ciReading, timeout time.Duration, waitingFor string) (ciStep, error) {
	wait, err := g.store.WaitForCI(ctx, shiftID, reading.headSHA)
	if err != nil {
		return ciStep{}, err
	}
	if wait.Waited() < timeout {
		return ciStep{kind: ciHold, reading: reading}, nil
	}
	return ciStep{kind: ciPark, reason: reasonCINotFinished, reading: reading, message: fmt.Sprintf(
		"the forge's CI on the pull request's head did not finish within %s, so the pull request is not ready for review: %s. The pull request stays open and a person is asked to take over",
		timeout, waitingFor)}, nil
}

func (g ciGate) reRunOnce(ctx context.Context, shiftID int64, fp provider.ForgeProvider, repo string, reading ciReading, timeout time.Duration) (ciStep, error) {
	wait, err := g.store.WaitForCI(ctx, shiftID, reading.headSHA)
	if err != nil {
		return ciStep{}, err
	}
	if wait.ReRunAt == nil {
		err := provider.ReRunCI(ctx, fp, repo, reading.headSHA)
		if err == nil {
			if _, err := g.store.MarkCIReRun(ctx, shiftID, reading.headSHA); err != nil {
				return ciStep{}, err
			}
			g.log.Info("forge CI gate: CI re-run requested", "shift", shiftID, "repo", repo, "head", reading.headSHA,
				"checks", checkNames(reading.excused))
			return ciStep{kind: ciHold, reading: reading}, nil
		}
		why := "the forge cannot re-run CI"
		if !errors.Is(err, provider.ErrNoReRun) {
			g.log.Warn("forge CI gate: CI re-run refused", "shift", shiftID, "repo", repo, "head", reading.headSHA, "err", err)
			why = "the forge refused to re-run CI (" + err.Error() + ")"
		}
		return notPassed(reading, why), nil
	}
	if !reportedSince(reading.excused, *wait.ReRunAt) && wait.Waited() < timeout {
		return ciStep{kind: ciHold, reading: reading}, nil
	}
	return notPassed(reading, "CI was re-run once"), nil
}

func notPassed(reading ciReading, after string) ciStep {
	return ciStep{kind: ciPark, reason: reasonCINotPassed, reading: reading, message: fmt.Sprintf(
		"the forge's CI on the pull request's head did not pass and no failing check is the writer's to fix, so no fix round runs; %s. %s. The pull request stays open and a person is asked to take over",
		after, excusedList(reading.excused))}
}

// reportedSince reports whether every excused check was reported after at,
// so the result is the re-run's and not the run before it.
func reportedSince(excused []excusedCheck, at time.Time) bool {
	for _, e := range excused {
		if e.check.UpdatedAt.IsZero() || !e.check.UpdatedAt.After(at) {
			return false
		}
	}
	return true
}

// gateOnForgeCI holds back a Shift that would settle at awaiting_review
// until the forge's CI passed on its pull request's head. It reports whether
// it took the Shift's next step itself: held it open, opened a fix round or
// closed it. A Shift with no pull request, no resolved forge, a forge
// provider that cannot read commit statuses, or a forge that reports no
// checks is not held. A forge that can read statuses but is unreachable holds
// the Shift until the wait times out.
func (e *Engine) gateOnForgeCI(ctx context.Context, si store.ShiftInfo, tp plan.TeamPlan, reports []store.RunReport) (bool, error) {
	item, err := e.Store.WorkItem(ctx, si.WorkItemID)
	if err != nil {
		return false, err
	}
	if item.Target == nil {
		return false, nil
	}
	repo := item.Target.Owner + "/" + item.Target.Repo
	_, pr := pullRequest(reports, repo)
	if pr == 0 {
		return false, nil
	}
	fp, skip := e.forgeOf(*item.Target)
	if _, reads := fp.(provider.CommitStatusReader); skip != "" || !reads {
		return false, nil
	}
	reading, readErr := readForgeCI(ctx, fp, repo, pr)
	step, err := ciGate{store: e.Store, log: e.Log}.decide(ctx, si.ID, fp, repo, reading, readErr, e.CIWaits.timeout(*item.Target))
	if err != nil {
		return false, err
	}
	switch step.kind {
	case ciHold:
		e.Log.Debug("forge CI gate: holding the shift", "shift", si.ID, "work_item", si.WorkItemID, "head", reading.headSHA)
		return true, e.Store.MarkCIHeld(ctx, si.ID, reading.headSHA, si.Round)
	case ciPark:
		return true, e.close(ctx, si, step.reason, step.message, false, reports)
	case ciFix:
		return true, e.forgeCIFixRound(ctx, si, tp, reports, step.reading)
	}
	return false, nil
}

// forgeCIFixRound re-opens the writing Round with the failing checks in its
// briefing, under the same pool and cap as any fix round (ADR-0017). A reviewer's
// approval does not override it.
func (e *Engine) forgeCIFixRound(ctx context.Context, si store.ShiftInfo, tp plan.TeamPlan, reports []store.RunReport, reading ciReading) error {
	writerIdx, hasWriter := tp.WriterRound()
	reason := reasonFixCap
	allowed := hasWriter && tp.MaxFixRounds > 0
	if allowed {
		reason, allowed = e.fixRoundBounds(ctx, si, tp)
	}
	if !allowed {
		return e.close(ctx, si, reason, ciFixLoopMessage(reason, reading.failing), false, reports)
	}
	if err := e.Store.RecordCIFixRound(ctx, si.ID, reading.headSHA, si.Round+1, ciFailures(reading.failing)); err != nil {
		return err
	}
	round, err := e.Store.OpenRound(ctx, si.ID, si.Round, e.storeRoles(si.Team, tp.Rounds[writerIdx]))
	if err != nil {
		e.Log.Debug("forge CI fix round open skipped", "shift", si.ID, "from_round", si.Round, "err", err)
		return nil
	}
	e.Log.Info("fix round opening: forge CI failed", "shift", si.ID, "work_item", si.WorkItemID, "round", round,
		"head", reading.headSHA, "checks", checkList(reading.failing))
	return nil
}

func ciFixLoopMessage(reason string, failing []provider.CommitCheck) string {
	why := "the fix-round cap was reached"
	if reason == reasonLoopBudget {
		why = "the budget could not fund another fix round"
	}
	return fmt.Sprintf("the forge's CI still fails on the pull request's head and %s: %s. The pull request stays open and a person is asked to take over",
		why, checkList(failing))
}

func ciFailures(checks []provider.CommitCheck) []store.CIFailure {
	out := make([]store.CIFailure, 0, len(checks))
	for _, c := range checks {
		out = append(out, store.CIFailure{Context: c.Context, State: string(c.State), Description: c.Description, TargetURL: c.TargetURL})
	}
	return out
}

func checkList(checks []provider.CommitCheck) string {
	parts := make([]string, 0, len(checks))
	for _, c := range checks {
		parts = append(parts, describeCheck(c))
	}
	return strings.Join(parts, "; ")
}

func excusedList(excused []excusedCheck) string {
	parts := make([]string, 0, len(excused))
	for _, e := range excused {
		parts = append(parts, describeCheck(e.check)+", not sent to the writer because "+e.why)
	}
	return strings.Join(parts, "; ")
}

func checkNames(excused []excusedCheck) []string {
	names := make([]string, 0, len(excused))
	for _, e := range excused {
		names = append(names, e.check.Context)
	}
	return names
}

func describeCheck(c provider.CommitCheck) string {
	s := fmt.Sprintf("%q %s", c.Context, c.State)
	if d := strings.TrimSpace(c.Description); d != "" {
		s += fmt.Sprintf(" (%s)", d)
	}
	if c.TargetURL != "" {
		s += " " + c.TargetURL
	}
	return s
}
