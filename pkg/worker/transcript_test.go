//go:build unix

package worker

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/harness"
	"github.com/ploeg-hq/ploeg/pkg/harness/adapters/acp"
	"github.com/ploeg-hq/ploeg/pkg/llmbroker"
	"github.com/ploeg-hq/ploeg/pkg/transcript"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

type transcriptAPI struct {
	mu      sync.Mutex
	entries []transcript.Entry
	paths   []string
	status  int
}

func (a *transcriptAPI) server(t *testing.T) string {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a.mu.Lock()
		defer a.mu.Unlock()
		a.paths = append(a.paths, r.URL.Path)
		if strings.HasSuffix(r.URL.Path, "/transcript") {
			var body struct {
				Entries []transcript.Entry `json:"entries"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("transcript body: %v", err)
			}
			if a.status != 0 {
				w.WriteHeader(a.status)
				return
			}
			a.entries = append(a.entries, body.Entries...)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

const fakeACPAgent = `#!/bin/sh
emit() { printf '%s\n' "$1"; }
while IFS= read -r line; do
  id=$(printf '%s' "$line" | sed -n 's/.*"id":\([0-9]*\).*/\1/p')
  case "$line" in
    *'"method":"initialize"'*)
      emit '{"jsonrpc":"2.0","id":'"$id"',"result":{"protocolVersion":1,"agentCapabilities":{},"authMethods":[]}}' ;;
    *'"method":"session/new"'*)
      emit '{"jsonrpc":"2.0","id":'"$id"',"result":{"sessionId":"s1"}}' ;;
    *'"method":"session/prompt"'*)
      emit '{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":{"sessionUpdate":"agent_thought_chunk","content":{"type":"text","text":"Check the README."}}}}'
      emit '{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":{"sessionUpdate":"tool_call","toolCallId":"t1","title":"cat README.md","kind":"read","status":"completed","rawInput":{"command":"cat README.md"},"content":[{"type":"content","content":{"type":"text","text":"# example"}}]}}}'
      emit '{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"My key is '"$LLM_API_KEY"'; nothing to change."}}}}'
      emit '{"jsonrpc":"2.0","id":'"$id"',"result":{"stopReason":"end_turn"}}' ;;
  esac
done
`

func runACPAgainstFixture(t *testing.T, api *transcriptAPI, cfg Config) harness.OutcomeReport {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "agent.sh")
	if err := os.WriteFile(bin, []byte(fakeACPAgent), 0o755); err != nil {
		t.Fatal(err)
	}
	adapter, err := acp.New("custom", acp.ProfileOverrides{Argv: []string{bin}}, acp.Options{
		PromptTimeout: 20 * time.Second, IdleTimeout: 20 * time.Second, CancelGrace: time.Second, TermGrace: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	cfg.APIURL, cfg.ForgeURL = api.server(t), gitForge(t, map[string]string{"README.md": "# example\n"})
	cfg.DefaultForge, cfg.BuilderToken, cfg.ForgeTokenAccess = harness.ForgeForgejo, "tok", ForgeTokenReadOnly
	cfg.RepoOwner, cfg.RepoName, cfg.BaseBranch, cfg.WorkDir = "webgrip", "example", "development", t.TempDir()
	w := New(cfg, adapter, llmbroker.Static{Key: "sk-live-0123456789abcdef"}, discardLog())
	claimed := &ClaimResponse{RunToken: "rt", WorkItem: work.WorkItem{ID: "1", ExternalID: "7", Title: "t"}}
	return w.execute(context.Background(), claimed, "agent/vik-7", "trace", "", "")
}

func TestTheWorkerShipsTheAgentSessionAsATranscript(t *testing.T) {
	api := &transcriptAPI{}
	report := runACPAgainstFixture(t, api, Config{Transcript: true, TranscriptThoughts: true})
	if report.Outcome != work.OutcomeNoChangeNeeded {
		t.Fatalf("report = %+v", report)
	}
	api.mu.Lock()
	defer api.mu.Unlock()
	var lines []string
	for i, e := range api.entries {
		if e.Seq != int64(i+1) {
			t.Fatalf("entry %d has seq %d", i, e.Seq)
		}
		lines = append(lines, string(e.Kind)+": "+e.Text+e.Title+e.Input)
	}
	want := []string{
		"stage: Harness acp started for role default",
		"stage: Agent session started (custom)",
		"thought: Check the README.",
		"tool_call: cat README.mdcat README.md",
		"tool_output: # example",
		"message: My key is [redacted]; nothing to change.",
		"stage: Agent turn ended: end_turn",
		"stage: Harness acp finished: no_change_needed",
	}
	if strings.Join(lines, "\n") != strings.Join(want, "\n") {
		t.Fatalf("transcript:\n%s\nwant:\n%s", strings.Join(lines, "\n"), strings.Join(want, "\n"))
	}
	for _, p := range api.paths {
		if !strings.HasPrefix(p, "/api/v1/runs/rt/") {
			t.Fatalf("transcript sent to %s", p)
		}
	}
}

func TestATranscriptThatCannotBeDeliveredNeverChangesTheRun(t *testing.T) {
	for _, status := range []int{http.StatusNotFound, http.StatusInternalServerError} {
		api := &transcriptAPI{status: status}
		start := time.Now()
		report := runACPAgainstFixture(t, api, Config{Transcript: true})
		if report.Outcome != work.OutcomeNoChangeNeeded {
			t.Fatalf("HTTP %d: report = %+v", status, report)
		}
		if elapsed := time.Since(start); elapsed > 30*time.Second {
			t.Fatalf("HTTP %d: an undeliverable transcript delayed the Run by %s", status, elapsed)
		}
	}
}

func TestNoTranscriptIsSentWhenItIsSwitchedOff(t *testing.T) {
	api := &transcriptAPI{}
	runACPAgainstFixture(t, api, Config{})
	api.mu.Lock()
	defer api.mu.Unlock()
	for _, p := range api.paths {
		if strings.HasSuffix(p, "/transcript") {
			t.Fatal("a switched-off worker sent a transcript")
		}
	}
}
