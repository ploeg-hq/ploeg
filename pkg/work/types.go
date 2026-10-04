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
	StateProposed       State = "proposed"
	StateQueued         State = "queued"
	StateLeased         State = "leased"
	StateNeedsHuman     State = "needs_human"
	StateAwaitingReview State = "awaiting_review"
	StateStale          State = "stale"
	StateDone           State = "done"
	// StateWithdrawn means a person took the mandate back: the tracker item
	// was unassigned or an operator cancelled it. Only a new assignment
	// re-queues it.
	StateWithdrawn State = "withdrawn"
)

// Origin records whether a WorkItem came from the tracker (assignment) or
// from other work as a follow-up: a forge event (R9) or a Run that created it
// (Product R12).
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
)

// Valid reports whether o is a known outcome enum value.
func (o Outcome) Valid() bool {
	switch o {
	case OutcomePROpened, OutcomePRUpdated, OutcomeIssueUpdated,
		OutcomeFollowUpCreated, OutcomeStuck, OutcomeFailed, OutcomeNoChangeNeeded:
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
	// failed without any gateway traffic, or an ACP harness reported an
	// authentication, quota or upstream error.
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
)

// Valid reports whether f is a known failure reason enum value.
func (f FailureReason) Valid() bool {
	switch f {
	case FailureInfraNode, FailureInfraLLM, FailureAgentError, FailureBudget, FailureLeaseLost, FailureTimeout, FailureIdle:
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

// InfraFailureReasons lists the IsInfra reasons for SQL that must partition
// runs the same way. Derived from the enum so a new reason cannot be added to
// one and forgotten in the other.
func InfraFailureReasons() []string {
	all := []FailureReason{FailureInfraNode, FailureInfraLLM, FailureAgentError, FailureBudget, FailureLeaseLost, FailureTimeout, FailureIdle}
	out := make([]string, 0, len(all))
	for _, f := range all {
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
}

// InstructionFile is one agent instruction or configuration file in a Run's
// clone: its slash-separated path relative to the repository root and the
// hex SHA-256 of its content.
type InstructionFile struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}
