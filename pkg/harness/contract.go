// Package harness defines the contract between Ploeg and an agent
// harness: TaskSpec in, OutcomeReport out. Adapters wrap concrete
// harnesses (OpenHands, Claude Code, opencode, …) behind the Adapter
// interface in adapter.go. The JSON shapes are published as versioned
// schemas in docs/contracts/ (backlog #59) and pinned by contract_test.go.
// See docs/design.md §5.
package harness

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ploeg-hq/ploeg/pkg/work"
)

// TaskSpec is the harness contract input: everything a run needs to know
// about the work, the repo, and its identity.
type TaskSpec struct {
	WorkItem   work.WorkItem    `json:"workItem"`
	Role       string           `json:"role,omitempty"` // specialist role within the team
	Checkpoint *work.Checkpoint `json:"checkpoint,omitempty"`
	Repo       RepoRef          `json:"repo"`
	Branch     string           `json:"branch"`  // e.g. agent/vik-<id>
	TraceID    string           `json:"traceId"` // ploeg-<12hex>; doubles as the LiteLLM key alias
	// Briefing carries earlier Rounds' findings into this one (ADR-0011).
	// Ploeg reads them from the Shift and injects them; the agent never calls
	// a forge or a Ploeg API to fetch them (R6).
	Briefing []Finding `json:"briefing,omitempty"`
	// ReviewFindings are the findings of the Shift's last review Round with
	// the writer's answer to each (ADR-0121). A writer answers them; a reader
	// checks the answers. Empty when that review raised none by id.
	ReviewFindings []BriefedFinding `json:"reviewFindings,omitempty"`
	// Questions are the questions earlier Runs asked that reach this Run,
	// answered or not (ADR-0121).
	Questions []BriefedQuestion `json:"questions,omitempty"`
	// ActiveWork names the other active Shifts in the Run's repository and
	// the paths they share with it, as ploegd rendered it within the active
	// work section's budget (ADR-0123, ADR-0124). Tracker titles in it are
	// quoted.
	ActiveWork string `json:"activeWork,omitempty"`
	// OpenSpec is the OpenSpec change the Work Item names, located in the
	// clone and rendered as a brief by the worker. Nil when it names none.
	OpenSpec *OpenSpecBrief `json:"openSpec,omitempty"`
	// Knowledge describes the knowledge pack the worker selected for this Run
	// and wrote outside the clone. Nil when no knowledge source is configured
	// or nothing matched.
	Knowledge *KnowledgeBrief `json:"knowledge,omitempty"`
	// Context records the files people attached to the Work Item that this
	// Run was given, oldest first (proposed, context bundles proof of
	// concept). The worker unpacked them outside the clone.
	Context []ContextItem `json:"context,omitempty"`
	// ContextIndex is the index of the unpacked context as the prompt
	// carries it. The worker composes the prompt; it is not part of the
	// published Task Spec.
	ContextIndex string `json:"-"`
	// Limits are the limits this Run works within, as known before its
	// prompt starts (ADR-0133). The prompt states them; they are not part of
	// the published Task Spec.
	Limits RunLimits `json:"-"`
	// Credentials are delivered out-of-band (env, mounted secrets), never here (R8).
}

// RunLimits are the limits a Run's prompt states to its agent (ADR-0133).
// A zero field is unknown or no limit, and the prompt leaves it out.
type RunLimits struct {
	// Time is the most the prompt may run before Ploeg stops the Run.
	Time time.Duration
	// Idle is how long the agent may show no activity before Ploeg stops
	// the Run.
	Idle time.Duration
	// BudgetUSD is the model budget the Run's credential is minted at.
	BudgetUSD float64
	// Round is the Run's Round in its Shift and MaxRounds the most Rounds
	// the Shift can run, fix Rounds included.
	Round, MaxRounds int
}

// KnowledgeBrief is the provenance of a Run's knowledge pack: which concepts
// it was given, from which version of each source, so a Run can be explained
// and replayed against the knowledge it had.
type KnowledgeBrief struct {
	// Index is the pack's table of contents, the part the prompt carries.
	Index    string            `json:"index"`
	Concepts []string          `json:"concepts"`
	Sources  []KnowledgeSource `json:"sources"`
	Bytes    int               `json:"bytes"`
	// Omitted counts matching concepts the size limit left out.
	Omitted int `json:"omitted,omitempty"`
	// Notes are the keys of the Work Item Notes the pack holds and Levels
	// each level's budget and use; the worker reports both in its briefed
	// checkpoint (ADR-0124), so they are not part of the Task Spec.
	Notes  []string         `json:"-"`
	Levels []KnowledgeLevel `json:"-"`
}

// KnowledgeLevel is what one level of a knowledge pack used of its budget.
type KnowledgeLevel struct {
	Level                         string
	Budget, Bytes, Items, Omitted int
	Digest                        string
}

// ContextRef names one context item a claim hands a Run: what to download
// with the Run's capability and how to verify it. Never the bytes.
type ContextRef struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	MediaType string    `json:"mediaType"`
	SHA256    string    `json:"sha256"`
	Bytes     int64     `json:"bytes"`
	Files     int       `json:"files"`
	Note      string    `json:"note,omitempty"`
	AddedAt   time.Time `json:"addedAt"`
	// Phase is before_start or while_steering: whether a Run of the Work
	// Item had already started when the person attached it.
	Phase string `json:"phase"`
}

// ContextItem is the provenance of one context item a Run was given.
type ContextItem struct {
	ID      string    `json:"id"`
	Name    string    `json:"name"`
	SHA256  string    `json:"sha256"`
	Files   int       `json:"files"`
	Bytes   int64     `json:"bytes"`
	AddedAt time.Time `json:"addedAt"`
	Phase   string    `json:"phase"`
	Note    string    `json:"note,omitempty"`
}

// RunNotesRequest is the body of POST /api/v1/runs/{token}/notes
// (docs/contracts/run-api.v1.schema.json#/$defs/runNotesRequest): notes a
// running Run sends while it works, so a Run stopped later keeps what it
// found (ADR-0088, amended 2026-10-17).
type RunNotesRequest struct {
	Notes []RunNote `json:"notes"`
}

// RunNote is one note a running Run sends. Key is unique within the Run;
// resending a key changes nothing.
type RunNote struct {
	Key string `json:"key"`
	// ByWorker marks the worker's own note on why the Run stopped, as
	// opposed to a finding the agent wrote.
	ByWorker bool     `json:"byWorker,omitempty"`
	Learning Learning `json:"learning"`
}

// RunNotesResponse is the HTTP 200 body of POST /api/v1/runs/{token}/notes.
type RunNotesResponse struct {
	Queued int `json:"queued"`
}

// KnowledgeResponse is the body of GET /api/v1/runs/{token}/knowledge
// (docs/contracts/run-api.v1.schema.json#/$defs/knowledgeResponse): what the
// Tenant's graphs hold for the Run's own scopes. It never carries a graph
// credential (ADR-0087, proposed).
type KnowledgeResponse struct {
	Notes KnowledgeNotes `json:"notes"`
	// Concepts is the reviewed knowledge of the Run's Tenant, Client and
	// repository; absent from a ploegd that serves none.
	Concepts *KnowledgeConcepts `json:"concepts,omitempty"`
}

// KnowledgeConcepts are the reviewed concepts a Run may read: every
// Tenant-scoped concept, those of its Work Item's Client, and those of its
// Work Target repository (ADR-0087, proposed).
type KnowledgeConcepts struct {
	Status string             `json:"status"`
	Commit string             `json:"commit,omitempty"`
	Items  []KnowledgeConcept `json:"items"`
}

// KnowledgeConcept is one reviewed concept: its path inside its scope and
// the whole OKF document.
type KnowledgeConcept struct {
	Scope    string `json:"scope"`
	Path     string `json:"path"`
	Document string `json:"document"`
}

// Knowledge read statuses.
const (
	KnowledgeOK          = "ok"
	KnowledgeUnavailable = "unavailable"
	KnowledgeOff         = "off"
)

// KnowledgeNotes are the Work Item Notes of the Run's root Work Item, newest
// first, written without review (ADR-0088, proposed).
type KnowledgeNotes struct {
	Status string         `json:"status"`
	Commit string         `json:"commit,omitempty"`
	Items  []WorkItemNote `json:"items"`
}

// WorkItemNote is one note as a Run reads it.
type WorkItemNote struct {
	Key            string    `json:"key"`
	WorkItem       string    `json:"workItem,omitempty"`
	Repository     string    `json:"repository,omitempty"`
	Type           string    `json:"type"`
	Title          string    `json:"title"`
	Description    string    `json:"description,omitempty"`
	Resource       string    `json:"resource,omitempty"`
	Tags           []string  `json:"tags,omitempty"`
	Body           string    `json:"body"`
	SuggestedScope string    `json:"suggestedScope,omitempty"`
	WrittenBy      string    `json:"writtenBy,omitempty"`
	WrittenAt      time.Time `json:"writtenAt,omitzero"`
	// Provenance (ADR-0122). An older ploegd sends none of these; a reader
	// then treats the note as an untrusted Work Item note a Run wrote.
	Level        string    `json:"level,omitempty"`
	Subject      string    `json:"subject,omitempty"`
	AuthorKind   string    `json:"authorKind,omitempty"`
	Author       string    `json:"author,omitempty"`
	SourceRefs   []string  `json:"sourceRefs,omitempty"`
	Trust        string    `json:"trust,omitempty"`
	ObservedAt   time.Time `json:"observedAt,omitzero"`
	ExpiresAt    time.Time `json:"expiresAt,omitzero"`
	PromotedFrom string    `json:"promotedFrom,omitempty"`
	Evidence     []string  `json:"evidence,omitempty"`
	ApprovedBy   string    `json:"approvedBy,omitempty"`
	// Contested is set while a person has not yet decided between this note
	// and another current note on the same subject that disagrees with it.
	Contested bool `json:"contested,omitempty"`
}

// KnowledgeSource is one bundle a pack drew from and its content digest.
// A graph source also names the graph commit it was read at, or why it was
// not read.
type KnowledgeSource struct {
	Name   string `json:"name"`
	Digest string `json:"digest"`
	Commit string `json:"commit,omitempty"`
	// Status is unavailable when the graph could not be read in time; the
	// Run went ahead without it.
	Status string `json:"status,omitempty"`
}

// Learning is one thing a Run proposes Ploeg should know next time, in the
// shape of an OKF concept. Where a deployment writes Work Item Notes it
// becomes one at once, read by later Runs under the same root Work Item
// (ADR-0088, proposed); elsewhere a person reviews it before any Run is
// briefed with it.
type Learning struct {
	Type        string   `json:"type"`
	Title       string   `json:"title"`
	Description string   `json:"description,omitempty"`
	Resource    string   `json:"resource,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	Body        string   `json:"body"`
	// Scope is how widely the agent suggests the learning applies:
	// work-item (the default), repository, client or tenant. A wider scope
	// is only a suggestion; a Tenant admin decides it as a Promotion.
	Scope string `json:"scope,omitempty"`
	// Subject names what the learning is about, so a later note on the same
	// subject replaces it or is flagged as disagreeing (ADR-0122). Absent,
	// it is the type and title.
	Subject string `json:"subject,omitempty"`
	// Supersedes is the key of a note the Run was briefed with that this
	// learning replaces.
	Supersedes string `json:"supersedes,omitempty"`
	// ExpiresAt is when a volatile fact stops being true, at most a year
	// ahead; no Run reads the note after it.
	ExpiresAt *time.Time `json:"expiresAt,omitempty"`
}

// Finding is one earlier Run's contribution to the blackboard, attributed to
// the Role that made it. Prose, not structure: the same text goes to the pull
// request and into the next Round's prompt (ADR-0011).
type Finding struct {
	Role     string `json:"role"`
	Round    int    `json:"round"`
	Findings string `json:"findings"`
}

// OpenSpecBrief is the specification a Work Item's OpenSpec change gives a
// Run. Root is the repository-relative directory holding the change's
// openspec/ directory, "." for the repository root. Source says whether Brief
// came from the openspec CLI or from the change's files.
type OpenSpecBrief struct {
	Change string `json:"change"`
	Root   string `json:"root"`
	Source string `json:"source"`
	Brief  string `json:"brief"`
}

// The values of OpenSpecBrief.Source.
const (
	OpenSpecSourceCLI   = "cli"
	OpenSpecSourceFiles = "files"
)

type RepoRef struct {
	Forge      string `json:"forge,omitempty"`
	ForgeURL   string `json:"forgeUrl"`
	Owner      string `json:"owner"`
	Name       string `json:"name"`
	BaseBranch string `json:"baseBranch,omitempty"`
}

const (
	ForgeForgejo = "forgejo"
	ForgeGitLab  = "gitlab"
	ForgeGitHub  = "github"
)

func (r RepoRef) Dialect() string {
	if r.Forge == "" {
		return ForgeForgejo
	}
	return r.Forge
}

func (r RepoRef) ProjectPath() string { return r.Owner + "/" + r.Name }

// OutcomeReport is the harness contract output. A zero-value Outcome ("")
// means "no structured signal" — the orchestrator falls back to forge
// ground truth (PR poll) and exit-code heuristics.
type OutcomeReport struct {
	Outcome       work.Outcome     `json:"outcome"`
	Summary       string           `json:"summary"`
	Links         []string         `json:"links,omitempty"` // PRs, commits, created follow-ups
	Checkpoint    *work.Checkpoint `json:"checkpoint,omitempty"`
	StuckReason   string           `json:"stuckReason,omitempty"`   // mandatory when Outcome == stuck (R4)
	Usage         *Usage           `json:"usage,omitempty"`         // reserved for backlog #66
	FailureReason string           `json:"failureReason,omitempty"` // ploeg-internal failure taxonomy (VIK-597); set by the orchestrator, never by the harness
	// Findings is a reading Run's contribution to the blackboard (ADR-0011):
	// markdown prose Ploeg publishes to the pull request and injects into the
	// next Round's Briefing. A writer normally leaves it empty.
	Findings string `json:"findings,omitempty"`
	// Verdict is a reading Run's answer to "is this done?" — approve or
	// request_changes (ADR-0017). It is the only field by which an agent
	// influences what runs next, and it can do exactly one thing: re-open the
	// plan's own writing Round. Ignored from a writing Role.
	Verdict string `json:"verdict,omitempty"`
	// Problem and Solution are a writing Run's account of its change
	// (ADR-0042): what was wrong or missing, and what the Run changed. Each is
	// markdown that opens in everyday words for a product owner and a client
	// and may end with a "### For developers" section. They decide nothing,
	// and ploegd ignores them from a reading Role.
	Problem  string `json:"problem,omitempty"`
	Solution string `json:"solution,omitempty"`
	// CreatedWorkItems are Work Items this Run proposes (Product R12): a
	// split, a clarification that makes work Ready, or work it discovered.
	// Ploeg stores each within the Team's created-work limits and records a
	// reason for every entry it rejects. The agent never dispatches them.
	CreatedWorkItems []CreatedWorkItem `json:"createdWorkItems,omitempty"`
	// Learnings are what this Run proposes later Runs should know: a pitfall
	// it hit, a convention it had to discover, a fact about the repository.
	// The worker keeps at most MaxLearnings and writes them as OKF concepts
	// for a person to review; none reaches a briefing unreviewed.
	Learnings []Learning `json:"learnings,omitempty"`
	// Verification is the worker's own run of the configured checks on a
	// writing Run's checkout. The worker sets it and discards any value an
	// adapter or agent reported; absent on a payload from an older worker.
	Verification *Verification `json:"verification,omitempty"`
	// Delivery is what the worker read on the forge about the Run's pull
	// request, before and after the harness ran (ADR-0059). Only the worker
	// sets it; absent on a payload from an older worker.
	Delivery *Delivery `json:"delivery,omitempty"`
	// Harness is the Harness the Run ran with as the worker knows it
	// (ADR-0097). Only the worker sets it and replaces any value an adapter
	// or agent reported; absent on a payload from an older worker.
	Harness *RunHarness `json:"harness,omitempty"`
	// FindingItems are a reading Run's findings by id (ADR-0121). ploegd
	// assigns each new finding its id; an entry naming a briefed finding's id
	// raises that finding again. Ignored from a writing Role.
	FindingItems []ReviewFinding `json:"findingItems,omitempty"`
	// FindingAnswers are a writing Run's answers to the findings it was
	// briefed with. Ignored from a reading Role.
	FindingAnswers []FindingAnswer `json:"findingAnswers,omitempty"`
	// Questions are what the Run asks a person, the next reviewer or the
	// next Run instead of guessing. A question to a person parks the Work
	// Item at needs_human when the Round ends.
	Questions []Question `json:"questions,omitempty"`
	// QuestionAnswers are the Run's answers to questions it was briefed
	// with that were addressed to a reviewer or the next Run.
	QuestionAnswers []QuestionAnswer `json:"questionAnswers,omitempty"`
}

// RunHarness names the Harness a Run ran with. A field the worker does not
// know is empty and left out.
type RunHarness struct {
	Name    string        `json:"name,omitempty"`
	Profile string        `json:"profile,omitempty"`
	Version string        `json:"version,omitempty"`
	Agent   *HarnessAgent `json:"agent,omitempty"`
}

// HarnessAgent is the agent's own name and version as it reported them
// during the Run, such as an ACP agent's agentInfo.
type HarnessAgent struct {
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
}

// DeliveryObservation says what the worker's two forge reads showed about the
// Run's pull request.
type DeliveryObservation string

// The values of DeliveryObservation.
const (
	// DeliveryOpened: no pull request before the harness ran, one after.
	DeliveryOpened DeliveryObservation = "opened"
	// DeliveryUpdated: the same pull request before and after, and its head
	// commit changed during the Run.
	DeliveryUpdated DeliveryObservation = "updated"
	// DeliveryNone: no pull request after the Run, or the same one with the
	// same head commit.
	DeliveryNone DeliveryObservation = "none"
	// DeliveryUnknown: a forge read failed, so nothing is known.
	DeliveryUnknown DeliveryObservation = "unknown"
)

// Delivery binds a Run's pull request to the forge, repository and branch the
// worker read it from. Number, URL, Head and Base describe the pull request
// open after the Run, and are empty when there is none or the read failed.
// HeadBefore is that pull request's head commit before the harness ran, when
// it was already open then.
type Delivery struct {
	Forge      string              `json:"forge,omitempty"`
	Repository string              `json:"repository"`
	Branch     string              `json:"branch"`
	Observed   DeliveryObservation `json:"observed"`
	Number     int                 `json:"number,omitempty"`
	URL        string              `json:"url,omitempty"`
	Base       string              `json:"base,omitempty"`
	Head       string              `json:"head,omitempty"`
	HeadBefore string              `json:"headBefore,omitempty"`
	Reason     string              `json:"reason,omitempty"`
}

// CreatedWorkItem is one Work Item a Run proposes. Team is a request, not a
// routing decision: Ploeg accepts only a Team it knows.
type CreatedWorkItem struct {
	Title       string           `json:"title"`
	Description string           `json:"description"`
	Ready       bool             `json:"ready"`
	Team        string           `json:"team,omitempty"`
	Kind        work.CreatedKind `json:"kind"`
}

// Bounds on a CreatedWorkItem, mirrored by outcomereport.v1.schema.json.
const (
	MaxCreatedWorkItems      = 50
	MaxCreatedTitleLen       = 200
	MaxCreatedDescriptionLen = 16384
	MaxCreatedTeamLen        = 128
)

// ValidateCreatedWorkItems checks the entries' shape against the contract.
// It does not apply a Team's limits: ploegd does that when it stores them.
func ValidateCreatedWorkItems(items []CreatedWorkItem) error {
	if len(items) > MaxCreatedWorkItems {
		return fmt.Errorf("createdWorkItems has %d entries; at most %d are accepted", len(items), MaxCreatedWorkItems)
	}
	for i, it := range items {
		if strings.TrimSpace(it.Title) == "" || utf8.RuneCountInString(it.Title) > MaxCreatedTitleLen {
			return fmt.Errorf("createdWorkItems[%d]: title must be 1 to %d characters", i, MaxCreatedTitleLen)
		}
		if utf8.RuneCountInString(it.Description) > MaxCreatedDescriptionLen {
			return fmt.Errorf("createdWorkItems[%d]: description exceeds %d characters", i, MaxCreatedDescriptionLen)
		}
		if !it.Kind.Valid() {
			return fmt.Errorf("createdWorkItems[%d]: kind must be split, clarify or discovered", i)
		}
		if utf8.RuneCountInString(it.Team) > MaxCreatedTeamLen {
			return fmt.Errorf("createdWorkItems[%d]: team exceeds %d characters", i, MaxCreatedTeamLen)
		}
	}
	return nil
}

// The closed set of verdicts. A reading Run may return one; anything else is
// rejected at the API boundary.
const (
	VerdictApprove        = "approve"
	VerdictRequestChanges = "request_changes"
)

// ValidVerdict reports whether v is empty (no opinion) or a known verdict.
func ValidVerdict(v string) bool {
	return v == "" || v == VerdictApprove || v == VerdictRequestChanges
}

// Usage carries per-run cost/usage a harness can report (backlog #66) and
// the harness-native resume handle (backlog #70). All fields optional.
//
// The pointer and map fields follow ADR-0045: nil means the harness did not
// report the figure, and a reported zero stays zero. An adapter never fills
// one in from a default.
type Usage struct {
	InputTokens  int64   `json:"inputTokens,omitempty"`
	OutputTokens int64   `json:"outputTokens,omitempty"`
	CostUSD      float64 `json:"costUsd,omitempty"`
	SessionID    string  `json:"sessionId,omitempty"`

	// CacheReadInputTokens and CacheCreationInputTokens are input tokens
	// served from, and written to, the provider's prompt cache.
	CacheReadInputTokens     *int64 `json:"cacheReadInputTokens,omitempty"`
	CacheCreationInputTokens *int64 `json:"cacheCreationInputTokens,omitempty"`
	// Turns is the number of agent turns the harness counted.
	Turns *int64 `json:"turns,omitempty"`
	// DurationMs is the harness's own wall-clock time for the Run, and
	// APIDurationMs the part of it spent waiting on the model API.
	DurationMs    *int64 `json:"durationMs,omitempty"`
	APIDurationMs *int64 `json:"apiDurationMs,omitempty"`
	// ToolCalls counts tool invocations, and ToolCallsByKind splits them by
	// the harness's tool kind.
	ToolCalls       *int64           `json:"toolCalls,omitempty"`
	ToolCallsByKind map[string]int64 `json:"toolCallsByKind,omitempty"`
	// PeakContextTokens is the largest context fill the harness reported,
	// and ContextWindowTokens the size of that window.
	PeakContextTokens   *int64 `json:"peakContextTokens,omitempty"`
	ContextWindowTokens *int64 `json:"contextWindowTokens,omitempty"`
	// ModelUsage is the harness's own split of the Run's usage by model.
	ModelUsage map[string]ModelUsage `json:"modelUsage,omitempty"`
}

// ModelUsage is the part of a Run's usage one model accounts for, as the
// harness reported it.
type ModelUsage struct {
	InputTokens              *int64   `json:"inputTokens,omitempty"`
	OutputTokens             *int64   `json:"outputTokens,omitempty"`
	CacheReadInputTokens     *int64   `json:"cacheReadInputTokens,omitempty"`
	CacheCreationInputTokens *int64   `json:"cacheCreationInputTokens,omitempty"`
	CostUSD                  *float64 `json:"costUsd,omitempty"`
	ContextWindowTokens      *int64   `json:"contextWindowTokens,omitempty"`
}

// HasActivity reports whether u records model turns or tool calls, which
// only a Run that reached its model can have.
func (u *Usage) HasActivity() bool {
	if u == nil {
		return false
	}
	return (u.Turns != nil && *u.Turns > 0) || (u.ToolCalls != nil && *u.ToolCalls > 0)
}
