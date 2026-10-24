// Package work defines Ploeg's core semantics: work items, leases,
// checkpoints, and outcomes. See docs/design.md §3.
package work

import "time"

// State is the lifecycle position of a WorkItem inside Ploeg.
// The tracker item it mirrors remains the source of truth for content.
type State string

const (
	StateIngested State = "ingested"
	// StateProposed holds a Work Item a Run created until a person approves
	// or rejects it. It is never claimable.
	StateProposed State = "proposed"
	StateQueued   State = "queued"
	// StateActive means a Team is working the Work Item: it has a live Shift
	// or, on the single-writer path, a Lease (ADR-0093).
	StateActive         State = "active"
	StateNeedsHuman     State = "needs_human"
	StateAwaitingReview State = "awaiting_review"
	StateStale          State = "stale"
	StateDone           State = "done"
	// StateWithdrawn means a person took the mandate back: the tracker item
	// was unassigned or an operator cancelled it. Only a new assignment
	// re-queues it.
	StateWithdrawn State = "withdrawn"
)

// RetiredStateLeased is the name StateActive had before ADR-0093. Ploeg no
// longer writes or emits it; ParseState still accepts it from requests until
// the deprecation window closes.
const RetiredStateLeased = "leased"

// States lists every Work Item state in lifecycle order.
var States = []State{StateIngested, StateProposed, StateQueued, StateActive, StateNeedsHuman,
	StateAwaitingReview, StateStale, StateDone, StateWithdrawn}

// ParseState returns the Work Item state a request names. It accepts the
// retired "leased" as StateActive (ADR-0093).
func ParseState(s string) (State, bool) {
	if s == RetiredStateLeased {
		return StateActive, true
	}
	for _, state := range States {
		if string(state) == s {
			return state, true
		}
	}
	return "", false
}

// CostStatus says how far a cost has settled. It is the one value set every
// cost Ploeg reports draws from (ADR-0093); a place that cannot reach a value
// documents the subset it uses.
type CostStatus string

const (
	// CostObserved is spend seen so far that can still move: the Run or Ask
	// is live, or its account has not settled.
	CostObserved CostStatus = "observed"
	// CostProvisional is a cost settled once and still inside its correction
	// window, so a late gateway charge may raise it.
	CostProvisional CostStatus = "provisional"
	// CostSettled is final.
	CostSettled CostStatus = "settled"
	// CostUnknown means Ploeg has no trustworthy figure, which is not zero.
	CostUnknown CostStatus = "unknown"
)

// CostStatuses lists every Cost Status from least to most settled, unknown last.
var CostStatuses = []CostStatus{CostObserved, CostProvisional, CostSettled, CostUnknown}

// Origin records whether a WorkItem came from the tracker (assignment), from
// other work as a follow-up: a forge event (R9) or a Run that created it
// (Product R12), or from a pull request someone else opened that a Team
// reads (OriginPullRequest, ADR-0109).
type Origin string

const (
	OriginAssignment Origin = "assignment"
	OriginFollowUp   Origin = "follow_up"
)

// ProviderPloeg is the provider of a Work Item that exists only in Ploeg,
// such as one a Run created. No tracker holds it, so nothing is written back.
const ProviderPloeg = "ploeg"

// CreatedKind says why a Run created a Work Item (Product R12).
type CreatedKind string

const (
	CreatedSplit      CreatedKind = "split"
	CreatedClarify    CreatedKind = "clarify"
	CreatedDiscovered CreatedKind = "discovered"
)

// Valid reports whether k is a known created-work kind.
func (k CreatedKind) Valid() bool {
	return k == CreatedSplit || k == CreatedClarify || k == CreatedDiscovered
}

// Outcome is the terminal report of a run. Stuck carries a mandatory
// reason and routes to a human queue.
type Outcome string

const (
	OutcomePROpened        Outcome = "pr_opened"
	OutcomePRUpdated       Outcome = "pr_updated"
	OutcomeIssueUpdated    Outcome = "issue_updated"
	OutcomeFollowUpCreated Outcome = "follow_up_created"
	OutcomeStuck           Outcome = "stuck"
	OutcomeFailed          Outcome = "failed"
	OutcomeNoChangeNeeded  Outcome = "no_change_needed"
	// OutcomeYielded is a Run that stopped at a safe point because a person
	// took it over (ADR-0142). Only the worker reports it, and only after
	// ploegd asked the Run to yield.
	OutcomeYielded Outcome = "yielded"
)

// Valid reports whether o is a known outcome enum value.
func (o Outcome) Valid() bool {
	switch o {
	case OutcomePROpened, OutcomePRUpdated, OutcomeIssueUpdated,
		OutcomeFollowUpCreated, OutcomeStuck, OutcomeFailed, OutcomeNoChangeNeeded, OutcomeYielded:
		return true
	}
	return false
}

// AssertsDelivery reports whether o claims a pull request was opened or
// updated. Only the worker may report such an outcome, from what it read on
// the forge (ADR-0059); an agent's claim is discarded.
func (o Outcome) AssertsDelivery() bool {
	return o == OutcomePROpened || o == OutcomePRUpdated
}

// FailureReason classifies why a run failed (agent_runs.failure_reason).
// Set by the worker or sweeper; queried by dashboards and the run-forensics
// view (VIK-597). The empty string means "not a failure" or "unclassified".
type FailureReason string

const (
	// FailureInfraNode is written where the machine, not the agent, is known
	// to have ended the Run: the worker received SIGTERM (pod shutdown or
	// eviction), ploegd could not mint the Run's push credential, the worker
	// could not fetch a reading Run's branch from the forge, or an ACP
	// harness failed to start or speak the protocol. The sweeper never writes
	// it, because it sees only an expired lease, not why the pod died.
	FailureInfraNode FailureReason = "infra_node"
	// FailureInfraLLM is written when a Run that needed the model gateway
	// failed without any gateway traffic, or an ACP harness could not create
	// its session because the gateway or provider refused it: the Run's own
	// key over its budget, a spent budget the gateway did not name, a
	// rejected key, a rate limit or quota, or an upstream error
	// (litellm.ClassifyError).
	FailureInfraLLM FailureReason = "infra_llm"
	// FailureAgentError is written when the harness exited with an error.
	FailureAgentError FailureReason = "agent_error"
	// FailureBudget is written when an ACP harness hit its token ceiling.
	FailureBudget FailureReason = "budget"
	// FailureLeaseLost is written by the worker when lease renewal failed and
	// by the sweeper for every Run whose lease or deadline expired.
	FailureLeaseLost FailureReason = "lease_lost"
	// FailureTimeout is written when PLOEG_HARNESS_TIMEOUT stopped the
	// harness: it was still working when its wall-clock limit ran out.
	FailureTimeout FailureReason = "timeout"
	// FailureIdle is written when PLOEG_HARNESS_IDLE_TIMEOUT stopped the
	// harness: it printed nothing and made no model call for that long.
	FailureIdle FailureReason = "idle"
	// FailureCredentialLeak is written when the worker found one of the Run's
	// credentials, or its canary, in a forge request or a pushed commit. The
	// Run's pushed commits stay on its branch for forensics.
	FailureCredentialLeak FailureReason = "credential_leak"
	// FailureSharedBudgetSpent is written when the model gateway refused the
	// Run because a LiteLLM budget wider than the Run's own key is spent: a
	// team, user, end user, organization, project, tag or model access group
	// budget (litellm.GatewayError.SharedBudgetSpent). Every retry would be
	// refused the same way until a person raises or resets that budget.
	FailureSharedBudgetSpent FailureReason = "shared_budget_spent"
)

// FailureReasons lists every known failure reason, for metrics and SQL that
// must cover the whole enum.
func FailureReasons() []FailureReason {
	return []FailureReason{FailureInfraNode, FailureInfraLLM, FailureAgentError, FailureBudget, FailureLeaseLost, FailureTimeout, FailureIdle, FailureCredentialLeak, FailureSharedBudgetSpent}
}

// Valid reports whether f is a known failure reason enum value.
func (f FailureReason) Valid() bool {
	switch f {
	case FailureInfraNode, FailureInfraLLM, FailureAgentError, FailureBudget, FailureLeaseLost, FailureTimeout, FailureIdle, FailureCredentialLeak, FailureSharedBudgetSpent:
		return true
	}
	return false
}

// IsInfra reports whether f names a failure the agent had no part in: the pod
// was killed, the node went away, the gateway did not answer.
//
// The distinction decides who gets called. An agent that fails three times is
// telling you something about the work and a person should read it; a pod
// evicted three times is telling you something about the cluster and retrying
// is the whole remedy (R2). Charging both to the same budget is how a healthy
// ticket parks itself as `needs_human` having never had a real attempt.
func (f FailureReason) IsInfra() bool {
	switch f {
	case FailureInfraNode, FailureInfraLLM, FailureLeaseLost:
		return true
	}
	return false
}

// ParksForHuman reports whether a Run that failed for f stops its Work Item
// for a person instead of being retried by either retry budget. A retry after
// a credential leak would give fresh credentials to the same agent on the
// same branch (ADR-0105), and a retry after a shared LiteLLM budget is spent
// would be refused by the gateway again (ADR-0111), so only a person may
// decide to try again.
func (f FailureReason) ParksForHuman() bool {
	return f == FailureCredentialLeak || f == FailureSharedBudgetSpent
}

// InfraFailureReasons lists the IsInfra reasons for SQL that must partition
// runs the same way. Derived from the enum so a new reason cannot be added to
// one and forgotten in the other.
func InfraFailureReasons() []string {
	var out []string
	for _, f := range FailureReasons() {
		if f.IsInfra() {
			out = append(out, string(f))
		}
	}
	return out
}

// WorkItem mirrors one tracker item (provider + external id + revision).
type WorkItem struct {
	ID         string `json:"id"`
	Provider   string `json:"provider"`   // tracker provider name, e.g. "vikunja"
	ExternalID string `json:"externalId"` // provider-scoped id of the tracker item
	Revision   string `json:"revision"`   // provider revision/etag for staleness detection
	Team       string `json:"team"`       // team the item is queued for; empty until assigned
	State      State  `json:"state"`
	Origin     Origin `json:"origin"`
	Priority   int    `json:"priority"` // rank mirrored from the tracker; drives queue order (R10)
	Title      string `json:"title"`
	// Description is the tracker item body (Vikunja sends HTML); the harness
	// adapter composes it into the task prompt.
	Description string `json:"description"`
	URL         string `json:"url"`
	// ExternalScope is the tracker's own container for this item (Vikunja
	// project id) — the input to Target resolution. Recorded even when no
	// mapping matched, so onboarding a repo is one query away.
	ExternalScope string `json:"externalScope,omitempty"`
	// Target is where this item's changes land. Nil = unresolved; the worker
	// then falls back to its env-configured repo. Pointer because encoding/json
	// cannot elide an empty struct.
	Target *Target `json:"target,omitempty"`
	// RouteRule is the id of the routing rule that decided Team and Target,
	// recorded so an audit can answer why this item went where it went.
	RouteRule string `json:"routeRule,omitempty"`
	// RouteHint is the tracker label that selected the Target among the
	// registered targets (ADR-0038). Empty when a board's default decided.
	RouteHint string `json:"routeHint,omitempty"`
	// Labels are the tracker item's label titles, read from the authoritative
	// item at ingest for routing and never stored.
	Labels []string `json:"-"`
	// SourceWorkItemID names the Work Item a Follow-Up was created from.
	// Empty for work that did not come from other work.
	SourceWorkItemID string `json:"sourceWorkItemId,omitempty"`
	// SourceBranch is the branch of the pull request a Follow-Up repairs. A
	// Follow-Up works on this branch instead of deriving its own.
	SourceBranch string `json:"sourceBranch,omitempty"`
	// SourcePR is the pull request number a Follow-Up refers to; zero when the
	// forge event did not name one.
	SourcePR  int       `json:"sourcePr,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
	// TrackerCreatedAt is when the tracker says the item was created, read
	// at ingest; zero when the tracker did not say (ADR-0057).
	TrackerCreatedAt time.Time `json:"-"`
	// EstimateSeconds is the tracker's time estimate, nil when the tracker
	// keeps none or none was set (ADR-0057).
	EstimateSeconds *int64 `json:"-"`
	// Closed reports that the tracker marked the item done or closed when
	// FetchItem read it. It is never stored.
	Closed bool `json:"-"`
}

// Target is where a Work Item's changes land: the forge coordinates a Run
// reads, writes, and opens a PR in. It belongs to the Work Item, never to the
// Team — a Team is a capability manifest (roles, harness, model, budget) and
// names no repository (R11).
//
// A Target is a coordinate, not a connection: it carries a forge *id* to be
// resolved against a forge registry, never a URL and never a credential (R8).
type Target struct {
	Forge      string `json:"forge,omitempty"` // forge registry id; empty = the default forge
	Owner      string `json:"owner"`
	Repo       string `json:"repo"`
	BaseBranch string `json:"baseBranch,omitempty"` // empty = the repo's default branch (VIK-589)
}

// Resolved reports whether the Target names a repository. Owner and Repo are
// atomic: a half-resolved Target is never blended with a fallback, because
// cloning one repo and pushing to another is the worst failure this seam has.
func (t Target) Resolved() bool { return t.Owner != "" && t.Repo != "" }

// Key is the repo-scoped identity used for per-repo serialization (backlog
// #42) and for the (target, branch) reverse lookup that follow-up routing
// needs (R9).
func (t Target) Key() string { return t.Forge + "/" + t.Owner + "/" + t.Repo }

// Lease is a team's crash-safe claim on a work item. Renewed by the
// running Job; expiry releases the item mechanically.
type Lease struct {
	WorkItemID string
	Team       string
	ExpiresAt  time.Time
	RenewedAt  time.Time
}

// Checkpoint is the small durable progress record enabling resume.
// Everything else is re-derived from git/forge state. NodeName and PodUID
// are set from the downward API on the first checkpoint so forensics survive
// pod/job cleanup (VIK-597).
type Checkpoint struct {
	WorkItemID string `json:"workItemId,omitempty"`
	Phase      string `json:"phase"` // e.g. "branch_created", "changes_made", "pr_opened"
	Branch     string `json:"branch,omitempty"`
	PRURL      string `json:"prUrl,omitempty"`
	// Commit is the commit a reading Run had checked out from the branch
	// under review. Empty when the Run reviewed the base branch.
	Commit   string    `json:"commit,omitempty"`
	NodeName string    `json:"nodeName,omitempty"`
	PodUID   string    `json:"podUid,omitempty"`
	At       time.Time `json:"at,omitempty"`

	// InstructionFiles is every agent instruction file the worker found in
	// the clone before the harness ran, with the digest of what it read.
	InstructionFiles []InstructionFile `json:"instructionFiles,omitempty"`

	// Limits are the limits that stop the Run's prompt, sent with the
	// prompt_started checkpoint (ADR-0114).
	Limits *RunLimits `json:"limits,omitempty"`

	// PlannedPaths are the repository paths the Run plans to change, when
	// it knows them before it pushes. Ploeg keeps the newest list per Shift
	// and names it to other Runs in the same repository (ADR-0123).
	PlannedPaths []string `json:"plannedPaths,omitempty"`

	// Board is what the Run's briefing was read at and held, sent with the
	// briefed checkpoint (ADR-0124).
	Board *BoardBriefing `json:"board,omitempty"`
}

// PhasePromptStarted is the checkpoint phase a worker writes when the
// agent's prompt starts, carrying the Run's limits.
const PhasePromptStarted = "prompt_started"

// RunLimits are the limits that stop a Run's prompt (ADR-0114). PromptSeconds
// is the most the prompt may run from PromptStartedAt and IdleSeconds how long
// the agent may show no activity. Zero is no limit.
type RunLimits struct {
	PromptSeconds   int64     `json:"promptSeconds,omitempty"`
	IdleSeconds     int64     `json:"idleSeconds,omitempty"`
	PromptStartedAt time.Time `json:"promptStartedAt"`
}

// InstructionFile is one agent instruction or configuration file in a Run's
// clone: its slash-separated path relative to the repository root and the
// hex SHA-256 of its content.
type InstructionFile struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

// AskRole is the Role of an Ask: a read-only Run about a Work Item with no
// Shift, no Round and no Lease, paid from an allowance (ADR-0082). No crew
// Role may take this name.
const AskRole = "ask"

// ChatRole is the Role of a chat message: a read-only Run about no Work Item,
// with no Shift, no Round and no Lease, paid from a Team's Chat Allowance and
// metered under the Team's ask inference policy (ADR-0128). No crew Role may
// take this name.
const ChatRole = "chat"
