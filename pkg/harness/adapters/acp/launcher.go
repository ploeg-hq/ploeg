//go:build unix

package acp

import (
	"bufio"
	"context"
	"io"
	"os"
	"os/exec"
	"sync"
	"syscall"
)

// launcher is the subprocess seam. Injecting it is what lets the whole adapter
// be driven by an in-process fake in tests, with no `go build`, no network, and
// no real agent binary.
type launcher interface {
	launch(ctx context.Context, argv []string, dir string, env []string, stderr io.Writer, onRaw func([]byte)) (*process, error)
}

// process is a running agent, with its protocol channel already separated from
// its human-readable noise.
type process struct {
	// Stdin is async-buffered: a child that stops reading can never wedge the
	// JSON-RPC dispatcher, which is the classic stdio deadlock.
	Stdin io.WriteCloser
	// Stdout carries ONLY lines that look like JSON-RPC. Banners, progress
	// bars and stray console.log go to the stderr writer instead.
	Stdout io.Reader
	// Wait blocks for exit and returns the exit code. Safe to call repeatedly.
	Wait func() (int, error)
	// Signal delivers to the whole process GROUP, not just the direct child.
	Signal func(os.Signal) error
	// Kill SIGKILLs the group. Idempotent.
	Kill func()
}

// syscallTERM is the polite stop, delivered to the whole process group.
var syscallTERM os.Signal = syscall.SIGTERM

type execLauncher struct{}

func (execLauncher) launch(_ context.Context, argv []string, dir string, env []string, stderr io.Writer, onRaw func([]byte)) (*process, error) {
	if len(argv) == 0 {
		return nil, errEmptyArgv
	}
	// Deliberately NOT exec.CommandContext. Node-based ACP agents (opencode and
	// the npm adapter processes) fork children; killing only the direct child
	// orphans processes that still hold the DinD socket and the per-run LiteLLM
	// key. We own the lifecycle so we can signal the process group.
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = dir
	cmd.Env = env
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	stdinPipe, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	cmd.Stderr = stderr

	if err := cmd.Start(); err != nil {
		return nil, err
	}

	protocol := newJSONLFilter(stdoutPipe, stderr, onRaw)
	stdin := newAsyncWriter(stdinPipe)

	var (
		waitOnce sync.Once
		code     int
		waitErr  error
	)
	wait := func() (int, error) {
		waitOnce.Do(func() {
			waitErr = cmd.Wait()
			code = -1
			if cmd.ProcessState != nil {
				code = cmd.ProcessState.ExitCode()
			}
		})
		return code, waitErr
	}

	pgid := cmd.Process.Pid // Setpgid makes the child its own group leader
	signalGroup := func(sig os.Signal) error {
		s, ok := sig.(syscall.Signal)
		if !ok {
			return cmd.Process.Signal(sig)
		}
		// Negative pid = the whole group. Fall back to the direct child if the
		// group is already gone.
		if err := syscall.Kill(-pgid, s); err != nil {
			return cmd.Process.Signal(sig)
		}
		return nil
	}
	var killOnce sync.Once
	kill := func() {
		killOnce.Do(func() {
			_ = syscall.Kill(-pgid, syscall.SIGKILL)
			_ = cmd.Process.Kill()
		})
	}

	return &process{Stdin: stdin, Stdout: protocol, Wait: wait, Signal: signalGroup, Kill: kill}, nil
}

// --- stdout demultiplexing -------------------------------------------------

// maxProtocolLine bounds one JSON-RPC line. An agent that emits a single
// enormous line is malfunctioning; we would rather drop it than exhaust the
// worker's memory, and the worker container shares its limit with the harness.
const maxProtocolLine = 8 << 20

// newJSONLFilter splits an agent's stdout: lines whose first non-space byte is
// '{' go to the protocol channel, everything else is diverted to noise.
//
// Agents print banners, update notices, progress bars and the occasional
// console.log on stdout. Without this, the first such line is a JSON-RPC parse
// error and the session dies for a cosmetic reason. With it, a wrong
// subcommand (`opencode` instead of `opencode acp`) shows up as "initialize
// never completed, and here is the human-readable text it printed instead" —
// a diagnosable infra failure rather than a mystery.
// onRaw, when non-nil, receives every protocol line BEFORE the SDK parses it.
// That ordering is load-bearing: coder/acp-go-sdk v0.13.5 is generated from
// schema 0.13.5 and its session/update union does not carry the ACP v1 usage
// shape ({used,size,cost}), so an agent sending v1 usage has it silently
// dropped by the SDK's decoder. Reading semantics off the raw line is the only
// mitigation that actually holds — a decoder placed AFTER the SDK inherits
// whatever the SDK failed to parse.
func newJSONLFilter(src io.Reader, noise io.Writer, onRaw func([]byte)) io.Reader {
	pr, pw := io.Pipe()
	go func() {
		sc := bufio.NewScanner(src)
		sc.Buffer(make([]byte, 0, 64<<10), maxProtocolLine)
		for sc.Scan() {
			line := sc.Bytes()
			if isJSONLine(line) {
				if onRaw != nil {
					onRaw(append([]byte{}, line...))
				}
				if _, err := pw.Write(append(append([]byte{}, line...), '\n')); err != nil {
					// The reader went away; drain the rest into noise so the
					// child never blocks on a full pipe.
					_, _ = io.Copy(noise, src)
					break
				}
				continue
			}
			if noise != nil && len(line) > 0 {
				_, _ = noise.Write(append(append([]byte{}, line...), '\n'))
			}
		}
		_ = pw.CloseWithError(sc.Err())
	}()
	return pr
}

func isJSONLine(b []byte) bool {
	for _, c := range b {
		switch c {
		case ' ', '\t', '\r':
			continue
		case '{':
			return true
		default:
			return false
		}
	}
	return false
}

// --- async stdin -----------------------------------------------------------

const stdinQueueDepth = 64

// asyncWriter decouples the dispatcher from the child's stdin pipe. Every
// client-side response is handed to a buffered channel rather than written
// inline, so a child that stops reading stalls one goroutine instead of the
// whole protocol.
type asyncWriter struct {
	ch     chan []byte
	closed chan struct{}
	once   sync.Once
	w      io.WriteCloser
}

func newAsyncWriter(w io.WriteCloser) *asyncWriter {
	a := &asyncWriter{ch: make(chan []byte, stdinQueueDepth), closed: make(chan struct{}), w: w}
	go func() {
		defer w.Close()
		for {
			select {
			case b, ok := <-a.ch:
				if !ok {
					return
				}
				if _, err := w.Write(b); err != nil {
					return
				}
			case <-a.closed:
				return
			}
		}
	}()
	return a
}

func (a *asyncWriter) Write(p []byte) (int, error) {
	b := append([]byte{}, p...)
	select {
	case a.ch <- b:
		return len(p), nil
	case <-a.closed:
		return 0, io.ErrClosedPipe
	default:
		// Queue full: the child is not draining. Dropping is wrong (the
		// protocol would desync) and blocking is worse (deadlock), so report
		// it and let the caller's watchdog escalate to SIGTERM.
		return 0, errStdinBacklog
	}
}

func (a *asyncWriter) Close() error {
	a.once.Do(func() { close(a.closed) })
	return nil
}

type acpError string

func (e acpError) Error() string { return string(e) }

const (
	errEmptyArgv    = acpError("acp: empty argv")
	errStdinBacklog = acpError("acp: agent is not reading stdin (write queue full)")
)
