package acp

import (
	"encoding/json"
	"sort"
	"strings"
	"sync"
)

// Ring sizes. The message ring feeds Summary and the fallback StuckReason;
// the thought ring is a last resort only. Both are bounded because an agent
// that loops can emit megabytes, and the whole report ends up in a Postgres
// row.
const (
	maxMessageBytes = 16 << 10
	maxThoughtBytes = 4 << 10
	maxTrackedTools = 512
)

// toolCall is one tool invocation, folded across its tool_call and any number
// of tool_call_update events.
type toolCall struct {
	ID      string
	Title   string
	Kind    ToolKind
	Status  ToolCallStatus
	Paths   []string
	seq     int // arrival order, for stable reporting
	failMsg string
}

// sessionState accumulates everything the protocol told us about one run.
//
// Concurrency: every mutator is called from the protocol read loop and takes
// the mutex; the outcome builder reads under the same mutex after the loop has
// stopped. Mutators must never block — no I/O, no unbuffered sends — because
// blocking here wedges the whole JSON-RPC dispatcher.
type sessionState struct {
	mu sync.Mutex

	sessionID string
	mode      string

	messages strings.Builder
	thoughts strings.Builder

	tools   map[string]*toolCall
	toolSeq int

	untrackedTools       int64
	untrackedToolsByKind map[ToolKind]int64

	plan []planEntry

	// usage, accumulated. Pointers stay nil until an agent volunteers a value:
	// nil means "unknown", which pkg/worker already treats differently from
	// zero. Never synthesise a zero-valued Usage.
	usedTokens, sizeTokens *int64
	inTokens, outTokens    *int64
	costUSD                *float64
	sawUsage               bool

	// unknownKinds records enum values we could not parse, so the adapter can
	// log each distinct one exactly once instead of per event.
	unknownKinds map[string]bool

	events int
}

func newSessionState() *sessionState {
	return &sessionState{
		tools:                map[string]*toolCall{},
		untrackedToolsByKind: map[ToolKind]int64{},
		unknownKinds:         map[string]bool{},
	}
}

// applyUpdate decodes and folds one session/update payload. It returns the
// parsed kind so the caller can drive checkpoints, and never errors: a payload
// we cannot read is counted and dropped, because a malformed event must not
// end an otherwise healthy run.
func (s *sessionState) applyUpdate(raw json.RawMessage) UpdateKind {
	var e updateEnvelope
	if err := json.Unmarshal(raw, &e); err != nil {
		s.mu.Lock()
		s.unknownKinds["<undecodable>"] = true
		s.mu.Unlock()
		return UpdateUnknown
	}
	kind := ParseUpdateKind(e.SessionUpdate)

	s.mu.Lock()
	defer s.mu.Unlock()
	s.events++

	switch kind {
	case UpdateAgentMessageChunk:
		if e.Content != nil {
			appendBounded(&s.messages, e.Content.Text, maxMessageBytes)
		}
	case UpdateAgentThoughtChunk:
		if e.Content != nil {
			appendBounded(&s.thoughts, e.Content.Text, maxThoughtBytes)
		}
	case UpdateUserMessageChunk:
		// Our own prompt echoed back. Ignored on purpose: including it would
		// let the ticket text leak into Summary and read as agent output.

	case UpdateToolCall, UpdateToolCallUpdate:
		s.foldToolCall(e, kind)

	case UpdatePlan:
		if e.Entries != nil {
			s.plan = e.Entries
		}

	case UpdateSessionInfo:
		if e.SessionID != "" {
			s.sessionID = e.SessionID
		}

	case UpdateCurrentMode:
		if e.CurrentModeID != "" {
			s.mode = e.CurrentModeID
		}

	case UpdateUsage:
		s.foldUsage(decodeUsage(e))

	case UpdateUnknown:
		if e.SessionUpdate != "" {
			s.unknownKinds[e.SessionUpdate] = true
		}
	}
	return kind
}

// foldToolCall registers a new call or folds a status update onto an existing
// one, so a call that is announced then updated three times stays one entry.
func (s *sessionState) foldToolCall(e updateEnvelope, kind UpdateKind) {
	id := e.ToolCallID
	if id == "" {
		// An update with no id cannot be correlated; treat it as its own call
		// rather than dropping the evidence.
		id = "anon-" + itoa(s.toolSeq)
	}
	tc, ok := s.tools[id]
	if !ok {
		if len(s.tools) >= maxTrackedTools {
			if kind == UpdateToolCall {
				s.untrackedTools++
				s.untrackedToolsByKind[toolKindOrOther(e.Kind)]++
			}
			return // runaway agent; the ones we have are representative
		}
		tc = &toolCall{ID: id, seq: s.toolSeq}
		s.toolSeq++
		s.tools[id] = tc
	}
	if e.Title != "" {
		tc.Title = e.Title
	}
	if e.Kind != "" {
		tc.Kind = ParseToolKind(e.Kind)
	}
	if st := ParseToolCallStatus(e.Status); st != ToolStatusNone {
		tc.Status = st
	} else if kind == UpdateToolCall && tc.Status == ToolStatusNone {
		tc.Status = ToolPending
	}
	for _, l := range e.Locations {
		if l.Path != "" && !contains(tc.Paths, l.Path) {
			tc.Paths = append(tc.Paths, l.Path)
		}
	}
	if tc.Status == ToolFailed && e.Content != nil && e.Content.Text != "" {
		tc.failMsg = e.Content.Text
	}
}

// foldUsage keeps the largest value seen for each field. Agents differ on
// whether usage_update is cumulative or incremental; max() is correct for
// cumulative and a defensible floor for incremental, and it can never report
// more than the agent claimed.
func (s *sessionState) foldUsage(u usageEvent) {
	s.sawUsage = true
	maxInto(&s.usedTokens, u.Used)
	maxInto(&s.sizeTokens, u.Size)
	maxInto(&s.inTokens, u.InputTokens)
	maxInto(&s.outTokens, u.OutputTokens)
	if u.CostUSD != nil && (s.costUSD == nil || *u.CostUSD > *s.costUSD) {
		v := *u.CostUSD
		s.costUSD = &v
	}
}

// --- readers (call after the read loop has stopped) ---

// mutated reports whether any workspace-changing tool call completed. This is
// the signal that separates "nothing to do" from "did the work and failed to
// push it".
func (s *sessionState) mutated() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, tc := range s.tools {
		if tc.Kind.Mutates() && tc.Status == ToolCompleted {
			return true
		}
	}
	return false
}

// failedTools lists calls that ended in failure, newest evidence first.
func (s *sessionState) failedTools() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var calls []*toolCall
	for _, tc := range s.tools {
		if tc.Status == ToolFailed {
			calls = append(calls, tc)
		}
	}
	sort.Slice(calls, func(i, j int) bool { return calls[i].seq < calls[j].seq })
	out := make([]string, 0, len(calls))
	for _, tc := range calls {
		label := tc.Title
		if label == "" {
			label = string(tc.Kind)
		}
		if tc.failMsg != "" {
			label += ": " + firstLine(tc.failMsg)
		}
		out = append(out, label)
	}
	return out
}

// incompletePlan lists plan entries the agent never finished.
func (s *sessionState) incompletePlan() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, e := range s.plan {
		if ParsePlanEntryStatus(e.Status) != PlanCompleted && strings.TrimSpace(e.Content) != "" {
			out = append(out, strings.TrimSpace(e.Content))
		}
	}
	return out
}

func (s *sessionState) lastMessage() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return strings.TrimSpace(s.messages.String())
}

func (s *sessionState) lastThought() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return strings.TrimSpace(s.thoughts.String())
}

// eventCount is the idle watchdog's definition of progress: any protocol
// event at all. A stuck agent emits nothing; a slow one still streams.
func (s *sessionState) eventCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.events
}

func (s *sessionState) session() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sessionID
}

// contextExhausted reports whether the agent was near its context ceiling,
// which turns a bare max_tokens into an explainable one.
func (s *sessionState) contextExhausted() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.usedTokens == nil || s.sizeTokens == nil || *s.sizeTokens == 0 {
		return false
	}
	return float64(*s.usedTokens)/float64(*s.sizeTokens) > 0.95
}

func (s *sessionState) unknownEnumValues() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.unknownKinds))
	for k := range s.unknownKinds {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func (s *sessionState) toolTallyLocked() (int64, map[string]int64) {
	total := int64(len(s.tools)) + s.untrackedTools
	if total == 0 {
		return 0, nil
	}
	byKind := map[string]int64{}
	for _, tc := range s.tools {
		byKind[string(toolKindOrOther(string(tc.Kind)))]++
	}
	for k, n := range s.untrackedToolsByKind {
		byKind[string(k)] += n
	}
	return total, byKind
}

// --- helpers ---

func toolKindOrOther(kind string) ToolKind {
	if kind == "" {
		return ToolOther
	}
	return ParseToolKind(kind)
}

func appendBounded(b *strings.Builder, s string, max int) {
	if s == "" || b.Len() >= max {
		return
	}
	if room := max - b.Len(); len(s) > room {
		s = s[:room]
	}
	b.WriteString(s)
}

func maxInto(dst **int64, v *int64) {
	if v == nil {
		return
	}
	if *dst == nil || *v > **dst {
		n := *v
		*dst = &n
	}
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return strings.TrimSpace(s)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
