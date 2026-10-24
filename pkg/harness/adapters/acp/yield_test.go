package acp

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/harness"
)

// fakeAgentMidTool starts an edit when prompted, finishes it on its own
// after a second, and answers a session/cancel with the cancelled stop
// reason. It writes "tool done" and "cancel" to log in the order they happen.
func fakeAgentMidTool(log string) string {
	return `
emit() { printf '%s\n' "$1"; }
prompt=
while IFS= read -r line; do
  id=$(printf '%s' "$line" | sed -n 's/.*"id":\([0-9]*\).*/\1/p')
  case "$line" in
    *'"method":"initialize"'*)
      emit '{"jsonrpc":"2.0","id":'"$id"',"result":{"protocolVersion":1,"agentCapabilities":{},"authMethods":[]}}' ;;
    *'"method":"session/new"'*)
      emit '{"jsonrpc":"2.0","id":'"$id"',"result":{"sessionId":"s1"}}' ;;
    *'"method":"session/prompt"'*)
      prompt=$id
      emit '{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":{"sessionUpdate":"tool_call","toolCallId":"t1","kind":"edit","status":"in_progress","title":"edit main.go"}}}'
      ( sleep 1
        echo "tool done" >> '` + log + `'
        emit '{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":{"sessionUpdate":"tool_call_update","toolCallId":"t1","status":"completed"}}}' ) & ;;
    *'"method":"session/cancel"'*)
      echo "cancel" >> '` + log + `'
      emit '{"jsonrpc":"2.0","id":'"$prompt"',"result":{"stopReason":"cancelled"}}' ;;
  esac
done
`
}

func TestAdapter_Yields(t *testing.T) {
	a, err := New("custom", ProfileOverrides{Argv: []string{"agent"}}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !harness.AdapterYields(a) {
		t.Fatal("an ACP adapter must declare that it yields")
	}
}

// ADR-0142: a yield lets the running tool call finish, then cancels the turn
// over the protocol and returns ErrYielded.
func TestRun_YieldWaitsForTheRunningToolThenCancelsTheTurn(t *testing.T) {
	log := t.TempDir() + "/order"
	bin := writeScript(t, fakeAgentMidTool(log))
	a, err := New("custom", ProfileOverrides{Argv: []string{bin}}, Options{
		PromptTimeout: time.Minute, IdleTimeout: time.Minute, CancelGrace: 5 * time.Second, TermGrace: time.Second,
		YieldGrace: 30 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	yield := make(chan struct{})
	env := testEnv(t)
	env.Yield = yield
	catchUps := 0
	env.CatchUp = func(context.Context) string { catchUps++; return "changed" }
	go func() { time.Sleep(300 * time.Millisecond); close(yield) }()

	start := time.Now()
	_, err = a.Run(context.Background(), testSpec(), env)
	if !errors.Is(err, harness.ErrYielded) {
		t.Fatalf("err = %v, want ErrYielded", err)
	}
	if took := time.Since(start); took > 20*time.Second {
		t.Fatalf("yield took %s", took)
	}
	raw, _ := os.ReadFile(log)
	if got := strings.Fields(strings.ReplaceAll(string(raw), "tool done", "tool_done")); len(got) != 2 || got[0] != "tool_done" || got[1] != "cancel" {
		t.Fatalf("order = %q, want the tool to finish before the cancel", raw)
	}
	if catchUps != 0 {
		t.Fatalf("a yielded Run caught up %d times", catchUps)
	}
}

// A tool that never finishes does not hold the yield past its grace.
func TestRun_YieldCancelsAfterItsGraceWhenAToolNeverFinishes(t *testing.T) {
	log := t.TempDir() + "/order"
	script := strings.Replace(fakeAgentMidTool(log), "sleep 1", "sleep 300", 1)
	bin := writeScript(t, script)
	a, err := New("custom", ProfileOverrides{Argv: []string{bin}}, Options{
		PromptTimeout: time.Minute, IdleTimeout: time.Minute, CancelGrace: 5 * time.Second, TermGrace: time.Second,
		YieldGrace: 500 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	yield := make(chan struct{})
	go func() { time.Sleep(300 * time.Millisecond); close(yield) }()
	env := testEnv(t)
	env.Yield = yield
	start := time.Now()
	if _, err := a.Run(context.Background(), testSpec(), env); !errors.Is(err, harness.ErrYielded) {
		t.Fatalf("err = %v, want ErrYielded", err)
	}
	if took := time.Since(start); took < 800*time.Millisecond {
		t.Fatalf("yield took %s, less than the tool's grace", took)
	}
	raw, _ := os.ReadFile(log)
	if strings.TrimSpace(string(raw)) != "cancel" {
		t.Fatalf("log = %q, want only the cancel", raw)
	}
}

// A Run nobody asked to yield ends as it always did.
func TestRun_WithoutAYieldReturnsNoYield(t *testing.T) {
	bin := writeScript(t, fakeAgentScript(`"end_turn"`, ``))
	_, err := runAdapter(t, bin, time.Minute)
	if errors.Is(err, harness.ErrYielded) {
		t.Fatal("an unasked Run reported a yield")
	}
}
