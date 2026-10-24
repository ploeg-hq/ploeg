package launch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"sync"
	"syscall"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/harness"
)

// ErrClosed is returned once the connection to the launcher is gone.
var ErrClosed = errors.New("the connection to the harness launcher is closed")

const helloTimeout = 30 * time.Second

// Client is the worker's end of the launcher connection. It implements
// harness.Spawner, so every process it starts runs in the harness's
// container.
type Client struct {
	conn       net.Conn
	pid        int
	executable string
	writeMu    sync.Mutex
	mu         sync.Mutex
	next       uint32
	procs      map[uint32]*remoteProcess
	replies    map[uint32]chan reply
	done       chan struct{}
	err        error
}

var _ harness.Spawner = (*Client)(nil)

// Accept listens on socket, takes the first connection, removes the socket
// so nothing else can connect, and completes the handshake with the
// launcher on the other end.
func Accept(ctx context.Context, socket string) (*Client, error) {
	if err := os.Remove(socket); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("clear the launcher socket %s: %w", socket, err)
	}
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: socket, Net: "unix"})
	if err != nil {
		return nil, fmt.Errorf("listen for the harness launcher on %s: %w", socket, err)
	}
	ln.SetUnlinkOnClose(true)
	stop := context.AfterFunc(ctx, func() { _ = ln.Close() })
	conn, err := ln.Accept()
	stop()
	_ = ln.Close()
	if err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("no harness launcher connected to %s: %w", socket, context.Cause(ctx))
		}
		return nil, fmt.Errorf("accept the harness launcher on %s: %w", socket, err)
	}
	return NewClient(conn)
}

// NewClient completes the handshake on conn and starts reading it.
func NewClient(conn net.Conn) (*Client, error) {
	_ = conn.SetReadDeadline(time.Now().Add(helloTimeout))
	f, err := readFrame(conn)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("read the launcher's hello: %w", err)
	}
	var h hello
	if f.kind != frameHello || json.Unmarshal(f.payload, &h) != nil {
		conn.Close()
		return nil, errors.New("the launcher did not start with a hello")
	}
	if h.Protocol != ProtocolVersion {
		conn.Close()
		return nil, fmt.Errorf("the launcher speaks protocol %d, this worker %d; run both from the same ploeg-worker binary", h.Protocol, ProtocolVersion)
	}
	_ = conn.SetReadDeadline(time.Time{})
	c := &Client{conn: conn, pid: h.PID, executable: h.Executable, procs: map[uint32]*remoteProcess{}, replies: map[uint32]chan reply{}, done: make(chan struct{})}
	go c.read()
	return c, nil
}

// PID is the launcher's process id in its own PID namespace; 1 when it is
// its container's first process, as the chart runs it.
func (c *Client) PID() int { return c.pid }

// Executable is the launcher binary's path in the harness's container, the
// program a harness-side helper such as the Claude Code hook guard runs.
func (c *Client) Executable() string { return c.executable }

// Close ends the connection; the launcher then kills every process it
// started.
func (c *Client) Close() error {
	err := c.conn.Close()
	<-c.done
	return err
}

func (c *Client) read() {
	for {
		f, err := readFrame(c.conn)
		if err != nil {
			c.fail(err)
			return
		}
		c.dispatch(f)
	}
}

func (c *Client) fail(err error) {
	c.mu.Lock()
	c.err = fmt.Errorf("%w: %v", ErrClosed, err)
	procs, replies := c.procs, c.replies
	c.procs, c.replies = map[uint32]*remoteProcess{}, map[uint32]chan reply{}
	close(c.done)
	c.mu.Unlock()
	for _, p := range procs {
		p.lost(c.err)
	}
	for _, ch := range replies {
		close(ch)
	}
}

func (c *Client) closedErr() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err != nil {
		return c.err
	}
	return ErrClosed
}

func (c *Client) dispatch(f frame) {
	if f.kind == frameReply {
		c.mu.Lock()
		ch := c.replies[f.stream]
		delete(c.replies, f.stream)
		c.mu.Unlock()
		var r reply
		if ch != nil && json.Unmarshal(f.payload, &r) == nil {
			ch <- r
		}
		return
	}
	c.mu.Lock()
	p := c.procs[f.stream]
	c.mu.Unlock()
	if p == nil {
		return
	}
	switch f.kind {
	case frameStarted:
		var s started
		_ = json.Unmarshal(f.payload, &s)
		p.started <- s
	case frameStdout:
		p.writeStdout(f.payload)
	case frameStdoutEnd:
		p.endStdout(nil)
	case frameStderr:
		_, _ = p.stderr.Write(f.payload)
	case frameExit:
		var e exited
		_ = json.Unmarshal(f.payload, &e)
		c.mu.Lock()
		delete(c.procs, f.stream)
		c.mu.Unlock()
		p.exit(e.Code, exitError(e))
	}
}

func exitError(e exited) error {
	if e.Code == 0 && e.Error == "" {
		return nil
	}
	status := e.Error
	if status == "" {
		status = fmt.Sprintf("exit status %d", e.Code)
	}
	return &harness.ExitError{Code: e.Code, Status: status}
}

func (c *Client) send(f frame) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	select {
	case <-c.done:
		return c.closedErr()
	default:
	}
	return writeFrame(c.conn, f)
}

func (c *Client) id() uint32 {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.next++
	return c.next
}

// Start runs cmd in the harness's container.
func (c *Client) Start(ctx context.Context, cmd harness.Command) (harness.Process, error) {
	if len(cmd.Argv) == 0 {
		return nil, errors.New("launch: empty argv")
	}
	p := &remoteProcess{client: c, started: make(chan started, 1), stderr: writerOrDiscard(cmd.Stderr), exited: make(chan struct{})}
	if cmd.StdoutPipe {
		p.stdoutPipe = newChunkQueue()
	} else {
		p.stdout = writerOrDiscard(cmd.Stdout)
	}
	if cmd.Stdin {
		p.stdin = &remoteStdin{p: p}
	}
	c.mu.Lock()
	if c.err != nil {
		c.mu.Unlock()
		return nil, c.err
	}
	c.next++
	p.id = c.next
	c.procs[p.id] = p
	c.mu.Unlock()
	env := cmd.Env
	if env == nil {
		env = []string{}
	}
	if err := c.send(jsonFrame(frameStart, p.id, startRequest{Argv: cmd.Argv, Dir: cmd.Dir, Env: env, Stdin: cmd.Stdin})); err != nil {
		c.forget(p.id)
		return nil, err
	}
	select {
	case s := <-p.started:
		if s.Error != "" {
			c.forget(p.id)
			return nil, errors.New(s.Error)
		}
	case <-p.exited:
		return nil, p.err
	case <-ctx.Done():
		p.Kill()
		return nil, context.Cause(ctx)
	}
	go p.killOnCancel(ctx)
	return p, nil
}

func (c *Client) forget(id uint32) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.procs, id)
}

func (c *Client) ask(ctx context.Context, q query) (reply, error) {
	id := c.id()
	ch := make(chan reply, 1)
	c.mu.Lock()
	if c.err != nil {
		c.mu.Unlock()
		return reply{}, c.err
	}
	c.replies[id] = ch
	c.mu.Unlock()
	if err := c.send(jsonFrame(frameQuery, id, q)); err != nil {
		return reply{}, err
	}
	select {
	case r, ok := <-ch:
		if !ok {
			return reply{}, c.closedErr()
		}
		if r.Error != "" {
			return r, errors.New(r.Error)
		}
		return r, nil
	case <-ctx.Done():
		return reply{}, context.Cause(ctx)
	}
}

// LookPath resolves file against path in the harness's container.
func (c *Client) LookPath(ctx context.Context, file, path string) (string, error) {
	r, err := c.ask(ctx, query{Op: queryLookPath, File: file, Path: path})
	return r.Path, err
}

// CheckDir fails unless dir is a directory in the harness's container.
func (c *Client) CheckDir(ctx context.Context, dir string) error {
	_, err := c.ask(ctx, query{Op: queryCheckDir, Dir: dir})
	return err
}

// Environ is the harness container's own environment.
func (c *Client) Environ(ctx context.Context) ([]string, error) {
	r, err := c.ask(ctx, query{Op: queryEnviron})
	return r.Env, err
}

// KillAll ends every process left in the harness's container.
func (c *Client) KillAll(ctx context.Context) error {
	_, err := c.ask(ctx, query{Op: queryKillAll})
	return err
}

type remoteProcess struct {
	client     *Client
	id         uint32
	started    chan started
	stdout     io.Writer
	stdoutPipe *chunkQueue
	stderr     io.Writer
	stdin      *remoteStdin
	exitOnce   sync.Once
	exited     chan struct{}
	code       int
	err        error
}

func (p *remoteProcess) Stdin() io.WriteCloser {
	if p.stdin == nil {
		return nil
	}
	return p.stdin
}

func (p *remoteProcess) Stdout() io.Reader {
	if p.stdoutPipe == nil {
		return nil
	}
	return p.stdoutPipe
}

func (p *remoteProcess) writeStdout(b []byte) {
	if p.stdoutPipe != nil {
		p.stdoutPipe.push(b)
		return
	}
	_, _ = p.stdout.Write(b)
}

func (p *remoteProcess) endStdout(err error) {
	if p.stdoutPipe != nil {
		p.stdoutPipe.close(err)
	}
}

func (p *remoteProcess) exit(code int, err error) {
	p.exitOnce.Do(func() {
		p.code, p.err = code, err
		p.endStdout(nil)
		close(p.exited)
	})
}

func (p *remoteProcess) lost(err error) {
	p.endStdout(err)
	p.exit(-1, err)
}

func (p *remoteProcess) Wait() (int, error) {
	<-p.exited
	return p.code, p.err
}

func (p *remoteProcess) Signal(sig syscall.Signal) error {
	select {
	case <-p.exited:
		return os.ErrProcessDone
	default:
	}
	return p.client.send(jsonFrame(frameSignal, p.id, signalRequest{Signal: int(sig)}))
}

func (p *remoteProcess) Kill() { _ = p.Signal(syscall.SIGKILL) }

func (p *remoteProcess) killOnCancel(ctx context.Context) {
	select {
	case <-ctx.Done():
		p.Kill()
	case <-p.exited:
	}
}

type remoteStdin struct {
	p      *remoteProcess
	closed sync.Once
}

func (s *remoteStdin) Write(b []byte) (int, error) {
	select {
	case <-s.p.exited:
		return 0, io.ErrClosedPipe
	default:
	}
	written := 0
	for len(b) > 0 {
		n := min(len(b), chunkSize)
		if err := s.p.client.send(frame{kind: frameStdin, stream: s.p.id, payload: append([]byte(nil), b[:n]...)}); err != nil {
			return written, err
		}
		written += n
		b = b[n:]
	}
	return written, nil
}

func (s *remoteStdin) Close() error {
	var err error
	s.closed.Do(func() {
		select {
		case <-s.p.exited:
		default:
			err = s.p.client.send(frame{kind: frameStdinClose, stream: s.p.id})
		}
	})
	return err
}

func writerOrDiscard(w io.Writer) io.Writer {
	if w == nil {
		return io.Discard
	}
	return w
}
