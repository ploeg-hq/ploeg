package acp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/harness"
)

// toolCallingGateway is an OpenAI-compatible model gateway that answers the
// first turn with one tool call and every later turn with a plain stop. It lets
// the real-binary tier drive an agent through a tool call with no model and no
// spend. calls counts the tool-call answers, so a test can tell an agent that
// never reached the tool call from one whose call was refused.
func toolCallingGateway(t *testing.T, tool string, args map[string]string) (baseURL string, calls *atomic.Int32) {
	t.Helper()
	calls = &atomic.Int32{}
	rawArgs, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/chat/completions") {
			http.NotFound(w, r)
			return
		}
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Messages []struct {
				Role string `json:"role"`
			} `json:"messages"`
			Tools []json.RawMessage `json:"tools"`
		}
		_ = json.Unmarshal(body, &req)
		answered := false
		for _, m := range req.Messages {
			if m.Role == "tool" {
				answered = true
			}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		chunk := func(delta map[string]any, finish any) {
			b, _ := json.Marshal(map[string]any{
				"id": "chatcmpl-drill", "object": "chat.completion.chunk", "created": 1, "model": "drill",
				"choices": []map[string]any{{"index": 0, "delta": delta, "finish_reason": finish}},
			})
			fmt.Fprintf(w, "data: %s\n\n", b)
		}
		if len(req.Tools) == 0 || answered {
			chunk(map[string]any{"role": "assistant", "content": "done"}, nil)
			chunk(map[string]any{}, "stop")
		} else {
			calls.Add(1)
			chunk(map[string]any{"role": "assistant", "tool_calls": []map[string]any{{
				"index": 0, "id": "call_drill", "type": "function",
				"function": map[string]any{"name": tool, "arguments": string(rawArgs)},
			}}}, nil)
			chunk(map[string]any{}, "tool_calls")
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(srv.Close)
	return srv.URL + "/v1", calls
}

// TestHostileRepository_OpenCodeAsksBeforeAMutationOutsideAllowAlways drives the
// real opencode binary through the adapter with a gateway that makes it edit a
// file or run a command, and checks the Run's permission mode decides. Under
// allow_read_only and deny_all the call must reach the adapter's permission
// policy and be refused; under allow_always it must run, which is the case
// that proves the drill can see a mutation at all.
func TestHostileRepository_OpenCodeAsksBeforeAMutationOutsideAllowAlways(t *testing.T) {
	if os.Getenv(hostileRepositoryOptIn) != "1" {
		t.Skipf("runs the real opencode binary; set %s=1 (the attack-drill workflow does) to run it", hostileRepositoryOptIn)
	}
	if _, err := exec.LookPath("opencode"); err != nil {
		t.Skip("opencode is not on PATH")
	}
	for _, tool := range []struct {
		name string
		args func(repo string) map[string]string
		mark string
	}{
		{
			name: "bash",
			mark: "ran-a-command",
			args: func(string) map[string]string {
				return map[string]string{"command": "touch ran-a-command", "description": "Touch a marker"}
			},
		},
		{
			name: "write",
			mark: "edited-a-file",
			args: func(repo string) map[string]string {
				return map[string]string{"filePath": filepath.Join(repo, "edited-a-file"), "content": "x\n"}
			},
		},
	} {
		for _, tc := range []struct {
			mode    PermissionMode
			mutates bool
		}{
			{PermissionAllowAll, true},
			{PermissionReadOnly, false},
			{PermissionDenyAll, false},
		} {
			t.Run(tool.name+"/"+string(tc.mode), func(t *testing.T) {
				repo := t.TempDir()
				baseURL, calls := toolCallingGateway(t, tool.name, tool.args(repo))
				a, err := New("opencode", ProfileOverrides{}, Options{
					PermissionMode: tc.mode,
					PromptTimeout:  2 * time.Minute,
					IdleTimeout:    time.Minute,
				})
				if err != nil {
					t.Fatal(err)
				}
				env := harness.RunEnv{
					RepoDir:    repo,
					ScratchDir: t.TempDir(),
					Prompt:     "Do the task.",
					BaseEnv:    os.Environ(),
					LLM: harness.LLMEnv{
						APIKey:  "ploeg-isolated-canary",
						BaseURL: baseURL,
						Model:   "drill",
						TraceID: "ploeg-permission000001",
					},
					Stderr: &testWriter{t},
				}
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
				defer cancel()
				rep, runErr := a.Run(ctx, testSpec(), env)
				t.Logf("outcome %s (%s), err %v", rep.Outcome, rep.Summary, runErr)

				if calls.Load() == 0 {
					t.Fatalf("opencode never asked the gateway for a turn with tools; the check proves nothing")
				}
				_, statErr := os.Stat(filepath.Join(repo, tool.mark))
				switch mutated := statErr == nil; {
				case tc.mutates && !mutated:
					t.Fatalf("under %s opencode did not run %s; the drill cannot see a mutation", tc.mode, tool.name)
				case !tc.mutates && mutated:
					t.Fatalf("under %s opencode ran %s without the adapter's permission policy refusing it", tc.mode, tool.name)
				}
			})
		}
	}
}
