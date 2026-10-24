package harness

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
)

// Command is one process a Spawner starts in the harness's container. Env is
// its whole environment; nothing of the spawner's own environment is added.
// Argv[0] resolves against the PATH of the container the process runs in.
type Command struct {
	Argv       []string
	Dir        string
	Env        []string
	Stdin      bool
	StdoutPipe bool
	Stdout     io.Writer
	Stderr     io.Writer
}

// Process is a started Command. Stdin is nil unless the Command asked for it,
// Stdout is nil unless it set StdoutPipe. Wait returns the exit code and the
// error a failed process ended with; an error that carries the code
// implements ExitCode() int. Signal and Kill reach the process's whole group.
type Process interface {
	Stdin() io.WriteCloser
	Stdout() io.Reader
	Wait() (int, error)
	Signal(syscall.Signal) error
	Kill()
}

// Spawner starts the processes of a Run where the harness runs: the worker's
// own container, or the harness's container behind ploeg-worker harness-serve
// (ADR-0138). Cancelling the context given to Start kills the process group.
type Spawner interface {
	Start(ctx context.Context, c Command) (Process, error)
	LookPath(ctx context.Context, file, path string) (string, error)
	CheckDir(ctx context.Context, dir string) error
	Environ(ctx context.Context) ([]string, error)
	KillAll(ctx context.Context) error
}

// ExitError is a process that ran and ended unsuccessfully.
type ExitError struct {
	Code   int
	Status string
}

func (e *ExitError) Error() string { return e.Status }

// ExitCode is the process's exit code, -1 when a signal ended it.
func (e *ExitError) ExitCode() int { return e.Code }

// ExitCode reports the exit code err carries, if any.
func ExitCode(err error) (int, bool) {
	var coded interface{ ExitCode() int }
	if errors.As(err, &coded) {
		return coded.ExitCode(), true
	}
	return 0, false
}

// LookPath finds an executable named file in the directories of path, the
// way a shell does; a file with a slash is checked as written.
func LookPath(file, path string) (string, error) {
	if strings.Contains(file, "/") {
		if err := executable(file); err != nil {
			return "", &exec.Error{Name: file, Err: err}
		}
		return file, nil
	}
	for _, dir := range filepath.SplitList(path) {
		if !filepath.IsAbs(dir) {
			continue
		}
		candidate := filepath.Join(dir, file)
		if executable(candidate) == nil {
			return candidate, nil
		}
	}
	return "", &exec.Error{Name: file, Err: exec.ErrNotFound}
}

func executable(file string) error {
	info, err := os.Stat(file)
	if err != nil {
		return err
	}
	if info.IsDir() || info.Mode().Perm()&0o111 == 0 {
		return notExecutable(file)
	}
	return nil
}

func notExecutable(file string) error { return fmt.Errorf("%s is not an executable file", file) }

// CheckDir fails unless dir is a directory.
func CheckDir(dir string) error {
	info, err := os.Stat(dir)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("%s is not a directory", dir)
	}
	return nil
}

// LocalSpawner starts processes in the worker's own container, as the
// worker's children. It is the spawner of a pod with harnessContainer: shared.
type LocalSpawner struct {
	mu     sync.Mutex
	groups map[int]bool
}

// NewLocalSpawner returns a spawner whose KillAll ends every process group
// it started.
func NewLocalSpawner() *LocalSpawner { return &LocalSpawner{groups: map[int]bool{}} }

func (s *LocalSpawner) LookPath(_ context.Context, file, path string) (string, error) {
	return LookPath(file, path)
}

func (s *LocalSpawner) CheckDir(_ context.Context, dir string) error { return CheckDir(dir) }

func (s *LocalSpawner) Environ(context.Context) ([]string, error) { return os.Environ(), nil }

func (s *LocalSpawner) KillAll(context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for group := range s.groups {
		killGroup(group)
		delete(s.groups, group)
	}
	return nil
}

func (s *LocalSpawner) track(group int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.groups == nil {
		s.groups = map[int]bool{}
	}
	s.groups[group] = true
}

func (s *LocalSpawner) Start(ctx context.Context, c Command) (Process, error) {
	if len(c.Argv) == 0 {
		return nil, errEmptyArgv
	}
	cmd := exec.Command(c.Argv[0], c.Argv[1:]...)
	cmd.Dir = c.Dir
	cmd.Env = append([]string{}, c.Env...)
	if cmd.Env == nil {
		cmd.Env = []string{}
	}
	ownGroup(cmd)
	cmd.WaitDelay = processWaitDelay
	p := &localProcess{cmd: cmd, done: make(chan struct{})}
	if c.Stdin {
		stdin, err := cmd.StdinPipe()
		if err != nil {
			return nil, err
		}
		p.stdin = stdin
	}
	if c.StdoutPipe {
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			return nil, err
		}
		p.stdout = stdout
	} else {
		cmd.Stdout = nonNil(c.Stdout)
	}
	cmd.Stderr = nonNil(c.Stderr)
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	p.group = cmd.Process.Pid
	s.track(p.group)
	go p.killOnCancel(ctx)
	return p, nil
}

type localProcess struct {
	cmd    *exec.Cmd
	group  int
	stdin  io.WriteCloser
	stdout io.Reader
	once   sync.Once
	done   chan struct{}
	code   int
	err    error
}

func (p *localProcess) Stdin() io.WriteCloser { return p.stdin }
func (p *localProcess) Stdout() io.Reader     { return p.stdout }

func (p *localProcess) Wait() (int, error) {
	p.once.Do(func() {
		p.err = p.cmd.Wait()
		p.code = -1
		if p.cmd.ProcessState != nil {
			p.code = p.cmd.ProcessState.ExitCode()
		}
		close(p.done)
	})
	return p.code, p.err
}

func (p *localProcess) Signal(sig syscall.Signal) error {
	if err := signalGroup(p.group, sig); err != nil {
		return p.cmd.Process.Signal(sig)
	}
	return nil
}

func (p *localProcess) Kill() {
	killGroup(p.group)
	_ = p.cmd.Process.Kill()
}

func (p *localProcess) killOnCancel(ctx context.Context) {
	select {
	case <-ctx.Done():
		p.Kill()
	case <-p.done:
	}
}
