package harness

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/work"
)

// scriptAdapter is a minimal CommandAdapter for exercising RunCommand.
type scriptAdapter struct {
	argv        []string
	extraEnv    []string
	outcomeFile string
	capture     bool
	parse       func(TaskSpec, ExecResult) (OutcomeReport, error)
}

func (s scriptAdapter) Name() string     { return "script" }
func (s scriptAdapter) ExpectsLLM() bool { return false }
func (s scriptAdapter) Prepare(TaskSpec, RunEnv) (Invocation, error) {
	return Invocation{Argv: s.argv, ExtraEnv: s.extraEnv, OutcomeFile: s.outcomeFile, CaptureStdout: s.capture}, nil
}
func (s scriptAdapter) ParseOutcome(spec TaskSpec, res ExecResult) (OutcomeReport, error) {
	if s.parse != nil {
		return s.parse(spec, res)
	}
	return OutcomeReport{}, nil
}

func writeScript(t *testing.T, body string) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "script.sh")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin
}

func testEnv(t *testing.T) RunEnv {
	t.Helper()
	return RunEnv{
		RepoDir:    t.TempDir(),
		ScratchDir: t.TempDir(),
		Prompt:     "do the thing",
		BaseEnv:    os.Environ(),
		Log:        slog.New(slog.DiscardHandler),
	}
}

func TestRunCommand_Success(t *testing.T) {
	bin := writeScript(t, "echo hello; exit 0")
	report, err := RunCommand(scriptAdapter{argv: []string{bin}}).Run(context.Background(), TaskSpec{}, testEnv(t))
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if report.Outcome != "" {
		t.Errorf("expected zero-value report, got %+v", report)
	}
}

func TestRunCommand_FailureCarriesTail(t *testing.T) {
	bin := writeScript(t, "echo boom >&2; exit 7")
	var gotTail string
	ad := scriptAdapter{argv: []string{bin}, parse: func(_ TaskSpec, res ExecResult) (OutcomeReport, error) {
		gotTail = string(res.LogTail)
		if res.ExitCode != 7 {
			t.Errorf("exit code = %d, want 7", res.ExitCode)
		}
		return OutcomeReport{}, nil
	}}
	_, err := RunCommand(ad).Run(context.Background(), TaskSpec{}, testEnv(t))
	if err == nil {
		t.Fatal("expected exec error for exit 7")
	}
	if !strings.Contains(gotTail, "boom") {
		t.Errorf("log tail missing stderr output: %q", gotTail)
	}
}

func TestRunCommand_CtxCancelKills(t *testing.T) {
	bin := writeScript(t, "exec sleep 30")
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(100 * time.Millisecond); cancel() }()
	done := make(chan struct{})
	var err error
	go func() {
		_, err = RunCommand(scriptAdapter{argv: []string{bin}}).Run(ctx, TaskSpec{}, testEnv(t))
		close(done)
	}()
	select {
	case <-done:
		if err == nil {
			t.Error("expected an error from the cancelled run")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled run did not return within 5s")
	}
}

func TestRunCommand_OutcomeFile(t *testing.T) {
	env := testEnv(t)
	outcome := filepath.Join(env.ScratchDir, "outcome.json")
	bin := writeScript(t, "echo '{}' > "+outcome)
	var sawFile string
	ad := scriptAdapter{argv: []string{bin}, outcomeFile: outcome, parse: func(_ TaskSpec, res ExecResult) (OutcomeReport, error) {
		sawFile = res.OutcomeFile
		return OutcomeReport{Outcome: work.OutcomeNoChangeNeeded, Summary: "done"}, nil
	}}
	report, err := RunCommand(ad).Run(context.Background(), TaskSpec{}, env)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if sawFile != outcome {
		t.Errorf("OutcomeFile = %q, want %q", sawFile, outcome)
	}
	if report.Outcome != work.OutcomeNoChangeNeeded {
		t.Errorf("report = %+v", report)
	}
}

func TestRunCommand_MissingOutcomeFileNotReported(t *testing.T) {
	env := testEnv(t)
	bin := writeScript(t, "exit 0")
	ad := scriptAdapter{argv: []string{bin}, outcomeFile: filepath.Join(env.ScratchDir, "never-written.json"),
		parse: func(_ TaskSpec, res ExecResult) (OutcomeReport, error) {
			if res.OutcomeFile != "" {
				t.Errorf("OutcomeFile = %q, want empty for a file the harness never wrote", res.OutcomeFile)
			}
			return OutcomeReport{}, nil
		}}
	if _, err := RunCommand(ad).Run(context.Background(), TaskSpec{}, env); err != nil {
		t.Fatal(err)
	}
}

func TestRunCommand_CaptureStdout(t *testing.T) {
	bin := writeScript(t, `echo '{"type":"result"}'`)
	var captured string
	ad := scriptAdapter{argv: []string{bin}, capture: true, parse: func(_ TaskSpec, res ExecResult) (OutcomeReport, error) {
		captured = string(res.Stdout)
		return OutcomeReport{}, nil
	}}
	if _, err := RunCommand(ad).Run(context.Background(), TaskSpec{}, testEnv(t)); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(captured) != `{"type":"result"}` {
		t.Errorf("captured stdout = %q", captured)
	}
}

func TestRunCommand_ParseErrorDowngradesToZeroValue(t *testing.T) {
	bin := writeScript(t, "exit 0")
	ad := scriptAdapter{argv: []string{bin}, parse: func(TaskSpec, ExecResult) (OutcomeReport, error) {
		return OutcomeReport{Outcome: work.OutcomeFailed}, harnessError("bad envelope")
	}}
	report, err := RunCommand(ad).Run(context.Background(), TaskSpec{}, testEnv(t))
	if err != nil {
		t.Fatalf("parse error must not mask the (nil) exec error, got %v", err)
	}
	if report.Outcome != "" {
		t.Errorf("parse error must yield a zero-value report, got %+v", report)
	}
}

func TestRunCommand_EmptyArgv(t *testing.T) {
	if _, err := RunCommand(scriptAdapter{}).Run(context.Background(), TaskSpec{}, testEnv(t)); err == nil {
		t.Fatal("expected error for empty argv")
	}
}

func TestRunCommand_ExtraEnvReachesProcess(t *testing.T) {
	env := testEnv(t)
	marker := filepath.Join(env.ScratchDir, "env.txt")
	bin := writeScript(t, `echo "$PLOEG_TEST_MARKER" > `+marker)
	ad := scriptAdapter{argv: []string{bin}, extraEnv: []string{"PLOEG_TEST_MARKER=it-works"}}
	if _, err := RunCommand(ad).Run(context.Background(), TaskSpec{}, env); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(marker)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(b)) != "it-works" {
		t.Errorf("extra env not visible to process: %q", b)
	}
}
