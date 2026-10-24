package shiftengine

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/forgefacts"
	"github.com/ploeg-hq/ploeg/pkg/provider"
	"github.com/ploeg-hq/ploeg/pkg/store"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

// ReviewWatch moves Work Items out of awaiting_review when a human merges or
// closes their pull request. Forge webhooks drive it; Reconcile asks the forge
// directly and covers deliveries that never arrived.
type ReviewWatch struct {
	Store *store.Store
	// Forges is keyed like Engine.Forges: by forge instance id, and by dialect
	// name for the webhook route. Both keys of one instance hold the same value.
	Forges       map[string]provider.ForgeProvider
	DefaultForge string
	Trackers     map[string]provider.TrackerProvider
	// MarkTrackerDone lets a merge set the tracker item to done, for providers
	// that map done to a status.
	MarkTrackerDone bool
	// OutcomeNotes records each settled review as a repository-level note,
	// and a merge as evidence for the Work Item's repository promotions
	// (ADR-0122). Set when the deployment writes notes.
	OutcomeNotes bool
	// CIWaits is how long an item whose head CI went back to pending may
	// stay in awaiting_review, per route, as Engine.CIWaits (ADR-0119).
	CIWaits CIWaits
	// Conflicts is told once each time an item's pull request becomes
	// conflicted, so the Team that owns it can rebase it (ADR-0146). Nil
	// records the merge state and nothing else.
	Conflicts ConflictFollowUps
	Log       *slog.Logger
}

// ConflictFollowUps acts on a pull request the forge confirmed as
// conflicting with its base branch while its Work Item awaits review.
type ConflictFollowUps interface {
	RebaseConflicted(ctx context.Context, forge, repo, branch string, pr int, base string)
}

type reviewTarget struct {
	item  store.ReviewItem
	forge provider.ForgeProvider
	repo  string
	pr    int
	link  string
}

// HandleForgeEvent settles the Work Item whose pull request a merged or closed
// event names. forge is the provider key the webhook arrived on. Other event
// kinds are ignored.
func (w *ReviewWatch) HandleForgeEvent(ctx context.Context, forge string, ev provider.ForgeEvent) error {
	next, reason, ok := reviewTransition(ev.Kind)
	if !ok || ev.PR <= 0 || ev.Repo == "" {
		return nil
	}
	fp := w.Forges[forge]
	if fp == nil {
		return nil
	}
	targets, err := w.targets(ctx)
	if err != nil {
		return err
	}
	for _, t := range targets {
		if t.forge != fp || t.pr != ev.PR || !strings.EqualFold(t.repo, ev.Repo) {
			continue
		}
		if err := w.settle(ctx, t, next, reason); err != nil {
			return err
		}
	}
	return nil
}

// Reconcile asks the forge for the state of every awaiting_review pull
// request and settles the ones that are no longer open. Errors are logged per
// item so one unreachable repository does not stall the rest.
func (w *ReviewWatch) Reconcile(ctx context.Context) {
	targets, err := w.targets(ctx)
	if err != nil {
		w.log().Error("review reconcile: awaiting_review read failed", "err", err)
		return
	}
	for _, t := range targets {
		if ctx.Err() != nil {
			return
		}
		read, reportsMergeability, err := provider.ReadPullRequest(ctx, t.forge, t.repo, t.pr)
		if err != nil {
			w.log().Warn("review reconcile: pull request state unavailable",
				"work_item", t.item.WorkItemID, "repo", t.repo, "pr", t.pr, "err", err)
			continue
		}
		facts := read.Facts
		ci, ciErr := w.recordFacts(ctx, t, facts)
		if reportsMergeability && facts.State == provider.PullRequestOpen {
			w.recordMergeCheck(ctx, t, read)
		}
		if facts.State == provider.PullRequestOpen {
			w.gateOnForgeCI(ctx, t, facts, ci, ciErr)
			continue
		}
		var kind provider.ForgeEventKind
		switch facts.State {
		case provider.PullRequestMerged:
			kind = provider.ForgePRMerged
		case provider.PullRequestClosed:
			kind = provider.ForgePRClosed
		default:
			continue
		}
		next, reason, _ := reviewTransition(kind)
		if err := w.settle(ctx, t, next, reason); err != nil {
			w.log().Error("review reconcile: settle failed", "work_item", t.item.WorkItemID, "err", err)
		}
	}
}

func reviewTransition(kind provider.ForgeEventKind) (work.State, string, bool) {
	switch kind {
	case provider.ForgePRMerged:
		return work.StateDone, "pull request merged", true
	case provider.ForgePRClosed:
		return work.StateNeedsHuman, "pull request closed without merging", true
	}
	return "", "", false
}

func (w *ReviewWatch) targets(ctx context.Context) ([]reviewTarget, error) {
	items, err := w.Store.AwaitingReview(ctx)
	if err != nil {
		return nil, err
	}
	var out []reviewTarget
	for _, it := range items {
		if it.Target == nil {
			continue
		}
		repo := it.Target.Owner + "/" + it.Target.Repo
		link, pr := store.RunPullRequest(it.Delivery, it.Links, repo)
		if pr == 0 && it.Origin == work.OriginPullRequest {
			// The item reads a pull request someone else opened
			// (ADR-0109): a merge or close of that pull request settles it.
			pr = it.SourcePR
		}
		if pr == 0 {
			continue
		}
		forgeID := it.Target.Forge
		if forgeID == "" {
			forgeID = w.DefaultForge
		}
		fp := w.Forges[forgeID]
		if fp == nil {
			continue
		}
		out = append(out, reviewTarget{item: it, forge: fp, repo: repo, pr: pr, link: link})
	}
	return out, nil
}

func (w *ReviewWatch) recordFacts(ctx context.Context, t reviewTarget, facts provider.PullRequestFacts) (*provider.CommitStatus, error) {
	ci, ciErr := forgefacts.CI(ctx, t.forge, t.repo, facts.HeadSHA)
	if ciErr != nil {
		w.log().Warn("review reconcile: commit status unavailable",
			"work_item", t.item.WorkItemID, "repo", t.repo, "pr", t.pr, "head", facts.HeadSHA, "err", ciErr)
	}
	pr := forgefacts.PullRequest{Forge: t.forge.Name(), Repo: t.repo, Number: t.pr, WorkItemID: t.item.WorkItemID}
	if _, err := w.Store.RecordPullRequestFacts(ctx, forgefacts.Facts(pr, facts, ci, time.Now())); err != nil {
		w.log().Error("review reconcile: pull request facts not recorded",
			"work_item", t.item.WorkItemID, "repo", t.repo, "pr", t.pr, "err", err)
		return ci, ciErr
	}
	if err := forgefacts.RecordChangedPaths(ctx, w.Store, t.forge, pr, facts.HeadSHA); err != nil {
		w.log().Warn("review reconcile: changed paths not recorded; they stay unknown",
			"work_item", t.item.WorkItemID, "repo", t.repo, "pr", t.pr, "head", facts.HeadSHA, "err", err)
	}
	return ci, ciErr
}

// gateOnForgeCI takes an item out of awaiting_review when its head CI is no
// longer success (ADR-0119, rule 6): it waits for pending CI, re-runs a
// cancelled one once, and then moves the item to needs_human. The Shift that
// handed it to review has closed, so a red check the writer could fix opens no
// fix round here and Ploeg starts no new Shift on its own: a person decides.
// A forge it cannot read leaves the item where it is.
func (w *ReviewWatch) gateOnForgeCI(ctx context.Context, t reviewTarget, facts provider.PullRequestFacts, head *provider.CommitStatus, readErr error) {
	if readErr != nil || t.item.Origin == work.OriginPullRequest || t.item.ShiftID == 0 || t.item.Target == nil {
		return
	}
	reading, err := sortForgeCI(ctx, t.forge, t.repo, facts, head)
	if err != nil {
		w.log().Warn("review reconcile: forge CI unreadable; the item stays in review until a read succeeds",
			"work_item", t.item.WorkItemID, "repo", t.repo, "pr", t.pr, "err", err)
		return
	}
	step, err := ciGate{store: w.Store, log: w.log()}.decide(ctx, t.item.ShiftID, t.forge, t.repo, reading, nil, w.CIWaits.timeout(*t.item.Target))
	if err != nil {
		w.log().Error("review reconcile: forge CI gate failed", "work_item", t.item.WorkItemID, "err", err)
		return
	}
	reason, message := step.reason, step.message
	switch step.kind {
	case ciProceed, ciHold:
		return
	case ciFix:
		reason = reasonCINotPassed
		message = "the forge's CI failed on the pull request's head after it was handed to review, and the Shift that wrote it has closed, so no fix round runs: " +
			checkList(step.reading.failing) + ". The pull request stays open and a person is asked to take over"
	}
	moved, err := w.Store.LeaveReviewForCI(ctx, t.item.WorkItemID, reason, message)
	if err != nil || !moved {
		if err != nil {
			w.log().Error("review reconcile: leaving review failed", "work_item", t.item.WorkItemID, "err", err)
		}
		return
	}
	w.log().Info("review left: forge CI is not success", "work_item", t.item.WorkItemID, "repo", t.repo, "pr", t.pr,
		"head", facts.HeadSHA, "reason", reason)
	w.notifyCI(ctx, t, message)
}

func (w *ReviewWatch) notifyCI(ctx context.Context, t reviewTarget, message string) {
	tp, ok := w.Trackers[t.item.Provider]
	if !ok {
		return
	}
	var b strings.Builder
	b.WriteString("This item is no longer ready for review: " + message + ".\n\n")
	if t.link != "" {
		fmt.Fprintf(&b, "**Pull request:** %s\n\n", t.link)
	}
	b.WriteString("Ploeg moved the item to needs_human. Assign it again to start a new attempt.\n")
	if err := tp.Comment(ctx, t.item.ExternalID, b.String()); err != nil {
		w.log().Error("tracker comment failed", "work_item", t.item.WorkItemID, "external_id", t.item.ExternalID, "err", err)
	}
	if err := tp.SetStatus(ctx, t.item.ExternalID, work.StateNeedsHuman); err != nil {
		w.log().Error("tracker status write failed", "work_item", t.item.WorkItemID, "external_id", t.item.ExternalID, "err", err)
	}
}

func (w *ReviewWatch) recordMergeCheck(ctx context.Context, t reviewTarget, read provider.PullRequestMergeability) {
	key := store.PullRequestKey{Forge: t.forge.Name(), Repo: t.repo, Number: t.pr}
	check, _, err := w.Store.RecordMergeCheck(ctx, key, t.item.WorkItemID, store.MergeObservation{
		Mergeable: read.Mergeable, HeadSHA: read.Facts.HeadSHA, BaseBranch: read.BaseBranch,
	}, time.Now())
	if err != nil {
		w.log().Error("review reconcile: merge state not recorded",
			"work_item", t.item.WorkItemID, "repo", t.repo, "pr", t.pr, "err", err)
		return
	}
	if !check.Entered || w.Conflicts == nil || t.item.Origin == work.OriginPullRequest || t.item.Branch == "" {
		return
	}
	base := read.BaseBranch
	if base == "" && t.item.Target != nil {
		base = t.item.Target.BaseBranch
	}
	w.Conflicts.RebaseConflicted(ctx, t.forge.Name(), t.repo, t.item.Branch, t.pr, base)
}

func (w *ReviewWatch) settle(ctx context.Context, t reviewTarget, next work.State, reason string) error {
	moved, err := w.Store.SettleReview(ctx, t.item.WorkItemID, next, reason)
	if err != nil || !moved {
		return err
	}
	w.log().Info("review settled", "work_item", t.item.WorkItemID, "state", string(next),
		"repo", t.repo, "pr", t.pr, "reason", reason)
	if w.OutcomeNotes {
		if err := w.Store.ReviewOutcomeNote(ctx, t.item.WorkItemID, t.pr, next == work.StateDone, time.Now()); err != nil {
			w.log().Error("review outcome note not recorded; later Runs miss it", "work_item", t.item.WorkItemID, "pr", t.pr, "err", err)
		}
	}
	w.notify(ctx, t, next)
	return nil
}

func (w *ReviewWatch) notify(ctx context.Context, t reviewTarget, next work.State) {
	tp, ok := w.Trackers[t.item.Provider]
	if !ok {
		return
	}
	if err := tp.Comment(ctx, t.item.ExternalID, reviewMessage(next, t.link, w.MarkTrackerDone)); err != nil {
		w.log().Error("tracker comment failed", "work_item", t.item.WorkItemID, "external_id", t.item.ExternalID, "err", err)
	}
	if next == work.StateDone && !w.MarkTrackerDone {
		return
	}
	if err := tp.SetStatus(ctx, t.item.ExternalID, next); err != nil {
		w.log().Error("tracker status write failed", "work_item", t.item.WorkItemID, "external_id", t.item.ExternalID, "err", err)
	}
}

func reviewMessage(next work.State, link string, markDone bool) string {
	var b strings.Builder
	if next == work.StateDone {
		b.WriteString("The pull request for this item was merged.\n\n")
	} else {
		b.WriteString("The pull request for this item was closed without merging.\n\n")
	}
	if link != "" {
		fmt.Fprintf(&b, "**Pull request:** %s\n\n", link)
	}
	switch {
	case next != work.StateDone:
		b.WriteString("Ploeg moved the item to needs_human. Assign it again to start a new attempt.\n")
	case markDone:
		b.WriteString("Ploeg marked the item done.\n")
	default:
		b.WriteString("Ploeg marked its Work Item done. This task stays open until it is in production and its first telemetry has been seen.\n")
	}
	return b.String()
}

func (w *ReviewWatch) log() *slog.Logger {
	if w.Log != nil {
		return w.Log
	}
	return slog.Default()
}
