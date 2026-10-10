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
	Log             *slog.Logger
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
		w.recordFacts(ctx, t, facts)
		if reportsMergeability && facts.State == provider.PullRequestOpen {
			w.recordMergeCheck(ctx, t, read)
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

func (w *ReviewWatch) recordFacts(ctx context.Context, t reviewTarget, facts provider.PullRequestFacts) {
	ci, err := forgefacts.CI(ctx, t.forge, t.repo, facts.HeadSHA)
	if err != nil {
		w.log().Warn("review reconcile: commit status unavailable",
			"work_item", t.item.WorkItemID, "repo", t.repo, "pr", t.pr, "head", facts.HeadSHA, "err", err)
	}
	pr := forgefacts.PullRequest{Forge: t.forge.Name(), Repo: t.repo, Number: t.pr, WorkItemID: t.item.WorkItemID}
	if _, err := w.Store.RecordPullRequestFacts(ctx, forgefacts.Facts(pr, facts, ci, time.Now())); err != nil {
		w.log().Error("review reconcile: pull request facts not recorded",
			"work_item", t.item.WorkItemID, "repo", t.repo, "pr", t.pr, "err", err)
		return
	}
	if err := forgefacts.RecordChangedPaths(ctx, w.Store, t.forge, pr, facts.HeadSHA); err != nil {
		w.log().Warn("review reconcile: changed paths not recorded; they stay unknown",
			"work_item", t.item.WorkItemID, "repo", t.repo, "pr", t.pr, "head", facts.HeadSHA, "err", err)
	}
}

func (w *ReviewWatch) recordMergeCheck(ctx context.Context, t reviewTarget, read provider.PullRequestMergeability) {
	key := store.PullRequestKey{Forge: t.forge.Name(), Repo: t.repo, Number: t.pr}
	if _, _, err := w.Store.RecordMergeCheck(ctx, key, t.item.WorkItemID, store.MergeObservation{
		Mergeable: read.Mergeable, HeadSHA: read.Facts.HeadSHA, BaseBranch: read.BaseBranch,
	}, time.Now()); err != nil {
		w.log().Error("review reconcile: merge state not recorded",
			"work_item", t.item.WorkItemID, "repo", t.repo, "pr", t.pr, "err", err)
	}
}

func (w *ReviewWatch) settle(ctx context.Context, t reviewTarget, next work.State, reason string) error {
	moved, err := w.Store.SettleReview(ctx, t.item.WorkItemID, next, reason)
	if err != nil || !moved {
		return err
	}
	w.log().Info("review settled", "work_item", t.item.WorkItemID, "state", string(next),
		"repo", t.repo, "pr", t.pr, "reason", reason)
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
