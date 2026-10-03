// Package acp adapts any agent speaking the Agent Client Protocol (ACP wire
// version 1) to Ploeg's harness seam — opencode, Gemini CLI, Goose and
// OpenHands natively, plus Codex and Claude Code through npm adapter processes.
// Backlog #64; it also closes #63 (a bespoke opencode adapter).
//
// This implements harness.Adapter DIRECTLY rather than harness.CommandAdapter.
// ACP is a session protocol: the adapter owns a subprocess, a bidirectional
// JSON-RPC channel and a cancel handshake, none of which fit spawn-and-wait.
// pkg/harness/adapter.go's own doc comment specifies this split.
//
// Protocol version 1 only. v2 exists as alpha pre-releases whose announcement
// says the wire protocol "can, and will, change"; homelab-cluster ADR-0051
// records the decision to stay on v1.
package acp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	sdk "github.com/coder/acp-go-sdk"

	"github.com/ploeg-hq/ploeg/pkg/harness"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

// Timeouts. Every phase is bounded, because a wedged agent that never returns
// would otherwise hold the lease until the sweeper reclaims it — turning a
// retryable stall into fifteen minutes of nothing.
const (
	defaultInitTimeout    = 30 * time.Second
	defaultSessionTimeout = 60 * time.Second
	defaultPromptTimeout  = 45 * time.Minute
	defaultIdleTimeout    = 10 * time.Minute
	defaultCancelGrace    = 30 * time.Second
	defaultTermGrace      = 10 * time.Second
)

// Options configure one adapter. Zero values take the defaults above.
type Options struct {
	PermissionMode PermissionMode
	MaxPermissions int
	MaxPerMinute   int
	PromptTimeout  time.Duration
	IdleTimeout    time.Duration
	CancelGrace    time.Duration
	TermGrace      time.Duration
}

// Adapter drives one ACP agent per run.
type Adapter struct {
	profile Profile
	opts    Options
	launch  launcher // seam: an in-process fake in tests
}

// New builds an adapter for a named profile. It returns an error rather than
// falling back to a default, so a misconfigured team fails at worker startup —
// before Claim, so it never burns a lease or an attempt.
func New(profileName string, overrides ProfileOverrides, opts Options) (*Adapter, error) {
	p, err := Lookup(profileName, overrides)
	if err != nil {
		return nil, err
	}
	return &Adapter{profile: p, opts: withDefaults(opts), launch: execLauncher{}}, nil
}

func withDefaults(o Options) Options {
	if o.PermissionMode == "" {
		o.PermissionMode = PermissionAllowAll
	}
	if o.PromptTimeout <= 0 {
		o.PromptTimeout = defaultPromptTimeout
	}
	if o.IdleTimeout <= 0 {
		o.IdleTimeout = defaultIdleTimeout
	}
	if o.CancelGrace <= 0 {
		o.CancelGrace = defaultCancelGrace
	}
	if o.TermGrace <= 0 {
		o.TermGrace = defaultTermGrace
	}
	return o
}

func (a *Adapter) Name() string     { return "acp" }
func (a *Adapter) ExpectsLLM() bool { return true }

// Run executes one prompt turn and maps the result onto an OutcomeReport.
//
// Phases: launch → initialize → session/new → session/prompt → shutdown. Each
// records how far it got, because a failure BEFORE the prompt is an
// infrastructure problem with the pod rather than a problem with the ticket —
// the distinction architecture.md §9.9 (VIK-596) says is currently inverted.
func (a *Adapter) Run(ctx context.Context, spec harness.TaskSpec, env harness.RunEnv) (harness.OutcomeReport, error) {
	log := env.Log
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	state := newSessionState()
	perms := NewPermissionPolicy(a.opts.PermissionMode, a.opts.MaxPermissions, a.opts.MaxPerMinute)

	var tail harness.TailBuffer
	noise := io.MultiWriter(nonNil(env.Stderr), &tail)

	var cp *checkpointer
	defer func() { cp.stop() }()

	// stopProcess is a no-op until the agent is launched, and the shutdown
	// sequence afterwards. finish() runs it BEFORE reading the tail: os/exec
	// copies the child's stderr on its own goroutine, and that copy is only
	// guaranteed complete once Wait returns. Without this ordering a child that
	// prints its complaint and exits immediately — `opencode: unknown command
	// 'acp'`, the single most likely misconfiguration — races us, and the
	// failure reason arrives carrying only "peer disconnected", which is
	// undiagnosable. Total wall time is unchanged: the deferred shutdown ran
	// before Run returned anyway.
	stopProcess := func() {}
	res := result{phase: phaseLaunch}

	// The drop box (ADR-0018). ACP reports a stop reason, not a review: the
	// protocol has no channel for a reading Run's findings and verdict, so the
	// agent returns those the same way it does on every other harness — the
	// file PLOEG_OUTCOME_FILE names. Read in finish() because finish() is the
	// single funnel every return path uses, so a review survives a failed
	// shutdown handshake, a watchdog, and a lost lease alike.
	outcomePath := harness.DropBoxPath(env.ScratchDir, spec.TraceID)
	_ = os.Remove(outcomePath) // never inherit a previous run's report

	finish := func() (harness.OutcomeReport, error) {
		stopProcess()
		res.stderrTail = tailString(&tail)
		res.ctxErr = ctx.Err()
		rep := Build(state, res)
		box, boxErr := harness.ReadDropBox(outcomePath)
		if boxErr != nil {
			// A corrupt drop box is the agent's fault and must not mask the
			// adapter's own classification of the run.
			log.Warn("ignoring unreadable outcome drop box", "path", outcomePath, "err", boxErr)
			return rep, res.err
		}
		return harness.MergeDropBox(rep, box), res.err
	}

	argv, extraEnv, err := a.profile.Prepare(spec, env, a.opts.PermissionMode)
	if err != nil {
		res.err = fmt.Errorf("acp profile %q: %w", a.profile.Name, err)
		return finish()
	}
	extraEnv = append(extraEnv, harness.DropBoxEnv+"="+outcomePath)
	log.Info("starting acp agent", "profile", a.profile.Name, "argv", argv)

	// State is accumulated from the RAW protocol line, never from the SDK's
	// parsed union — see newJSONLFilter for why. The SDK is used for transport
	// and request/response correlation only.
	cp = newCheckpointerFor(env.Checkpoint, spec.Branch)
	onRaw := func(line []byte) {
		if kind, ok := decodeSessionUpdate(line, state); ok {
			cp.observe(kind, state)
		}
	}
	proc, err := a.launch.launch(ctx, argv, env.RepoDir, append(append([]string{}, env.BaseEnv...), extraEnv...), noise, onRaw)
	if err != nil {
		res.err = err
		return finish()
	}

	// Guarantees, in order, on every return path: the process group dies, and
	// Wait is reaped so we never leak a zombie.
	var shutdownOnce sync.Once
	shutdown := func() {
		shutdownOnce.Do(func() {
			_ = proc.Stdin.Close()
			_ = proc.Signal(syscallTERM)
			select {
			case <-waitDone(proc):
			case <-time.After(a.opts.TermGrace):
				proc.Kill()
				<-waitDone(proc)
			}
		})
	}
	defer shutdown()
	stopProcess = shutdown

	cl := newClient(state, perms, log)
	conn := sdk.NewClientSideConnection(cl, proc.Stdin, proc.Stdout)
	// NOT conn.SetLogger(log): NewClientSideConnection starts the protocol read
	// loop before it returns, and SetLogger writes a field that loop reads —
	// the race detector flags it every run. Reported upstream-worthy; we lose
	// nothing, since the agent's own output already reaches the pod log through
	// the launcher's noise channel.

	// --- initialize ---
	res.phase = phaseInit
	initCtx, cancelInit := context.WithTimeout(ctx, defaultInitTimeout)
	initResp, err := conn.Initialize(initCtx, sdk.InitializeRequest{
		ProtocolVersion: sdk.ProtocolVersionNumber,
		ClientInfo:      &sdk.Implementation{Name: "ploeg", Version: "1"},
		// Everything false on purpose — see client.go for why fs/* and
		// terminal/* are refused rather than implemented.
		ClientCapabilities: sdk.ClientCapabilities{Terminal: false},
	})
	cancelInit()
	if err != nil {
		res.err = fmt.Errorf("initialize: %w", err)
		return finish()
	}
	if got := int(initResp.ProtocolVersion); got != sdk.ProtocolVersionNumber {
		// Never soldier on across a version we cannot decode: that
		// manufactures garbage outcomes, which is the opposite of the point.
		res.err = fmt.Errorf("agent negotiated protocol version %d, this client speaks %d",
			got, sdk.ProtocolVersionNumber)
		return finish()
	}

	// --- session/new ---
	res.phase = phaseNewSess
	sessCtx, cancelSess := context.WithTimeout(ctx, defaultSessionTimeout)
	sess, err := conn.NewSession(sessCtx, sdk.NewSessionRequest{
		Cwd:        env.RepoDir,
		McpServers: []sdk.McpServer{},
	})
	cancelSess()
	if err != nil {
		res.err = fmt.Errorf("session/new: %w", err)
		return finish()
	}
	cp.mark("agent_started")

	// --- session/prompt ---
	res.phase = phasePrompt
	promptCtx, cancelPrompt := context.WithCancel(ctx)
	defer cancelPrompt()

	done := make(chan promptOutcome, 1)
	go func() {
		resp, err := conn.Prompt(promptCtx, sdk.PromptRequest{
			SessionId: sess.SessionId,
			Prompt:    []sdk.ContentBlock{{Text: &sdk.ContentBlockText{Text: env.Prompt}}},
		})
		done <- promptOutcome{resp, err}
	}()

	stop, why := a.awaitPrompt(ctx, done, cl, state, conn, sess.SessionId, log)
	switch why {
	case stopNormal:
		res.phase = phaseCompleted
		res.stop = ParseStopReason(string(stop.resp.StopReason))
		res.err = stop.err
		if stop.err != nil {
			res.phase = phasePrompt // died mid-flight, not a clean stop reason
		}
	case stopTimeout:
		res.timedOut = true
	case stopStorm:
		res.permStorm = true
	case stopCancelled:
		// ctx.Err() is picked up in finish(); the lease was lost.
	}

	if why != stopNormal {
		// Give the agent the cancel it is owed, then stop waiting.
		cancelCtx, cancelC := context.WithTimeout(context.Background(), 2*time.Second)
		_ = conn.Cancel(cancelCtx, sdk.CancelNotification{SessionId: sess.SessionId})
		cancelC()
		select {
		case r := <-done:
			// The agent honoured it and gave us a real stop reason.
			if r.err == nil {
				res.stop = ParseStopReason(string(r.resp.StopReason))
			}
		case <-time.After(a.opts.CancelGrace):
		}
		cancelPrompt()
	}

	if unknown := state.unknownEnumValues(); len(unknown) > 0 {
		log.Warn("acp: unrecognised protocol values (ignored, run unaffected)",
			"values", strings.Join(unknown, ","))
	}
	shutdown()
	if code, _ := proc.Wait(); code > 0 {
		res.exitCode = code
	}
	return finish()
}

type stopWhy int

const (
	stopNormal stopWhy = iota
	stopTimeout
	stopStorm
	stopCancelled
)

type promptOutcome struct {
	resp sdk.PromptResponse
	err  error
}

// awaitPrompt waits for the turn, racing it against three independent
// watchdogs. They run on their own timers precisely so a wedged read loop
// cannot also wedge the escalation.
func (a *Adapter) awaitPrompt(
	ctx context.Context,
	done <-chan promptOutcome,
	cl *client, state *sessionState, conn *sdk.ClientSideConnection,
	_ sdk.SessionId, log *slog.Logger,
) (promptOutcome, stopWhy) {

	hard := time.NewTimer(a.opts.PromptTimeout)
	defer hard.Stop()
	idle := time.NewTicker(a.opts.IdleTimeout / 2)
	defer idle.Stop()

	lastEvents := -1
	for {
		select {
		case r := <-done:
			return r, stopNormal

		case <-ctx.Done():
			log.Warn("acp: run cancelled (lease lost)")
			return promptOutcome{}, stopCancelled

		case <-cl.stormed():
			return promptOutcome{}, stopStorm

		case <-hard.C:
			log.Warn("acp: prompt exceeded its hard wall", "timeout", a.opts.PromptTimeout)
			return promptOutcome{}, stopTimeout

		case <-idle.C:
			// Progress is any protocol event at all. Two consecutive ticks
			// with no new event means the agent has genuinely stopped.
			n := state.eventCount()
			if n == lastEvents {
				log.Warn("acp: no protocol activity", "idleTimeout", a.opts.IdleTimeout, "events", n)
				return promptOutcome{}, stopTimeout
			}
			lastEvents = n

		case <-conn.Done():
			// Transport died. Drain done so the error surfaces rather than
			// being reported as a timeout.
			select {
			case r := <-done:
				return r, stopNormal
			case <-time.After(time.Second):
				return promptOutcome{err: errors.New("acp: transport closed mid-prompt")}, stopNormal
			}
		}
	}
}

// --- checkpoints -----------------------------------------------------------

// Checkpoint policy: phase transitions only, deduplicated, plus a throttled
// heartbeat. Each call is an HTTP round-trip and a two-insert Postgres
// transaction, so this cannot run per event — and it is invoked from the
// protocol read loop, so it must never block.
const (
	checkpointHeartbeat = 5 * time.Minute
	maxCheckpoints      = 15
)

type checkpointer struct {
	emit   func(work.Checkpoint)
	branch string
	ch     chan work.Checkpoint
	mu     sync.Mutex
	seen   map[string]bool
	last   time.Time
	sent   int
	done   chan struct{}
	once   sync.Once
}

func newCheckpointerFor(emit func(work.Checkpoint), branch string) *checkpointer {
	c := &checkpointer{emit: emit, branch: branch, seen: map[string]bool{}, done: make(chan struct{})}
	if emit == nil {
		return c // nil-guarded: no goroutine, every mark is a no-op
	}
	c.ch = make(chan work.Checkpoint, 8)
	go func() {
		for {
			select {
			case cp := <-c.ch:
				emit(cp)
			case <-c.done:
				return
			}
		}
	}()
	return c
}

// mark records a phase transition at most once.
func (c *checkpointer) mark(phase string) {
	if c.ch == nil {
		return
	}
	c.mu.Lock()
	if c.seen[phase] || c.sent >= maxCheckpoints {
		c.mu.Unlock()
		return
	}
	c.seen[phase] = true
	c.sent++
	c.last = time.Now()
	c.mu.Unlock()
	c.send(work.Checkpoint{Phase: phase, Branch: c.branch})
}

// observe turns protocol events into the two phases worth recording, plus a
// throttled heartbeat. changed_files is the one that earns its keep: it
// separates "the pod died before doing anything" from "the pod died after
// forty minutes of real work" — exactly the forensics VIK-597 wanted.
func (c *checkpointer) observe(kind UpdateKind, s *sessionState) {
	if c.ch == nil {
		return
	}
	switch kind {
	case UpdatePlan:
		c.mark("plan_ready")
	case UpdateToolCall, UpdateToolCallUpdate:
		if s.mutated() {
			c.mark("changes_made")
		}
	}
	c.mu.Lock()
	stale := time.Since(c.last) > checkpointHeartbeat && c.sent < maxCheckpoints
	if stale {
		c.last = time.Now()
		c.sent++
	}
	c.mu.Unlock()
	if stale {
		c.send(work.Checkpoint{Phase: "progress", Branch: c.branch})
	}
}

// send never blocks: dropping a checkpoint is always better than stalling the
// protocol read loop.
func (c *checkpointer) send(cp work.Checkpoint) {
	select {
	case c.ch <- cp:
	default:
	}
}

func (c *checkpointer) stop() {
	if c == nil || c.ch == nil {
		return
	}
	c.once.Do(func() { close(c.done) })
}

// decodeSessionUpdate pulls a session/update payload straight off the wire and
// folds it into state. Returns false for any other method.
func decodeSessionUpdate(line []byte, s *sessionState) (UpdateKind, bool) {
	var rpc rpcLine
	if err := json.Unmarshal(line, &rpc); err != nil || rpc.Method != "session/update" {
		return UpdateUnknown, false
	}
	var params sessionUpdateParams
	if err := json.Unmarshal(rpc.Params, &params); err != nil || len(params.Update) == 0 {
		return UpdateUnknown, false
	}
	return s.applyUpdate(params.Update), true
}

// --- small helpers ---------------------------------------------------------

func waitDone(p *process) <-chan struct{} {
	ch := make(chan struct{})
	go func() { _, _ = p.Wait(); close(ch) }()
	return ch
}

func nonNil(w io.Writer) io.Writer {
	if w == nil {
		return io.Discard
	}
	return w
}

func tailString(t *harness.TailBuffer) string { return strings.TrimSpace(string(t.Bytes())) }

var _ harness.Adapter = (*Adapter)(nil)
