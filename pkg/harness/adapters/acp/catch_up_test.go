package acp

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

// ADR-0124: when the agent's turn ends on its own, the adapter asks for the
// board's changes once and sends a non-empty answer as one further prompt
// turn; an empty answer sends nothing more.
func TestRun_CatchesUpWithTheBoardInOneFurtherTurn(t *testing.T) {
	for _, tc := range []struct {
		name    string
		answer  string
		prompts int
	}{
		{"changed", "# Board changes since your briefing\n\n- the ticket changed", 2},
		{"unchanged", "", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			log := t.TempDir() + "/prompts"
			bin := writeScript(t, fakeAgentScript(`"end_turn"`, `printf '%s\n' "$line" >> '`+log+`'`))
			a, err := New("custom", ProfileOverrides{Argv: []string{bin}}, Options{
				PromptTimeout: time.Minute, IdleTimeout: time.Minute, CancelGrace: time.Second, TermGrace: time.Second,
			})
			if err != nil {
				t.Fatal(err)
			}
			asked := 0
			env := testEnv(t)
			env.CatchUp = func(context.Context) string { asked++; return tc.answer }
			if _, err := a.Run(context.Background(), testSpec(), env); err != nil {
				t.Fatalf("run error: %v", err)
			}
			raw, _ := os.ReadFile(log)
			lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
			if asked != 1 || len(lines) != tc.prompts {
				t.Fatalf("asked %d times, %d prompts: %s", asked, len(lines), raw)
			}
			if tc.prompts == 2 && !strings.Contains(lines[1], "the ticket changed") {
				t.Fatalf("second prompt lacks the changes: %s", lines[1])
			}
		})
	}
}
