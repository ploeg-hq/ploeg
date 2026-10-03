//go:build unix

package acp

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// The subprocess tests re-exec this test binary as a fake agent (the idiom
// os/exec's own tests use). No `go build`, no network, no real agent — but a
// genuine child process, which is the only way to cover process groups, signal
// escalation and a real stdio pipe.
func TestMain(m *testing.M) {
	if body := os.Getenv("ACP_HELPER"); body != "" {
		helperMain(body)
		return
	}
	os.Exit(m.Run())
}

func helperMain(body string) {
	switch body {
	case "banner-then-json":
		fmt.Println("opencode v1.18.9")
		fmt.Println("  ⠋ starting…")
		fmt.Println(`{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":1}}`)
		fmt.Fprintln(os.Stderr, "some stderr noise")
		fmt.Println("thanks for using opencode!")

	case "fork-child-then-sleep":
		// A grandchild that outlives a naive kill of the direct child. This is
		// the node-agent shape: the CLI forks a worker that holds resources.
		cmd := exec.Command("/bin/sh", "-c", "sleep 300")
		_ = cmd.Start()
		fmt.Println(`{"grandchild":` + strconv.Itoa(cmd.Process.Pid) + `}`)
		os.Stdout.Sync()
		time.Sleep(300 * time.Second)

	case "ignore-sigterm":
		// Refuses a polite stop; only SIGKILL ends it.
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGTERM)
		fmt.Println(`{"ready":true}`)
		os.Stdout.Sync()
		time.Sleep(300 * time.Second)

	case "never-read-stdin":
		fmt.Println(`{"ready":true}`)
		os.Stdout.Sync()
		time.Sleep(300 * time.Second)

	case "echo-stdin":
		sc := bufio.NewScanner(os.Stdin)
		for sc.Scan() {
			fmt.Println(`{"echo":"` + sc.Text() + `"}`)
			os.Stdout.Sync()
		}
	}
	os.Exit(0)
}

func helper(t *testing.T, body string) (argv []string, env []string) {
	t.Helper()
	return []string{os.Args[0]}, append(os.Environ(), "ACP_HELPER="+body)
}

func readLines(t *testing.T, r io.Reader, n int, within time.Duration) []string {
	t.Helper()
	out := make(chan []string, 1)
	go func() {
		var got []string
		sc := bufio.NewScanner(r)
		for len(got) < n && sc.Scan() {
			got = append(got, sc.Text())
		}
		out <- got
	}()
	select {
	case got := <-out:
		return got
	case <-time.After(within):
		t.Fatalf("did not read %d protocol line(s) within %v", n, within)
		return nil
	}
}

// A wrong subcommand is the likeliest misconfiguration, and it must not read as
// a protocol parse error. The banner belongs in the log; only JSON reaches the
// dispatcher.
func TestLauncher_BannerNeverReachesTheProtocolChannel(t *testing.T) {
	var noise safeBuffer
	argv, env := helper(t, "banner-then-json")
	p, err := execLauncher{}.launch(context.Background(), argv, t.TempDir(), env, &noise, nil)
	if err != nil {
		t.Fatal(err)
	}
	lines := drainLines(t, p.Stdout, 5*time.Second)
	if len(lines) != 1 || !strings.Contains(lines[0], `"protocolVersion":1`) {
		t.Fatalf("protocol channel got %q", lines)
	}
	_, _ = p.Wait()
	got := noise.String()
	for _, want := range []string{"opencode v1.18.9", "thanks for using opencode", "some stderr noise"} {
		if !strings.Contains(got, want) {
			t.Errorf("noise channel missing %q; got:\n%s", want, got)
		}
	}
	if strings.Contains(got, "protocolVersion") {
		t.Error("a protocol line leaked into the log channel")
	}
}

// The reason this adapter does not use exec.CommandContext: killing the direct
// child would leave a grandchild holding the DinD socket and the per-run
// LiteLLM key until its TTL expired.
func TestLauncher_KillReapsTheWholeProcessGroup(t *testing.T) {
	var noise safeBuffer
	argv, env := helper(t, "fork-child-then-sleep")
	p, err := execLauncher{}.launch(context.Background(), argv, t.TempDir(), env, &noise, nil)
	if err != nil {
		t.Fatal(err)
	}
	lines := readLines(t, p.Stdout, 1, 5*time.Second)
	var grandchild int
	if _, err := fmt.Sscanf(lines[0], `{"grandchild":%d}`, &grandchild); err != nil {
		t.Fatalf("could not read grandchild pid from %q: %v", lines[0], err)
	}
	if !alive(grandchild) {
		t.Fatalf("grandchild %d was not running to begin with", grandchild)
	}

	p.Kill()
	_, _ = p.Wait()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if !alive(grandchild) {
			return // reaped
		}
		time.Sleep(20 * time.Millisecond)
	}
	_ = syscall.Kill(grandchild, syscall.SIGKILL) // don't leak it out of the test
	t.Fatalf("grandchild %d survived Kill() — the process group was not signalled", grandchild)
}

func TestLauncher_SignalEscalatesToTheGroup(t *testing.T) {
	var noise safeBuffer
	argv, env := helper(t, "ignore-sigterm")
	p, err := execLauncher{}.launch(context.Background(), argv, t.TempDir(), env, &noise, nil)
	if err != nil {
		t.Fatal(err)
	}
	readLines(t, p.Stdout, 1, 5*time.Second)

	if err := p.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("Signal: %v", err)
	}
	// The helper ignores SIGTERM, so a polite stop must NOT be assumed to work.
	select {
	case <-waitCh(p):
		t.Fatal("process exited on a SIGTERM it explicitly ignores")
	case <-time.After(300 * time.Millisecond):
	}
	p.Kill()
	select {
	case <-waitCh(p):
	case <-time.After(5 * time.Second):
		t.Fatal("SIGKILL did not end the process")
	}
}

func TestLauncher_WaitIsIdempotent(t *testing.T) {
	argv, env := helper(t, "banner-then-json")
	var noise safeBuffer
	p, err := execLauncher{}.launch(context.Background(), argv, t.TempDir(), env, &noise, nil)
	if err != nil {
		t.Fatal(err)
	}
	readLines(t, p.Stdout, 1, 5*time.Second)
	c1, _ := p.Wait()
	c2, _ := p.Wait() // a second Wait is "wait: no child processes" without the Once
	if c1 != c2 {
		t.Errorf("Wait returned %d then %d", c1, c2)
	}
}

// A child that stops reading stdin must stall one goroutine, never the
// dispatcher. The backlog surfaces as an error the watchdog can act on.
func TestLauncher_StdinBacklogDoesNotDeadlock(t *testing.T) {
	argv, env := helper(t, "never-read-stdin")
	var noise safeBuffer
	p, err := execLauncher{}.launch(context.Background(), argv, t.TempDir(), env, &noise, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Kill()
	readLines(t, p.Stdout, 1, 5*time.Second)

	done := make(chan error, 1)
	go func() {
		payload := bytes.Repeat([]byte("x"), 256<<10)
		for i := 0; i < stdinQueueDepth*4; i++ {
			if _, err := p.Stdin.Write(payload); err != nil {
				done <- err
				return
			}
		}
		done <- nil
	}()
	select {
	case err := <-done:
		if err != nil && err != errStdinBacklog {
			t.Fatalf("unexpected write error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("writing to a non-reading child blocked the caller — stdio deadlock")
	}
}

func TestLauncher_StdinReachesTheChild(t *testing.T) {
	argv, env := helper(t, "echo-stdin")
	var noise safeBuffer
	p, err := execLauncher{}.launch(context.Background(), argv, t.TempDir(), env, &noise, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Kill()
	if _, err := p.Stdin.Write([]byte("hello\n")); err != nil {
		t.Fatal(err)
	}
	lines := readLines(t, p.Stdout, 1, 5*time.Second)
	if len(lines) != 1 || !strings.Contains(lines[0], `"echo":"hello"`) {
		t.Errorf("child got %q", lines)
	}
}

func TestLauncher_EmptyArgvIsAnError(t *testing.T) {
	if _, err := (execLauncher{}).launch(context.Background(), nil, t.TempDir(), nil, io.Discard, nil); err != errEmptyArgv {
		t.Errorf("err = %v, want %v", err, errEmptyArgv)
	}
}

func TestJSONLFilter_Classification(t *testing.T) {
	in := strings.Join([]string{
		"plain banner",
		`{"a":1}`,
		`   {"b":2}`, // leading space is still protocol
		"",
		"[1,2,3]", // a JSON array is not a JSON-RPC object; treat as noise
		"\t{\"c\":3}",
		"trailing note",
	}, "\n")
	var noise bytes.Buffer
	// Drain to EOF, not to a line count: only EOF proves the filter goroutine
	// has classified every line, so asserting on `noise` before it is a race.
	got := drainLines(t, newJSONLFilter(strings.NewReader(in), &noise, nil), 2*time.Second)
	want := []string{`{"a":1}`, `   {"b":2}`, "\t{\"c\":3}"}
	if len(got) != len(want) {
		t.Fatalf("protocol lines = %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("protocol[%d] = %q, want %q", i, got[i], want[i])
		}
	}
	for _, w := range []string{"plain banner", "[1,2,3]", "trailing note"} {
		if !strings.Contains(noise.String(), w) {
			t.Errorf("noise missing %q", w)
		}
	}
}

// drainLines reads the protocol channel to EOF.
func drainLines(t *testing.T, r io.Reader, within time.Duration) []string {
	t.Helper()
	out := make(chan []string, 1)
	go func() {
		var got []string
		sc := bufio.NewScanner(r)
		for sc.Scan() {
			got = append(got, sc.Text())
		}
		out <- got
	}()
	select {
	case got := <-out:
		return got
	case <-time.After(within):
		t.Fatalf("protocol channel did not reach EOF within %v", within)
		return nil
	}
}

// --- helpers ---

func waitCh(p *process) <-chan struct{} {
	ch := make(chan struct{})
	go func() { _, _ = p.Wait(); close(ch) }()
	return ch
}

// alive reports whether a pid exists. Signal 0 performs error checking without
// actually sending anything.
// alive reports whether pid is a running process — which is deliberately not
// the same question as "does this pid exist".
//
// A grandchild killed by the group signal becomes a ZOMBIE until someone
// reaps it. Its parent died in the same signal, so it is reparented to PID 1,
// and PID 1 reaps it only if PID 1 is an init. Under `docker run golang go
// test` (how this repo runs its gates without a local toolchain) and on the
// CI runner, PID 1 is the go driver, which reaps nothing — so the corpse sits
// in the table indefinitely and kill(pid, 0) keeps succeeding for it.
//
// Counting that as "survived" tested the container's init, not the launcher.
// A zombie holds no descriptors: not the DinD socket, not the per-run LiteLLM
// key — which is the entire property TestLauncher_KillReapsTheWholeProcessGroup
// exists to protect.
//
// (Production is unaffected either way: one run per pod, and the pod exits.)
func alive(pid int) bool {
	if syscall.Kill(pid, 0) != nil {
		return false
	}
	return !isZombie(pid)
}

// isZombie reads Linux's process state. Elsewhere — darwin, where PID 1 is
// launchd and does reap — there is nothing to correct for, and the absence of
// /proc reports false.
func isZombie(pid int) bool {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return false
	}
	// Field 2 (comm) is parenthesised and may itself contain spaces and
	// parens, so the state character is found from the LAST ')', never by
	// splitting on whitespace.
	i := bytes.LastIndexByte(b, ')')
	if i < 0 || i+2 >= len(b) {
		return false
	}
	return b[i+2] == 'Z'
}

type safeBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *safeBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *safeBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}
