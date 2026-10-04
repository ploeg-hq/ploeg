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
	// OpenSpec is the OpenSpec change the Work Item names, located in the
	// clone and rendered as a brief by the worker. Nil when it names none.
	OpenSpec *OpenSpecBrief `json:"openSpec,omitempty"`
	// Context records the files people attached to the Work Item that this
	// Run was given, oldest first (proposed, context bundles proof of
	// concept). The worker unpacked them outside the clone.
	Context []ContextItem `json:"context,omitempty"`
	// ContextIndex is the index of the unpacked context as the prompt
	// carries it. The worker composes the prompt; it is not part of the
	// published Task Spec.
	ContextIndex string `json:"-"`
	// Credentials are delivered out-of-band (env, mounted secrets), never here (R8).
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
	// (ADR-0042): what was wrong or missing, and what the Run changed. Markdown
	// for the person who reviews the pull request. They decide nothing, and
	// ploegd ignores them from a reading Role.
	Problem  string `json:"problem,omitempty"`
	Solution string `json:"solution,omitempty"`
	// CreatedWorkItems are Work Items this Run proposes (Product R12): a
	// split, a clarification that makes work Ready, or work it discovered.
	// Ploeg stores each within the Team's created-work limits and records a
	// reason for every entry it rejects. The agent never dispatches them.
	CreatedWorkItems []CreatedWorkItem `json:"createdWorkItems,omitempty"`
	// Verification is the worker's own run of the configured checks on a
	// writing Run's checkout. The worker sets it and discards any value an
	// adapter or agent reported; absent on a payload from an older worker.
	Verification *Verification `json:"verification,omitempty"`
	// Delivery is what the worker read on the forge about the Run's pull
	// request, before and after the harness ran (ADR-0059). Only the worker
	// sets it; absent on a payload from an older worker.
	Delivery *Delivery `json:"delivery,omitempty"`
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
