// Package httpapi exposes ploegd's two surfaces: webhook ingest (tracker →
// queued work) and the run API an agent container uses (claim, renew,
// checkpoint, outcome) per the worker-claims-at-startup convention
// (backlog #48).
package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/followup"
	"github.com/ploeg-hq/ploeg/pkg/forgebroker"
	"github.com/ploeg-hq/ploeg/pkg/gate"
	"github.com/ploeg-hq/ploeg/pkg/harness"
	"github.com/ploeg-hq/ploeg/pkg/provider"
	"github.com/ploeg-hq/ploeg/pkg/store"
	"github.com/ploeg-hq/ploeg/pkg/target"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

type Server struct {
	OperatorConfig OperatorConfig
	WorkerSecurity *WorkerSecurity
	LLMControl     *LLMControl
	Store          *store.Store
	Trackers       map[string]provider.TrackerProvider
	// Targets resolves a tracker scope to the repository the work lands in.
	// Nil = no mapping configured; every item stays unresolved and workers use
	// their env-configured repo (the pre-decoupling behavior).
	Targets target.Resolver
	// Readiness gates the registered targets (ADR-0038): an item routed to an
	// unready one is refused at ingest, and a Run claiming one is stopped
	// before it starts. Nil = no target registry, no gate.
	Readiness *target.Gate
	// ScopeTeams pins a tracker container to one team (config: a project's
	// `team:`). The pin BEATS the assignee mapping — the config has always
	// said "Team routes this project's work to one team. Empty means the
	// assignee decides", and until this field the second sentence was the
	// whole implementation. Nil or missing scope = the assignee decides,
	// unchanged.
	ScopeTeams map[string]string
	LeaseTTL   time.Duration
	Log        *slog.Logger
	// Engine is the Shift lifecycle fast path (run-multi-agent-shifts): ingest
	// opens, an outcome report evaluates. Nil = no shift engine configured,
	// dispatch unchanged. Engine errors are logged, never returned to the
	// caller — the sweeper's EvaluateAll repairs whatever a lost fast path
	// leaves behind (R2), so a webhook or worker must never see a 5xx for it.
	Engine ShiftEngine
	// Forges hosts the forge webhook route. Nil or missing = the endpoint
	// answers 404 for that provider; nothing else in ploegd depends on it.
	Forges map[string]provider.ForgeProvider
	// Reviews settles awaiting_review Work Items from merged and closed pull
	// request events. Nil = those events are recorded only.
	Reviews ReviewSettler
	// ForgeCreds mints the per-run push credential a writing Run gets
	// (ADR-0013 tier 2). Nil = the worker keeps its env credential, which is
	// the pre-tier-2 behaviour.
	ForgeCreds forgebroker.Broker
	// RoleCaps supplies the per-Run spending ceiling for a (team, role),
	// implemented by plan.Plans. Nil = no caps, and the authorization is
	// bounded by the Shift pool alone.
	RoleCaps RoleCaps
	// TeamCaps bounds how many Runs a team may have running at once. A claim
	// over the cap answers 204 exactly like an empty queue. Nil = unlimited.
	TeamCaps TeamCaps
	// TrackerWebhooks is the latest Vikunja webhook coverage check, reported
	// by /readyz. Nil = no check configured.
	TrackerWebhooks *WebhookCoverage
	// CreatedWork holds each Team's limits on the Work Items its Runs create
	// (ADR-0031). A Team absent here gets followup.Default.
	CreatedWork map[string]followup.Policy
	// MetricsCacheTTL is how long a /metrics result is reused before the
	// database is read again. Zero means DefaultMetricsCacheTTL; negative
	// disables the cache.
	MetricsCacheTTL time.Duration

	metrics metricsCache
	// FollowUps holds each Team's opt-in to acting on forge events. A Team
	// that is missing acts on nothing; its forge events are only recorded.
	FollowUps map[string]work.ForgeFollowUps
	// ForgeBots are the forge logins Ploeg itself acts as. A review from one
	// of them never sends work back to a Team.
	ForgeBots []string
	// Deploys authenticates POST /api/v1/deploys (ADR-0047). Nil disables
	// the endpoint, which then answers 404.
	Deploys *DeployAuth
	// Gates maps each configured board's statuses to delivery gates
	// (ADR-0051). Ploeg records the gate moves and every status move of a
	// board here (ADR-0080); a board absent here records neither.
	Gates gate.Boards
	// ContextMaxBytes bounds one context upload and ContextMaxTotalBytes a
	// Work Item's context together (proposed, context bundles proof of
	// concept). Zero = DefaultContextMaxBytes and DefaultContextMaxTotalBytes.
	ContextMaxBytes      int64
	ContextMaxTotalBytes int64

	pipelineWork  sync.WaitGroup
	pipelineOnce  sync.Once
	pipelineSlots chan struct{}
}

// ReviewSettler is implemented by shiftengine.ReviewWatch.
type ReviewSettler interface {
	HandleForgeEvent(ctx context.Context, forge string, ev provider.ForgeEvent) error
}

// RoleCaps is the slice of the team-plan config the claim path needs.
type RoleCaps interface {
	RoleCap(team, role string) float64
}

// PlannerRoles is implemented by plan.Plans. A RoleCaps that also
// implements it marks planner Roles in the claim response (ADR-0031).
type PlannerRoles interface {
	IsPlanner(team, role string) bool
}

func (s *Server) createdWorkPolicy(team string) followup.Policy {
	if p, ok := s.CreatedWork[team]; ok {
		return p
	}
	return followup.Default()
}

func (s *Server) knownTeam(team string) bool {
	if _, ok := s.OperatorConfig.Teams[team]; ok {
		return true
	}
	_, ok := s.CreatedWork[team]
	return ok
}

// TeamCaps is the slice of the team roster the claim path needs to enforce a
// team's concurrency cap. MaxRunning returns 0 for an unlimited team.
type TeamCaps interface {
	MaxRunning(team string) int
}

func (s *Server) maxRunning(team string) int {
	if s.TeamCaps == nil {
		return 0
	}
	return s.TeamCaps.MaxRunning(team)
}

// ShiftEngine is implemented by pkg/shiftengine. An interface here rather
// than the concrete type, so httpapi tests that never touch Shifts do not
// need an engine, and the engine package stays free to import httpapi types
// if it ever must.
type ShiftEngine interface {
	EnsureShift(ctx context.Context, workItemID int64, item work.WorkItem) error
	EvaluateItem(ctx context.Context, workItemID int64) error
	// RefreshUsageReport re-renders the Shift's usage report after a late
	// settlement. The settlement sweep calls it after a successful Settle and
	// only then; a Shift with no pull request is a silent no-op.
	RefreshUsageReport(ctx context.Context, shiftID int64) error
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("GET /readyz", s.handleReady)
	mux.HandleFunc("GET /metrics", s.handleMetrics)
	mux.HandleFunc("POST /webhooks/tracker/{provider}", s.handleTrackerWebhook)
	mux.HandleFunc("POST /webhooks/forge/{provider}", s.handleForgeWebhook)
	mux.HandleFunc("POST /api/v1/claim", s.handleClaim)
	mux.HandleFunc("POST /api/v1/runs/{token}/renew", s.handleRenew)
	mux.HandleFunc("POST /api/v1/runs/{token}/checkpoint", s.handleCheckpoint)
	mux.HandleFunc("POST /api/v1/runs/{token}/outcome", s.handleOutcome)
	mux.HandleFunc("GET /api/v1/runs/{token}/context/{id}", s.handleRunContext)
	mux.HandleFunc("GET /api/v1/queue/{team}", s.handleQueue)
	mux.HandleFunc("/api/v1/deploys", s.handleDeploy)
	mux.Handle("/api/v1/operator/", s.operatorHandler())
	s.RegisterLLMControl(mux)
	return s.WorkerHandler(mux)
}

func (s *Server) handleReady(w http.ResponseWriter, r *http.Request) {
	if err := s.Store.Ping(r.Context()); err != nil {
		http.Error(w, "db unreachable", http.StatusServiceUnavailable)
		return
	}
	if s.TrackerWebhooks == nil {
		w.WriteHeader(http.StatusOK)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ready", "vikunjaWebhooks": s.TrackerWebhooks.report()})
}

// handleTrackerWebhook verifies + parses via the provider, then fast-acks:
// assigned events queue work, unassigned events withdraw it, everything else
// is dropped after normalization.
func (s *Server) handleTrackerWebhook(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("provider")
	tp, ok := s.Trackers[name]
	if !ok {
		http.Error(w, "unknown tracker provider", http.StatusNotFound)
		return
	}
	events, err := tp.ParseWebhook(r)
	if err != nil {
		s.Log.Warn("webhook rejected", "provider", name, "err", err)
		http.Error(w, "webhook rejected", http.StatusBadRequest)
		return
	}
	for _, ev := range events {
		if ev.Kind == provider.TrackerUpdated || ev.Kind == provider.TrackerClosed {
			s.observeGate(r.Context(), name, tp, ev)
			s.observeEpics(r.Context(), name, tp, ev.ExternalID)
		}
		if ev.Kind == provider.TrackerClosed {
			if err := s.trackerClosed(r.Context(), name, ev); err != nil {
				s.Log.Error("withdrawal failed", "provider", name, "external_id", ev.ExternalID, "err", err)
				http.Error(w, "withdrawal failed", http.StatusInternalServerError)
				return
			}
			continue
		}
		if ev.Kind == provider.TrackerUnassigned {
			if err := s.trackerUnassigned(r.Context(), name, ev); err != nil {
				s.Log.Error("withdrawal failed", "provider", name, "external_id", ev.ExternalID, "err", err)
				http.Error(w, "withdrawal failed", http.StatusInternalServerError)
				return
			}
			continue
		}
		if ev.Kind != provider.TrackerAssigned {
			continue
		}
		item, labelsRead := s.mirror(r.Context(), tp, ev)
		s.pinTeam(&item)
		if refusal := s.resolveTarget(r.Context(), &item, labelsRead, ev); refusal != nil {
			if err := s.refuseRoute(r.Context(), tp, item, refusal); err != nil {
				s.Log.Error("route refusal audit failed", "provider", name, "external_id", ev.ExternalID, "err", err)
				http.Error(w, "ingest failed", http.StatusInternalServerError)
				return
			}
			continue
		}
		id, state, err := s.Store.IngestAssigned(r.Context(), item)
		if err != nil {
			s.Log.Error("ingest failed", "provider", name, "external_id", ev.ExternalID, "err", err)
			http.Error(w, "ingest failed", http.StatusInternalServerError)
			return
		}
		s.observeEpics(r.Context(), name, tp, ev.ExternalID)
		// Log the actual post-upsert state: a re-assignment of a live
		// (queued/leased) item refreshes the mirror without re-queuing (VIK-588).
		if state == work.StateQueued {
			s.Log.Info("work item queued", "id", id, "team", item.Team, "title", item.Title)
			// The Shift fast path. Crash between the commit above and this call
			// leaves a queued item with no Shift — the sweeper's repair case.
			if s.Engine != nil {
				if err := s.Engine.EnsureShift(r.Context(), id, item); err != nil {
					s.Log.Error("shift open failed; sweeper will repair", "id", id, "err", err)
				}
			}
		} else {
			s.Log.Info("work item refreshed, not queued", "id", id, "state", string(state), "team", item.Team, "title", item.Title)
		}
		s.observeGate(r.Context(), name, tp, ev)
	}
	w.WriteHeader(http.StatusAccepted)
}

func (s *Server) handleForgeWebhook(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("provider")
	fp, ok := s.Forges[name]
	if !ok {
		// Not an error worth alarming on: a forge may be configured to notify
		// an instance that does not host that provider.
		s.Log.Warn("forge webhook for an unknown provider", "provider", name)
		http.Error(w, "unknown forge provider", http.StatusNotFound)
		return
	}

	events, err := fp.ParseWebhook(r)
	if err != nil {
		s.Log.Warn("forge webhook rejected", "provider", name, "err", err)
		http.Error(w, "webhook rejected", http.StatusBadRequest)
		return
	}

	delivery := r.Header.Get("X-Forgejo-Delivery")
	if delivery == "" {
		delivery = r.Header.Get("X-Gitea-Delivery")
	}
	seen, err := s.Store.SeenDelivery(r.Context(), name, delivery)
	if err != nil {
		s.Log.Error("forge webhook dedup failed", "provider", name, "err", err)
		http.Error(w, "dedup failed", http.StatusInternalServerError)
		return
	}
	if seen {
		s.Log.Info("forge webhook redelivered; ignoring", "provider", name, "delivery", delivery)
		w.WriteHeader(http.StatusAccepted)
		return
	}

	for _, ev := range events {
		if err := s.Store.AuditForgeEvent(r.Context(), name, store.ForgeEventAudit{
			Kind: string(ev.Kind), Repo: ev.Repo, Branch: ev.Branch, PR: ev.PR,
			Actor: ev.Actor, Review: string(ev.Review), HeadSHA: ev.PullRequest.HeadSHA,
			MergeCommitSHA: ev.PullRequest.MergeCommitSHA, MergedBy: ev.PullRequest.MergedBy,
			MergedAt: ev.PullRequest.MergedAt,
		}); err != nil {
			s.Log.Error("forge event audit failed", "provider", name, "kind", ev.Kind, "err", err)
			continue
		}
		s.Log.Info("forge event recorded", "provider", name, "kind", ev.Kind,
			"repo", ev.Repo, "pr", ev.PR, "branch", ev.Branch)
		s.recordPullRequestFacts(r.Context(), fp, ev)
		if s.Reviews != nil && (ev.Kind == provider.ForgePRMerged || ev.Kind == provider.ForgePRClosed) {
			if err := s.Reviews.HandleForgeEvent(r.Context(), name, ev); err != nil {
				s.Log.Error("pull request settle failed; reconcile will retry", "provider", name,
					"repo", ev.Repo, "pr", ev.PR, "err", err)
			}
		}
		s.actOnForgeEvent(r.Context(), name, ev)
	}
	w.WriteHeader(http.StatusAccepted)
}

// mirror applies the thin-payload rule: prefer the provider's authoritative
// read, fall back to the webhook snapshot. The flag reports whether the
// authoritative read succeeded, which is the only source of labels.
func (s *Server) mirror(ctx context.Context, tp provider.TrackerProvider, ev provider.TrackerEvent) (work.WorkItem, bool) {
	if item, err := tp.FetchItem(ctx, ev.ExternalID); err == nil {
		item.Team = ev.Team
		if item.ExternalScope == "" {
			item.ExternalScope = ev.Scope.ID
		}
		return item, true
	}
	if ev.Item != nil {
		return *ev.Item, false
	}
	return work.WorkItem{Provider: tp.Name(), ExternalID: ev.ExternalID, Team: ev.Team,
		ExternalScope: ev.Scope.ID, Origin: work.OriginAssignment}, false
}

// pinTeam applies a container's pinned team once the scope is known — which
// for a thin webhook (clickup) is only after mirror has fetched the item, so
// this cannot live in the provider. It runs before resolveTarget so the
// team-qualified routing rules see the team the work will actually run as.
func (s *Server) pinTeam(item *work.WorkItem) {
	if item.ExternalScope == "" {
		return
	}
	pinned, ok := s.ScopeTeams[item.ExternalScope]
	if !ok || pinned == "" || pinned == item.Team {
		return
	}
	s.Log.Info("team pinned by tracker container", "external_id", item.ExternalID,
		"scope", item.ExternalScope, "assignee_team", item.Team, "team", pinned)
	item.Team = pinned
}

func (s *Server) resolveTarget(ctx context.Context, item *work.WorkItem, labelsRead bool, ev provider.TrackerEvent) *target.Refusal {
	if s.Targets == nil {
		return nil
	}
	if item.ExternalScope == "" {
		s.Log.Warn("tracker event carries no scope; target unresolved",
			"provider", item.Provider, "external_id", ev.ExternalID, "team", item.Team)
	}
	route, ok, err := s.Targets.Route(target.Request{Scope: item.ExternalScope, Team: item.Team, Labels: item.Labels, LabelsRead: labelsRead})
	var refusal *target.Refusal
	if errors.As(err, &refusal) {
		return refusal
	}
	if !ok {
		if item.ExternalScope != "" {
			s.Log.Warn("no target mapping for tracker scope; worker will use its env repo",
				"scope", item.ExternalScope, "team", item.Team, "external_id", ev.ExternalID)
		}
		return nil
	}
	if route.Key != "" && s.Readiness != nil {
		switch v := s.Readiness.Admit(ctx, route.Key); {
		case v.Unknown:
			s.Log.Warn("target readiness unknown at ingest; the claim checks it again",
				"external_id", ev.ExternalID, "target", route.Key, "reason", v.Reason)
		case !v.Ready:
			return &target.Refusal{Code: target.RefusalTargetNotReady, Reason: fmt.Sprintf("target %q is not ready: %s", route.Key, v.Reason), Allowed: []string{}}
		}
	}
	item.Target = &route.Target
	item.RouteRule = route.Rule
	item.RouteHint = route.Hint
	s.Log.Info("target resolved", "external_id", ev.ExternalID, "scope", item.ExternalScope,
		"team", item.Team, "repo", route.Target.Owner+"/"+route.Target.Repo, "branch", route.Target.BaseBranch,
		"forge", route.Target.Forge, "rule", route.Rule, "hint", route.Hint, "registered_target", route.Key)
	return nil
}

func (s *Server) refuseRoute(ctx context.Context, tp provider.TrackerProvider, item work.WorkItem, refusal *target.Refusal) error {
	s.Log.Warn("tracker item refused by routing; nothing queued", "provider", item.Provider,
		"external_id", item.ExternalID, "scope", item.ExternalScope, "team", item.Team, "reason", refusal.Reason)
	if err := s.Store.RefuseRoute(ctx, item, refusal.Code, refusal.Reason, refusal.Allowed); err != nil {
		return err
	}
	if err := tp.Comment(ctx, item.ExternalID, routeRefusalComment(refusal)); err != nil {
		s.Log.Error("route refusal comment failed", "provider", item.Provider, "external_id", item.ExternalID, "err", err)
	}
	return nil
}

func routeRefusalComment(refusal *target.Refusal) string {
	return "<p>Ploeg did not queue this task: " + html.EscapeString(refusal.Reason) + ".</p>" +
		"<p>Nothing was sent to any repository. Give the task exactly one <code>" + target.HintPrefix +
		"&lt;target&gt;</code> label this board allows, or ask an operator to register the target, then assign it again.</p>"
}

type claimRequest struct {
	Team string `json:"team"`
	// Role selects which slot of a Round this worker claims. Empty = the
	// pre-Shift claim over queued Work Items.
	Role string `json:"role,omitempty"`
}

type claimResponse struct {
	RunToken string        `json:"runToken"`
	Deadline time.Time     `json:"deadline"`
	WorkItem work.WorkItem `json:"workItem"`
	// Shift fields; all absent on a pre-Shift claim, so an old worker sees
	// exactly today's body.
	Shift      int64             `json:"shift,omitempty"`
	Role       string            `json:"role,omitempty"`
	Round      int               `json:"round,omitempty"`
	Writes     bool              `json:"writes,omitempty"`
	Branch     string            `json:"branch,omitempty"`
	Authorized float64           `json:"authorized,omitempty"`
	Briefing   []harness.Finding `json:"briefing,omitempty"`
	// ForgeToken is a push credential minted for THIS run and revoked when it
	// settles (ADR-0013 tier 2). Empty = use the env credential. Never logged,
	// never audited, never in a Task Spec (R8).
	ForgeToken string `json:"forgeToken,omitempty"`
	// ForgeTokenPerRun says whether ForgeToken was actually minted for this
	// run, or is the shared token the Static broker hands back unchanged.
	// Both arrive in the same field, and the worker cannot tell them apart —
	// which made it log a per-run credential on deployments that have none.
	ForgeTokenPerRun bool `json:"forgeTokenPerRun,omitempty"`
	// Planner marks a Role the team's plan configures as a planner
	// (ADR-0031).
	Planner bool `json:"planner,omitempty"`
	// PreAuthor marks a reading Run with no writing Run before its Round in
	// the Shift: only such a Run may review the base branch when the Shift's
	// branch does not exist.
	PreAuthor bool `json:"preAuthor,omitempty"`
	// Context names the files people attached to the Work Item before this
	// claim (proposed, context bundles proof of concept); never the bytes.
	Context []harness.ContextRef `json:"context,omitempty"`
}

// handleClaim leases the next unit of work for a team. 204 = empty-handed
// worker, exit 0 (backlog #49).
//
// With a role, the claim is Shift-scoped: the oldest pending Run for that
// (team, role), with its budget authorized in the same transaction. Without
// one, it is the pre-Shift claim over queued Work Items, unchanged.
func (s *Server) handleClaim(w http.ResponseWriter, r *http.Request) {
	var req claimRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Team == "" {
		http.Error(w, "body must be {\"team\": \"...\", \"role\": \"...\"}", http.StatusBadRequest)
		return
	}
	if req.Role != "" {
		s.claimRole(w, r, req)
		return
	}
	// A role-less worker still belongs to a Shift when uniform dispatch
	// synthesized one — its Run carries the empty role. Try that first and
	// fall through to the pre-Shift claim when there is none, so the same
	// pod serves both worlds and the kill switch needs no chart change.
	run, err := s.Store.ClaimRoleWithin(r.Context(), req.Team, "", s.LeaseTTL, 0, s.maxRunning(req.Team))
	switch {
	case err == nil:
		s.respondClaimedRun(w, r, req, run)
		return
	case errors.Is(err, store.ErrBudgetExhausted):
		s.Log.Warn("claim refused: shift budget exhausted", "team", req.Team, "role", "")
		w.WriteHeader(http.StatusNoContent)
		return
	case !errors.Is(err, store.ErrNoWork):
		s.Log.Error("role-less shift claim failed", "team", req.Team, "err", err)
		http.Error(w, "claim failed", http.StatusInternalServerError)
		return
	}
	claimed, err := s.Store.ClaimWithin(r.Context(), req.Team, s.LeaseTTL, s.maxRunning(req.Team))
	if errors.Is(err, store.ErrNoWork) {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if err != nil {
		s.Log.Error("claim failed", "team", req.Team, "err", err)
		http.Error(w, "claim failed", http.StatusInternalServerError)
		return
	}
	if s.stopUnreadyTarget(r.Context(), req.Team, claimed.RunToken, claimed.Item) {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	s.Log.Info("lease acquired", "team", req.Team, "work_item", claimed.Item.ID, "deadline", claimed.Deadline)
	writeJSON(w, http.StatusOK, claimResponse{RunToken: claimed.RunToken, Deadline: claimed.Deadline, WorkItem: claimed.Item,
		Context: s.claimContext(r.Context(), claimed.RunToken)})
}

// claimRole serves a Shift-scoped claim, carrying everything the Run needs
// that it cannot derive: its place in the Shift, the branch ploegd derived,
// the budget ceiling its credential must be minted at, and the briefing of
// earlier Rounds' findings (ADR-0011 — the agent fetches nothing itself).
func (s *Server) claimRole(w http.ResponseWriter, r *http.Request, req claimRequest) {
	cap := 0.0
	if s.RoleCaps != nil {
		cap = s.RoleCaps.RoleCap(req.Team, req.Role)
	}
	run, err := s.Store.ClaimRoleWithin(r.Context(), req.Team, req.Role, s.LeaseTTL, cap, s.maxRunning(req.Team))
	switch {
	case errors.Is(err, store.ErrNoWork):
		w.WriteHeader(http.StatusNoContent)
		return
	case errors.Is(err, store.ErrBudgetExhausted):
		// Not an error the worker can act on: nothing is spawned, no key is
		// minted, no attempt is burned. The sweeper parks the item with a
		// reason naming the spend.
		s.Log.Warn("claim refused: shift budget exhausted", "team", req.Team, "role", req.Role)
		w.WriteHeader(http.StatusNoContent)
		return
	case err != nil:
		s.Log.Error("role claim failed", "team", req.Team, "role", req.Role, "err", err)
		http.Error(w, "claim failed", http.StatusInternalServerError)
		return
	}

	s.respondClaimedRun(w, r, req, run)
}

// respondClaimedRun builds the Shift-shaped claim response, briefing and all.
func (s *Server) respondClaimedRun(w http.ResponseWriter, r *http.Request, req claimRequest, run *store.ClaimedRun) {
	if s.stopUnreadyTarget(r.Context(), req.Team, run.RunToken, run.Item) {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	resp := claimResponse{
		RunToken: run.RunToken, Deadline: run.Deadline, WorkItem: run.Item,
		Shift: run.ShiftID, Role: run.Role, Round: run.Round,
		Writes: run.Writes, Branch: run.Branch, Authorized: run.Authorized,
		PreAuthor: run.PreAuthor,
	}
	if planners, ok := s.RoleCaps.(PlannerRoles); ok && !run.Writes {
		resp.Planner = planners.IsPlanner(req.Team, run.Role)
	}
	// Prior Rounds only: this Round's siblings are still running, and Runs in
	// one Round never observe each other (ADR-0010).
	reports, err := s.Store.RoundReports(r.Context(), run.ShiftID)
	if err != nil {
		s.Log.Error("briefing read failed; run proceeds without it", "shift", run.ShiftID, "err", err)
	}
	for _, rep := range reports {
		if rep.Round < run.Round && rep.Findings != "" {
			resp.Briefing = append(resp.Briefing, harness.Finding{
				Role: rep.Role, Round: rep.Round, Findings: rep.Findings,
			})
		}
	}
	resp.Context = s.claimContext(r.Context(), run.RunToken)
	if notes, err := s.Store.ShiftReviews(r.Context(), run.ShiftID, run.Round); err != nil {
		s.Log.Error("review briefing read failed; run proceeds without it", "shift", run.ShiftID, "err", err)
	} else {
		resp.Briefing = append(resp.Briefing, reviewBriefing(notes)...)
	}
	if brief, restarted, err := s.Store.ShiftRestartBrief(r.Context(), run.ShiftID); err != nil {
		s.Log.Error("restart briefing read failed; run proceeds without it", "shift", run.ShiftID, "err", err)
	} else if restarted {
		resp.Briefing = append([]harness.Finding{restartBriefing(brief)}, resp.Briefing...)
	}
	// Push rights are minted per writing Run, so holding the Lease and being
	// able to push are one fact rather than two that can disagree. A reader
	// gets nothing here — it has no Lease and no business pushing.
	if run.Writes && s.ForgeCreds != nil && run.Item.Target != nil {
		cred, ok := s.mintForgeCredential(r.Context(), req.Team, run)
		if !ok {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if cred.ID != "" {
			resp.ForgeToken = cred.Token
			resp.ForgeTokenPerRun = true
		}
	}

	s.Log.Info("run claimed", "team", req.Team, "role", run.Role, "shift", run.ShiftID,
		"round", run.Round, "writes", run.Writes, "authorized", run.Authorized,
		"briefing", len(resp.Briefing), "context", len(resp.Context), "deadline", run.Deadline)
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) stopUnreadyTarget(ctx context.Context, team, runToken string, item work.WorkItem) bool {
	if s.Readiness == nil || item.Target == nil {
		return false
	}
	key, registered := s.Readiness.KeyOf(*item.Target)
	if !registered {
		return false
	}
	v := s.Readiness.Check(ctx, key)
	if v.Ready {
		return false
	}
	report := store.Report(work.OutcomeStuck, "target not ready", fmt.Sprintf("target %q is not ready: %s", key, v.Reason), nil, nil, nil)
	if v.Unknown {
		infra := string(work.FailureInfraNode)
		report = store.Report(work.OutcomeFailed, "could not check target readiness: "+v.Reason, "", nil, nil, &infra)
	}
	s.Log.Warn("claim stopped by the target readiness gate; no run started", "team", team,
		"work_item", item.ID, "target", key, "unknown", v.Unknown, "reason", v.Reason)
	res, err := s.Store.ReportOutcome(ctx, runToken, report)
	if err != nil {
		s.Log.Error("releasing the run failed", "work_item", item.ID, "err", err)
		return true
	}
	if s.Engine != nil && res.ShiftID != nil {
		if err := s.Engine.EvaluateItem(ctx, res.WorkItemID); err != nil {
			s.Log.Error("shift evaluate failed; sweeper will repair", "shift", *res.ShiftID, "err", err)
		}
	}
	return true
}

func (s *Server) handleRenew(w http.ResponseWriter, r *http.Request) {
	deadline, err := s.Store.Renew(r.Context(), r.PathValue("token"), s.LeaseTTL)
	if err != nil {
		runError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deadline": deadline})
}

func (s *Server) handleCheckpoint(w http.ResponseWriter, r *http.Request) {
	var cp work.Checkpoint
	if err := json.NewDecoder(r.Body).Decode(&cp); err != nil || cp.Phase == "" {
		http.Error(w, "checkpoint requires a phase", http.StatusBadRequest)
		return
	}
	if err := s.Store.Checkpoint(r.Context(), r.PathValue("token"), cp); err != nil {
		if errors.Is(err, store.ErrCheckpointRefused) {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		runError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// validateOutcomeReport enforces the closed enums and R4 at the boundary,
// before anything reaches the store.
//
// docs/contracts/outcomereport.v1.schema.json declares these constraints and
// pkg/harness/contract_test.go pins the schema to the Go types — but neither
// runs at request time. A schema nothing checks on the wire is documentation,
// not a gate: an adapter that typos `infra_llm` had its value stored verbatim
// and silently defeated pkg/worker's classification, which defers to an
// adapter-set failureReason.
//
// Pure by design, so the rules are testable without a database.
func validateOutcomeReport(req harness.OutcomeReport) error {
	if !req.Outcome.Valid() {
		return errors.New("outcome must be a known outcome enum value")
	}
	if req.Outcome == work.OutcomeStuck && req.StuckReason == "" {
		return errors.New("stuck outcome requires a stuckReason (R4)")
	}
	if req.FailureReason != "" && !work.FailureReason(req.FailureReason).Valid() {
		return errors.New("failureReason must be a known failure-reason enum value")
	}
	// The verdict is the one field by which an agent influences what runs
	// next (ADR-0017), so the enum is closed at the boundary for the same
	// reason failureReason is: a value nothing checks is documentation.
	if !harness.ValidVerdict(req.Verdict) {
		return errors.New("verdict must be approve or request_changes")
	}
	if req.Verification != nil {
		if err := req.Verification.Validate(); err != nil {
			return err
		}
	}
	if req.Delivery != nil && !req.Delivery.Observed.Valid() {
		return errors.New("delivery.observed must be opened, updated, none or unknown")
	}
	return harness.ValidateCreatedWorkItems(req.CreatedWorkItems)
}

// handleOutcome accepts the full harness.OutcomeReport shape
// (docs/contracts/outcomereport.v1.schema.json). The historical 4-field
// body remains valid: checkpoint and usage are additive. A final checkpoint
// riding inline is written before the outcome ends the run.
func (s *Server) handleOutcome(w http.ResponseWriter, r *http.Request) {
	var req harness.OutcomeReport
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "outcome must be a known outcome enum value", http.StatusBadRequest)
		return
	}
	if err := validateOutcomeReport(req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var usage json.RawMessage
	if req.Usage != nil {
		usage, _ = json.Marshal(req.Usage)
	}
	var failureReason *string
	if req.FailureReason != "" {
		fr := req.FailureReason
		failureReason = &fr
	}
	report := store.Report(req.Outcome, req.Summary, req.StuckReason, req.Links, usage, failureReason).
		WithFindings(req.Findings).WithVerdict(req.Verdict).WithProblemAndSolution(req.Problem, req.Solution).
		WithVerification(req.Verification).WithDelivery(req.Delivery).
		WithCreatedWork(req.CreatedWorkItems, s.createdWorkPolicy, s.knownTeam)
	if req.Checkpoint != nil && req.Checkpoint.Phase != "" {
		replay, err := s.Store.IsOutcomeReplay(r.Context(), r.PathValue("token"), report)
		if err != nil {
			runError(w, err)
			return
		}
		if !replay {
			err := s.Store.Checkpoint(r.Context(), r.PathValue("token"), *req.Checkpoint)
			switch {
			case errors.Is(err, store.ErrCheckpointRefused):
				s.Log.Warn("inline checkpoint refused; the outcome is still recorded", "err", err)
			case err != nil:
				runError(w, err)
				return
			}
		}
	}
	res, err := s.Store.ReportOutcome(r.Context(), r.PathValue("token"), report)
	if err != nil {
		if errors.Is(err, store.ErrUnknownRun) {
			runError(w, err)
			return
		}
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if s.ForgeCreds != nil && res.ForgeTokenID != "" {
		revokeCtx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), forgeCleanupTimeout)
		if err := s.ForgeCreds.Revoke(revokeCtx, forgebroker.Credential{ID: res.ForgeTokenID}); err != nil {
			s.Log.Error("forge credential revoke failed; the forge sweep will retry", "err", err)
		}
		cancel()
	}
	s.Log.Info("outcome reported", "outcome", req.Outcome, "summary", req.Summary,
		"created_proposed", len(req.CreatedWorkItems), "created_accepted", len(res.Created))
	s.openCreatedShifts(r.Context(), res.Created)
	// The Shift fast path: this report may have completed a Round. Errors are
	// logged, never surfaced — the worker's report succeeded, and the sweeper
	// repairs a lost evaluation (R2).
	if s.Engine != nil && res.ShiftID != nil {
		if err := s.Engine.EvaluateItem(r.Context(), res.WorkItemID); err != nil {
			s.Log.Error("shift evaluate failed; sweeper will repair", "shift", *res.ShiftID, "err", err)
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) openCreatedShifts(ctx context.Context, created []store.CreatedWorkItem) {
	for _, c := range created {
		if c.State != work.StateQueued {
			continue
		}
		s.ensureShift(ctx, c.ID)
	}
}

func (s *Server) ensureShift(ctx context.Context, id int64) {
	if s.Engine == nil {
		return
	}
	item, err := s.Store.WorkItem(ctx, id)
	if err == nil {
		err = s.Engine.EnsureShift(ctx, id, item)
	}
	if err != nil {
		s.Log.Error("shift open failed; sweeper will repair", "id", id, "err", err)
	}
}

func (s *Server) handleQueue(w http.ResponseWriter, r *http.Request) {
	items, err := s.Store.QueueSnapshot(r.Context(), r.PathValue("team"))
	if err != nil {
		http.Error(w, "queue read failed", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func runError(w http.ResponseWriter, err error) {
	if errors.Is(err, store.ErrUnknownRun) {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	http.Error(w, err.Error(), http.StatusInternalServerError)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
