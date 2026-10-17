package shiftengine

import (
	"context"
	"log/slog"
	"strings"

	"github.com/ploeg-hq/ploeg/pkg/plan"
	"github.com/ploeg-hq/ploeg/pkg/provider"
	"github.com/ploeg-hq/ploeg/pkg/store"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

// DependencyUpdateWatch turns the open pull requests of listed authors on a
// registered target, such as a dependency bot's, into Work Items a Team
// explains on the pull request (ADR-0106). Reconcile lists each target's open
// pull requests on a timer; Consider takes one pull request a forge webhook
// named. Both lead to the same Work Item, once per pull request, and again
// when its title changed.
type DependencyUpdateWatch struct {
	Store *store.Store
	// Forges is keyed like Engine.Forges: by forge instance id, and by
	// dialect name for the webhook route.
	Forges       map[string]provider.ForgeProvider
	DefaultForge string
	Routes       []work.DependencyUpdateRoute
	// Plans decides whether a Team can explain at all: it needs a reading
	// Role that is not a shadow.
	Plans plan.Plans
	// Engine opens the Shift of a Work Item this watch queued. Nil leaves it
	// to the sweeper.
	Engine *Engine
	Log    *slog.Logger
}

// Reconcile lists every route's open pull requests and queues an explanation
// for each one by a listed author that has none yet, or whose title changed.
// Errors are logged per route so one unreachable repository does not stall
// the rest.
func (w *DependencyUpdateWatch) Reconcile(ctx context.Context) {
	for _, route := range w.Routes {
		if ctx.Err() != nil {
			return
		}
		fp, lister, ok := w.forgeFor(route)
		if !ok {
			continue
		}
		repo := route.Target.Owner + "/" + route.Target.Repo
		pulls, err := lister.ListOpenPullRequests(ctx, repo)
		if err != nil {
			w.log().Warn("dependency updates: open pull requests unavailable", "target", route.Key, "repo", repo, "err", err)
			continue
		}
		for _, pr := range pulls {
			if ctx.Err() != nil {
				return
			}
			w.explain(ctx, route, fp, pr)
		}
	}
}

// Consider takes a pull request a forge webhook reported as opened or pushed
// to. forge is the provider key the webhook arrived on. A pull request on a
// repository without a policy, or by an author outside it, does nothing.
func (w *DependencyUpdateWatch) Consider(ctx context.Context, forge, repo string, number int, author string) {
	fp := w.Forges[forge]
	if fp == nil || number <= 0 {
		return
	}
	for _, route := range w.Routes {
		if !strings.EqualFold(route.Target.Owner+"/"+route.Target.Repo, repo) {
			continue
		}
		routeForge, lister, ok := w.forgeFor(route)
		if !ok || routeForge != fp {
			continue
		}
		if author != "" && !route.Policy.Explains(author) {
			return
		}
		pr, err := lister.PullRequestSummary(ctx, repo, number)
		if err != nil {
			w.log().Warn("dependency updates: pull request unreadable", "target", route.Key, "repo", repo, "pr", number, "err", err)
			return
		}
		w.explain(ctx, route, fp, pr)
		return
	}
}

func (w *DependencyUpdateWatch) forgeFor(route work.DependencyUpdateRoute) (provider.ForgeProvider, provider.PullRequestLister, bool) {
	forgeID := route.Target.Forge
	if forgeID == "" {
		forgeID = w.DefaultForge
	}
	fp := w.Forges[forgeID]
	if fp == nil {
		w.log().Error("dependency updates: no provider for the target's forge", "target", route.Key, "forge", forgeID)
		return nil, nil, false
	}
	lister, ok := fp.(provider.PullRequestLister)
	if !ok {
		w.log().Error("dependency updates: the forge provider cannot list pull requests", "target", route.Key, "forge", fp.Name())
		return nil, nil, false
	}
	return fp, lister, true
}

// explain queues the Work Item for one pull request when the route's policy
// covers it, and opens its Shift.
func (w *DependencyUpdateWatch) explain(ctx context.Context, route work.DependencyUpdateRoute, fp provider.ForgeProvider, pr provider.PullRequestSummary) {
	repo := route.Target.Owner + "/" + route.Target.Repo
	switch {
	case pr.Number <= 0, pr.Draft, !route.Policy.Explains(pr.Author):
		return
	case route.Target.BaseBranch != "" && pr.BaseBranch != "" && pr.BaseBranch != route.Target.BaseBranch:
		return
	case pr.HeadBranch == "":
		w.log().Warn("dependency updates: pull request names no head branch", "target", route.Key, "repo", repo, "pr", pr.Number)
		return
	}
	tp, ok := w.Plans[route.Policy.Team]
	if !ok {
		w.log().Error("dependency updates: the team has no plan", "target", route.Key, "team", route.Policy.Team)
		return
	}
	if _, ok := tp.ReviewOnly(); !ok {
		w.log().Error("dependency updates: the team's plan has no reading Role that is not a shadow", "target", route.Key, "team", route.Policy.Team)
		return
	}
	title, description := work.PullRequestBrief(work.PullRequestSubject{
		Repo: repo, Number: pr.Number, URL: pr.URL, Title: pr.Title, Body: pr.Body, Author: pr.Author,
		HeadBranch: pr.HeadBranch, BaseBranch: pr.BaseBranch,
	})
	id, item, result, err := w.Store.EnsurePullRequestExplanation(ctx, store.PullRequestExplanation{
		Forge: fp.Name(), Target: route.Target, Team: route.Policy.Team, Priority: route.Policy.Priority,
		Number: pr.Number, Title: title, Description: description, URL: pr.URL,
		HeadBranch: pr.HeadBranch, HeadSHA: pr.HeadSHA, Author: pr.Author, PullRequestTitle: pr.Title,
		MaxOpen: route.Policy.OpenCap(), MaxReviews: route.Policy.ReviewCap(),
	})
	if err != nil {
		w.log().Error("dependency updates: explanation not queued", "target", route.Key, "repo", repo, "pr", pr.Number, "err", err)
		return
	}
	switch result {
	case store.PullRequestExplanationCreated, store.PullRequestExplanationRequeued:
		w.log().Info("dependency update explanation queued", "target", route.Key, "repo", repo, "pr", pr.Number,
			"author", pr.Author, "team", route.Policy.Team, "work_item", id, "result", string(result))
		if w.Engine != nil {
			if err := w.Engine.EnsureShift(ctx, id, item); err != nil {
				w.log().Error("dependency updates: shift open failed; sweeper will repair", "work_item", id, "err", err)
			}
		}
	case store.PullRequestExplanationUnchanged:
	default:
		w.log().Info("dependency update explanation not queued", "target", route.Key, "repo", repo, "pr", pr.Number,
			"reason", string(result))
	}
}

func (w *DependencyUpdateWatch) log() *slog.Logger {
	if w.Log == nil {
		return slog.Default()
	}
	return w.Log
}
