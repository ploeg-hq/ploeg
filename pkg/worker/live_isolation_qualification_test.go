package worker

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/harness"
	"github.com/ploeg-hq/ploeg/pkg/harness/adapters/claudecode"
	"github.com/ploeg-hq/ploeg/pkg/llmbroker"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

const (
	qualificationOptIn      = "PLOEG_HARNESS_CONFORMANCE"
	qualificationLLMKey     = "PLOEG_CONFORMANCE_LLM_API_KEY"
	qualificationLLMURL     = "PLOEG_CONFORMANCE_LLM_BASE_URL"
	qualificationLLMModel   = "PLOEG_CONFORMANCE_LLM_MODEL"
	qualificationForgeURL   = "PLOEG_CONFORMANCE_FORGE_URL"
	qualificationForgeToken = "PLOEG_CONFORMANCE_FORGE_TOKEN"
	qualificationForgeRepo  = "PLOEG_CONFORMANCE_FORGE_REPO"
	qualificationForgeBase  = "PLOEG_CONFORMANCE_FORGE_BASE_BRANCH"
	qualificationForgeKind  = "PLOEG_CONFORMANCE_FORGE"
	qualificationDeadline   = 15 * time.Minute
)

type qualificationSetup struct {
	bin                         string
	modelKey, modelURL, model   string
	forgeURL, forgeToken, forge string
	owner, repo, base           string
}

func requireQualificationSetup(t *testing.T) qualificationSetup {
	t.Helper()
	if os.Getenv(qualificationOptIn) != "1" {
		t.Skipf("the credential isolation qualification makes model calls and opens a pull request; set %s=1 to run it", qualificationOptIn)
	}
	var missing []string
	need := func(name string) string {
		v := strings.TrimSpace(os.Getenv(name))
		if v == "" {
			missing = append(missing, name)
		}
		return v
	}
	s := qualificationSetup{
		modelKey:   need(qualificationLLMKey),
		modelURL:   need(qualificationLLMURL),
		model:      need(qualificationLLMModel),
		forgeURL:   need(qualificationForgeURL),
		forgeToken: need(qualificationForgeToken),
		base:       os.Getenv(qualificationForgeBase),
		forge:      os.Getenv(qualificationForgeKind),
	}
	owner, repo, _ := strings.Cut(need(qualificationForgeRepo), "/")
	s.owner, s.repo = owner, repo
	if len(missing) > 0 {
		t.Skipf("the qualification needs a model gateway test key and a test forge repository; unset: %s", strings.Join(missing, ", "))
	}
	if s.owner == "" || s.repo == "" {
		t.Fatalf("%s must be owner/name", qualificationForgeRepo)
	}
	if s.base == "" {
		s.base = "main"
	}
	if s.forge == "" {
		s.forge = harness.ForgeForgejo
	}
	bin, err := exec.LookPath(claudecode.DefaultBin)
	if err != nil {
		t.Skipf("%q is not on PATH", claudecode.DefaultBin)
	}
	s.bin = bin
	return s
}

type launchRecorder struct {
	harness.Spawner
	mu       sync.Mutex
	commands []harness.Command
}

func (s *launchRecorder) Start(ctx context.Context, c harness.Command) (harness.Process, error) {
	s.mu.Lock()
	s.commands = append(s.commands, c)
	s.mu.Unlock()
	return s.Spawner.Start(ctx, c)
}

type qualifiedClaude struct {
	*claudecode.Adapter
	spawner *launchRecorder
	output  bytes.Buffer
	outMu   sync.Mutex
}

func (q *qualifiedClaude) Write(p []byte) (int, error) {
	q.outMu.Lock()
	defer q.outMu.Unlock()
	return q.output.Write(p)
}

func (q *qualifiedClaude) harnessOutput() string {
	q.outMu.Lock()
	defer q.outMu.Unlock()
	return q.output.String()
}

type qualifiedRun struct{ q *qualifiedClaude }

func (r qualifiedRun) Name() string     { return r.q.Name() }
func (r qualifiedRun) ExpectsLLM() bool { return r.q.ExpectsLLM() }

func (r qualifiedRun) Run(ctx context.Context, spec harness.TaskSpec, env harness.RunEnv) (harness.OutcomeReport, error) {
	inner := env.Spawner
	if inner == nil {
		inner = harness.NewLocalSpawner()
	}
	r.q.spawner.Spawner = inner
	env.Spawner = r.q.spawner
	env.Stdout = io.MultiWriter(nonNilWriter(env.Stdout), r.q)
	env.Stderr = io.MultiWriter(nonNilWriter(env.Stderr), r.q)
	return harness.RunCommand(r.q.Adapter).Run(ctx, spec, env)
}

func nonNilWriter(w io.Writer) io.Writer {
	if w == nil {
		return io.Discard
	}
	return w
}

type gatewayFront struct {
	key                 string
	withKey, withoutKey atomic.Int64
}

func (g *gatewayFront) server(t *testing.T, upstream string) (root, baseURL string) {
	t.Helper()
	target, err := url.Parse(upstream)
	if err != nil || target.Host == "" {
		t.Fatalf("%s is not an absolute URL", qualificationLLMURL)
	}
	proxy := &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			r.Out.URL.Scheme = target.Scheme
			r.Out.URL.Host = target.Host
			r.Out.Host = target.Host
		},
		FlushInterval: -1,
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if presentsModelKey(r.Header, g.key) {
			g.withKey.Add(1)
		} else {
			g.withoutKey.Add(1)
		}
		proxy.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv.URL, srv.URL + strings.TrimRight(target.Path, "/")
}

func presentsModelKey(h http.Header, key string) bool {
	bearer, ok := authorizationCredential(h, "Bearer")
	return (ok && bearer == key) || h.Get("X-Api-Key") == key
}

type syncLog struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *syncLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}

func (l *syncLog) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.String()
}

func secretEncodings(secret string) []string {
	return []string{
		secret,
		base64.StdEncoding.EncodeToString([]byte(secret)),
		base64.StdEncoding.EncodeToString([]byte(forgeGitUser + ":" + secret)),
		url.QueryEscape(secret),
	}
}

func carriesSecret(text string, secrets map[string]string) []string {
	var found []string
	for name, secret := range secrets {
		for _, form := range secretEncodings(secret) {
			if strings.Contains(text, form) {
				found = append(found, name)
				break
			}
		}
	}
	return found
}

func filesCarryingSecrets(t *testing.T, root string, secrets map[string]string) []string {
	t.Helper()
	var hits []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !d.Type().IsRegular() {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		for _, name := range carriesSecret(string(raw), secrets) {
			hits = append(hits, fmt.Sprintf("%s holds the real %s", path, name))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	return hits
}

// TestLiveClaudeCodeCredentialIsolation is the credential isolation
// qualification of the claude-code harness (ADR-0034, the 2026-10-04
// qualification record). It runs one writer Run through the worker's own
// execute path with both proxies on, the real claude binary, a model gateway
// test key and a test forge repository, and fails unless the harness held
// only placeholders, every model call reached the gateway with the real key
// the worker's proxy swapped in, and the Run still opened a pull request.
func TestLiveClaudeCodeCredentialIsolation(t *testing.T) {
	setup := requireQualificationSetup(t)

	front := &gatewayFront{key: setup.modelKey}
	frontRoot, gatewayURL := front.server(t, setup.modelURL)

	transcripts := &syncLog{}
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/transcript") {
			body, _ := io.ReadAll(r.Body)
			_, _ = transcripts.Write(append(body, '\n'))
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(api.Close)

	stamp := time.Now().UTC().Format("20060102150405")
	branch := "agent/vik-qualify-" + stamp
	workDir := t.TempDir()
	logs := &syncLog{}
	claude := &qualifiedClaude{Adapter: claudecode.New(setup.bin, ""), spawner: &launchRecorder{}}
	cfg := Config{
		APIURL: api.URL, ForgeURL: setup.forgeURL, DefaultForge: setup.forge, BuilderToken: setup.forgeToken,
		RepoOwner: setup.owner, RepoName: setup.repo, BaseBranch: setup.base, WorkDir: workDir,
		LLMBaseURL: gatewayURL, LLMModel: setup.model,
		LLMKeyIsolation: KeyIsolationProxy, ForgeTokenIsolation: ForgeTokenIsolationProxy,
		Transcript: true, HarnessTimeout: qualificationDeadline, HarnessIdleTimeout: 5 * time.Minute,
	}
	w := New(cfg, qualifiedRun{claude}, llmbroker.Static{Key: setup.modelKey}, slog.New(slog.NewJSONHandler(logs, nil)))
	claimed := &ClaimResponse{RunToken: "qualification", Role: "builder", Writes: true, WorkItem: work.WorkItem{
		ID: stamp, Provider: "vikunja", ExternalID: "qualify-" + stamp,
		Title:       "Add the file QUALIFICATION.md",
		Description: "Create QUALIFICATION.md at the repository root containing the single line `claude-code credential isolation qualification " + stamp + "`. Change nothing else.",
	}}

	ctx, cancel := context.WithTimeout(context.Background(), qualificationDeadline+5*time.Minute)
	defer cancel()
	report := w.execute(ctx, claimed, branch, "ploeg-qualify-"+stamp, "", "")
	t.Logf("MEASURED outcome=%s pr=%v model_calls_with_real_key=%d model_calls_without_it=%d", report.Outcome, report.Delivery, front.withKey.Load(), front.withoutKey.Load())

	secrets := map[string]string{"model key": setup.modelKey, "forge token": setup.forgeToken}

	claude.spawner.mu.Lock()
	commands := append([]harness.Command(nil), claude.spawner.commands...)
	claude.spawner.mu.Unlock()
	if len(commands) == 0 {
		t.Fatalf("the harness never started, so nothing was qualified; worker log:\n%s", tail([]byte(logs.String()), 4000))
	}
	for _, c := range commands {
		if found := carriesSecret(strings.Join(c.Argv, "\x00"), secrets); len(found) > 0 {
			t.Errorf("claude's argv carried the real %v", found)
		}
		if found := carriesSecret(strings.Join(c.Env, "\x00"), secrets); len(found) > 0 {
			t.Errorf("claude's environment carried the real %v", found)
		}
		for _, key := range []string{"ANTHROPIC_API_KEY", "LLM_API_KEY", "AGENT_BUILDER_TOKEN"} {
			if v := envValue(c.Env, key); !strings.HasPrefix(v, "ploeg-isolated-") {
				t.Errorf("%s in claude's environment is not the Run's placeholder", key)
			}
		}
		for _, key := range []string{"ANTHROPIC_BASE_URL", "LLM_BASE_URL"} {
			if v := envValue(c.Env, key); !strings.HasPrefix(v, "http://127.0.0.1:") || strings.HasPrefix(v, frontRoot) {
				t.Errorf("%s=%q in claude's environment is not the worker's model proxy", key, v)
			}
		}
		if v := envValue(c.Env, "GIT_CONFIG_VALUE_0"); v == "" || !strings.Contains(envValue(c.Env, "GIT_CONFIG_KEY_0"), "127.0.0.1") {
			t.Errorf("claude's git is not rewritten to the worker's forge proxy")
		}
	}
	for _, hit := range filesCarryingSecrets(t, workDir, secrets) {
		t.Error(hit)
	}
	if found := carriesSecret(claude.harnessOutput(), secrets); len(found) > 0 {
		t.Errorf("claude's own output carried the real %v", found)
	}
	if found := carriesSecret(transcripts.String(), secrets); len(found) > 0 {
		t.Errorf("the Run's transcript carried the real %v", found)
	}
	if strings.TrimSpace(transcripts.String()) == "" {
		t.Errorf("no transcript reached ploegd, so the transcript was not checked")
	}

	if front.withKey.Load() == 0 {
		t.Errorf("no model call reached the gateway with the real key, so none went through the worker's model proxy")
	}
	if n := front.withoutKey.Load(); n > 0 {
		t.Errorf("%d model calls reached the gateway without the real key the worker's proxy swaps in", n)
	}
	workerLog := logs.String()
	for _, line := range []string{"per-run key isolated behind the worker's loopback proxy", "forge token isolated behind the worker's loopback proxy"} {
		if !strings.Contains(workerLog, line) {
			t.Errorf("the worker never logged %q, so a proxy was not on", line)
		}
	}
	if strings.Contains(workerLog, proxyRefusalMessage) {
		t.Logf("a credential proxy refused a request during the Run:\n%s", refusalLines(workerLog))
		if strings.Contains(refusalLines(workerLog), string(refusalMissingPlaceholder)) || strings.Contains(refusalLines(workerLog), string(refusalWrongPlaceholder)) {
			t.Errorf("claude sent a request without the Run's placeholder")
		}
	}

	if report.Outcome != work.OutcomePROpened && report.Outcome != work.OutcomePRUpdated {
		t.Errorf("the Run ended %s (%s), not with a pull request", report.Outcome, report.StuckReason)
	}
	if report.Delivery == nil || report.Delivery.URL == "" || report.Delivery.Head == "" {
		t.Errorf("the worker observed no delivered pull request on %s: %+v", branch, report.Delivery)
	} else {
		t.Logf("MEASURED delivered %s head=%s branch=%s", report.Delivery.URL, report.Delivery.Head, branch)
	}
	if t.Failed() {
		t.Logf("claude output tail:\n%s", tail([]byte(claude.harnessOutput()), 4000))
	}
}

func refusalLines(log string) string {
	var lines []string
	for _, line := range strings.Split(log, "\n") {
		if strings.Contains(line, proxyRefusalMessage) {
			lines = append(lines, line)
		}
	}
	return strings.Join(lines, "\n")
}
