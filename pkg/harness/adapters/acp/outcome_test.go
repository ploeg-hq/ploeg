package acp

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/work"
)

// feed applies a sequence of session/update payloads, written as JSON exactly
// as an agent would send them. Fixtures rather than Go structs on purpose:
// these tests must catch a decoding change, not just a mapping change.
func feed(t *testing.T, payloads ...string) *sessionState {
	t.Helper()
	s := newSessionState()
	for _, p := range payloads {
		s.applyUpdate(json.RawMessage(p))
	}
	return s
}

const (
	msg      = `{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":%q}}`
	thought  = `{"sessionUpdate":"agent_thought_chunk","content":{"type":"text","text":%q}}`
	editDone = `{"sessionUpdate":"tool_call","toolCallId":"t1","title":"edit main.go","kind":"edit","status":"completed","locations":[{"path":"main.go"}]}`
	readDone = `{"sessionUpdate":"tool_call","toolCallId":"t2","title":"read","kind":"read","status":"completed"}`
)

func TestBuild_StopReasonMatrix(t *testing.T) {
	tests := []struct {
		name        string
		state       *sessionState
		res         result
		wantOutcome work.Outcome
		wantFailure work.FailureReason
		reasonHas   string
	}{
		{
			name:        "lease lost outranks everything",
			state:       feed(t, editDone),
			res:         result{phase: phasePrompt, ctxErr: errors.New("context canceled")},
			wantOutcome: work.OutcomeFailed,
			wantFailure: work.FailureLeaseLost,
		},
		{
			name:        "binary missing is infra_node, not a parked ticket",
			state:       newSessionState(),
			res:         result{phase: phaseLaunch, err: errors.New("exec: \"opencode\": not found")},
			wantOutcome: work.OutcomeFailed,
			wantFailure: work.FailureInfraNode,
			reasonHas:   "not found",
		},
		{
			name:        "unspeakable protocol version is infra_node",
			state:       newSessionState(),
			res:         result{phase: phaseInit, err: errors.New("protocolVersion 2 unsupported")},
			wantOutcome: work.OutcomeFailed,
			wantFailure: work.FailureInfraNode,
		},
		{
			name:        "expired key on session/new is infra_llm",
			state:       newSessionState(),
			res:         result{phase: phaseNewSess, err: errors.New("auth_required: 401 from upstream")},
			wantOutcome: work.OutcomeFailed,
			wantFailure: work.FailureInfraLLM,
		},
		{
			name:        "rate limit on session/new is infra_llm",
			state:       newSessionState(),
			res:         result{phase: phaseNewSess, stderrTail: "HTTP 429 quota exceeded"},
			wantOutcome: work.OutcomeFailed,
			wantFailure: work.FailureInfraLLM,
		},
		{
			name:        "unrecognised session/new failure stays infra_node",
			state:       newSessionState(),
			res:         result{phase: phaseNewSess, err: errors.New("disk full")},
			wantOutcome: work.OutcomeFailed,
			wantFailure: work.FailureInfraNode,
		},
		{
			name:        "idle watchdog is retryable, not stuck",
			state:       feed(t, editDone),
			res:         result{phase: phasePrompt, watchdog: &watchdogFire{kind: watchdogIdle, limit: 10 * time.Minute, events: 29}},
			wantOutcome: work.OutcomeFailed,
			wantFailure: work.FailureAgentError,
			reasonHas:   "no protocol activity",
		},
		{
			name:        "prompt wall is retryable, not stuck",
			state:       feed(t, editDone),
			res:         result{phase: phasePrompt, watchdog: &watchdogFire{kind: watchdogPromptWall, limit: 45 * time.Minute}},
			wantOutcome: work.OutcomeFailed,
			wantFailure: work.FailureAgentError,
			reasonHas:   "prompt ran past",
		},
		{
			name:        "transport death mid-prompt is retryable",
			state:       feed(t, readDone),
			res:         result{phase: phasePrompt, err: errors.New("EOF"), exitCode: 1},
			wantOutcome: work.OutcomeFailed,
			wantFailure: work.FailureAgentError,
		},
		{
			name:        "permission storm parks for a human",
			state:       feed(t, readDone),
			res:         result{phase: phasePrompt, permStorm: true},
			wantOutcome: work.OutcomeStuck,
			reasonHas:   "permission requests",
		},
		{
			name:        "refusal parks with the refusal text as the reason",
			state:       feed(t, jsonf(msg, "I won't modify production credentials.")),
			res:         result{phase: phaseCompleted, stop: StopRefusal},
			wantOutcome: work.OutcomeStuck,
			reasonHas:   "production credentials",
		},
		{
			name: "turn budget parks and names the unfinished plan steps",
			state: feed(t, `{"sessionUpdate":"plan","entries":[
				{"content":"add the migration","status":"completed"},
				{"content":"wire the store method","status":"in_progress"},
				{"content":"update the chart","status":"pending"}]}`),
			res:         result{phase: phaseCompleted, stop: StopMaxTurnRequests},
			wantOutcome: work.OutcomeStuck,
			reasonHas:   "wire the store method",
		},
		{
			name:        "token ceiling is budget and retryable",
			state:       feed(t, editDone),
			res:         result{phase: phaseCompleted, stop: StopMaxTokens},
			wantOutcome: work.OutcomeFailed,
			wantFailure: work.FailureBudget,
		},
		{
			name: "context exhaustion explains the token ceiling",
			state: feed(t, editDone,
				`{"sessionUpdate":"usage_update","used":199000,"size":200000}`),
			res:         result{phase: phaseCompleted, stop: StopMaxTokens},
			wantOutcome: work.OutcomeFailed,
			wantFailure: work.FailureBudget,
			reasonHas:   "context window",
		},
		{
			name:        "agent-initiated cancel parks",
			state:       feed(t, readDone),
			res:         result{phase: phaseCompleted, stop: StopCancelled},
			wantOutcome: work.OutcomeStuck,
		},
		{
			name:        "end_turn with no mutation is no_change_needed",
			state:       feed(t, readDone, jsonf(msg, "Nothing to change; the fix is already present.")),
			res:         result{phase: phaseCompleted, stop: StopEndTurn},
			wantOutcome: work.OutcomeNoChangeNeeded,
		},
		{
			name:        "end_turn WITH mutation defers to the forge",
			state:       feed(t, editDone),
			res:         result{phase: phaseCompleted, stop: StopEndTurn},
			wantOutcome: "", // no structured signal; the worker's PR poll decides
		},
		{
			name:        "an unknown stop reason behaves as end_turn",
			state:       feed(t, editDone),
			res:         result{phase: phaseCompleted, stop: ParseStopReason("teleported")},
			wantOutcome: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Build(tc.state, tc.res)
			if got.Outcome != tc.wantOutcome {
				t.Errorf("outcome = %q, want %q", got.Outcome, tc.wantOutcome)
			}
			if got.FailureReason != string(tc.wantFailure) {
				t.Errorf("failureReason = %q, want %q", got.FailureReason, tc.wantFailure)
			}
			if tc.reasonHas != "" && !strings.Contains(got.StuckReason, tc.reasonHas) {
				t.Errorf("stuckReason %q does not contain %q", got.StuckReason, tc.reasonHas)
			}
			// R4, enforced at the source rather than only at the report API.
			if got.Outcome == work.OutcomeStuck && strings.TrimSpace(got.StuckReason) == "" {
				t.Error("stuck outcome with no reason (R4)")
			}
			if got.FailureReason != "" && !work.FailureReason(got.FailureReason).Valid() {
				t.Errorf("failureReason %q is not in the taxonomy", got.FailureReason)
			}
			// An adapter never asserts forge state; only the worker polls.
			if got.Outcome == work.OutcomePROpened || got.Outcome == work.OutcomePRUpdated {
				t.Errorf("adapter asserted forge state (%q)", got.Outcome)
			}
		})
	}
}

func TestBuild_UsageOnlyWhenVolunteered(t *testing.T) {
	t.Run("silent agent yields nil usage", func(t *testing.T) {
		// A zero-valued Usage would trip pkg/worker's VIK-586 heuristic and
		// relabel a real agent error as an LLM infra failure.
		if got := Build(feed(t, `{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"done"}}`), result{phase: phaseCompleted, stop: StopEndTurn}); got.Usage != nil {
			t.Errorf("usage = %+v, want nil (unknown, not zero)", got.Usage)
		}
	})

	t.Run("tool calls alone are kept, and are not a zero usage", func(t *testing.T) {
		u := Build(feed(t, readDone), result{phase: phaseCompleted, stop: StopEndTurn}).Usage
		if u == nil || u.ToolCalls == nil || *u.ToolCalls != 1 || u.ToolCallsByKind["read"] != 1 {
			t.Fatalf("usage = %+v, want one read tool call", u)
		}
		if !u.HasActivity() {
			t.Error("a Run with tool calls must count as model traffic for pkg/worker")
		}
		if u.PeakContextTokens != nil || u.ContextWindowTokens != nil {
			t.Errorf("context figures = %v/%v, want absent", u.PeakContextTokens, u.ContextWindowTokens)
		}
	})

	t.Run("acp v1 shape", func(t *testing.T) {
		s := feed(t, `{"sessionUpdate":"usage_update","used":1234,"size":200000,"cost":0.42}`)
		u := Build(s, result{phase: phaseCompleted, stop: StopEndTurn}).Usage
		if u == nil {
			t.Fatal("usage = nil")
		}
		if u.CostUSD != 0.42 {
			t.Errorf("cost = %v, want 0.42", u.CostUSD)
		}
		if u.InputTokens != 1234 {
			t.Errorf("inputTokens = %d, want 1234 (used, so there IS token evidence)", u.InputTokens)
		}
	})

	t.Run("sdk 0.13.x token shape", func(t *testing.T) {
		s := feed(t, `{"sessionUpdate":"usage_update","input":{"tokens":900},"output":{"tokens":120}}`)
		u := Build(s, result{phase: phaseCompleted, stop: StopEndTurn}).Usage
		if u == nil {
			t.Fatal("usage = nil")
		}
		if u.InputTokens != 900 || u.OutputTokens != 120 {
			t.Errorf("tokens = %d/%d, want 900/120", u.InputTokens, u.OutputTokens)
		}
		// cost is optional in ACP; absent must stay zero, and the worker must
		// not read that as "no LLM traffic" — hence the token evidence above.
		if u.CostUSD != 0 {
			t.Errorf("cost = %v, want 0 (absent)", u.CostUSD)
		}
	})

	t.Run("cost as an object", func(t *testing.T) {
		s := feed(t, `{"sessionUpdate":"usage_update","used":10,"size":100,"cost":{"amount":1.25,"currency":"USD"}}`)
		u := Build(s, result{phase: phaseCompleted, stop: StopEndTurn}).Usage
		if u == nil || u.CostUSD != 1.25 {
			t.Errorf("usage = %+v, want cost 1.25", u)
		}
	})

	t.Run("unreadable cost is absent, never zero-by-accident", func(t *testing.T) {
		s := feed(t, `{"sessionUpdate":"usage_update","used":10,"size":100,"cost":"lots"}`)
		u := Build(s, result{phase: phaseCompleted, stop: StopEndTurn}).Usage
		if u == nil {
			t.Fatal("usage = nil")
		}
		if u.CostUSD != 0 {
			t.Errorf("cost = %v, want 0", u.CostUSD)
		}
		if u.InputTokens != 10 {
			t.Errorf("token evidence lost: %+v", u)
		}
	})

	t.Run("session id is the resume handle", func(t *testing.T) {
		s := feed(t, `{"sessionUpdate":"session_info_update","sessionId":"sess-7"}`)
		u := Build(s, result{phase: phaseCompleted, stop: StopEndTurn}).Usage
		if u == nil || u.SessionID != "sess-7" {
			t.Errorf("usage = %+v, want sessionId sess-7", u)
		}
	})
}

func TestState_ToolCallFolding(t *testing.T) {
	// One call announced pending, then updated to completed, stays one call.
	s := feed(t,
		`{"sessionUpdate":"tool_call","toolCallId":"t1","title":"edit","kind":"edit","status":"pending","locations":[{"path":"a.go"}]}`,
		`{"sessionUpdate":"tool_call_update","toolCallId":"t1","status":"in_progress"}`,
		`{"sessionUpdate":"tool_call_update","toolCallId":"t1","status":"completed","locations":[{"path":"b.go"}]}`)
	if !s.mutated() {
		t.Error("completed edit did not register as a mutation")
	}
	if len(s.tools) != 1 {
		t.Fatalf("tracked %d calls, want the one call folded", len(s.tools))
	}
	if paths := s.tools["t1"].Paths; len(paths) != 2 || paths[0] != "a.go" || paths[1] != "b.go" {
		t.Errorf("paths = %v, want [a.go b.go]", paths)
	}
}

func TestBuild_KeepsToolTallyAndPeakContext(t *testing.T) {
	s := feed(t,
		editDone,
		`{"sessionUpdate":"tool_call_update","toolCallId":"t1","status":"completed"}`,
		readDone,
		`{"sessionUpdate":"tool_call","toolCallId":"t3","title":"go test","kind":"execute","status":"pending"}`,
		`{"sessionUpdate":"tool_call","toolCallId":"t4","title":"mystery"}`,
		`{"sessionUpdate":"usage_update","used":150000,"size":200000}`,
		`{"sessionUpdate":"usage_update","used":90000,"size":200000}`)
	u := Build(s, result{phase: phaseCompleted, stop: StopEndTurn}).Usage
	if u == nil {
		t.Fatal("usage = nil")
	}
	if u.ToolCalls == nil || *u.ToolCalls != 4 {
		t.Errorf("toolCalls = %v, want 4 (an update folds into its call)", u.ToolCalls)
	}
	want := map[string]int64{"edit": 1, "read": 1, "execute": 1, "other": 1}
	if len(u.ToolCallsByKind) != len(want) {
		t.Errorf("toolCallsByKind = %v, want %v", u.ToolCallsByKind, want)
	}
	for k, n := range want {
		if u.ToolCallsByKind[k] != n {
			t.Errorf("toolCallsByKind[%s] = %d, want %d", k, u.ToolCallsByKind[k], n)
		}
	}
	if u.PeakContextTokens == nil || *u.PeakContextTokens != 150000 {
		t.Errorf("peakContextTokens = %v, want 150000 (the peak, not the last fill)", u.PeakContextTokens)
	}
	if u.ContextWindowTokens == nil || *u.ContextWindowTokens != 200000 {
		t.Errorf("contextWindowTokens = %v, want 200000", u.ContextWindowTokens)
	}
}

func TestBuild_UsageWithoutToolCallsReportsZeroCalls(t *testing.T) {
	u := Build(feed(t, `{"sessionUpdate":"usage_update","input":{"tokens":10},"output":{"tokens":2}}`),
		result{phase: phaseCompleted, stop: StopEndTurn}).Usage
	if u == nil || u.ToolCalls == nil || *u.ToolCalls != 0 || u.ToolCallsByKind != nil {
		t.Errorf("usage = %+v, want toolCalls 0 and no split", u)
	}
}

func TestBuild_CountsToolCallsPastTheTrackingCap(t *testing.T) {
	var payloads []string
	for i := 0; i < maxTrackedTools+3; i++ {
		payloads = append(payloads, `{"sessionUpdate":"tool_call","toolCallId":"c`+itoa(i)+`","kind":"read","status":"pending"}`)
	}
	payloads = append(payloads, `{"sessionUpdate":"tool_call_update","toolCallId":"c`+itoa(maxTrackedTools+1)+`","status":"completed"}`)
	u := Build(feed(t, payloads...), result{phase: phaseCompleted, stop: StopEndTurn}).Usage
	if u == nil || u.ToolCalls == nil || *u.ToolCalls != int64(maxTrackedTools+3) {
		t.Fatalf("toolCalls = %v, want %d", u.ToolCalls, maxTrackedTools+3)
	}
	if u.ToolCallsByKind["read"] != int64(maxTrackedTools+3) {
		t.Errorf("toolCallsByKind = %v", u.ToolCallsByKind)
	}
}

func TestState_PendingEditIsNotAMutation(t *testing.T) {
	// An edit that never completed did not change anything, and treating it as
	// a mutation would turn a clean no-op run into a false "changed files
	// without a PR" park.
	s := feed(t, `{"sessionUpdate":"tool_call","toolCallId":"t1","kind":"edit","status":"pending"}`)
	if s.mutated() {
		t.Error("pending edit counted as a mutation")
	}
}

func TestState_ReadIsNotAMutation(t *testing.T) {
	if feed(t, readDone).mutated() {
		t.Error("completed read counted as a mutation")
	}
}

func TestState_UnknownVariantIsIgnoredNotFatal(t *testing.T) {
	s := feed(t,
		`{"sessionUpdate":"quantum_entanglement_update","spooky":true}`,
		editDone)
	if !s.mutated() {
		t.Error("an unknown variant swallowed the following event")
	}
	if got := s.unknownEnumValues(); len(got) != 1 || got[0] != "quantum_entanglement_update" {
		t.Errorf("unknownEnumValues = %v", got)
	}
}

func TestState_UndecodablePayloadIsSurvivable(t *testing.T) {
	s := newSessionState()
	s.applyUpdate(json.RawMessage(`{"sessionUpdate":`)) // truncated
	s.applyUpdate(json.RawMessage(editDone))
	if !s.mutated() {
		t.Error("a malformed payload killed the accumulator")
	}
}

func TestState_UserMessageIsNotEchoedIntoSummary(t *testing.T) {
	s := feed(t,
		`{"sessionUpdate":"user_message_chunk","content":{"type":"text","text":"SECRET TICKET BODY"}}`,
		jsonf(msg, "done"))
	if strings.Contains(s.lastMessage(), "SECRET") {
		t.Error("the prompt echo leaked into the agent message ring")
	}
}

func TestState_RingsAreBounded(t *testing.T) {
	huge := strings.Repeat("x", maxMessageBytes*2)
	s := feed(t, jsonf(msg, huge))
	if got := len(s.lastMessage()); got > maxMessageBytes {
		t.Errorf("message ring grew to %d, cap is %d", got, maxMessageBytes)
	}
}

func TestParsers_TolerateSpellingAndUnknowns(t *testing.T) {
	for in, want := range map[string]StopReason{
		"end_turn": StopEndTurn, "endTurn": StopEndTurn,
		"max_turn_requests": StopMaxTurnRequests, "maxTurnRequests": StopMaxTurnRequests,
		"canceled": StopCancelled, "cancelled": StopCancelled,
		"refusal": StopRefusal, "": StopUnknown, "nonsense": StopUnknown,
	} {
		if got := ParseStopReason(in); got != want {
			t.Errorf("ParseStopReason(%q) = %q, want %q", in, got, want)
		}
	}
	for in, want := range map[string]ToolKind{
		"edit": ToolEdit, "switchMode": ToolSwitchMode, "switch_mode": ToolSwitchMode,
		"": ToolOther, "teleport": ToolOther,
	} {
		if got := ParseToolKind(in); got != want {
			t.Errorf("ParseToolKind(%q) = %q, want %q", in, got, want)
		}
	}
	for k, want := range map[ToolKind]bool{
		ToolEdit: true, ToolDelete: true, ToolMove: true,
		ToolRead: false, ToolExecute: false, ToolOther: false,
	} {
		if got := k.Mutates(); got != want {
			t.Errorf("%q.Mutates() = %v, want %v", k, got, want)
		}
	}
}

func jsonf(format, arg string) string {
	b, err := json.Marshal(arg)
	if err != nil {
		panic(err)
	}
	return strings.Replace(format, "%q", string(b), 1)
}

func TestBuild_NamesTheWatchdogThatStoppedTheRun(t *testing.T) {
	tests := []struct {
		name        string
		fire        watchdogFire
		wantSummary string
		wantReason  string
	}{
		{
			name:        "idle watchdog",
			fire:        watchdogFire{kind: watchdogIdle, limit: 10 * time.Minute, events: 29},
			wantSummary: "acp idle watchdog stopped the agent: no protocol activity for 10m0s",
			wantReason:  "no protocol activity for 10m0s after 29 events",
		},
		{
			name:        "prompt wall",
			fire:        watchdogFire{kind: watchdogPromptWall, limit: 45 * time.Minute},
			wantSummary: "acp prompt wall stopped the agent: the turn ran past 45m0s",
			wantReason:  "prompt ran past 45m0s",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fire := tc.fire
			got := Build(feed(t, jsonf(msg, "still thinking")), result{phase: phasePrompt, watchdog: &fire})
			if got.Summary != tc.wantSummary {
				t.Errorf("summary = %q, want %q", got.Summary, tc.wantSummary)
			}
			if !strings.HasPrefix(got.StuckReason, tc.wantReason) {
				t.Errorf("stuckReason = %q, want it to start with %q", got.StuckReason, tc.wantReason)
			}
			if !strings.Contains(got.StuckReason, "still thinking") {
				t.Errorf("stuckReason = %q lost the agent's last message", got.StuckReason)
			}
		})
	}
}
