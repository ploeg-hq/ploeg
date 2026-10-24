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
	"strings"
	"sync"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/followup"
	"github.com/ploeg-hq/ploeg/pkg/forgebroker"
	"github.com/ploeg-hq/ploeg/pkg/gate"
	"github.com/ploeg-hq/ploeg/pkg/harness"
	"github.com/ploeg-hq/ploeg/pkg/provider"
	"github.com/ploeg-hq/ploeg/pkg/runidentity"
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
	// RunIdentity signs the Run identities a Team's declared plugins receive
	// and publishes its key set (ADR-0149). Nil = both routes answer 404.
	RunIdentity *runidentity.Signer
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
	// PullRequestReads queues a reading when a pull request a
	// registered target's policy covers is opened or pushed to (ADR-0109).
	// Nil = no target opted in; the events are recorded only.
	PullRequestReads PullRequestReader
	// ForgeCreds mints the per-run push credential a writing Run gets
	// (ADR-0013 tier 2). Nil = the worker keeps its env credential, which is
	// the pre-tier-2 behaviour.
	ForgeCreds forgebroker.Broker
	// ClaudeSessions enables Runs executed in a Team's self-hosted Claude
	// environment (ADR-0084). Nil = both of its routes answer 404.
	ClaudeSessions *ClaudeSessions
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
	// Connections answers GET /api/v1/operator/connections. Nil answers an
	// empty view.
	Connections *Connections
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
	// FastChecks is each repository's fast check, keyed by FastCheckRoute
	// (ADR-0146). A writing Run's claim carries its repository's.
	FastChecks map[string][]string
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
	// AskAllowanceUSD is the limit a Team's monthly Ask Allowance opens with
	// and AskBudgetUSD the per-Ask Budget (ADR-0082). Zero means
	// DefaultAskAllowanceUSD and DefaultAskBudgetUSD.
	AskAllowanceUSD float64
	AskBudgetUSD    float64
	// ChatAllowanceUSD is the limit a Team's monthly Chat Allowance opens
	// with and ChatBudgetUSD the per-chat Budget (ADR-0128). Zero means
	// DefaultChatAllowanceUSD and DefaultChatBudgetUSD.
	ChatAllowanceUSD float64
	ChatBudgetUSD    float64
	// DisableRunTranscripts refuses Run transcripts (ADR-0090): the
	// ingestion route answers 404 and workers stop sending. Transcripts
	// already stored stay readable.
	DisableRunTranscripts bool
	// Notes is the Tenant's notes graph (ADR-0088, proposed). Nil writes
	// and serves no Work Item Notes, so a Run's learnings stay where the
	// worker keeps them.
	Notes NoteGraph
	// Knowledge is the Tenant's reviewed knowledge graph (ADR-0087,
	// proposed). Nil serves Runs no graph concepts.
	Knowledge ConceptGraph
	// Concepts writes accepted Client and Tenant promotions to the reviewed
	// knowledge graph as the Tenant's review actor (ADR-0122). Nil leaves
	// them accepted and waiting.
	Concepts ConceptWriter
	// Version is this ploegd's version, recorded on each Run it claims or
	// admits (ADR-0091). Empty records it as unknown.
	Version string
	// TrackerPeople maps each tracker's lowercased usernames to Ploeg's
	// person ids (config trackers.<kind>.people). The client-carried Runs of
	// a Work Item a mapped user handed to Ploeg act for that person
	// (ADR-0112); an unmapped user's wait until configuration maps them.
	TrackerPeople map[string]map[string]string

	pipelineWork  sync.WaitGroup
	pipelineOnce  sync.Once
	pipelineSlots chan struct{}
}

// ReviewSettler is implemented by shiftengine.ReviewWatch.
type ReviewSettler interface {
	HandleForgeEvent(ctx context.Context, forge string, ev provider.ForgeEvent) error
}

// PullRequestReader is implemented by shiftengine.PullRequestReadWatch
// (ADR-0109). Consider decides whether a pull request that was opened or
// pushed to is one a Team reads, and queues the Work Item when it is.
type PullRequestReader interface {
	Consider(ctx context.Context, forge, repo string, pr int, author string)
}

// RoleCaps is the slice of the team-plan config the claim path needs.
type RoleCaps interface {
	RoleCap(team, role string) float64
}

// RoundLimits is implemented by plan.Plans. A RoleCaps that also implements
// it tells each claimed Run the most Rounds its Shift can run (ADR-0133).
type RoundLimits interface {
	MaxRounds(team string) int
}

// PlannerRoles is implemented by plan.Plans. A RoleCaps that also
// implements it marks planner Roles in the claim response (ADR-0031).
type PlannerRoles interface {
	IsPlanner(team, role string) bool
}

func (s *Server) maxRounds(team string) int {
	if rounds, ok := s.RoleCaps.(RoundLimits); ok {
		return rounds.MaxRounds(team)
	}
	return 0
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
	// Dispatches reports whether a queued Work Item of the team gets a Shift.
	Dispatches(team string) bool
	// PlanRoles lists the distinct Roles of the team's plan (ADR-0098).
	PlanRoles(team string) ([]string, bool)
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
	mux.HandleFunc("GET /api/v1/runs/{token}/knowledge", s.handleRunKnowledge)
	mux.HandleFunc("POST /api/v1/runs/{token}/notes", s.handleRunNotes)
	mux.HandleFunc("POST /api/v1/runs/{token}/board", s.handleRunBoard)
	mux.HandleFunc("POST /api/v1/runs/{token}/claude-session", s.handleRecordClaudeSession)
	mux.HandleFunc("GET /api/v1/runs/{token}/claude-session", s.handleClaudeSessionState)
	mux.HandleFunc("POST /api/v1/runs/{token}/transcript", s.handleRunTranscript)
	mux.HandleFunc("POST /api/v1/runs/{token}/forge-credential", s.handleRefreshForgeCredential)
	mux.HandleFunc("POST /api/v1/claude-sessions/forge-credential", s.handleClaudeForgeCredential)
	mux.HandleFunc("POST /api/v1/claude-sessions/report", s.handleClaudeSessionReport)
	mux.HandleFunc("GET /api/v1/queue/{team}", s.handleQueue)
	mux.HandleFunc("/api/v1/deploys", s.handleDeploy)
	mux.Handle("/api/v1/operator/", s.operatorHandler())
	s.RegisterLLMControl(mux)
	s.registerRunIdentity(mux)
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
	var delivery string
	if identifier, ok := tp.(provider.DeliveryIdentifier); ok {
		delivery = identifier.DeliveryID(r)
	}
	deliveryKey := "tracker:" + name
	recorded, err := s.Store.DeliveryRecorded(r.Context(), deliveryKey, delivery)
	if err != nil {
		s.Log.Error("tracker webhook dedup failed", "provider", name, "err", err)
		http.Error(w, "dedup failed", http.StatusInternalServerError)
		return
	}
	if recorded {
		s.Log.Info("tracker webhook redelivered; its effects are already recorded", "provider", name, "delivery", delivery)
		w.WriteHeader(http.StatusAccepted)
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
		if ev.Actor != "" {
			if err := s.Store.RecordHandedBy(r.Context(), id, ev.Actor, s.TrackerPeople[name][strings.ToLower(ev.Actor)]); err != nil {
				s.Log.Error("recording who handed the work item failed", "provider", name, "external_id", ev.ExternalID, "err", err)
			}
		}
		// Log the actual post-upsert state: a re-assignment of a live
		// (queued/active) item refreshes the mirror without re-queuing (VIK-588).
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
	if err := s.Store.RecordDelivery(r.Context(), deliveryKey, delivery); err != nil {
		s.Log.Error("tracker webhook delivery not recorded; a redelivery repeats its idempotent effects", "provider", name, "delivery", delivery, "err", err)
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

	delivery := forgeDeliveryID(r.Header)
	if identifier, ok := fp.(provider.DeliveryIdentifier); ok {
		delivery = identifier.DeliveryID(r)
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
	// Harness and HarnessProfile name the worker's harness and its adapter
	// profile. ploegd records them on the claimed Run (ADR-0091); absent
	// leaves them unknown.
	Harness        string `json:"harness,omitempty"`
	HarnessProfile string `json:"harnessProfile,omitempty"`
	// HarnessVersion is the version the worker read from its Harness binary
	// at startup; ploegd records it once on the claimed Run (ADR-0097).
	HarnessVersion string `json:"harnessVersion,omitempty"`
	// Yields says the worker's Harness can stop at a safe point for a person
	// (ADR-0142); ploegd records it on the claimed Run. Absent is false.
	Yields bool `json:"yields,omitempty"`
}

func (r claimRequest) harnessKey() string {
	if r.Harness == "" {
		return ""
	}
	return store.HarnessKey(r.Harness, r.HarnessProfile)
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
	MaxRounds  int               `json:"maxRounds,omitempty"`
	// ReviewFindings are the findings of the Shift's last review Round by
	// id, each with the writer's answer, and Questions the questions that
	// reach this Run (ADR-0121). Both absent when there are none.
	ReviewFindings []harness.BriefedFinding  `json:"reviewFindings,omitempty"`
	Questions      []harness.BriefedQuestion `json:"questions,omitempty"`
	// ActiveWork is the other active work in the Run's repository, within
	// the active work section's budget (ADR-0123, ADR-0124).
	ActiveWork string `json:"activeWork,omitempty"`
	// ForgeToken is a push credential minted for THIS run and revoked when it
	// settles (ADR-0013 tier 2). Empty = use the env credential. Never logged,
	// never audited, never in a Task Spec (R8).
	ForgeToken string `json:"forgeToken,omitempty"`
	// ForgeTokenPerRun says whether ForgeToken was actually minted for this
	// run, or is the shared token the Static broker hands back unchanged.
	// Both arrive in the same field, and the worker cannot tell them apart —
	// which made it log a per-run credential on deployments that have none.
	ForgeTokenPerRun bool `json:"forgeTokenPerRun,omitempty"`
	// ForgeTokenExpiresAt is when the forge ends ForgeToken by itself, absent
	// for a token that lives until it is revoked. The worker refreshes an
	// expiring token through POST /api/v1/runs/{token}/forge-credential.
	ForgeTokenExpiresAt *time.Time `json:"forgeTokenExpiresAt,omitempty"`
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
	// Model is the model resolved for the Run (ADR-0103); the worker calls it
	// instead of its configured one. Absent when the Run stores none.
	Model string `json:"model,omitempty"`
	// Board is the half of the Run's version vector ploegd recorded at the
	// claim (ADR-0124); absent when it could not be recorded.
	Board *work.BoardVersions `json:"board,omitempty"`
	// Handback is set on a writing Run of the Round a person's hand back
	// reopened (ADR-0142): where to continue and the commit the taken-over
	// Run yielded at.
	Handback  *work.Handback `json:"handback,omitempty"`
	FastCheck []string       `json:"fastCheck,omitempty"`
}

// FastCheckRoute keys Server.FastChecks: a repository "owner/name" on a forge
// instance, empty for the deployment's default forge, compared without case.
func FastCheckRoute(forge, repo string) string {
	return strings.ToLower(strings.TrimSpace(forge)) + " " + strings.ToLower(strings.TrimSpace(repo))
}

func (s *Server) fastCheckFor(item work.WorkItem, writes bool) []string {
	if !writes || item.Target == nil {
		return nil
	}
	return s.FastChecks[FastCheckRoute(item.Target.Forge, item.Target.Owner+"/"+item.Target.Repo)]
}

// handleClaim leases the next unit of work for a team. 204 = empty-handed
// worker, exit 0 (backlog #49).
//
// With a role, the claim is Shift-scoped: the oldest pending Run for that
// (team, role), with its budget authorized in the same transaction. Without
// one, it is the pre-Shift claim over queued Work Items, unchanged.
func (s *Server) handleClaim(w http.ResponseWriter, r *http.Request) {
	var req claimRequest
	err := decodeBounded(w, r, claimBodyLimit, &req)
	if refuseOversizedBody(w, err) {
		return
	}
	if err != nil || req.Team == "" {
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
	run, err := s.Store.ClaimRoleFor(r.Context(), req.Team, "", req.harnessKey(), s.LeaseTTL, 0, s.maxRunning(req.Team))
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
	s.recordRunConfig(r.Context(), claimed.RunToken, req.Team, "", true, runHarness{Name: req.Harness, Profile: req.HarnessProfile, Version: req.HarnessVersion})
	s.Log.Info("lease acquired", "team", req.Team, "work_item", claimed.Item.ID, "deadline", claimed.Deadline)
	writeJSON(w, http.StatusOK, claimResponse{RunToken: claimed.RunToken, Deadline: claimed.Deadline, WorkItem: claimed.Item,
		Context: s.claimContext(r.Context(), claimed.RunToken), Board: s.recordBoardClaim(r.Context(), claimed.RunToken),
		Briefing:  s.consumeOperatorNotes(r.Context(), claimed.Item.ID, claimed.RunToken, 0),
		FastCheck: s.fastCheckFor(claimed.Item, true)})
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
	run, err := s.Store.ClaimRoleFor(r.Context(), req.Team, req.Role, req.harnessKey(), s.LeaseTTL, cap, s.maxRunning(req.Team))
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
	s.recordRunConfig(r.Context(), run.RunToken, req.Team, run.Role, true, runHarness{Name: req.Harness, Profile: req.HarnessProfile, Version: req.HarnessVersion, Model: run.Model})
	s.recordRunYields(r.Context(), run.RunToken, req.Yields)
	resp := claimResponse{
		RunToken: run.RunToken, Deadline: run.Deadline, WorkItem: run.Item,
		Shift: run.ShiftID, Role: run.Role, Round: run.Round,
		Writes: run.Writes, Branch: run.Branch, Authorized: run.Authorized,
		PreAuthor: run.PreAuthor, Model: run.Model,
	}
	if planners, ok := s.RoleCaps.(PlannerRoles); ok && !run.Writes {
		resp.Planner = planners.IsPlanner(req.Team, run.Role)
	}
	resp.MaxRounds = s.maxRounds(req.Team)
	resp.Board = s.recordBoardClaim(r.Context(), run.RunToken)
	resp.Briefing, resp.ReviewFindings, resp.Questions = s.runBriefing(r.Context(), run)
	resp.Context = s.claimContext(r.Context(), run.RunToken)
	resp.ActiveWork = s.activeWork(r.Context(), run.Item.ID)
	if run.Writes {
		resp.Handback = s.claimHandback(r.Context(), run.ShiftID, run.Round)
	}
	resp.FastCheck = s.fastCheckFor(run.Item, run.Writes)
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
			if !cred.ExpiresAt.IsZero() {
				expires := cred.ExpiresAt.UTC()
				resp.ForgeTokenExpiresAt = &expires
			}
		}
	}

	if run.Role != "ask" {
		resp.Briefing = append(resp.Briefing, s.consumeOperatorNotes(r.Context(), run.Item.ID, run.RunToken, run.Round)...)
	}

	s.Log.Info("run claimed", "team", req.Team, "role", run.Role, "shift", run.ShiftID,
		"round", run.Round, "writes", run.Writes, "authorized", run.Authorized,
		"briefing", len(resp.Briefing), "review_findings", len(resp.ReviewFindings), "questions", len(resp.Questions),
		"context", len(resp.Context), "deadline", run.Deadline)
	writeJSON(w, http.StatusOK, resp)
}

// runBriefing is what a claimed Run is briefed with: the board briefing of
// earlier Rounds, reviews, a restart and catch-ups, and the last review's
// findings by id with the questions that reach the Run. A read that fails is
// logged and the Run proceeds without it.
func (s *Server) runBriefing(ctx context.Context, run *store.ClaimedRun) ([]harness.Finding, []harness.BriefedFinding, []harness.BriefedQuestion) {
	var in store.ReplayInputs
	reports, err := s.Store.RoundReports(ctx, run.ShiftID)
	if err != nil {
		s.Log.Error("briefing read failed; run proceeds without it", "shift", run.ShiftID, "err", err)
	}
	in.Reports = reports
	findings, questions, err := s.Store.FindingsBrief(ctx, run.ShiftID, run.Round, run.Writes)
	if err != nil {
		s.Log.Error("findings briefing read failed; run proceeds without it", "shift", run.ShiftID, "err", err)
	}
	if notes, err := s.Store.ShiftReviews(ctx, run.ShiftID, run.Round); err != nil {
		s.Log.Error("review briefing read failed; run proceeds without it", "shift", run.ShiftID, "err", err)
	} else {
		in.Reviews = notes
	}
	if brief, restarted, err := s.Store.ShiftRestartBrief(ctx, run.ShiftID); err != nil {
		s.Log.Error("restart briefing read failed; run proceeds without it", "shift", run.ShiftID, "err", err)
	} else if restarted {
		in.Restart = &brief
	}
	if catchUps, err := s.Store.PriorRoundCatchUps(ctx, run.ShiftID, run.Round, time.Time{}); err != nil {
		s.Log.Error("board catch-up briefing read failed; run proceeds without it", "shift", run.ShiftID, "err", err)
	} else {
		in.CatchUps = catchUps
	}
	briefing := shiftBriefing(in, run.Round)
	if run.Writes {
		if failures, err := s.Store.CIFixBriefing(ctx, run.ShiftID, run.Round); err != nil {
			s.Log.Error("forge CI briefing read failed; run proceeds without it", "shift", run.ShiftID, "err", err)
		} else if len(failures) > 0 {
			briefing = append(briefing, forgeCIBriefing(failures, run.Round))
		}
	}
	return briefing, findings, questions
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
	token := r.PathValue("token")
	deadline, err := s.Store.Renew(r.Context(), token, s.LeaseTTL)
	if err != nil {
		runError(w, err)
		return
	}
	body := map[string]any{"deadline": deadline}
	if s.yieldRequested(r.Context(), token) {
		body["yield"] = true
	}
	writeJSON(w, http.StatusOK, body)
}

func (s *Server) handleCheckpoint(w http.ResponseWriter, r *http.Request) {
	var cp work.Checkpoint
	err := decodeBounded(w, r, checkpointBodyLimit, &cp)
	if refuseOversizedBody(w, err) {
		return
	}
	if err != nil || cp.Phase == "" {
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
	if err := store.ValidateYieldReport(req.Yield); err != nil {
		return err
	}
	if err := harness.ValidateBlackboard(req); err != nil {
		return err
	}
	return harness.ValidateCreatedWorkItems(req.CreatedWorkItems)
}

// handleOutcome accepts the full harness.OutcomeReport shape
// (docs/contracts/outcomereport.v1.schema.json). The historical 4-field
// body remains valid: checkpoint and usage are additive. A final checkpoint
// riding inline is written before the outcome ends the run.
func (s *Server) handleOutcome(w http.ResponseWriter, r *http.Request) {
	var req harness.OutcomeReport
	if err := decodeBounded(w, r, outcomeBodyLimit, &req); err != nil {
		if refuseOversizedBody(w, err) {
			return
		}
		http.Error(w, "outcome must be a known outcome enum value", http.StatusBadRequest)
		return
	}
	if err := validateOutcomeReport(req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	err := s.applyOutcome(r.Context(), r.PathValue("token"), req)
	switch {
	case err == nil:
		w.WriteHeader(http.StatusNoContent)
	case errors.Is(err, store.ErrUnknownRun):
		runError(w, err)
	case errors.Is(err, errOutcomeStore):
		runError(w, errors.Unwrap(err))
	default:
		http.Error(w, err.Error(), http.StatusBadRequest)
	}
}

// errOutcomeStore wraps a store failure applyOutcome hit before the outcome,
// which the route answers as a server error rather than a refusal.
var errOutcomeStore = errors.New("outcome store failure")

// applyOutcome records a validated Outcome Report for the Run: its inline
// checkpoint, the outcome, the push credential's revocation, the created
// Work Items and the Shift's fast path. A worker's outcome route and the
// settlement of a client-carried Run from its runner's report share it.
func (s *Server) applyOutcome(ctx context.Context, token string, req harness.OutcomeReport) error {
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
		WithBlackboard(req.FindingItems, req.FindingAnswers, req.Questions, req.QuestionAnswers).
		WithCreatedWork(req.CreatedWorkItems, s.createdWorkPolicy, s.knownTeam)
	if s.Notes != nil {
		report = report.WithNotes(keptLearnings(req.Learnings))
	}
	if req.Checkpoint != nil && req.Checkpoint.Phase != "" {
		replay, err := s.Store.IsOutcomeReplay(ctx, token, report)
		if err != nil {
			if errors.Is(err, store.ErrUnknownRun) {
				return err
			}
			return fmt.Errorf("%w: %w", errOutcomeStore, err)
		}
		if !replay {
			err := s.Store.Checkpoint(ctx, token, *req.Checkpoint)
			switch {
			case errors.Is(err, store.ErrCheckpointRefused):
				s.Log.Warn("inline checkpoint refused; the outcome is still recorded", "err", err)
			case errors.Is(err, store.ErrUnknownRun):
				return err
			case err != nil:
				return fmt.Errorf("%w: %w", errOutcomeStore, err)
			}
		}
	}
	if req.Outcome == work.OutcomeYielded {
		if err := s.Store.RecordYield(ctx, token, req.Yield); err != nil {
			switch {
			case errors.Is(err, store.ErrUnknownRun):
				return err
			case errors.Is(err, store.ErrYieldNotRequested):
				return errors.New("a Run reports yielded only after a person asked it to yield")
			default:
				return fmt.Errorf("%w: %w", errOutcomeStore, err)
			}
		}
	}
	res, err := s.Store.ReportOutcome(ctx, token, report)
	if err != nil {
		return err
	}
	if s.ForgeCreds != nil && res.ForgeTokenID != "" {
		revokeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), forgeCleanupTimeout)
		if err := s.ForgeCreds.Revoke(revokeCtx, forgebroker.Credential{ID: res.ForgeTokenID}); err != nil {
			s.Log.Error("forge credential revoke failed; the forge sweep will retry", "err", err)
		}
		cancel()
	}
	s.recordReportedHarness(ctx, token, req.Harness)
	s.Log.Info("outcome reported", "outcome", req.Outcome, "summary", req.Summary,
		"created_proposed", len(req.CreatedWorkItems), "created_accepted", len(res.Created))
	s.openCreatedShifts(ctx, res.Created)
	// The Shift fast path: this report may have completed a Round. Errors are
	// logged, never surfaced — the worker's report succeeded, and the sweeper
	// repairs a lost evaluation (R2).
	if s.Engine != nil && res.ShiftID != nil {
		if err := s.Engine.EvaluateItem(ctx, res.WorkItemID); err != nil {
			s.Log.Error("shift evaluate failed; sweeper will repair", "shift", *res.ShiftID, "err", err)
		}
	}
	return nil
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
