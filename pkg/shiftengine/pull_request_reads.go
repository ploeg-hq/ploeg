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

// PullRequestReadWatch turns the open pull requests of listed authors on a
// registered target into Work Items a Team reads and reports on, on the pull
// request (ADR-0109). Reconcile lists each target's open pull requests on a
// timer; Consider takes one pull request a forge webhook named. Both lead to
// the same Work Item, once per pull request, and again when its title
// changed.
type PullRequestReadWatch struct {
	Store *store.Store
	// Forges is keyed like Engine.Forges: by forge instance id, and by
	// dialect name for the webhook route.
	Forges       map[string]provider.ForgeProvider
	DefaultForge string
	Routes       []work.PullRequestReadRoute
	// Plans decides whether a Team can read at all: it needs a reading Role
	// that is not a shadow.
	Plans plan.Plans
	// Engine opens the Shift of a Work Item this watch queued. Nil leaves it
	// to the sweeper.
	Engine *Engine
	Log    *slog.Logger
}

// Reconcile lists every route's open pull requests and queues a reading for
// each one by a listed author that has none yet, or whose title changed.
// Errors are logged per route so one unreachable repository does not stall
// the rest.
func (w *PullRequestReadWatch) Reconcile(ctx context.Context) {
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
			w.log().Warn("pull request reads: open pull requests unavailable", "target", route.Key, "repo", repo, "err", err)
			continue
		}
		for _, pr := range pulls {
			if ctx.Err() != nil {
				return
			}
			w.read(ctx, route, fp, pr)
		}
	}
}

// Consider takes a pull request a forge webhook reported as opened or pushed
// to. forge is the provider key the webhook arrived on. The first route of
// the repository whose authors include the pull request's author reads it; a
// repository without routes, or an author outside every route, does nothing.
func (w *PullRequestReadWatch) Consider(ctx context.Context, forge, repo string, number int, author string) {
	fp := w.Forges[forge]
	if fp == nil || number <= 0 {
		return
	}
	var summary *provider.PullRequestSummary
	for _, route := range w.Routes {
		if !strings.EqualFold(route.Target.Owner+"/"+route.Target.Repo, repo) {
			continue
		}
		routeForge, lister, ok := w.forgeFor(route)
		if !ok || routeForge != fp {
			continue
		}
		if author != "" && !route.Policy.Reads(author) {
			continue
		}
		if summary == nil {
			pr, err := lister.PullRequestSummary(ctx, repo, number)
			if err != nil {
				w.log().Warn("pull request reads: pull request unreadable", "target", route.Key, "repo", repo, "pr", number, "err", err)
				return
			}
			summary = &pr
		}
		if !route.Policy.Reads(summary.Author) {
			continue
		}
		w.read(ctx, route, fp, *summary)
		return
	}
}

func (w *PullRequestReadWatch) forgeFor(route work.PullRequestReadRoute) (provider.ForgeProvider, provider.PullRequestLister, bool) {
	forgeID := route.Target.Forge
	if forgeID == "" {
		forgeID = w.DefaultForge
	}
	fp := w.Forges[forgeID]
	if fp == nil {
		w.log().Error("pull request reads: no provider for the target's forge", "target", route.Key, "forge", forgeID)
		return nil, nil, false
	}
	lister, ok := fp.(provider.PullRequestLister)
	if !ok {
		w.log().Error("pull request reads: the forge provider cannot list pull requests", "target", route.Key, "forge", fp.Name())
		return nil, nil, false
	}
	return fp, lister, true
}

// read queues the Work Item for one pull request when the route covers it,
// and opens its Shift.
func (w *PullRequestReadWatch) read(ctx context.Context, route work.PullRequestReadRoute, fp provider.ForgeProvider, pr provider.PullRequestSummary) {
	repo := route.Target.Owner + "/" + route.Target.Repo
	switch {
	case pr.Number <= 0, pr.Draft, !route.Policy.Reads(pr.Author):
		return
	case route.Target.BaseBranch != "" && pr.BaseBranch != "" && pr.BaseBranch != route.Target.BaseBranch:
		return
	case pr.HeadBranch == "":
		w.log().Warn("pull request reads: pull request names no head branch", "target", route.Key, "repo", repo, "pr", pr.Number)
		return
	}
	tp, ok := w.Plans[route.Policy.Team]
	if !ok {
		w.log().Error("pull request reads: the team has no plan", "target", route.Key, "team", route.Policy.Team)
		return
	}
	if _, ok := tp.ReviewOnly(); !ok {
		w.log().Error("pull request reads: the team's plan has no reading Role that is not a shadow", "target", route.Key, "team", route.Policy.Team)
		return
	}
	title, description := work.PullRequestBrief(work.PullRequestSubject{
		Repo: repo, Number: pr.Number, URL: pr.URL, Title: pr.Title, Body: pr.Body, Author: pr.Author,
		HeadBranch: pr.HeadBranch, BaseBranch: pr.BaseBranch, Instructions: route.Policy.Brief(),
	})
	id, item, result, err := w.Store.EnsurePullRequestReading(ctx, store.PullRequestReading{
		Forge: fp.Name(), Target: route.Target, Team: route.Policy.Team, Priority: route.Policy.Priority,
		Number: pr.Number, Title: title, Description: description, URL: pr.URL,
		HeadBranch: pr.HeadBranch, HeadSHA: pr.HeadSHA, Author: pr.Author, PullRequestTitle: pr.Title,
		MaxOpen: route.Policy.OpenCap(), MaxReviews: route.Policy.ReviewCap(),
	})
	if err != nil {
		w.log().Error("pull request reads: reading not queued", "target", route.Key, "repo", repo, "pr", pr.Number, "err", err)
		return
	}
	switch result {
	case store.PullRequestReadingCreated, store.PullRequestReadingRequeued:
		w.log().Info("pull request reading queued", "target", route.Key, "repo", repo, "pr", pr.Number,
			"author", pr.Author, "team", route.Policy.Team, "work_item", id, "result", string(result))
		if w.Engine != nil {
			if err := w.Engine.EnsureShift(ctx, id, item); err != nil {
				w.log().Error("pull request reads: shift open failed; sweeper will repair", "work_item", id, "err", err)
			}
		}
	case store.PullRequestReadingUnchanged:
	default:
		w.log().Info("pull request reading not queued", "target", route.Key, "repo", repo, "pr", pr.Number,
			"reason", string(result))
	}
}

func (w *PullRequestReadWatch) log() *slog.Logger {
	if w.Log == nil {
		return slog.Default()
	}
	return w.Log
}
