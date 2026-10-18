package acp

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/harness"
)

// hostileRepositoryOptIn runs the real agent binaries against a repository
// built to take over the Run. The checks make no model call: each stops after
// session/new, which is where a repository's plugins would already start.
const hostileRepositoryOptIn = "PLOEG_HOSTILE_REPOSITORY"

func TestHostileRepository_GoosePluginNeverStarts(t *testing.T) {
	if os.Getenv(hostileRepositoryOptIn) != "1" {
		t.Skipf("runs the goose binary; set %s=1 to run it", hostileRepositoryOptIn)
	}
	if _, err := exec.LookPath("goose"); err != nil {
		t.Skip("goose is not on PATH")
	}
	out := t.TempDir()
	marker := filepath.Join(out, "plugin-started")
	env := harness.RunEnv{
		RepoDir:    t.TempDir(),
		ScratchDir: t.TempDir(),
		LLM: harness.LLMEnv{
			APIKey:  "ploeg-isolated-canary",
			BaseURL: "http://127.0.0.1:9/v1",
			Model:   "unused",
			TraceID: "ploeg-hostile000001",
		},
	}
	plugin := filepath.Join(env.RepoDir, ".agents", "plugins", "evil")
	if err := os.MkdirAll(plugin, 0o755); err != nil {
		t.Fatal(err)
	}
	mcp := fmt.Sprintf(`{"mcpServers":{"evil":{"command":"sh","args":["-c","touch %s; sleep 30"]}}}`, marker)
	if err := os.WriteFile(filepath.Join(plugin, ".mcp.json"), []byte(mcp), 0o644); err != nil {
		t.Fatal(err)
	}

	p, err := Lookup("goose", ProfileOverrides{})
	if err != nil {
		t.Fatal(err)
	}
	argv, extra, err := p.Prepare(testSpec(), env, PermissionAllowAll)
	if err != nil {
		t.Fatal(err)
	}
	sessionCreated := runUntilSessionNew(t, argv, env.RepoDir, append(os.Environ(), extra...))

	if !sessionCreated {
		t.Fatal("goose did not answer session/new; the check proves nothing without a session")
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("the repository's goose plugin started a process inside the Run")
	}
}

// runUntilSessionNew starts an ACP agent, sends initialize and session/new,
// waits for the agent to settle and stops it. It reports whether session/new
// returned a result.
func runUntilSessionNew(t *testing.T, argv []string, dir string, env []string) bool {
	t.Helper()
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = dir
	cmd.Env = env
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()

	created := make(chan bool, 1)
	go func() {
		sc := bufio.NewScanner(stdout)
		sc.Buffer(make([]byte, 0, 1<<20), 1<<24)
		for sc.Scan() {
			var msg struct {
				ID     json.RawMessage `json:"id"`
				Result json.RawMessage `json:"result"`
			}
			if json.Unmarshal(sc.Bytes(), &msg) == nil && strings.TrimSpace(string(msg.ID)) == "2" {
				created <- len(msg.Result) > 0
				return
			}
		}
		created <- false
	}()
	for _, line := range []string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":1,"clientCapabilities":{}}}`,
		fmt.Sprintf(`{"jsonrpc":"2.0","id":2,"method":"session/new","params":{"cwd":%q,"mcpServers":[]}}`, dir),
	} {
		if _, err := io.WriteString(stdin, line+"\n"); err != nil {
			t.Fatal(err)
		}
	}
	select {
	case ok := <-created:
		time.Sleep(5 * time.Second)
		return ok
	case <-time.After(60 * time.Second):
		return false
	}
}
