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

	"github.com/ploeg-hq/ploeg/pkg/harness/adapters/acp"
	"github.com/ploeg-hq/ploeg/pkg/llmbroker"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

const promptFailingAgent = `#!/bin/sh
while IFS= read -r line; do
  id=$(printf '%s' "$line" | sed -n 's/.*"id":\([0-9]*\).*/\1/p')
  case "$line" in
    *'"method":"initialize"'*)
      printf '{"jsonrpc":"2.0","id":%s,"result":{"protocolVersion":1,"agentCapabilities":{},"authMethods":[]}}\n' "$id" ;;
    *'"method":"session/new"'*)
      curl -s -o /dev/null -H "Authorization: Bearer $LLM_API_KEY" "$LLM_BASE_URL/model/info"
      printf '{"jsonrpc":"2.0","id":%s,"result":{"sessionId":"s1"}}\n' "$id" ;;
    *'"method":"session/prompt"'*)
      if [ -n "$CALL_MODEL" ]; then
        curl -s -o /dev/null -H "Authorization: Bearer $LLM_API_KEY" -H 'Content-Type: application/json' -d '{}' "$LLM_BASE_URL/chat/completions"
      fi
      printf '{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":%s}}}}\n' "$(cat "$REPLAY_DIR/text.json")"
      printf '{"jsonrpc":"2.0","id":%s,"error":%s}\n' "$id" "$(cat "$REPLAY_DIR/error.json")" ;;
  esac
done
`

type pathGateway struct {
	mu    sync.Mutex
	paths []string
}

func (g *pathGateway) seen() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string(nil), g.paths...)
}

func replayPromptFailure(t *testing.T, harnessError string, callModel bool) (work.FailureReason, string, []string) {
	t.Helper()
	gw := &pathGateway{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gw.mu.Lock()
		gw.paths = append(gw.paths, r.Method+" "+r.URL.Path)
		gw.mu.Unlock()
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	t.Cleanup(srv.Close)

	dir := t.TempDir()
	text, _ := json.Marshal("Error: " + harnessError)
	errBody, _ := json.Marshal(map[string]any{"code": -32603, "message": "Internal error",
		"data": map[string]any{"details": harnessError, "reason": "Failed to process prompt"}})
	for name, body := range map[string][]byte{"text.json": text, "error.json": errBody} {
		if err := os.WriteFile(filepath.Join(dir, name), body, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	bin := filepath.Join(t.TempDir(), "agent.sh")
	if err := os.WriteFile(bin, []byte(promptFailingAgent), 0o755); err != nil {
		t.Fatal(err)
	}
	adapter, err := acp.New("custom", acp.ProfileOverrides{Argv: []string{bin}}, acp.Options{
		PromptTimeout: 20 * time.Second, IdleTimeout: 20 * time.Second, CancelGrace: time.Second, TermGrace: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	env := runEnv(t)
	env.LLM.BaseURL = srv.URL + "/v1"
	env.BaseEnv = append(env.BaseEnv, "REPLAY_DIR="+dir)
	if callModel {
		env.BaseEnv = append(env.BaseEnv, "CALL_MODEL=1")
	}
	report, _, _ := runAgent(context.Background(), discardLog(), evidenceBroker(), adapter, testTaskSpec(), env,
		llmbroker.MintRequest{RunToken: "abc123def456ff", BudgetUSD: 2}, 0, KeyIsolationProxy)
	return work.FailureReason(report.FailureReason), report.Summary, gw.seen()
}

func TestReplayedPromptFailuresBeforeAnyModelCallAreHarnessConfig(t *testing.T) {
	for name, harnessError := range map[string]string{
		"run 353 lmnr": "observe() got an unexpected keyword argument 'rollout_entrypoint'",
		"run 292 mcp":  "6 validation errors for MCPConfig mcpServers.litellm.TransformingStdioMCPServer Value error, At least one of 'tools', 'include_tags', or 'exclude_tags' is required",
	} {
		t.Run(name, func(t *testing.T) {
			reason, summary, paths := replayPromptFailure(t, harnessError, false)
			if !contains(paths, "GET /v1/model/info") {
				t.Fatalf("the gateway saw %v; the replay must send session/new's model/info through the proxy", paths)
			}
			if reason != work.FailureHarnessConfig {
				t.Fatalf("failure reason = %q, want harness_config: the prompt failed before any completion call (gateway saw %v)", reason, paths)
			}
			if !strings.Contains(summary, harnessError[:30]) {
				t.Errorf("summary %q does not quote the harness's error", summary)
			}
		})
	}
}

func TestReplayedPromptFailureAfterACompletionCallStaysAgentError(t *testing.T) {
	reason, _, paths := replayPromptFailure(t, "the tool crashed", true)
	if !contains(paths, "POST /v1/chat/completions") {
		t.Fatalf("the gateway saw %v, want a completion call", paths)
	}
	if reason != work.FailureAgentError {
		t.Fatalf("failure reason = %q, want agent_error: the agent reached the model before it failed", reason)
	}
}

func TestOnlyModelDrivingRequestsCountAsModelCalls(t *testing.T) {
	for path, want := range map[string]bool{
		"/v1/chat/completions": true, "/chat/completions": true, "/v1/completions": true, "/v1/responses": true,
		"/v1/messages": true, "/v1beta/models/gemini:generateContent": true,
		"/v1/model/info": false, "/v1/models": false, "/mcp": false, "/health": false, "/v1/embeddings": false,
	} {
		if got := isModelCall(path); got != want {
			t.Errorf("isModelCall(%q) = %v, want %v", path, got, want)
		}
	}
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}
