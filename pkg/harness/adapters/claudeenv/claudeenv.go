// Package claudeenv runs a Run as a Claude Code session in a Team's
// self-hosted Claude environment (ADR-0084). The worker dispatches the
// session; the Team's runner executes it, pushes its commits and opens the
// pull request; this adapter waits for the runner's report and turns
// Claude's final message into the Run's outcome.
package claudeenv

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/harness"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

// Name is the adapter's harness name (PLOEG_HARNESS).
const Name = "claude-environment"

// DefaultBin is the Claude Code CLI.
const DefaultBin = "claude"

// DefaultPollEvery is how often the adapter asks ploegd for the runner's
// report.
const DefaultPollEvery = 15 * time.Second

// Adapter dispatches one session per Run.
type Adapter struct {
	Bin           string
	EnvironmentID string
	// ConfigDir is a CLAUDE_CONFIG_DIR that already holds the Team's login.
	// Empty = log in from RefreshToken into the Run's scratch directory.
	ConfigDir string
	// RefreshToken and Scopes are a Team member's claude.ai login
	// (CLAUDE_CODE_OAUTH_REFRESH_TOKEN, CLAUDE_CODE_OAUTH_SCOPES).
	RefreshToken, Scopes string
	PollEvery            time.Duration

	// exec runs a command; nil = os/exec. Tests replace it.
	exec func(ctx context.Context, dir string, env []string, name string, args ...string) ([]byte, error)
}

func (a *Adapter) Name() string     { return Name }
func (a *Adapter) ExpectsLLM() bool { return true }

// Run dispatches the session, records it, waits for the runner's report and
// returns the outcome Claude's final message carries.
func (a *Adapter) Run(ctx context.Context, spec harness.TaskSpec, env harness.RunEnv) (harness.OutcomeReport, error) {
	if env.RemoteSession == nil {
		return harness.OutcomeReport{}, errors.New("claude-environment needs the worker's remote session API")
	}
	if !strings.HasPrefix(a.EnvironmentID, "ccpool_") {
		return harness.OutcomeReport{}, fmt.Errorf("claude-environment needs a ccpool_ environment id, got %q", a.EnvironmentID)
	}
	configDir, err := a.login(ctx, env)
	if err != nil {
		return harness.OutcomeReport{}, err
	}
	base := spec.Repo.BaseBranch
	if base == "" {
		base = "main"
	}
	ref := base
	if spec.Branch != "" && a.branchExists(ctx, env, spec.Branch) {
		ref = spec.Branch
	}

	dispatchEnv := append(withoutModelCredentials(env.BaseEnv), "CLAUDE_CONFIG_DIR="+configDir)
	out, err := a.run(ctx, env.RepoDir, dispatchEnv, a.bin(),
		"-p", env.Prompt+SessionContract(spec, base),
		"--environment", a.EnvironmentID, "--ref", ref, "--output-format", "json")
	session, perr := parseDispatch(out)
	if err != nil || perr != nil {
		return harness.OutcomeReport{}, fmt.Errorf("dispatch the session: %w", errors.Join(err, perr))
	}
	if err := env.RemoteSession.Record(ctx, harness.RemoteSession{
		EnvironmentID: a.EnvironmentID, SessionID: session,
		BaseBranch: base, Title: spec.WorkItem.Title, Reference: work.Reference(spec.WorkItem),
	}); err != nil {
		return harness.OutcomeReport{}, fmt.Errorf("record the session: %w", err)
	}
	if env.Log != nil {
		env.Log.Info("dispatched the run to a self-hosted claude environment", "environment", a.EnvironmentID, "session", session, "ref", ref)
	}
	if env.Checkpoint != nil {
		env.Checkpoint(work.Checkpoint{Phase: "agent_started", Branch: spec.Branch})
	}
	harness.ReportPromptStart(ctx, env, 0, 0)

	state, err := a.wait(ctx, env)
	if err != nil {
		return harness.OutcomeReport{}, err
	}
	report, err := outcomeFrom(state.FinalMessage, env.ScratchDir, spec.TraceID)
	if err != nil && env.Log != nil {
		env.Log.Warn("the session's final message carried no outcome", "err", err)
	}
	if !finished(state) {
		return report, fmt.Errorf("the session ended %s without a final message", state.ExitReason)
	}
	return report, nil
}

// finished reports whether the session ended after Claude's last turn. A
// dispatched session stays attached after its turn until the runner
// releases it as idle, which the runner reports as interrupted; only a
// final message tells that release apart from a session stopped mid-turn.
func finished(state harness.RemoteSessionState) bool {
	switch state.ExitReason {
	case "completed":
		return true
	case "interrupted":
		return strings.TrimSpace(state.FinalMessage) != ""
	default:
		return false
	}
}

// SessionContract replaces the delivery contract's push, pull request and
// outcome-file steps for a session on a runner without forge credentials.
func SessionContract(spec harness.TaskSpec, base string) string {
	return fmt.Sprintf(`

## Where you run

You run as a Claude Code session on the Team's own runner, not in Ploeg's
worker. The runner has no forge credentials and there is no
PLOEG_OUTCOME_FILE, so these rules replace the steps above that push, open
or update a pull request, or write that file:

- Commit your work on the branch you are on; every commit still ends with the
  trailers above. Do not push, and do not open or update a pull request.
- After you finish, the runner pushes your commits to %[1]s and, if none is
  open, opens the pull request with base %[2]s, titled with the Work Item's
  title, with your problem and solution as its description.
- End with a final message that is only the JSON you would have written to
  PLOEG_OUTCOME_FILE, and nothing else.
`, spec.Branch, base)
}

func (a *Adapter) bin() string {
	if a.Bin != "" {
		return a.Bin
	}
	return DefaultBin
}

func (a *Adapter) run(ctx context.Context, dir string, env []string, name string, args ...string) ([]byte, error) {
	if a.exec != nil {
		return a.exec(ctx, dir, env, name, args...)
	}
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir, cmd.Env = dir, env
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return stdout.Bytes(), fmt.Errorf("%s: %w: %s", name, err, tail(stderr.String(), 500))
	}
	return stdout.Bytes(), nil
}

// login returns a CLAUDE_CONFIG_DIR holding the Team's login.
func (a *Adapter) login(ctx context.Context, env harness.RunEnv) (string, error) {
	if a.ConfigDir != "" {
		return a.ConfigDir, nil
	}
	if a.RefreshToken == "" || a.Scopes == "" {
		return "", errors.New("claude-environment needs a Team login: a config directory, or a refresh token with its scopes")
	}
	dir := filepath.Join(env.ScratchDir, "claude-config")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	loginEnv := append(withoutModelCredentials(env.BaseEnv), "CLAUDE_CONFIG_DIR="+dir,
		"CLAUDE_CODE_OAUTH_REFRESH_TOKEN="+a.RefreshToken, "CLAUDE_CODE_OAUTH_SCOPES="+a.Scopes)
	if _, err := a.run(ctx, env.ScratchDir, loginEnv, a.bin(), "auth", "login"); err != nil {
		return "", fmt.Errorf("log in to the Team's claude.ai account: %w", err)
	}
	return dir, nil
}

func (a *Adapter) branchExists(ctx context.Context, env harness.RunEnv, branch string) bool {
	out, err := a.run(ctx, env.RepoDir, env.BaseEnv, "git", "ls-remote", "--heads", "origin", "refs/heads/"+branch)
	return err == nil && len(bytes.TrimSpace(out)) > 0
}

func (a *Adapter) wait(ctx context.Context, env harness.RunEnv) (harness.RemoteSessionState, error) {
	every := a.PollEvery
	if every <= 0 {
		every = DefaultPollEvery
	}
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		state, err := env.RemoteSession.State(ctx)
		if err == nil && state.Reported {
			return state, nil
		}
		if err != nil && env.Log != nil {
			env.Log.Warn("could not read the session's report", "err", err)
		}
		if env.Activity != nil {
			env.Activity.Touch()
		}
		select {
		case <-ctx.Done():
			return harness.RemoteSessionState{}, context.Cause(ctx)
		case <-ticker.C:
		}
	}
}

func parseDispatch(out []byte) (string, error) {
	var res struct {
		OK        bool   `json:"ok"`
		SessionID string `json:"session_id"`
		Error     string `json:"error"`
	}
	line := bytes.TrimSpace(out)
	if i := bytes.LastIndexByte(line, '\n'); i >= 0 {
		line = line[i+1:]
	}
	if err := json.Unmarshal(line, &res); err != nil {
		return "", fmt.Errorf("unreadable dispatch result: %w", err)
	}
	if !res.OK || res.SessionID == "" {
		return "", fmt.Errorf("dispatch refused: %s", res.Error)
	}
	return res.SessionID, nil
}

// outcomeFrom writes the JSON object in Claude's final message to the Run's
// drop box and reads it the way every harness's outcome is read.
func outcomeFrom(message, scratchDir, traceID string) (harness.OutcomeReport, error) {
	obj := lastJSONObject(message)
	if obj == "" {
		return harness.OutcomeReport{}, errors.New("no JSON object in the final message")
	}
	path := harness.DropBoxPath(scratchDir, traceID)
	if err := os.WriteFile(path, []byte(obj), 0o600); err != nil {
		return harness.OutcomeReport{}, err
	}
	box, err := harness.ReadDropBox(path)
	if err != nil {
		return harness.OutcomeReport{}, err
	}
	return harness.MergeDropBox(harness.OutcomeReport{}, box), nil
}

// lastJSONObject returns the JSON object that ends at the message's last
// closing brace, so a message that wraps its JSON in a code fence or a
// sentence still yields it. It tries a bounded number of opening braces.
func lastJSONObject(s string) string {
	end := strings.LastIndexByte(s, '}')
	if end < 0 {
		return ""
	}
	start := end
	for tries := 0; tries < 256; tries++ {
		start = strings.LastIndexByte(s[:start], '{')
		if start < 0 {
			return ""
		}
		if candidate := s[start : end+1]; json.Valid([]byte(candidate)) {
			return candidate
		}
	}
	return ""
}

func withoutModelCredentials(env []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		key, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(key, "ANTHROPIC_") || strings.HasPrefix(key, "LLM_") || strings.HasPrefix(key, "CLAUDE_CODE_OAUTH_") ||
			key == "AGENT_BUILDER_TOKEN" || key == "CLAUDE_CONFIG_DIR" {
			continue
		}
		out = append(out, kv)
	}
	return out
}

func tail(s string, n int) string {
	if len(s) > n {
		return s[len(s)-n:]
	}
	return s
}
