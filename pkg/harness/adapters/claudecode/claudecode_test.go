package claudecode

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/harness"
	"github.com/ploeg-hq/ploeg/pkg/harness/harnesstest"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

func TestConformance(t *testing.T) {
	harnesstest.Run(t, harnesstest.Fixture{
		NewAdapter: func(_ *testing.T, bin string) harness.CommandAdapter { return New(bin, "") },
	})
}

func testEnv() harness.RunEnv {
	return harness.RunEnv{
		ScratchDir: "/tmp",
		Prompt:     "# Ticket VIK-596\n",
		LLM: harness.LLMEnv{
			APIKey:  "sk-minted",
			BaseURL: "http://litellm.gateway.svc.cluster.local:4000/v1",
			Model:   "claude-sonnet-5",
			TraceID: "ploeg-1cd43e1dfd6c",
		},
		Log: slog.New(slog.DiscardHandler),
	}
}

func TestPrepare_ArgvAndEnvMapping(t *testing.T) {
	inv, err := New("", "").Prepare(harness.TaskSpec{}, testEnv())
	if err != nil {
		t.Fatal(err)
	}
	want := []string{DefaultBin, "-p", "# Ticket VIK-596\n", "--output-format", "json", "--permission-mode", DefaultPermissionMode,
		"--settings", `{"disableAllHooks":true}`, "--strict-mcp-config"}
	if !slices.Equal(inv.Argv, want) {
		t.Errorf("argv = %v, want %v", inv.Argv, want)
	}
	if !inv.CaptureStdout {
		t.Error("claude-code must capture stdout for the JSON envelope")
	}
	for _, kv := range []string{
		"ANTHROPIC_API_KEY=sk-minted",
		// /v1 stripped: the Anthropic SDK appends /v1/... itself.
		"ANTHROPIC_BASE_URL=http://litellm.gateway.svc.cluster.local:4000",
		"ANTHROPIC_MODEL=claude-sonnet-5",
	} {
		if !slices.Contains(inv.ExtraEnv, kv) {
			t.Errorf("missing env %q in %v", kv, inv.ExtraEnv)
		}
	}
}

func TestPrepare_TargetRepositoryHooksAndMCPServersNeverRun(t *testing.T) {
	for _, a := range []*Adapter{New("", ""), New("claude-custom", "acceptEdits")} {
		inv, err := a.Prepare(harness.TaskSpec{}, testEnv())
		if err != nil {
			t.Fatal(err)
		}
		i := slices.Index(inv.Argv, "--settings")
		if i < 0 || i+1 >= len(inv.Argv) || inv.Argv[i+1] != `{"disableAllHooks":true}` {
			t.Errorf("argv does not disable hooks with one --settings JSON element: %q", inv.Argv)
		}
		if !slices.Contains(inv.Argv, "--strict-mcp-config") {
			t.Errorf("argv lets the target's .mcp.json servers start: %q", inv.Argv)
		}
		if slices.Contains(inv.Argv, "--mcp-config") {
			t.Errorf("argv names an MCP config, so --strict-mcp-config would load servers: %q", inv.Argv)
		}
	}
}

func TestRun_FakeClaudeOnPathIsToldToSkipTargetHooksAndMCPServers(t *testing.T) {
	fakeDir := t.TempDir()
	record := filepath.Join(t.TempDir(), "argv")
	fake := "#!/bin/sh\n" +
		"pwd >'" + record + ".cwd'\n" +
		`printf '%s\0' "$@" >'` + record + "'\n" +
		`printf '%s\n' '{"type":"result","subtype":"success","result":"ok","session_id":"fake-claude"}'` + "\n"
	if err := os.WriteFile(filepath.Join(fakeDir, DefaultBin), []byte(fake), 0o755); err != nil {
		t.Fatal(err)
	}
	path := fakeDir + string(os.PathListSeparator) + os.Getenv("PATH")
	t.Setenv("PATH", path)

	repo := t.TempDir()
	marker := filepath.Join(t.TempDir(), "target-code-ran")
	for name, body := range map[string]string{
		filepath.Join(".claude", "settings.json"): `{"enableAllProjectMcpServers":true,"hooks":{"SessionStart":[{"hooks":[{"type":"command","command":"touch ` + marker + `"}]}]}}`,
		".mcp.json": `{"mcpServers":{"target":{"command":"sh","args":["-c","touch ` + marker + `"]}}}`,
	} {
		if err := os.MkdirAll(filepath.Join(repo, filepath.Dir(name)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(repo, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	env := testEnv()
	env.RepoDir = repo
	env.ScratchDir = t.TempDir()
	env.BaseEnv = []string{"PATH=" + path, "HOME=" + t.TempDir()}
	report, err := harness.RunCommand(New("", "")).Run(context.Background(), harness.TaskSpec{TraceID: "ploeg-1cd43e1dfd6c"}, env)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if report.Usage == nil || report.Usage.SessionID != "fake-claude" {
		t.Fatalf("the fake claude on PATH did not produce this report: %+v", report)
	}

	raw, err := os.ReadFile(record)
	if err != nil {
		t.Fatalf("the fake claude did not record its argv: %v", err)
	}
	argv := strings.Split(strings.TrimSuffix(string(raw), "\x00"), "\x00")
	i := slices.Index(argv, "--settings")
	if i < 0 || i+1 >= len(argv) || argv[i+1] != `{"disableAllHooks":true}` {
		t.Errorf(`claude did not receive --settings '{"disableAllHooks":true}' as one argument: %q`, argv)
	}
	if !slices.Contains(argv, "--strict-mcp-config") {
		t.Errorf("claude did not receive --strict-mcp-config, so the target's .mcp.json servers would start: %q", argv)
	}
	if slices.Contains(argv, "--mcp-config") {
		t.Errorf("claude received an --mcp-config, so --strict-mcp-config would still load servers: %q", argv)
	}
	cwd, err := os.ReadFile(record + ".cwd")
	if err != nil {
		t.Fatal(err)
	}
	wantCwd, err := filepath.EvalSymlinks(repo)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(bytes.TrimSpace(cwd)); got != wantCwd {
		t.Errorf("claude ran in %q, want the target clone %q", got, wantCwd)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Error("target repository code ran during the run")
	}
}

func TestPrepare_PinsEveryModelAliasToTheRunModelAndDisablesTheAdvisor(t *testing.T) {
	inv, err := New("", "").Prepare(harness.TaskSpec{}, testEnv())
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(inv.ExtraEnv, AdvisorDisabled) {
		t.Errorf("the advisor must be off in every Run, got %v", inv.ExtraEnv)
	}
	for _, alias := range []string{"ANTHROPIC_DEFAULT_HAIKU_MODEL", "ANTHROPIC_DEFAULT_SONNET_MODEL", "ANTHROPIC_DEFAULT_OPUS_MODEL", "ANTHROPIC_DEFAULT_FABLE_MODEL"} {
		if !slices.Contains(inv.ExtraEnv, alias+"=claude-sonnet-5") {
			t.Errorf("%s must resolve to the Run's scoped model, got %v", alias, inv.ExtraEnv)
		}
	}
}

func TestPrepare_NoKeyNoEnv(t *testing.T) {
	env := testEnv()
	env.LLM = harness.LLMEnv{}
	inv, err := New("claude-custom", "acceptEdits").Prepare(harness.TaskSpec{}, env)
	if err != nil {
		t.Fatal(err)
	}
	// Narrowed to what the rule actually is: absent LLM wiring means no
	// ANTHROPIC_* is invented. The drop box (ADR-0018) is unconditional — a
	// reading Run returns its findings whether or not a key was minted.
	for _, kv := range inv.ExtraEnv {
		if strings.HasPrefix(kv, "ANTHROPIC_") {
			t.Errorf("no LLM wiring should mean no ANTHROPIC_* env, got %q", kv)
		}
	}
	if !slices.ContainsFunc(inv.ExtraEnv, func(kv string) bool {
		return strings.HasPrefix(kv, harness.DropBoxEnv+"=")
	}) {
		t.Errorf("every run must be told where its drop box is, got %v", inv.ExtraEnv)
	}
	if !slices.Contains(inv.ExtraEnv, AdvisorDisabled) {
		t.Errorf("the advisor must stay off without a key too, got %v", inv.ExtraEnv)
	}
	if inv.Argv[0] != "claude-custom" {
		t.Errorf("bin override ignored: %v", inv.Argv)
	}
	if !slices.Contains(inv.Argv, "acceptEdits") {
		t.Errorf("permission mode override ignored: %v", inv.Argv)
	}
}

func TestParseOutcome_MapsEnvelopeToUsage(t *testing.T) {
	envelope := `{"type":"result","subtype":"success","is_error":false,"result":"done",` +
		`"session_id":"sess-42","total_cost_usd":1.23,"usage":{"input_tokens":1000,"output_tokens":250}}`
	report, err := New("", "").ParseOutcome(harness.TaskSpec{}, harness.ExecResult{Stdout: []byte(envelope)})
	if err != nil {
		t.Fatal(err)
	}
	if report.Outcome != "" {
		t.Errorf("claude-code must not claim an outcome (forge poll is authoritative), got %q", report.Outcome)
	}
	u := report.Usage
	if u == nil || u.CostUSD != 1.23 || u.SessionID != "sess-42" || u.InputTokens != 1000 || u.OutputTokens != 250 {
		t.Errorf("usage = %+v", u)
	}
	// ADR-0045: what the envelope did not say stays unknown, never zero.
	if u.CacheReadInputTokens != nil || u.CacheCreationInputTokens != nil || u.Turns != nil ||
		u.DurationMs != nil || u.APIDurationMs != nil || u.ModelUsage != nil ||
		u.ToolCalls != nil || u.PeakContextTokens != nil {
		t.Errorf("absent envelope fields were filled in: %+v", u)
	}
	raw, err := json.Marshal(u)
	if err != nil {
		t.Fatal(err)
	}
	var keys map[string]any
	if err := json.Unmarshal(raw, &keys); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"cacheReadInputTokens", "cacheCreationInputTokens", "turns", "durationMs", "apiDurationMs", "modelUsage"} {
		if _, ok := keys[k]; ok {
			t.Errorf("usage JSON carries %q although the envelope had none: %s", k, raw)
		}
	}
}

func TestParseOutcome_KeepsEveryEnvelopeFigure(t *testing.T) {
	envelope, err := os.ReadFile(filepath.Join("testdata", "result_full.json"))
	if err != nil {
		t.Fatal(err)
	}
	report, err := New("", "").ParseOutcome(harness.TaskSpec{}, harness.ExecResult{Stdout: envelope})
	if err != nil {
		t.Fatal(err)
	}
	u := report.Usage
	if u == nil {
		t.Fatal("usage = nil")
	}
	if u.InputTokens != 1200 || u.OutputTokens != 9876 || u.CostUSD != 0.5812 || u.SessionID != "4f1c2a9e-sess" {
		t.Errorf("base usage = %+v", u)
	}
	for name, got := range map[string]*int64{
		"cacheReadInputTokens": u.CacheReadInputTokens, "cacheCreationInputTokens": u.CacheCreationInputTokens,
		"turns": u.Turns, "durationMs": u.DurationMs, "apiDurationMs": u.APIDurationMs,
	} {
		want := map[string]int64{"cacheReadInputTokens": 812004, "cacheCreationInputTokens": 45210,
			"turns": 37, "durationMs": 412345, "apiDurationMs": 301200}[name]
		if got == nil || *got != want {
			t.Errorf("%s = %v, want %d", name, got, want)
		}
	}
	if len(u.ModelUsage) != 2 {
		t.Fatalf("modelUsage = %+v, want two models", u.ModelUsage)
	}
	sonnet := u.ModelUsage["claude-sonnet-5"]
	if sonnet.InputTokens == nil || *sonnet.InputTokens != 1100 || sonnet.OutputTokens == nil || *sonnet.OutputTokens != 9500 ||
		sonnet.CacheReadInputTokens == nil || *sonnet.CacheReadInputTokens != 800000 ||
		sonnet.CacheCreationInputTokens == nil || *sonnet.CacheCreationInputTokens != 45000 ||
		sonnet.CostUSD == nil || *sonnet.CostUSD != 0.5501 ||
		sonnet.ContextWindowTokens == nil || *sonnet.ContextWindowTokens != 200000 {
		t.Errorf("modelUsage[claude-sonnet-5] = %+v", sonnet)
	}
	if u.ToolCalls != nil || u.PeakContextTokens != nil {
		t.Errorf("the envelope reports no tool calls or context fill, got %+v", u)
	}
}

func TestParseOutcome_ZeroReportedStaysZero(t *testing.T) {
	envelope := `{"type":"result","session_id":"s","num_turns":0,"usage":{"input_tokens":5,"cache_read_input_tokens":0}}`
	report, err := New("", "").ParseOutcome(harness.TaskSpec{}, harness.ExecResult{Stdout: []byte(envelope)})
	if err != nil {
		t.Fatal(err)
	}
	u := report.Usage
	if u.CacheReadInputTokens == nil || *u.CacheReadInputTokens != 0 || u.Turns == nil || *u.Turns != 0 {
		t.Errorf("a reported zero was lost: %+v", u)
	}
	if u.CacheCreationInputTokens != nil {
		t.Errorf("cacheCreationInputTokens = %d, want absent", *u.CacheCreationInputTokens)
	}
}

func TestParseOutcome_GarbageIsAnError(t *testing.T) {
	if _, err := New("", "").ParseOutcome(harness.TaskSpec{}, harness.ExecResult{Stdout: []byte("panic: boom")}); err == nil {
		t.Fatal("non-JSON stdout must be a parse error (downgraded to no-signal by RunCommand)")
	}
}

func TestParseOutcome_EmptyStdoutNoSignal(t *testing.T) {
	report, err := New("", "").ParseOutcome(harness.TaskSpec{}, harness.ExecResult{})
	if err != nil {
		t.Fatal(err)
	}
	if report.Outcome != "" || report.Usage != nil {
		t.Errorf("expected zero-value report, got %+v", report)
	}
}

func TestRun_MalformedEnvelopePreservesIndependentDropBox(t *testing.T) {
	const review = `{"outcome":"no_change_needed","summary":"review complete","findings":"missing boundary check","verdict":"request_changes"}`
	for _, tc := range []struct {
		name       string
		dropBox    string
		exit       string
		wantReview bool
	}{
		{name: "valid review", dropBox: review, exit: "0", wantReview: true},
		{name: "absent drop box", exit: "0"},
		{name: "malformed drop box", dropBox: "not JSON", exit: "0"},
		{name: "valid review and failed process", dropBox: review, exit: "7", wantReview: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bin := filepath.Join(t.TempDir(), "claude")
			script := "#!/bin/sh\n" +
				`if [ -n "$PLOEG_FAKE_DROPBOX" ]; then printf '%s\n' "$PLOEG_FAKE_DROPBOX" > "$PLOEG_OUTCOME_FILE"; fi` + "\n" +
				`printf '%s\n' 'not a JSON result envelope'` + "\n" +
				`exit "$PLOEG_FAKE_EXIT"` + "\n"
			if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
				t.Fatal(err)
			}
			env := testEnv()
			env.RepoDir, env.ScratchDir = t.TempDir(), t.TempDir()
			env.BaseEnv = []string{"PLOEG_FAKE_DROPBOX=" + tc.dropBox, "PLOEG_FAKE_EXIT=" + tc.exit}
			report, err := harness.RunCommand(New(bin, "")).Run(context.Background(), harness.TaskSpec{TraceID: "partial-envelope"}, env)
			if tc.exit == "0" {
				if err != nil {
					t.Fatalf("parse failure replaced a clean process exit: %v", err)
				}
			} else if exitErr, ok := err.(*exec.ExitError); !ok || exitErr.ExitCode() != 7 {
				t.Fatalf("process failure lost: got %v, want exit status 7", err)
			}
			if tc.wantReview {
				if report.Outcome != work.OutcomeNoChangeNeeded || report.Summary != "review complete" ||
					report.Findings != "missing boundary check" || report.Verdict != harness.VerdictRequestChanges {
					t.Fatalf("valid drop box lost: %+v", report)
				}
			} else if report.Outcome != "" || report.Summary != "" || report.Findings != "" || report.Verdict != "" {
				t.Fatalf("invalid or absent drop box became a report: %+v", report)
			}
			if report.Usage != nil {
				t.Fatalf("malformed envelope invented usage: %+v", report.Usage)
			}
		})
	}
}

type fakeClaude struct {
	dropBox string
	stdout  string
	exit    string
	sleep   string
}

func runFakeClaude(ctx context.Context, t *testing.T, fc fakeClaude, idle time.Duration) (harness.OutcomeReport, error) {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "claude")
	script := "#!/bin/sh\n" +
		`if [ -n "$PLOEG_FAKE_DROPBOX" ]; then printf '%s\n' "$PLOEG_FAKE_DROPBOX" > "$PLOEG_OUTCOME_FILE"; fi` + "\n" +
		`printf '%s' "$PLOEG_FAKE_STDOUT"` + "\n" +
		`if [ -n "$PLOEG_FAKE_SLEEP" ]; then exec sleep "$PLOEG_FAKE_SLEEP"; fi` + "\n" +
		`exit "${PLOEG_FAKE_EXIT:-0}"` + "\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	env := testEnv()
	env.RepoDir, env.ScratchDir = t.TempDir(), t.TempDir()
	env.IdleTimeout = idle
	env.BaseEnv = []string{
		"PATH=" + os.Getenv("PATH"),
		"PLOEG_FAKE_DROPBOX=" + fc.dropBox,
		"PLOEG_FAKE_STDOUT=" + fc.stdout,
		"PLOEG_FAKE_EXIT=" + fc.exit,
		"PLOEG_FAKE_SLEEP=" + fc.sleep,
	}
	return harness.RunCommand(New(bin, "")).Run(ctx, harness.TaskSpec{TraceID: "writer-account"}, env)
}

const validEnvelope = `{"type":"result","subtype":"success","result":"done","session_id":"sess-1","total_cost_usd":0.42,"usage":{"input_tokens":1200,"output_tokens":300}}`

func TestRun_MalformedEnvelopeKeepsTheWritersAccount(t *testing.T) {
	const (
		problem  = "Refunds over €500 fail with a 500."
		solution = "`refund.go` checks the limit before the call."
		account  = `{"problem":"` + problem + `","solution":"` + solution + `"}`
		created  = `[{"title":"Split the refund limit into config","description":"Found while fixing refunds.","ready":true,"kind":"discovered"}]`
	)
	for _, tc := range []struct {
		name         string
		dropBox      string
		stdout       string
		wantProblem  string
		wantSolution string
		wantFindings string
		wantCreated  int
	}{
		{name: "not JSON", dropBox: account, stdout: "not a JSON result envelope\n", wantProblem: problem, wantSolution: solution},
		{name: "banner before a valid envelope", dropBox: account, stdout: "Claude Code v9.9.9\n" + validEnvelope + "\n", wantProblem: problem, wantSolution: solution},
		{name: "truncated envelope", dropBox: account, stdout: `{"type":"result","total_cost_usd":0.42,"usage":{"input_tokens":`, wantProblem: problem, wantSolution: solution},
		{name: "problem alone", dropBox: `{"problem":"` + problem + `"}`, stdout: "garbage", wantProblem: problem},
		{name: "solution alone", dropBox: `{"solution":"` + solution + `"}`, stdout: "garbage", wantSolution: solution},
		{name: "findings alone", dropBox: `{"findings":"missing boundary check"}`, stdout: "garbage", wantFindings: "missing boundary check"},
		{name: "created work alone", dropBox: `{"createdWorkItems":` + created + `}`, stdout: "garbage", wantCreated: 1},
		{name: "agent-claimed usage is not accounting", dropBox: `{"problem":"` + problem + `","usage":{"costUsd":0,"sessionId":"agent-made-up"}}`, stdout: "garbage", wantProblem: problem},
		{name: "malformed drop box", dropBox: "not JSON", stdout: "garbage"},
		{name: "empty drop box", dropBox: "{}", stdout: "garbage"},
		{name: "whitespace-only account", dropBox: `{"problem":"  ","solution":"\n"}`, stdout: "garbage"},
		{name: "absent drop box", stdout: "garbage"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			report, err := runFakeClaude(context.Background(), t, fakeClaude{dropBox: tc.dropBox, stdout: tc.stdout}, 0)
			if err != nil {
				t.Fatalf("a clean process exit became an error: %v", err)
			}
			if strings.TrimSpace(report.Problem) != tc.wantProblem || strings.TrimSpace(report.Solution) != tc.wantSolution {
				t.Errorf("account = problem %q solution %q, want problem %q solution %q",
					report.Problem, report.Solution, tc.wantProblem, tc.wantSolution)
			}
			if report.Findings != tc.wantFindings {
				t.Errorf("findings = %q, want %q", report.Findings, tc.wantFindings)
			}
			if len(report.CreatedWorkItems) != tc.wantCreated {
				t.Errorf("created work = %+v, want %d entries", report.CreatedWorkItems, tc.wantCreated)
			}
			if report.Outcome != "" || report.Verdict != "" || report.Summary != "" || len(report.Links) != 0 {
				t.Errorf("an outcome-less drop box gained a conclusion: %+v", report)
			}
			if report.Usage != nil {
				t.Errorf("an unreadable envelope produced usage %+v; usage must stay unknown", report.Usage)
			}
		})
	}
}

func TestRun_ValidEnvelopeSuppliesUsageBesideTheWritersAccount(t *testing.T) {
	report, err := runFakeClaude(context.Background(), t, fakeClaude{
		dropBox: `{"problem":"p","solution":"s","usage":{"costUsd":0,"sessionId":"agent-made-up"}}`,
		stdout:  validEnvelope + "\n",
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if report.Problem != "p" || report.Solution != "s" {
		t.Errorf("account lost: %+v", report)
	}
	if report.Usage == nil || report.Usage.SessionID != "sess-1" || report.Usage.CostUSD != 0.42 ||
		report.Usage.InputTokens != 1200 || report.Usage.OutputTokens != 300 {
		t.Errorf("usage = %+v, want the envelope's figures", report.Usage)
	}
	if report.Outcome != "" {
		t.Errorf("outcome = %q, want none: Claude does not know whether its PR landed", report.Outcome)
	}
}

func TestRun_FailedProcessKeepsItsErrorAndTheWritersAccount(t *testing.T) {
	report, err := runFakeClaude(context.Background(), t, fakeClaude{
		dropBox: `{"problem":"p","solution":"s"}`, stdout: "garbage", exit: "7",
	}, 0)
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 7 {
		t.Fatalf("err = %v, want the process's exit status 7", err)
	}
	if report.Problem != "p" || report.Solution != "s" {
		t.Errorf("account lost on a failed process: %+v", report)
	}
	if report.Outcome != "" || report.Usage != nil {
		t.Errorf("a failed process gained an outcome or usage: %+v", report)
	}
}

func TestRun_StoppedProcessStaysStoppedWithTheWritersAccount(t *testing.T) {
	t.Run("idle timeout", func(t *testing.T) {
		report, err := runFakeClaude(context.Background(), t, fakeClaude{
			dropBox: `{"problem":"p","solution":"s"}`, stdout: "garbage", sleep: "30",
		}, 200*time.Millisecond)
		if !errors.Is(err, harness.ErrIdle) {
			t.Fatalf("err = %v, want ErrIdle", err)
		}
		if report.Problem != "p" || report.Solution != "s" || report.Outcome != "" {
			t.Errorf("report = %+v, want the account and no outcome", report)
		}
	})
	t.Run("cancelled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		go func() { time.Sleep(300 * time.Millisecond); cancel() }()
		report, err := runFakeClaude(ctx, t, fakeClaude{
			dropBox: `{"problem":"p","solution":"s"}`, stdout: "garbage", sleep: "30",
		}, 0)
		if err == nil {
			t.Fatal("a cancelled process reported success")
		}
		if report.Problem != "p" || report.Solution != "s" || report.Outcome != "" {
			t.Errorf("report = %+v, want the account and no outcome", report)
		}
	})
}

// ADR-0078: a Run whose key was granted MCP access groups loads exactly one
// MCP server, the gateway's, and --strict-mcp-config still keeps the target's
// .mcp.json out.
func TestPrepare_MCPGrantLoadsOnlyTheGatewayServer(t *testing.T) {
	env := testEnv()
	env.ScratchDir = t.TempDir()
	env.LLM.APIKey = "ploeg-isolated-placeholder"
	env.LLM.MCPURL = "http://127.0.0.1:41234/mcp"
	inv, err := New("", "").Prepare(harness.TaskSpec{}, env)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(inv.Argv, "--strict-mcp-config") {
		t.Fatalf("argv lets the target's .mcp.json servers start: %q", inv.Argv)
	}
	i := slices.Index(inv.Argv, "--mcp-config")
	if i < 0 || i+1 >= len(inv.Argv) {
		t.Fatalf("argv names no --mcp-config: %q", inv.Argv)
	}
	path := inv.Argv[i+1]
	if filepath.Dir(path) != env.ScratchDir {
		t.Fatalf("MCP config %q is not under the scratch directory", path)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("MCP config mode %v, want 0600", info.Mode().Perm())
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		MCPServers map[string]struct {
			Type    string            `json:"type"`
			URL     string            `json:"url"`
			Headers map[string]string `json:"headers"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal(body, &cfg); err != nil {
		t.Fatal(err)
	}
	if len(cfg.MCPServers) != 1 {
		t.Fatalf("MCP config names %d servers, want only the gateway: %s", len(cfg.MCPServers), body)
	}
	server, ok := cfg.MCPServers["litellm"]
	if !ok || server.Type != "http" || server.URL != "http://127.0.0.1:41234/mcp" {
		t.Fatalf("MCP server = %+v, want litellm over http at the proxy's /mcp", cfg.MCPServers)
	}
	if len(server.Headers) != 1 || server.Headers["x-litellm-api-key"] != "Bearer ploeg-isolated-placeholder" {
		t.Fatalf("MCP headers = %v, want only x-litellm-api-key with the harness's key", server.Headers)
	}
}

func TestPrepare_NoMCPConfigWithoutAKey(t *testing.T) {
	env := testEnv()
	env.ScratchDir = t.TempDir()
	env.LLM.APIKey = ""
	env.LLM.MCPURL = "http://127.0.0.1:41234/mcp"
	inv, err := New("", "").Prepare(harness.TaskSpec{}, env)
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(inv.Argv, "--mcp-config") {
		t.Fatalf("argv names an MCP config with no key to authenticate it: %q", inv.Argv)
	}
}
