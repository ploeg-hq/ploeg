package claudeenv

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/harness"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

type call struct {
	dir  string
	env  []string
	name string
	args []string
}

type fakeExec struct {
	mu           sync.Mutex
	calls        []call
	branchExists bool
	dispatch     string
}

func (f *fakeExec) run(_ context.Context, dir string, env []string, name string, args ...string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, call{dir, env, name, args})
	switch {
	case name == "git":
		if f.branchExists {
			return []byte("abc123\trefs/heads/agent/vik-7\n"), nil
		}
		return nil, nil
	case len(args) > 1 && args[0] == "auth":
		return nil, nil
	default:
		return []byte(f.dispatch), nil
	}
}

func (f *fakeExec) dispatchCall(t *testing.T) call {
	t.Helper()
	for _, c := range f.calls {
		if c.name == "claude" && len(c.args) > 0 && c.args[0] == "-p" {
			return c
		}
	}
	t.Fatal("no dispatch call")
	return call{}
}

type fakeSessions struct {
	mu       sync.Mutex
	recorded []harness.RemoteSession
	polls    int
	reportAt int
	state    harness.RemoteSessionState
}

func (f *fakeSessions) Record(_ context.Context, s harness.RemoteSession) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.recorded = append(f.recorded, s)
	return nil
}

func (f *fakeSessions) State(context.Context) (harness.RemoteSessionState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.polls++
	if f.polls < f.reportAt {
		return harness.RemoteSessionState{}, nil
	}
	return f.state, nil
}

func spec() harness.TaskSpec {
	return harness.TaskSpec{
		WorkItem: work.WorkItem{Provider: "vikunja", ExternalID: "7", Title: "Fix the thing"},
		Repo:     harness.RepoRef{Owner: "webgrip", Name: "fixture", BaseBranch: "development"},
		Branch:   "agent/vik-7",
		TraceID:  "ploeg-abc",
	}
}

func runEnv(t *testing.T, sessions harness.RemoteSessionAPI) harness.RunEnv {
	return harness.RunEnv{
		RepoDir: t.TempDir(), ScratchDir: t.TempDir(), Prompt: "## Delivery contract\n",
		BaseEnv:       []string{"PATH=/bin", "HOME=/h", "LLM_API_KEY=sk-model", "AGENT_BUILDER_TOKEN=forge", "ANTHROPIC_API_KEY=sk-ant"},
		RemoteSession: sessions,
	}
}

func adapter(f *fakeExec) *Adapter {
	return &Adapter{EnvironmentID: "ccpool_env1", ConfigDir: "/team-login", PollEvery: time.Millisecond, exec: f.run}
}

const okDispatch = `{"ok":true,"session_id":"session_01ABC","url":"https://claude.ai/code/session_01ABC"}`

func TestRun_DispatchesRecordsWaitsAndReadsTheOutcome(t *testing.T) {
	f := &fakeExec{dispatch: okDispatch}
	sessions := &fakeSessions{reportAt: 3, state: harness.RemoteSessionState{
		Reported: true, ExitReason: "completed", PushedCommit: "abc", PullRequest: "https://forge/pulls/1",
		FinalMessage: "Done.\n```json\n{\"problem\": \"It broke.\", \"solution\": \"- fixed a.go\"}\n```",
	}}
	report, err := adapter(f).Run(context.Background(), spec(), runEnv(t, sessions))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if report.Problem != "It broke." || report.Solution != "- fixed a.go" {
		t.Errorf("report = %+v", report)
	}

	d := f.dispatchCall(t)
	if !slices.Equal(d.args[2:], []string{"--environment", "ccpool_env1", "--ref", "development", "--output-format", "json"}) {
		t.Errorf("dispatch args = %q", d.args[2:])
	}
	if !strings.HasPrefix(d.args[1], "## Delivery contract\n") || !strings.Contains(d.args[1], "## Where you run") ||
		!strings.Contains(d.args[1], "pushes your commits to agent/vik-7") {
		t.Errorf("prompt does not carry the session contract:\n%s", d.args[1])
	}
	for _, kv := range d.env {
		if strings.HasPrefix(kv, "LLM_") || strings.HasPrefix(kv, "ANTHROPIC_") || strings.HasPrefix(kv, "AGENT_BUILDER_TOKEN=") {
			t.Errorf("dispatch environment carries %q", kv)
		}
	}
	if !slices.Contains(d.env, "CLAUDE_CONFIG_DIR=/team-login") {
		t.Errorf("dispatch environment lacks the Team login: %q", d.env)
	}
	want := harness.RemoteSession{EnvironmentID: "ccpool_env1", SessionID: "session_01ABC",
		BaseBranch: "development", Title: "Fix the thing", Reference: work.Reference(spec().WorkItem)}
	if len(sessions.recorded) != 1 || sessions.recorded[0] != want {
		t.Errorf("recorded %+v, want %+v", sessions.recorded, want)
	}
	if sessions.polls < 3 {
		t.Errorf("polled %d times, want it to wait for the report", sessions.polls)
	}
}

func TestRun_StartsFromTheShiftBranchWhenItExists(t *testing.T) {
	f := &fakeExec{dispatch: okDispatch, branchExists: true}
	sessions := &fakeSessions{state: harness.RemoteSessionState{Reported: true, ExitReason: "completed", FinalMessage: "{}"}}
	if _, err := adapter(f).Run(context.Background(), spec(), runEnv(t, sessions)); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if d := f.dispatchCall(t); d.args[5] != "agent/vik-7" {
		t.Errorf("--ref = %q, want the Shift branch", d.args[5])
	}
}

func TestRun_LogsInFromARefreshTokenIntoTheScratchDirectory(t *testing.T) {
	f := &fakeExec{dispatch: okDispatch}
	a := &Adapter{EnvironmentID: "ccpool_env1", RefreshToken: "rt", Scopes: "user:inference user:sessions:claude_code", PollEvery: time.Millisecond, exec: f.run}
	env := runEnv(t, &fakeSessions{state: harness.RemoteSessionState{Reported: true, ExitReason: "completed"}})
	if _, err := a.Run(context.Background(), spec(), env); err != nil {
		t.Fatalf("Run: %v", err)
	}
	login := f.calls[0]
	if !slices.Equal(login.args, []string{"auth", "login"}) ||
		!slices.Contains(login.env, "CLAUDE_CODE_OAUTH_REFRESH_TOKEN=rt") ||
		!slices.Contains(login.env, "CLAUDE_CONFIG_DIR="+env.ScratchDir+"/claude-config") {
		t.Errorf("login call = %+v", login)
	}
	for _, kv := range f.dispatchCall(t).env {
		if strings.HasPrefix(kv, "CLAUDE_CODE_OAUTH_") {
			t.Errorf("dispatch environment carries %q", kv)
		}
	}
}

func TestRun_HowTheSessionEnded(t *testing.T) {
	for _, tc := range []struct {
		name, exit, message string
		wantErr             bool
	}{
		{"released as idle after its turn", "interrupted", `{"problem":"p","solution":"s"}`, false},
		{"exited by itself", "completed", "", false},
		{"stopped mid-turn", "interrupted", "", true},
		{"crashed", "failed", `{"problem":"p","solution":"s"}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeExec{dispatch: okDispatch}
			sessions := &fakeSessions{state: harness.RemoteSessionState{Reported: true, ExitReason: tc.exit, FinalMessage: tc.message}}
			_, err := adapter(f).Run(context.Background(), spec(), runEnv(t, sessions))
			if (err != nil) != tc.wantErr {
				t.Errorf("Run = %v, want error %v", err, tc.wantErr)
			}
		})
	}
}

func TestRun_FailsWhenTheDispatchIsRefused(t *testing.T) {
	f := &fakeExec{dispatch: `{"ok":false,"error":"self-hosted runners are disabled by your organization's policy"}`}
	sessions := &fakeSessions{}
	_, err := adapter(f).Run(context.Background(), spec(), runEnv(t, sessions))
	if err == nil || !strings.Contains(err.Error(), "disabled by your organization's policy") {
		t.Errorf("Run = %v, want the refusal", err)
	}
	if len(sessions.recorded) != 0 {
		t.Errorf("a refused dispatch was recorded")
	}
}

func TestRun_StopsWaitingWhenTheLeaseIsLost(t *testing.T) {
	f := &fakeExec{dispatch: okDispatch}
	ctx, cancel := context.WithCancelCause(context.Background())
	lost := errors.New("lease lost")
	sessions := &fakeSessions{reportAt: 1 << 30}
	go func() { time.Sleep(20 * time.Millisecond); cancel(lost) }()
	if _, err := adapter(f).Run(ctx, spec(), runEnv(t, sessions)); !errors.Is(err, lost) {
		t.Errorf("Run = %v, want the lease-lost cause", err)
	}
}

func TestLastJSONObject(t *testing.T) {
	for in, want := range map[string]string{
		`{"a":1}`:                      `{"a":1}`,
		"text\n{\"a\":{\"b\":2}}\n":    `{"a":{"b":2}}`,
		"```json\n{\"a\":\"x}\"}\n```": `{"a":"x}"}`,
		"no json here":                 "",
		"{broken":                      "",
	} {
		if got := lastJSONObject(in); got != want {
			t.Errorf("lastJSONObject(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRun_ReportsThePromptStartBoundOnlyByTheRunsDeadline(t *testing.T) {
	f := &fakeExec{dispatch: okDispatch}
	sessions := &fakeSessions{reportAt: 1, state: harness.RemoteSessionState{Reported: true, ExitReason: "completed", FinalMessage: "Done."}}
	env := runEnv(t, sessions)
	var seen []harness.PromptStart
	env.PromptStarted = func(p harness.PromptStart) { seen = append(seen, p) }
	ctx, cancel := context.WithTimeout(context.Background(), time.Hour)
	defer cancel()
	if _, err := adapter(f).Run(ctx, spec(), env); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(seen) != 1 || seen[0].Prompt > time.Hour || seen[0].Prompt < 59*time.Minute || seen[0].Idle != 0 {
		t.Fatalf("prompt starts = %+v, want one bound by the hour deadline and no idle limit", seen)
	}
}
