//go:build unix

package launch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/harness"
)

const (
	dialRetry  = 100 * time.Millisecond
	drainDelay = 10 * time.Second
)

// Serve dials the worker's socket until it answers, then runs what the
// worker sends until the connection ends or ctx is cancelled. Every process
// it started is killed before it returns.
func Serve(ctx context.Context, socket string) error {
	var d net.Dialer
	for {
		conn, err := d.DialContext(ctx, "unix", socket)
		if err == nil {
			return ServeConn(ctx, conn)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("the worker never listened on %s: %w", socket, context.Cause(ctx))
		case <-time.After(dialRetry):
		}
	}
}

// ServeConn runs the launcher's side of one connection.
func ServeConn(ctx context.Context, conn net.Conn) error {
	s := &server{conn: conn, pid: os.Getpid(), procs: map[uint32]*serverProcess{}, children: map[int]bool{}, groups: map[int]bool{}}
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	defer conn.Close()
	defer s.killAll()
	reapCtx, stopReaping := context.WithCancel(ctx)
	defer stopReaping()
	if s.pid == 1 {
		go s.reapOrphans(reapCtx)
	}
	executable, _ := os.Executable()
	if err := s.send(jsonFrame(frameHello, 0, hello{Protocol: ProtocolVersion, PID: s.pid, Executable: executable})); err != nil {
		return err
	}
	for {
		f, err := readFrame(conn)
		if err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) || ctx.Err() != nil {
				return nil
			}
			return err
		}
		s.handle(f)
	}
}

type server struct {
	conn     net.Conn
	pid      int
	writeMu  sync.Mutex
	mu       sync.Mutex
	procs    map[uint32]*serverProcess
	children map[int]bool
	groups   map[int]bool
}

func (s *server) send(f frame) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return writeFrame(s.conn, f)
}

func (s *server) process(id uint32) *serverProcess {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.procs[id]
}

func (s *server) handle(f frame) {
	switch f.kind {
	case frameStart:
		var req startRequest
		if err := json.Unmarshal(f.payload, &req); err != nil {
			_ = s.send(jsonFrame(frameStarted, f.stream, started{Error: "launch: unreadable start request"}))
			return
		}
		s.start(f.stream, req)
	case frameStdin:
		if p := s.process(f.stream); p != nil && p.stdin != nil {
			p.stdin.push(f.payload)
		}
	case frameStdinClose:
		if p := s.process(f.stream); p != nil && p.stdin != nil {
			p.stdin.close(nil)
		}
	case frameSignal:
		var req signalRequest
		if p := s.process(f.stream); p != nil && json.Unmarshal(f.payload, &req) == nil {
			p.signal(syscall.Signal(req.Signal))
		}
	case frameQuery:
		var q query
		if err := json.Unmarshal(f.payload, &q); err != nil {
			_ = s.send(jsonFrame(frameReply, f.stream, reply{Error: "launch: unreadable query"}))
			return
		}
		_ = s.send(jsonFrame(frameReply, f.stream, s.answer(q)))
	}
}

func (s *server) answer(q query) reply {
	switch q.Op {
	case queryLookPath:
		path, err := harness.LookPath(q.File, q.Path)
		return reply{Path: path, Error: errorText(err)}
	case queryCheckDir:
		return reply{Error: errorText(harness.CheckDir(q.Dir))}
	case queryEnviron:
		return reply{Env: os.Environ()}
	case queryKillAll:
		s.killAll()
		return reply{}
	}
	return reply{Error: fmt.Sprintf("launch: unknown query %q", q.Op)}
}

func errorText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func (s *server) killAll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for group := range s.groups {
		_ = syscall.Kill(-group, syscall.SIGKILL)
	}
	if s.pid == 1 {
		_ = syscall.Kill(-1, syscall.SIGKILL)
	}
}

type serverProcess struct {
	server *server
	id     uint32
	cmd    *exec.Cmd
	stdin  *chunkQueue
	stdout *os.File
	stderr *os.File
}

func (s *server) start(id uint32, req startRequest) {
	p, err := s.spawn(id, req)
	if err != nil {
		_ = s.send(jsonFrame(frameStarted, id, started{Error: err.Error()}))
		return
	}
	_ = s.send(jsonFrame(frameStarted, id, started{}))
	forwarded := make(chan struct{}, 2)
	go p.forward(p.stdout, frameStdout, frameStdoutEnd, forwarded)
	go p.forward(p.stderr, frameStderr, frameStderrEnd, forwarded)
	go p.wait(forwarded)
}

func (s *server) spawn(id uint32, req startRequest) (*serverProcess, error) {
	if len(req.Argv) == 0 {
		return nil, errors.New("launch: empty argv")
	}
	cmd := exec.Command(req.Argv[0], req.Argv[1:]...)
	cmd.Dir = req.Dir
	cmd.Env = append([]string{}, req.Env...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdoutR, stdoutW, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	stderrR, stderrW, err := os.Pipe()
	if err != nil {
		closeAll(stdoutR, stdoutW)
		return nil, err
	}
	cmd.Stdout, cmd.Stderr = stdoutW, stderrW
	childEnds := []*os.File{stdoutW, stderrW}
	var stdinW *os.File
	if req.Stdin {
		var stdinR *os.File
		if stdinR, stdinW, err = os.Pipe(); err != nil {
			closeAll(stdoutR, stdoutW, stderrR, stderrW)
			return nil, err
		}
		cmd.Stdin = stdinR
		childEnds = append(childEnds, stdinR)
	}
	s.mu.Lock()
	err = cmd.Start()
	if err == nil {
		s.children[cmd.Process.Pid] = true
		s.groups[cmd.Process.Pid] = true
	}
	s.mu.Unlock()
	closeAll(childEnds...)
	if err != nil {
		closeAll(stdoutR, stderrR, stdinW)
		return nil, err
	}
	p := &serverProcess{server: s, id: id, cmd: cmd, stdout: stdoutR, stderr: stderrR}
	if stdinW != nil {
		p.stdin = newChunkQueue()
		go p.feed(stdinW)
	}
	s.mu.Lock()
	s.procs[id] = p
	s.mu.Unlock()
	return p, nil
}

func closeAll(files ...*os.File) {
	for _, f := range files {
		if f != nil {
			_ = f.Close()
		}
	}
}

func (p *serverProcess) forward(r *os.File, data, end frameKind, done chan<- struct{}) {
	defer func() { done <- struct{}{} }()
	buf := make([]byte, chunkSize)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			if p.server.send(frame{kind: data, stream: p.id, payload: append([]byte(nil), buf[:n]...)}) != nil {
				return
			}
		}
		if err != nil {
			_ = p.server.send(frame{kind: end, stream: p.id})
			return
		}
	}
}

func (p *serverProcess) feed(w *os.File) {
	defer w.Close()
	for {
		b, ok := p.stdin.pop()
		if !ok {
			return
		}
		if _, err := w.Write(b); err != nil {
			p.stdin.close(err)
			return
		}
	}
}

func (p *serverProcess) wait(forwarded <-chan struct{}) {
	err := p.cmd.Wait()
	code := p.cmd.ProcessState.ExitCode()
	p.server.mu.Lock()
	delete(p.server.children, p.cmd.Process.Pid)
	p.server.mu.Unlock()
	drained := time.After(drainDelay)
	for pending := 2; pending > 0; pending-- {
		select {
		case <-forwarded:
		case <-drained:
			closeAll(p.stdout, p.stderr)
			<-forwarded
		}
	}
	closeAll(p.stdout, p.stderr)
	if p.stdin != nil {
		p.stdin.close(nil)
	}
	p.server.mu.Lock()
	delete(p.server.procs, p.id)
	p.server.mu.Unlock()
	_ = p.server.send(jsonFrame(frameExit, p.id, exited{Code: code, Error: errorText(err)}))
}

func (p *serverProcess) signal(sig syscall.Signal) {
	if err := syscall.Kill(-p.cmd.Process.Pid, sig); err != nil {
		_ = p.cmd.Process.Signal(sig)
	}
}
