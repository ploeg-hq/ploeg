//go:build linux

package launch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/harness"
)

type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func shortSocket(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "pl")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return filepath.Join(dir, "launcher.sock")
}

func connectedPair(t *testing.T) (*Client, string, func() error) {
	t.Helper()
	socket := shortSocket(t)
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { result <- Serve(ctx, socket) }()
	served := sync.OnceValue(func() error { return <-result })
	acceptCtx, stop := context.WithTimeout(context.Background(), 10*time.Second)
	defer stop()
	client, err := Accept(acceptCtx, socket)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		client.Close()
		cancel()
		_ = served()
	})
	return client, socket, served
}

func running(pid int) bool {
	if syscall.Kill(pid, 0) != nil {
		return false
	}
	stat, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return false
	}
	state, _, ok := stateAndParent(stat)
	return ok && state != 'Z'
}

func eventually(t *testing.T, within time.Duration, cond func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return cond()
}

func readPID(t *testing.T, file string) int {
	t.Helper()
	var pid int
	if !eventually(t, 5*time.Second, func() bool {
		raw, err := os.ReadFile(file)
		if err != nil {
			return false
		}
		pid, err = strconv.Atoi(strings.TrimSpace(string(raw)))
		return err == nil
	}) {
		t.Fatalf("the process never wrote its pid to %s", file)
	}
	return pid
}

func TestLauncherRelaysStdioAndKillsTheGroup(t *testing.T) {
	client, _, _ := connectedPair(t)
	dir := t.TempDir()

	var stdout, stderr lockedBuffer
	proc, err := client.Start(context.Background(), harness.Command{
		Argv:  []string{"/bin/sh", "-c", `printf 'out:'; printf 'err' >&2; cat; printf ':%s' "$RELAYED"; exit 3`},
		Dir:   dir,
		Env:   []string{"RELAYED=only-this"},
		Stdin: true, Stdout: &stdout, Stderr: &stderr,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := proc.Stdin().Write([]byte("from-the-worker")); err != nil {
		t.Fatal(err)
	}
	if err := proc.Stdin().Close(); err != nil {
		t.Fatal(err)
	}
	code, err := proc.Wait()
	if code != 3 {
		t.Fatalf("exit code = %d (%v), want 3", code, err)
	}
	if got, ok := harness.ExitCode(err); !ok || got != 3 {
		t.Fatalf("Wait error %v does not carry exit code 3", err)
	}
	if stdout.String() != "out:from-the-worker:only-this" {
		t.Errorf("stdout = %q", stdout.String())
	}
	if stderr.String() != "err" {
		t.Errorf("stderr = %q", stderr.String())
	}

	pidFile := filepath.Join(dir, "background.pid")
	group, err := client.Start(context.Background(), harness.Command{
		Argv: []string{"/bin/sh", "-c", `sleep 60 & echo $! > "$PIDFILE"; sleep 60`},
		Dir:  dir,
		Env:  []string{"PIDFILE=" + pidFile, "PATH=/usr/bin:/bin"},
	})
	if err != nil {
		t.Fatal(err)
	}
	background := readPID(t, pidFile)
	group.Kill()
	waited := make(chan int, 1)
	go func() { code, _ := group.Wait(); waited <- code }()
	select {
	case code := <-waited:
		if code != -1 {
			t.Errorf("a killed process exited %d, want -1", code)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("Kill did not end the process")
	}
	if eventually(t, 5*time.Second, func() bool { return !running(background) }) == false {
		t.Fatalf("background child %d survived the group kill", background)
	}
}

func TestLauncherStdoutPipeEndsWhenTheProcessDoes(t *testing.T) {
	client, _, _ := connectedPair(t)
	proc, err := client.Start(context.Background(), harness.Command{
		Argv: []string{"/bin/sh", "-c", `echo '{"jsonrpc":"2.0"}'; read line; echo "$line"`}, Stdin: true, StdoutPipe: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := proc.Stdin().Write([]byte("answer\n")); err != nil {
		t.Fatal(err)
	}
	out, err := io.ReadAll(proc.Stdout())
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != "{\"jsonrpc\":\"2.0\"}\nanswer\n" {
		t.Errorf("stdout = %q", out)
	}
	if code, err := proc.Wait(); code != 0 || err != nil {
		t.Errorf("Wait = %d, %v", code, err)
	}
}

func TestLauncherCancelledContextKillsTheGroup(t *testing.T) {
	client, _, _ := connectedPair(t)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	proc, err := client.Start(ctx, harness.Command{Argv: []string{"/bin/sh", "-c", "sleep 60 & sleep 60"}, Env: []string{"PATH=/usr/bin:/bin"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := proc.Wait(); err == nil {
		t.Fatal("a cancelled process reported success")
	}
	if elapsed := time.Since(start); elapsed > 15*time.Second {
		t.Fatalf("cancellation took %s", elapsed)
	}
}

func TestLauncherKillAllEndsWhatAHarnessLeftBehind(t *testing.T) {
	client, _, _ := connectedPair(t)
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "leftover.pid")
	proc, err := client.Start(context.Background(), harness.Command{
		Argv: []string{"/bin/sh", "-c", `sleep 60 >/dev/null 2>&1 & echo $! > "$PIDFILE"`},
		Env:  []string{"PIDFILE=" + pidFile, "PATH=/usr/bin:/bin"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if code, err := proc.Wait(); code != 0 {
		t.Fatalf("harness exited %d: %v", code, err)
	}
	leftover := readPID(t, pidFile)
	if !running(leftover) {
		t.Fatalf("the leftover process %d already ended; nothing was drilled", leftover)
	}
	if err := client.KillAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !eventually(t, 5*time.Second, func() bool { return !running(leftover) }) {
		t.Fatalf("KillAll left process %d running", leftover)
	}
}

func TestLauncherAnswersFromItsOwnContainer(t *testing.T) {
	client, _, _ := connectedPair(t)
	ctx := context.Background()
	if self, err := os.Executable(); err != nil || client.Executable() != self {
		t.Errorf("Executable() = %q, want the launcher's own path %q (%v)", client.Executable(), self, err)
	}
	if path, err := client.LookPath(ctx, "sh", "/usr/bin:/bin"); err != nil || !strings.HasSuffix(path, "/sh") {
		t.Errorf("LookPath(sh) = %q, %v", path, err)
	}
	if _, err := client.LookPath(ctx, "ploeg-no-such-binary", "/usr/bin:/bin"); err == nil {
		t.Error("LookPath found a binary that does not exist")
	}
	if err := client.CheckDir(ctx, t.TempDir()); err != nil {
		t.Errorf("CheckDir(existing) = %v", err)
	}
	if err := client.CheckDir(ctx, filepath.Join(t.TempDir(), "absent")); err == nil {
		t.Error("CheckDir accepted a missing directory")
	}
	env, err := client.Environ(ctx)
	if err != nil || len(env) == 0 {
		t.Errorf("Environ = %v, %v", env, err)
	}
	if _, err := client.Start(ctx, harness.Command{Argv: []string{"/nonexistent/ploeg-harness"}}); err == nil {
		t.Error("a missing program started")
	}
}

func TestLauncherSocketTakesOneConnectionOnly(t *testing.T) {
	_, socket, _ := connectedPair(t)
	if _, err := os.Stat(socket); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the socket is still on disk after the launcher connected: %v", err)
	}
	if conn, err := net.Dial("unix", socket); err == nil {
		conn.Close()
		t.Fatal("a second process connected to the worker's launcher socket")
	}
}

func TestLauncherKillsItsProcessesWhenTheWorkerGoes(t *testing.T) {
	client, _, served := connectedPair(t)
	pidFile := filepath.Join(t.TempDir(), "orphan.pid")
	if _, err := client.Start(context.Background(), harness.Command{
		Argv: []string{"/bin/sh", "-c", `echo $$ > "$PIDFILE"; sleep 60`},
		Env:  []string{"PIDFILE=" + pidFile, "PATH=/usr/bin:/bin"},
	}); err != nil {
		t.Fatal(err)
	}
	pid := readPID(t, pidFile)
	client.Close()
	finished := make(chan error, 1)
	go func() { finished <- served() }()
	select {
	case err := <-finished:
		if err != nil {
			t.Fatalf("Serve = %v after the worker closed", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the launcher kept serving after the worker closed the connection")
	}
	if !eventually(t, 5*time.Second, func() bool { return !running(pid) }) {
		t.Fatalf("process %d outlived the worker's connection", pid)
	}
	if _, err := client.Start(context.Background(), harness.Command{Argv: []string{"true"}}); !errors.Is(err, ErrClosed) {
		t.Fatalf("Start after close = %v, want ErrClosed", err)
	}
}

func TestLauncherWithAnotherProtocolIsRefused(t *testing.T) {
	worker, launcher := net.Pipe()
	defer worker.Close()
	go func() {
		payload, _ := json.Marshal(hello{Protocol: ProtocolVersion + 1, PID: 1})
		_ = writeFrame(launcher, frame{kind: frameHello, payload: payload})
		launcher.Close()
	}()
	if _, err := NewClient(worker); err == nil || !strings.Contains(err.Error(), "protocol") {
		t.Fatalf("NewClient = %v, want a protocol refusal", err)
	}
}

func TestAcceptGivesUpWhenNoLauncherConnects(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := Accept(ctx, shortSocket(t)); err == nil || !strings.Contains(err.Error(), "no harness launcher connected") {
		t.Fatalf("Accept = %v", err)
	}
}

func TestFramesRefuseAnOversizedPayload(t *testing.T) {
	if err := writeFrame(io.Discard, frame{kind: frameStdout, payload: make([]byte, maxFrameSize+1)}); !errors.Is(err, errFrameTooLarge) {
		t.Fatalf("writeFrame = %v", err)
	}
	header := []byte{byte(frameStdout), 0, 0, 0, 1, 0xff, 0xff, 0xff, 0xff}
	if _, err := readFrame(bytes.NewReader(header)); !errors.Is(err, errFrameTooLarge) {
		t.Fatalf("readFrame = %v", err)
	}
}
