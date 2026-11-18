package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"strconv"

	"github.com/ploeg-hq/ploeg/pkg/store"
	"github.com/ploeg-hq/ploeg/pkg/target"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

// DefaultPrepareRepositoryMaxBudgetUSD is a Team's prepare cap when its
// configuration names none (ADR-0194).
const DefaultPrepareRepositoryMaxBudgetUSD = 2.00

const prepareDescription = "Ploeg's prepare_repository brief (ADR-0194): add a root AGENTS.md written from the repository's own code, citing where each command came from, and change no other path."

// PreparePullRequests is the forge side of a prepare_repository Work Item
// (ADR-0194): closing its pull request when a person cancels it, and asking
// whether a pull request a cancel left open still is.
type PreparePullRequests interface {
	ClosePreparePullRequest(ctx context.Context, workItemID int64, actor string) store.PrepareCancelled
	PullRequestStillOpen(ctx context.Context, t work.Target, number int) bool
	PullRequestStateOf(ctx context.Context, t work.Target, number int) string
	OpenPullRequestOn(ctx context.Context, item work.WorkItem) (number int, ok bool)
}

const withdrawnPreparesChecked = 10

func (s *Server) preparePullRequestState(ctx context.Context, item *store.OperatorItem) {
	if item.Kind != string(work.KindPrepareRepository) || item.PullRequest == nil || item.Target == nil {
		return
	}
	item.PullRequest.State = "unknown"
	forge, ok := s.Engine.(PreparePullRequests)
	if !ok || item.PullRequest.Number == nil {
		return
	}
	t := work.Target{Forge: item.Target.Forge, Owner: item.Target.Owner, Repo: item.Target.Repo, BaseBranch: item.Target.BaseBranch}
	item.PullRequest.State = forge.PullRequestStateOf(ctx, t, *item.PullRequest.Number)
}

// ForgeTokenIsolationProxy is the forge token isolation a deployment needs
// for prepare_repository work (ADR-0194): the worker's forge proxy is where
// its one deliverable is enforced.
const ForgeTokenIsolationProxy = "proxy"

const claudeEnvironmentHarness = "claude-environment"

// prepareUnsupported names why the team cannot run a prepare_repository Work
// Item without bypassing the worker's delivery fence, or "" when it can: the
// deployment's workers must isolate the forge token behind their proxy, and
// no writing Role may be carried by a person's client or dispatched to a
// Claude environment, whose runner pushes on its own.
func (s *Server) prepareUnsupported(team, requestedHarness string) string {
	if s.ForgeTokenIsolation != ForgeTokenIsolationProxy {
		return "the deployment's workers do not isolate the forge token behind their proxy (PLOEG_WORKER_FORGE_TOKEN_ISOLATION is not proxy), and the proxy is where a prepare Run's one deliverable is enforced"
	}
	roles, _ := s.writingRoles(team)
	for _, role := range roles {
		h, configured := s.OperatorConfig.TeamHarnesses[team][role]
		switch {
		case configured && h.PersonCarried():
			return fmt.Sprintf("its writing Role %q is carried by a person's own client, which pushes without the worker's forge proxy", role)
		case configured && h.Name == claudeEnvironmentHarness:
			return fmt.Sprintf("its writing Role %q runs in a Claude environment, whose runner pushes without the worker's forge proxy", role)
		case requestedHarness == claudeEnvironmentHarness:
			return fmt.Sprintf("the requested Harness runs its writing Role %q in a Claude environment, whose runner pushes without the worker's forge proxy", role)
		}
	}
	return ""
}

func (s *Server) writingRoles(team string) ([]string, bool) {
	if e, ok := s.Engine.(interface {
		WritingRoles(team string) ([]string, bool)
	}); ok {
		return e.WritingRoles(team)
	}
	return nil, false
}

func refusePrepareUnsupported(w http.ResponseWriter, team, reason string) {
	operatorError(w, 422, "prepare_unsupported_for_team", "Team "+team+" cannot prepare a repository: "+reason+".")
}

func prepareTitle(t work.Target) string {
	return "Add AGENTS.md to " + t.Owner + "/" + t.Repo
}

func admittedKind(k work.Kind) work.Kind {
	if k == "" {
		return work.KindChange
	}
	return k
}

func (s *Server) prepareCap(team string) float64 {
	if c, ok := s.PrepareRepositoryCaps[team]; ok && c > 0 {
		return c
	}
	return DefaultPrepareRepositoryMaxBudgetUSD
}

func refuseTargetNotReady(w http.ResponseWriter, key string, v target.Readiness, prepareAvailable bool) {
	operatorJSON(w, 422, map[string]any{"schemaVersion": "1.0", "error": map[string]any{
		"code":             "target_not_ready",
		"message":          "Target " + key + " is not ready: " + v.Reason,
		"prepareAvailable": prepareAvailable,
	}})
}

func refusePrepareInProgress(w http.ResponseWriter, e *store.PrepareInProgressError) {
	detail := map[string]any{"code": "prepare_in_progress", "message": "The target already has a prepare in progress: " + e.Error() + ". Finish or cancel it, or close its pull request, first."}
	if e.WorkItemID != 0 {
		detail["workItemId"] = strconv.FormatInt(e.WorkItemID, 10)
	}
	if e.PullRequestURL != "" {
		detail["pullRequestUrl"] = e.PullRequestURL
	}
	operatorJSON(w, 409, map[string]any{"schemaVersion": "1.0", "error": detail})
}

func (s *Server) noUnclosedPrepare(ctx context.Context, w http.ResponseWriter, t work.Target) bool {
	cancelled, err := s.Store.UnclosedPrepareCancellations(ctx, t)
	if err != nil {
		operatorError(w, 503, "unavailable", "Ploeg could not confirm the admission.")
		return false
	}
	forge, _ := s.Engine.(PreparePullRequests)
	for _, c := range cancelled {
		if forge != nil && !forge.PullRequestStillOpen(ctx, t, c.PullRequest) {
			continue
		}
		refusePrepareInProgress(w, &store.PrepareInProgressError{WorkItemID: c.WorkItemID, PullRequestURL: c.PullRequestURL})
		return false
	}
	if forge == nil {
		return true
	}
	withdrawn, err := s.Store.WithdrawnPrepares(ctx, t, withdrawnPreparesChecked)
	if err != nil {
		operatorError(w, 503, "unavailable", "Ploeg could not confirm the admission.")
		return false
	}
	for _, item := range withdrawn {
		if number, ok := forge.OpenPullRequestOn(ctx, item); ok && number > 0 {
			id, _ := strconv.ParseInt(item.ID, 10, 64)
			refusePrepareInProgress(w, &store.PrepareInProgressError{WorkItemID: id, PullRequest: number})
			return false
		}
	}
	return true
}

func (s *Server) closePreparePullRequest(ctx context.Context, id int64, actor string) *store.PrepareCancelled {
	item, err := s.Store.WorkItem(ctx, id)
	if err != nil || !item.PreparesRepository() {
		return nil
	}
	forge, ok := s.Engine.(PreparePullRequests)
	if !ok {
		return nil
	}
	c := forge.ClosePreparePullRequest(ctx, id, actor)
	if c.PullRequest == 0 {
		return nil
	}
	if err := s.Store.RecordPrepareCancelled(ctx, c); err != nil {
		s.Log.Error("prepare_cancelled not audited", "work_item", id, "err", err)
	}
	return &c
}
