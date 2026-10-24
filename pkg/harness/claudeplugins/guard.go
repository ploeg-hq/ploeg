package claudeplugins

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/harness"
	"github.com/ploeg-hq/ploeg/pkg/runidentity"
)

// Bounds on what one guarded hook reads and returns.
const (
	MaxHookInput  = 8 << 20
	MaxHookOutput = 64 << 10
	MaxHookStderr = 4 << 10
)

// PassedEnv names the variables of the hook's own environment the guard
// passes on. Everything else, such as the Run's model key, is withheld.
var PassedEnv = []string{
	"PATH", "LANG", "LC_ALL", "TZ", "TERM", "HOME", "TMPDIR",
	"CLAUDE_PROJECT_DIR", "CLAUDE_PLUGIN_ROOT", "CLAUDE_PLUGIN_DATA",
	"CLAUDE_ENV_FILE", "CLAUDE_CODE_SESSION_ID",
}

var keptOutput = map[string]bool{"systemMessage": true, "suppressOutput": true, "hookSpecificOutput": true}

var keptSpecificOutput = map[string]bool{"hookEventName": true, "additionalContext": true}

// Guard runs the hook specPath describes and always returns exit status 0:
// a hook that fails, hangs, exits 2 or answers with a decision that blocks,
// stops or rewrites cannot change the Run. Only context it adds and a
// message it shows pass through.
func Guard(specPath string, stdin io.Reader, stdout, stderr io.Writer, environ []string) int {
	body, err := os.ReadFile(specPath)
	if err != nil {
		fmt.Fprintf(stderr, "ploeg hook guard: %v\n", err)
		return 0
	}
	var spec GuardSpec
	if err := json.Unmarshal(body, &spec); err != nil {
		fmt.Fprintf(stderr, "ploeg hook guard: %v\n", err)
		return 0
	}
	timeout := spec.Timeout
	if timeout <= 0 || timeout > MaxHookTimeout {
		timeout = DefaultHookTimeout
	}
	input, _ := io.ReadAll(io.LimitReader(stdin, MaxHookInput))
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", spec.Command)
	cmd.Env = hookEnv(environ, spec)
	cmd.Stdin = bytes.NewReader(input)
	out := &capped{max: MaxHookOutput}
	errOut := &capped{max: MaxHookStderr}
	cmd.Stdout, cmd.Stderr = out, errOut
	harness.KillProcessGroupOnCancel(cmd)
	cmd.WaitDelay = time.Second
	runErr := cmd.Run()
	if errOut.buf.Len() > 0 {
		_, _ = stderr.Write(errOut.buf.Bytes())
	}
	if ctx.Err() != nil {
		fmt.Fprintf(stderr, "ploeg hook guard: hook stopped after %s\n", timeout)
		return 0
	}
	if runErr != nil {
		fmt.Fprintf(stderr, "ploeg hook guard: hook failed (%v); the Run continues\n", runErr)
		return 0
	}
	_, _ = stdout.Write(Sanitize(out.buf.Bytes()))
	return 0
}

func hookEnv(environ []string, spec GuardSpec) []string {
	have := map[string]string{}
	for _, kv := range environ {
		if k, v, ok := strings.Cut(kv, "="); ok {
			have[k] = v
		}
	}
	var env []string
	for _, k := range PassedEnv {
		if v, ok := have[k]; ok {
			env = append(env, k+"="+v)
		} else if k == "CLAUDE_PLUGIN_ROOT" && spec.PluginRoot != "" {
			env = append(env, k+"="+spec.PluginRoot)
		}
	}
	for k, v := range spec.Env {
		env = append(env, k+"="+v)
	}
	if token := currentRunIdentity(spec.RunIdentityFile); token != "" {
		env = append(env, RunIdentityEnv+"="+token)
	}
	return env
}

// currentRunIdentity reads the plugin's Run identity at the moment the hook
// starts, so every hook gets the token the worker last refreshed. A missing,
// malformed or expired token gives the hook none.
func currentRunIdentity(path string) string {
	if path == "" {
		return ""
	}
	body, err := os.ReadFile(path)
	if err != nil || len(body) > maxRunIdentityBytes {
		return ""
	}
	token := strings.TrimSpace(string(body))
	exp, ok := runidentity.UnverifiedExpiry(token)
	if !ok || !time.Now().Before(exp) {
		return ""
	}
	return token
}

const maxRunIdentityBytes = 8 << 10

// Sanitize keeps only what a hook may say to the Run. JSON output keeps
// systemMessage, suppressOutput and, of hookSpecificOutput, hookEventName and
// additionalContext; a decision, a permission answer, rewritten input and
// "continue": false are removed. Other output passes unchanged.
func Sanitize(out []byte) []byte {
	trimmed := bytes.TrimSpace(out)
	if !bytes.HasPrefix(trimmed, []byte("{")) {
		return out
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &doc); err != nil {
		return nil
	}
	kept := map[string]json.RawMessage{}
	for k, v := range doc {
		if !keptOutput[k] {
			continue
		}
		if k == "hookSpecificOutput" {
			var specific map[string]json.RawMessage
			if json.Unmarshal(v, &specific) != nil {
				continue
			}
			inner := map[string]json.RawMessage{}
			for ik, iv := range specific {
				if keptSpecificOutput[ik] {
					inner[ik] = iv
				}
			}
			if _, ok := inner["additionalContext"]; !ok {
				continue
			}
			v, _ = json.Marshal(inner)
		}
		kept[k] = v
	}
	if len(kept) == 0 {
		return nil
	}
	body, _ := json.Marshal(kept)
	return body
}

type capped struct {
	buf bytes.Buffer
	max int
}

func (c *capped) Write(p []byte) (int, error) {
	if room := c.max - c.buf.Len(); room > 0 {
		if len(p) > room {
			c.buf.Write(p[:room])
		} else {
			c.buf.Write(p)
		}
	}
	return len(p), nil
}
