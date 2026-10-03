package harnesstest_test

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/harness"
	"github.com/ploeg-hq/ploeg/pkg/harness/adapters/acp"
	"github.com/ploeg-hq/ploeg/pkg/harness/adapters/claudecode"
	"github.com/ploeg-hq/ploeg/pkg/harness/adapters/openhands"
	"github.com/ploeg-hq/ploeg/pkg/harness/harnesstest"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

const (
	liveOptIn       = "PLOEG_HARNESS_CONFORMANCE"
	liveHarnesses   = "PLOEG_CONFORMANCE_HARNESSES"
	liveAPIKey      = "PLOEG_CONFORMANCE_LLM_API_KEY"
	liveBaseURL     = "PLOEG_CONFORMANCE_LLM_BASE_URL"
	liveModel       = "PLOEG_CONFORMANCE_LLM_MODEL"
	liveRunDeadline = 10 * time.Minute
)

type expectation int

const (
	unmeasured expectation = iota
	loads
	doesNotLoad
)

func (e expectation) String() string {
	switch e {
	case loads:
		return "loads"
	case doesNotLoad:
		return "does not load"
	default:
		return "not claimed"
	}
}

type canaryExpectation struct {
	layout       harnesstest.CanaryLayout
	root, nested expectation
}

type liveHarness struct {
	name    string
	bin     string
	adapter func(bin string) (harness.Adapter, error)
	claims  []canaryExpectation
}

var liveHarnessTable = []liveHarness{
	{
		name: "openhands",
		bin:  openhands.DefaultEntrypoint,
		adapter: func(bin string) (harness.Adapter, error) {
			return harness.RunCommand(openhands.New(bin)), nil
		},
		claims: []canaryExpectation{
			{harnesstest.LayoutAgentsOnly, loads, doesNotLoad},
		},
	},
	{
		name: "claude-code",
		bin:  claudecode.DefaultBin,
		adapter: func(bin string) (harness.Adapter, error) {
			return harness.RunCommand(claudecode.New(bin, "")), nil
		},
		claims: []canaryExpectation{
			{harnesstest.LayoutAgentsOnly, unmeasured, doesNotLoad},
			{harnesstest.LayoutClaudeSymlink, loads, doesNotLoad},
		},
	},
	{
		name: "acp",
		bin:  "opencode",
		adapter: func(bin string) (harness.Adapter, error) {
			return acp.New("opencode", acp.ProfileOverrides{Entrypoint: bin}, acp.Options{})
		},
		claims: []canaryExpectation{
			{harnesstest.LayoutAgentsOnly, loads, doesNotLoad},
		},
	},
	{
		name: "acp-qwen-code",
		bin:  "qwen",
		adapter: func(bin string) (harness.Adapter, error) {
			return acp.New("qwen-code", acp.ProfileOverrides{Entrypoint: bin}, acp.Options{})
		},
		claims: []canaryExpectation{
			{harnesstest.LayoutAgentsOnly, loads, doesNotLoad},
		},
	},
	{
		name: "acp-goose",
		bin:  "goose",
		adapter: func(bin string) (harness.Adapter, error) {
			return acp.New("goose", acp.ProfileOverrides{Entrypoint: bin}, acp.Options{})
		},
		claims: []canaryExpectation{
			{harnesstest.LayoutAgentsOnly, loads, unmeasured},
		},
	},
	{
		name: "acp-openhands",
		bin:  "openhands",
		adapter: func(bin string) (harness.Adapter, error) {
			return acp.New("openhands", acp.ProfileOverrides{Entrypoint: bin}, acp.Options{})
		},
		claims: []canaryExpectation{
			{harnesstest.LayoutAgentsOnly, loads, unmeasured},
		},
	},
}

func requireLiveOptIn(t *testing.T) {
	t.Helper()
	if os.Getenv(liveOptIn) != "1" {
		t.Skipf("live harness conformance makes model calls; set %s=1 (mise run harness-conformance) to run it", liveOptIn)
	}
}

func selectedHarness(name string) bool {
	list := strings.TrimSpace(os.Getenv(liveHarnesses))
	if list == "" {
		return true
	}
	for _, n := range strings.Split(list, ",") {
		if strings.TrimSpace(n) == name {
			return true
		}
	}
	return false
}

func lookHarness(t *testing.T, h liveHarness) string {
	t.Helper()
	if !selectedHarness(h.name) {
		t.Skipf("%s is not in %s", h.name, liveHarnesses)
	}
	bin, err := exec.LookPath(h.bin)
	if err != nil {
		t.Skipf("%s: %q is not on PATH", h.name, h.bin)
	}
	return bin
}

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

func liveEnv(t *testing.T, repo, prompt string, out *lockedBuffer) harness.RunEnv {
	t.Helper()
	home := t.TempDir()
	scratch := t.TempDir()
	llm := harness.LLMEnv{
		APIKey:  os.Getenv(liveAPIKey),
		BaseURL: os.Getenv(liveBaseURL),
		Model:   os.Getenv(liveModel),
		TraceID: fmt.Sprintf("ploeg-%012x", time.Now().UnixNano()&0xffffffffffff),
	}
	base := []string{
		"HOME=" + home,
		"TMPDIR=" + scratch,
		"XDG_CONFIG_HOME=" + filepath.Join(home, ".config"),
		"XDG_DATA_HOME=" + filepath.Join(home, ".local", "share"),
		"XDG_CACHE_HOME=" + filepath.Join(home, ".cache"),
		"LLM_API_KEY=" + llm.APIKey,
		"LLM_BASE_URL=" + llm.BaseURL,
		"LLM_MODEL=" + llm.Model,
		"LLM_TRACE_ID=" + llm.TraceID,
	}
	for _, key := range []string{"PATH", "LANG", "LC_ALL", "TZ", "TERM", "DOCKER_HOST", "DOCKER_CERT_PATH", "DOCKER_TLS_VERIFY"} {
		if v, ok := os.LookupEnv(key); ok {
			base = append(base, key+"="+v)
		}
	}
	return harness.RunEnv{
		RepoDir:     repo,
		ScratchDir:  scratch,
		Prompt:      prompt,
		BaseEnv:     base,
		LLM:         llm,
		Stdout:      out,
		Stderr:      out,
		Log:         slog.New(slog.DiscardHandler),
		IdleTimeout: 5 * time.Minute,
	}
}

func liveSpec(traceID string) harness.TaskSpec {
	return harness.TaskSpec{
		WorkItem: work.WorkItem{ID: "1", Provider: "vikunja", ExternalID: "1", Title: "harness conformance probe"},
		Repo:     harness.RepoRef{ForgeURL: "http://forge.invalid", Owner: "conformance", Name: "canary"},
		Branch:   "agent/vik-1",
		TraceID:  traceID,
	}
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}

func TestLiveCanaries(t *testing.T) {
	requireLiveOptIn(t)
	for _, h := range liveHarnessTable {
		t.Run(h.name, func(t *testing.T) {
			bin := lookHarness(t, h)
			for _, claim := range h.claims {
				t.Run(string(claim.layout), func(t *testing.T) {
					ad, err := h.adapter(bin)
					if err != nil {
						t.Fatalf("adapter: %v", err)
					}
					repo := harnesstest.NewCanaryRepo(t, claim.layout)
					var out lockedBuffer
					env := liveEnv(t, repo, harnesstest.CanaryPrompt, &out)
					ctx, cancel := context.WithTimeout(context.Background(), liveRunDeadline)
					defer cancel()
					report, runErr := ad.Run(ctx, liveSpec(env.LLM.TraceID), env)
					if runErr != nil {
						t.Fatalf("run failed, so nothing was measured: %v\noutput tail:\n%s", runErr, tail(out.String(), 4000))
					}
					seen := harnesstest.SeenCanaries(out.String(), report.Summary, report.Findings)
					t.Logf("MEASURED harness=%s layout=%s root=%v nested=%v", h.name, claim.layout, seen.Root, seen.Nested)
					check := func(file string, claimed expectation, got bool) {
						switch {
						case claimed == loads && !got:
							t.Errorf("%s: docs/how-to/prepare-a-repository.md says %s loads %s by itself, but its canary did not appear", claim.layout, h.name, file)
						case claimed == doesNotLoad && got:
							t.Errorf("%s: %s loaded %s by itself; the how-to does not claim that", claim.layout, h.name, file)
						}
					}
					check("the root instruction file", claim.root, seen.Root)
					check("the nested instruction file", claim.nested, seen.Nested)
					if t.Failed() {
						t.Logf("output tail:\n%s", tail(out.String(), 4000))
					}
				})
			}
		})
	}
}

type withoutTargetGuards struct{ *claudecode.Adapter }

func (a withoutTargetGuards) Prepare(spec harness.TaskSpec, env harness.RunEnv) (harness.Invocation, error) {
	inv, err := a.Adapter.Prepare(spec, env)
	if err != nil {
		return inv, err
	}
	var argv []string
	for i := 0; i < len(inv.Argv); i++ {
		switch inv.Argv[i] {
		case "--settings":
			i++
		case "--strict-mcp-config":
		default:
			argv = append(argv, inv.Argv[i])
		}
	}
	inv.Argv = argv
	return inv, nil
}

func plantTargetCode(t *testing.T, repo, markers string) {
	t.Helper()
	hook := func(event string) string {
		return `[{"hooks":[{"type":"command","command":"touch '` + filepath.Join(markers, "hook-"+event) + `'"}]}]`
	}
	settings := `{"enableAllProjectMcpServers":true,"hooks":{` +
		`"SessionStart":` + hook("SessionStart") + `,` +
		`"UserPromptSubmit":` + hook("UserPromptSubmit") + `,` +
		`"Stop":` + hook("Stop") + `}}`
	mcp := `{"mcpServers":{"target":{"command":"sh","args":["-c","touch '` + filepath.Join(markers, "mcp-target") + `'; sleep 5"]}}}`
	for name, body := range map[string]string{
		filepath.Join(".claude", "settings.json"): settings,
		".mcp.json": mcp,
	} {
		path := filepath.Join(repo, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func markersIn(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	slices.Sort(names)
	return names
}

func TestLiveClaudeCodeIgnoresTargetHooksAndMCPServers(t *testing.T) {
	requireLiveOptIn(t)
	var claude liveHarness
	for _, h := range liveHarnessTable {
		if h.name == "claude-code" {
			claude = h
		}
	}
	bin := lookHarness(t, claude)
	repo := harnesstest.NewCanaryRepo(t, harnesstest.LayoutClaudeSymlink)
	const prompt = "Reply with the single word OK. Do not use any tool.\n"

	run := func(t *testing.T, ad harness.Adapter) []string {
		t.Helper()
		markers := t.TempDir()
		plantTargetCode(t, repo, markers)
		var out lockedBuffer
		env := liveEnv(t, repo, prompt, &out)
		ctx, cancel := context.WithTimeout(context.Background(), liveRunDeadline)
		defer cancel()
		if _, err := ad.Run(ctx, liveSpec(env.LLM.TraceID), env); err != nil {
			t.Fatalf("run failed, so nothing was measured: %v\noutput tail:\n%s", err, tail(out.String(), 4000))
		}
		time.Sleep(2 * time.Second)
		return markersIn(t, markers)
	}

	t.Run("guarded", func(t *testing.T) {
		if got := run(t, harness.RunCommand(claudecode.New(bin, ""))); len(got) > 0 {
			t.Errorf("the target repository's hooks or MCP servers ran under the claude-code adapter: %v", got)
		}
	})
	t.Run("control without the guards", func(t *testing.T) {
		got := run(t, harness.RunCommand(withoutTargetGuards{claudecode.New(bin, "")}))
		t.Logf("MEASURED unguarded claude ran: %v", got)
		if !slices.ContainsFunc(got, func(n string) bool { return strings.HasPrefix(n, "hook-") }) {
			t.Errorf("no planted hook ran even without --settings %s, so the guarded result proves nothing", claudecode.TargetHooksDisabled)
		}
	})
}
