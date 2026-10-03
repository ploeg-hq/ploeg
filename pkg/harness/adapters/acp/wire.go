package acp

import (
	"encoding/json"
	"strings"
)

// Ploeg owns these enums rather than importing the SDK's generated ones.
//
// The reason is concrete, not stylistic: coder/acp-go-sdk v0.13.5 is generated
// from ACP schema 0.13.5 while upstream is on the 1.x line, and the shapes have
// diverged exactly where it hurts — its UsageUpdate is token-shaped
// (Input/Output) and has NO field for `cost`, which is the field the budget
// gates want. Decoding semantics from ploeg-owned types off a raw tee of the
// protocol channel makes the mapping immune to SDK lag, immune to unknown enum
// values, and testable from JSON fixtures with no SDK involved at all.
//
// The rule for every parser here: an unknown value becomes a sentinel and is
// logged once. A run NEVER fails because of an enum.

// StopReason is PromptResponse.stopReason. Five values in ACP v1, plus a
// sentinel for anything a newer agent invents.
type StopReason string

const (
	StopEndTurn         StopReason = "end_turn"
	StopMaxTokens       StopReason = "max_tokens"
	StopMaxTurnRequests StopReason = "max_turn_requests"
	StopRefusal         StopReason = "refusal"
	StopCancelled       StopReason = "cancelled"
	StopUnknown         StopReason = "unknown"
)

// ParseStopReason normalises camelCase and snake_case spellings. An
// unrecognised value is StopUnknown, which the outcome builder treats as
// end_turn — deferring to forge ground truth rather than fabricating a failure.
func ParseStopReason(s string) StopReason {
	switch normalise(s) {
	case "end_turn":
		return StopEndTurn
	case "max_tokens":
		return StopMaxTokens
	case "max_turn_requests":
		return StopMaxTurnRequests
	case "refusal":
		return StopRefusal
	case "cancelled", "canceled":
		return StopCancelled
	default:
		return StopUnknown
	}
}

// UpdateKind is the session/update `sessionUpdate` discriminator. Eleven
// values in ACP v1; six of them drive the outcome, the rest are decoded so an
// unknown twelfth is ignored rather than fatal.
type UpdateKind string

const (
	UpdateUserMessageChunk  UpdateKind = "user_message_chunk"
	UpdateAgentMessageChunk UpdateKind = "agent_message_chunk"
	UpdateAgentThoughtChunk UpdateKind = "agent_thought_chunk"
	UpdateToolCall          UpdateKind = "tool_call"
	UpdateToolCallUpdate    UpdateKind = "tool_call_update"
	UpdatePlan              UpdateKind = "plan"
	UpdateAvailableCommands UpdateKind = "available_commands_update"
	UpdateCurrentMode       UpdateKind = "current_mode_update"
	UpdateConfigOption      UpdateKind = "config_option_update"
	UpdateSessionInfo       UpdateKind = "session_info_update"
	UpdateUsage             UpdateKind = "usage_update"
	UpdateUnknown           UpdateKind = "unknown"
)

func ParseUpdateKind(s string) UpdateKind {
	switch k := UpdateKind(normalise(s)); k {
	case UpdateUserMessageChunk, UpdateAgentMessageChunk, UpdateAgentThoughtChunk,
		UpdateToolCall, UpdateToolCallUpdate, UpdatePlan, UpdateAvailableCommands,
		UpdateCurrentMode, UpdateConfigOption, UpdateSessionInfo, UpdateUsage:
		return k
	default:
		return UpdateUnknown
	}
}

// ToolKind classifies what a tool call does. The mutating set is what tells
// "the agent edited 40 files and opened no PR" apart from "the agent looked
// around and correctly concluded there was nothing to do" — a distinction the
// openhands adapter cannot make, and the main fidelity win of this adapter.
type ToolKind string

const (
	ToolRead       ToolKind = "read"
	ToolEdit       ToolKind = "edit"
	ToolDelete     ToolKind = "delete"
	ToolMove       ToolKind = "move"
	ToolSearch     ToolKind = "search"
	ToolExecute    ToolKind = "execute"
	ToolThink      ToolKind = "think"
	ToolFetch      ToolKind = "fetch"
	ToolSwitchMode ToolKind = "switch_mode"
	ToolOther      ToolKind = "other"
)

func ParseToolKind(s string) ToolKind {
	switch k := ToolKind(normalise(s)); k {
	case ToolRead, ToolEdit, ToolDelete, ToolMove, ToolSearch,
		ToolExecute, ToolThink, ToolFetch, ToolSwitchMode:
		return k
	default:
		return ToolOther
	}
}

// Mutates reports whether a completed call of this kind changed the workspace.
func (k ToolKind) Mutates() bool {
	switch k {
	case ToolEdit, ToolDelete, ToolMove:
		return true
	default:
		return false
	}
}

// ToolCallStatus tracks one tool call through its lifecycle.
type ToolCallStatus string

const (
	ToolPending    ToolCallStatus = "pending"
	ToolInProgress ToolCallStatus = "in_progress"
	ToolCompleted  ToolCallStatus = "completed"
	ToolFailed     ToolCallStatus = "failed"
	ToolStatusNone ToolCallStatus = ""
)

func ParseToolCallStatus(s string) ToolCallStatus {
	switch st := ToolCallStatus(normalise(s)); st {
	case ToolPending, ToolInProgress, ToolCompleted, ToolFailed:
		return st
	default:
		return ToolStatusNone
	}
}

// PlanEntryStatus tracks one plan entry. Incomplete entries at
// max_turn_requests are quoted into the stuck reason — "which of your own
// steps did you not finish" is the most actionable thing we can tell a human.
type PlanEntryStatus string

const (
	PlanPending    PlanEntryStatus = "pending"
	PlanInProgress PlanEntryStatus = "in_progress"
	PlanCompleted  PlanEntryStatus = "completed"
)

func ParsePlanEntryStatus(s string) PlanEntryStatus {
	switch st := PlanEntryStatus(normalise(s)); st {
	case PlanInProgress, PlanCompleted:
		return st
	default:
		return PlanPending
	}
}

// normalise folds camelCase and kebab-case onto snake_case and lowercases, so
// `maxTurnRequests`, `max-turn-requests` and `MAX_TURN_REQUESTS` all match.
func normalise(s string) string {
	var b strings.Builder
	b.Grow(len(s) + 4)
	for i, r := range s {
		switch {
		case r == '-' || r == ' ':
			b.WriteByte('_')
		case r >= 'A' && r <= 'Z':
			if i > 0 && s[i-1] != '_' && s[i-1] != '-' && s[i-1] != ' ' {
				b.WriteByte('_')
			}
			b.WriteRune(r + ('a' - 'A'))
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// --- raw protocol decoding -------------------------------------------------

// rpcLine is the minimum needed to route one JSON-RPC line off the agent's
// stdout. Everything else is decoded lazily, per method.
type rpcLine struct {
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

// sessionUpdateParams is the params object of a session/update notification.
type sessionUpdateParams struct {
	SessionID string          `json:"sessionId"`
	Update    json.RawMessage `json:"update"`
}

// contentBlock is an ACP content block. Only text carries anything we use.
type contentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type toolLocation struct {
	Path string `json:"path"`
}

type planEntry struct {
	Content string `json:"content"`
	Status  string `json:"status"`
}

// updateEnvelope is a permissive union over all eleven session/update variants.
// Every field is optional; the `sessionUpdate` discriminator says which are
// meaningful. Decoding permissively (rather than per-variant) is what makes an
// unknown twelfth variant a no-op instead of a decode error.
type updateEnvelope struct {
	SessionUpdate string `json:"sessionUpdate"`

	// message / thought chunks
	Content *contentBlock `json:"content"`

	// tool_call / tool_call_update
	ToolCallID string         `json:"toolCallId"`
	Title      string         `json:"title"`
	Kind       string         `json:"kind"`
	Status     string         `json:"status"`
	Locations  []toolLocation `json:"locations"`

	// plan
	Entries []planEntry `json:"entries"`

	// current_mode_update
	CurrentModeID string `json:"currentModeId"`

	// session_info_update
	SessionID string `json:"sessionId"`

	// usage_update, ACP v1 shape: {used, size, cost}
	Used *int64           `json:"used"`
	Size *int64           `json:"size"`
	Cost *json.RawMessage `json:"cost"`

	// usage_update, coder/acp-go-sdk 0.13.x shape: {input:{tokens}, output:{tokens}}
	Input  *tokenSide `json:"input"`
	Output *tokenSide `json:"output"`
}

type tokenSide struct {
	Tokens *int64 `json:"tokens"`
}

// usageEvent is the normalised result of decoding a usage_update in EITHER
// shape. Every field is optional because the protocol makes them optional —
// notably `cost`, which is why noLLMTraffic in pkg/worker must not key on cost
// alone.
type usageEvent struct {
	Used, Size                *int64
	InputTokens, OutputTokens *int64
	CostUSD                   *float64
}

// decodeUsage reads both known usage shapes. `cost` is accepted as a bare
// number or as an object with an `amount`/`total`/`usd` field, because agents
// disagree and a cost we cannot read must be absent rather than zero.
func decodeUsage(e updateEnvelope) usageEvent {
	u := usageEvent{Used: e.Used, Size: e.Size}
	if e.Input != nil {
		u.InputTokens = e.Input.Tokens
	}
	if e.Output != nil {
		u.OutputTokens = e.Output.Tokens
	}
	if e.Cost != nil && len(*e.Cost) > 0 {
		raw := *e.Cost
		var num float64
		if err := json.Unmarshal(raw, &num); err == nil {
			u.CostUSD = &num
			return u
		}
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(raw, &obj); err == nil {
			for _, k := range []string{"amount", "total", "usd", "totalUsd", "costUsd"} {
				if v, ok := obj[k]; ok {
					if err := json.Unmarshal(v, &num); err == nil {
						u.CostUSD = &num
						return u
					}
				}
			}
		}
	}
	return u
}
