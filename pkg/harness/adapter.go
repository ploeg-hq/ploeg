package harness

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/work"
)

// Adapter is the harness seam: one Run per claim. The orchestrator
// (pkg/worker) owns everything around it — claim/lease/outcome reporting,
// git clone and identity, credential lifecycle, and PR-based outcome
// detection (the forge is ground truth for every harness, so outcome
// inference by PR poll lives OUTSIDE the adapter).
//
// ctx cancellation means the lease is lost: the adapter must kill its
// harness and return promptly.
//
// Session-protocol harnesses (ACP, backlog #64) implement Adapter
// directly; spawn-and-wait harnesses implement CommandAdapter and are
// lifted by RunCommand.
//
// ExpectsLLM distinguishes LLM-driven adapters (openhands, claude-code)
// from exec-only adapters — used by the orchestrator's outcome heuristics
// (VIK-586/VIK-597).
type Adapter interface {
	Name() string
	Run(ctx context.Context, spec TaskSpec, env RunEnv) (OutcomeReport, error)
	ExpectsLLM() bool
}

// RunEnv is everything the orchestrator provisions for one run.
type RunEnv struct {
	RepoDir    string // clone with git identity already configured; the harness's working directory
	ScratchDir string // task files, outcome artifacts
	Prompt     string // Ploeg's delivery-contract prompt (composed by the orchestrator, not the harness)
	BaseEnv    []string
	LLM        LLMEnv
	Stdout     io.Writer // pod log passthrough
	Stderr     io.Writer
	Checkpoint func(work.Checkpoint) // best-effort progress reporting; may be nil
	Log        *slog.Logger
	// IdleTimeout ends a spawned harness that shows no activity for this
	// long, returning ErrIdle. Zero disables the watchdog.
	IdleTimeout time.Duration
	// Activity is the clock the idle watchdog reads. Output on stdout or
	// stderr touches it; the orchestrator may touch it for other signs of
	// life, such as model traffic. Nil means output alone counts.
	Activity *Activity
}

// ErrIdle is returned by RunCommand when its idle watchdog ended a harness.
var ErrIdle = harnessError("harness showed no activity (no output, no model traffic) within its idle timeout")

// LLMEnv is the harness-neutral LLM wiring for one run. Adapters translate
// it into harness-native env names (LLM_* for OpenHands, ANTHROPIC_* for
// Claude Code). The same values are also present in BaseEnv under the LLM_*
// names for harnesses that read them directly.
type LLMEnv struct {
	APIKey  string // per-run minted key; "" = the harness image authenticates itself
	BaseURL string // OpenAI-compatible base URL (LLM_BASE_URL passthrough)
	Model   string // model name with proxy prefixes stripped
	TraceID string // ploeg-<12hex>
}

// CommandAdapter is the spawn-and-wait sub-contract: prepare task input,
// hand back one invocation, interpret the process result. RunCommand lifts
// it to Adapter.
type CommandAdapter interface {
	Name() string
	// Prepare writes harness-specific task input (under env.ScratchDir) and
	// returns the invocation to exec in env.RepoDir.
	Prepare(spec TaskSpec, env RunEnv) (Invocation, error)
	// ParseOutcome maps the process result to a report. Return the zero
	// value to signal "no structured output" — the orchestrator then falls
	// back to forge ground truth and exit-code heuristics.
	ParseOutcome(spec TaskSpec, res ExecResult) (OutcomeReport, error)
	// ExpectsLLM reports whether this adapter is LLM-driven (true for
	// openhands/claudecode, false for exec). Used by the orchestrator's
	// outcome heuristics (VIK-586/VIK-597).
	ExpectsLLM() bool
}

// Invocation is one harness process to run.
type Invocation struct {
	Argv     []string // argv[0] = binary on PATH or absolute path
	ExtraEnv []string // appended to RunEnv.BaseEnv
	// OutcomeFile optionally names a file the harness is expected to write
	// its OutcomeReport JSON to; RunCommand stats it into ExecResult.
	OutcomeFile string
	// CaptureStdout buffers the process stdout (bounded) into
	// ExecResult.Stdout for adapters that parse a structured result
	// envelope. Output still passes through to RunEnv.Stdout either way.
	CaptureStdout bool
}

// ExecResult is what a finished harness process left behind.
type ExecResult struct {
	ExitCode    int
	Err         error  // exec error (incl. ctx cancellation), nil on exit 0
	LogTail     []byte // last 8 KiB of combined output
	Stdout      []byte // captured stdout when Invocation.CaptureStdout, else nil
	OutcomeFile string // Invocation.OutcomeFile if declared AND present, else ""
}

// maxCapturedStdout bounds ExecResult.Stdout so a runaway harness cannot
// exhaust worker memory.
const maxCapturedStdout = 1 << 20 // 1 MiB

// RunCommand lifts a CommandAdapter to Adapter: exec the invocation in
// RepoDir, tee output into an 8 KiB tail, then hand the result to
// ParseOutcome. A ParseOutcome error downgrades to "no structured signal"
// (logged), never masks the exec error.
func RunCommand(ca CommandAdapter) Adapter { return commandRunner{ca} }

type commandRunner struct{ ca CommandAdapter }

func (r commandRunner) Name() string     { return r.ca.Name() }
func (r commandRunner) ExpectsLLM() bool { return r.ca.ExpectsLLM() }

func (r commandRunner) Run(ctx context.Context, spec TaskSpec, env RunEnv) (OutcomeReport, error) {
	inv, err := r.ca.Prepare(spec, env)
	if err != nil {
		return OutcomeReport{}, err
	}
	if len(inv.Argv) == 0 {
		return OutcomeReport{}, errEmptyArgv
	}

	runCtx, stop := context.WithCancelCause(ctx)
	defer stop(nil)
	cmd := exec.CommandContext(runCtx, inv.Argv[0], inv.Argv[1:]...)
	cmd.Dir = env.RepoDir
	cmd.Env = append(append([]string{}, env.BaseEnv...), inv.ExtraEnv...)
	killProcessGroupOnCancel(cmd)
	cmd.WaitDelay = processWaitDelay

	var tail TailBuffer
	activity := env.Activity
	if activity == nil {
		activity = NewActivity()
	}
	activity.Touch()
	stdout := io.MultiWriter(nonNil(env.Stdout), &tail, activity)
	var captured *limitBuffer
	if inv.CaptureStdout {
		captured = &limitBuffer{max: maxCapturedStdout}
		stdout = io.MultiWriter(nonNil(env.Stdout), &tail, captured, activity)
	}
	cmd.Stdout = stdout
	cmd.Stderr = io.MultiWriter(nonNil(env.Stderr), &tail, activity)

	if env.IdleTimeout > 0 {
		go watchIdle(runCtx, stop, activity, env.IdleTimeout)
	}
	runErr := cmd.Run()
	if errors.Is(context.Cause(runCtx), ErrIdle) {
		runErr = fmt.Errorf("%w after %s: %v", ErrIdle, env.IdleTimeout, runErr)
	}

	res := ExecResult{ExitCode: -1, Err: runErr, LogTail: tail.Bytes()}
	if cmd.ProcessState != nil {
		res.ExitCode = cmd.ProcessState.ExitCode()
	}
	if captured != nil {
		res.Stdout = captured.buf.Bytes()
	}
	if inv.OutcomeFile != "" {
		if _, statErr := os.Stat(inv.OutcomeFile); statErr == nil {
			res.OutcomeFile = inv.OutcomeFile
		}
	}

	report, parseErr := r.ca.ParseOutcome(spec, res)
	if parseErr != nil {
		if env.Log != nil {
			env.Log.Warn("harness outcome parse failed; falling back to heuristics",
				"adapter", r.ca.Name(), "err", parseErr)
		}
		return OutcomeReport{}, runErr
	}
	return report, runErr
}

const processWaitDelay = 10 * time.Second

// Activity records when a harness last showed a sign of life. It is safe for
// concurrent use, and every Write touches it.
type Activity struct{ last atomic.Int64 }

// NewActivity returns an Activity touched now.
func NewActivity() *Activity {
	a := &Activity{}
	a.Touch()
	return a
}

func (a *Activity) Write(p []byte) (int, error) {
	a.Touch()
	return len(p), nil
}

// Touch records a sign of life now. A nil Activity ignores it.
func (a *Activity) Touch() {
	if a != nil {
		a.last.Store(time.Now().UnixNano())
	}
}

func (a *Activity) idleFor() time.Duration {
	return time.Since(time.Unix(0, a.last.Load()))
}

func watchIdle(ctx context.Context, stop context.CancelCauseFunc, activity *Activity, limit time.Duration) {
	every := min(limit/4, 5*time.Second)
	every = max(every, 10*time.Millisecond)
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if activity.idleFor() >= limit {
				stop(ErrIdle)
				return
			}
		}
	}
}

type harnessError string

func (e harnessError) Error() string { return string(e) }

const errEmptyArgv = harnessError("harness adapter returned an empty argv")

func nonNil(w io.Writer) io.Writer {
	if w == nil {
		return io.Discard
	}
	return w
}

// TailBuffer keeps the last 8 KiB written — enough for a stuck reason.
//
// Safe for concurrent use. RunCommand only reads it after cmd.Run() returns,
// but a session adapter (ACP) pumps stderr on its own goroutine and may read
// the tail on an early-return path while that pump is still writing. The race
// detector found exactly that; a mutex here fixes it for every adapter rather
// than asking each one to remember.
type TailBuffer struct {
	mu  sync.Mutex
	buf []byte
}

func (t *TailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if len(t.buf) > 8192 {
		t.buf = t.buf[len(t.buf)-8192:]
	}
	return len(p), nil
}

// Bytes returns a COPY: handing out the live slice would let a caller read it
// while the next Write reslices underneath them.
func (t *TailBuffer) Bytes() []byte {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]byte(nil), t.buf...)
}

// limitBuffer keeps the first max bytes written and drops the rest.
type limitBuffer struct {
	buf bytes.Buffer
	max int
}

func (l *limitBuffer) Write(p []byte) (int, error) {
	if room := l.max - l.buf.Len(); room > 0 {
		if len(p) > room {
			l.buf.Write(p[:room])
		} else {
			l.buf.Write(p)
		}
	}
	return len(p), nil
}

// KillProcessGroupOnCancel makes cancelling cmd's context kill the whole
// process group it starts, not only its first process.
func KillProcessGroupOnCancel(cmd *exec.Cmd) { killProcessGroupOnCancel(cmd) }

// ProcessWaitDelay bounds how long a cancelled command's output pipes stay
// open after its process group was killed.
const ProcessWaitDelay = processWaitDelay
