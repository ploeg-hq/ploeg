//go:build linux

package worker

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/harness"
	"github.com/ploeg-hq/ploeg/pkg/harness/claudeplugins"
	"github.com/ploeg-hq/ploeg/pkg/llmbroker"
	"github.com/ploeg-hq/ploeg/pkg/runidentity"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

const (
	observerIssuer   = "https://ploeg.example"
	observerAudience = "https://observer.example/sessions"
)

// hookRunningAdapter stands in for Claude Code: it runs every installed
// SessionStart hook command the way Claude Code would, with the Run's whole
// environment plus a model key, and remembers what the hooks wrote.
type hookRunningAdapter struct {
	contextAdapter
	t          *testing.T
	pluginDirs []string
	hookErrors []string
}

func (a *hookRunningAdapter) Name() string { return "claude-code" }

func (a *hookRunningAdapter) Run(ctx context.Context, spec harness.TaskSpec, env harness.RunEnv) (harness.OutcomeReport, error) {
	a.pluginDirs = env.PluginDirs
	for _, dir := range env.PluginDirs {
		body, err := os.ReadFile(filepath.Join(dir, "hooks", "hooks.json"))
		if err != nil {
			a.hookErrors = append(a.hookErrors, err.Error())
			continue
		}
		var hooks struct {
			Hooks map[string][]struct {
				Hooks []struct {
					Command string `json:"command"`
				} `json:"hooks"`
			} `json:"hooks"`
		}
		if err := json.Unmarshal(body, &hooks); err != nil {
			a.hookErrors = append(a.hookErrors, err.Error())
			continue
		}
		for _, group := range hooks.Hooks["SessionStart"] {
			for _, h := range group.Hooks {
				cmd := exec.CommandContext(ctx, "/bin/sh", "-c", h.Command)
				cmd.Env = append(append([]string{}, env.BaseEnv...), "ANTHROPIC_API_KEY=sk-run-model-key",
					"CLAUDE_PLUGIN_ROOT="+dir, "CLAUDE_CODE_SESSION_ID=session-1")
				cmd.Stdin = strings.NewReader(`{"hook_event_name":"SessionStart","session_id":"session-1"}`)
				if out, err := cmd.CombinedOutput(); err != nil {
					a.hookErrors = append(a.hookErrors, err.Error()+": "+string(out))
				}
			}
		}
	}
	return a.contextAdapter.Run(ctx, spec, env)
}

func reportingMarketplace(_ context.Context, _ claudeplugins.Marketplace, dir string) error {
	files := map[string]string{
		".claude-plugin/marketplace.json": `{"plugins":[{"name":"session-report","source":"./session-report"}]}`,
		"session-report/hooks/hooks.json": `{"hooks":{"SessionStart":[{"hooks":[{"type":"command","command":"printf %s \"$AGENT_RUN_IDENTITY_TOKEN\" > \"$REPORT_OUT\"; env > \"$REPORT_OUT.env\""}]}]}}`,
	}
	for rel, body := range files {
		path := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			return err
		}
	}
	return nil
}

type fakeIdentityIssuer struct {
	signer   *runidentity.Signer
	requests atomic.Int32
	asked    atomic.Value
}

func newFakeIdentityIssuer(t *testing.T) (*fakeIdentityIssuer, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, _ := x509.MarshalPKCS8PrivateKey(key)
	signer, err := runidentity.NewSigner(observerIssuer, string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})), "")
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeIdentityIssuer{signer: signer}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/identity") {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		f.requests.Add(1)
		var req struct {
			Audiences []string `json:"audiences"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		f.asked.Store(req.Audiences)
		var ids []RunIdentity
		for _, aud := range req.Audiences {
			token, exp, err := signer.Sign(runidentity.Run{ID: "42", WorkItemID: "7", ShiftID: "3", Round: 1, Role: "reviewer",
				Team: "bronze", Harness: "claude-code", Branch: "agent/vik-7", Trace: "ploeg-0123456789ab"}, aud, time.Now().Add(time.Minute))
			if err != nil {
				http.Error(w, err.Error(), http.StatusConflict)
				return
			}
			ids = append(ids, RunIdentity{Audience: aud, Token: token, ExpiresAt: exp})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"identities": ids})
	}))
	t.Cleanup(srv.Close)
	return f, srv.URL
}

func runReportingPlugin(t *testing.T, plugin claudeplugins.Plugin, apiURL string) (*hookRunningAdapter, string) {
	t.Helper()
	guard, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "report")
	plugin.Env = append(plugin.Env, claudeplugins.EnvVar{Name: "REPORT_OUT", Value: out})
	forgeURL := gitForge(t, map[string]string{"main.go": "package main\n"})
	adapter := &hookRunningAdapter{t: t}
	w := New(Config{APIURL: apiURL, ForgeURL: forgeURL, DefaultForge: harness.ForgeForgejo, BuilderToken: "tok",
		ForgeTokenAccess: ForgeTokenReadOnly, RepoOwner: "webgrip", RepoName: "example", BaseBranch: "development",
		WorkDir: t.TempDir(), RepoKnowledgeDir: "-", ClaudePlugins: []claudeplugins.Plugin{plugin}, PluginHookGuard: guard},
		adapter, llmbroker.Static{}, discardLog())
	w.pluginFetch = reportingMarketplace
	claimed := &ClaimResponse{RunToken: "rt", Role: "reviewer", PreAuthor: true, Deadline: time.Now().Add(time.Minute),
		WorkItem: work.WorkItem{ID: "7", Title: "read"}}
	w.execute(context.Background(), claimed, "agent/vik-7", "trace", "", "")
	if !adapter.ran || len(adapter.pluginDirs) != 1 {
		t.Fatalf("the harness ran %v with plugins %v", adapter.ran, adapter.pluginDirs)
	}
	if len(adapter.hookErrors) > 0 {
		t.Fatalf("hooks failed: %v", adapter.hookErrors)
	}
	return adapter, out
}

var reportingPlugin = claudeplugins.Plugin{Name: "session-report",
	Marketplace: claudeplugins.Marketplace{URL: "https://forge.example/p.git", Commit: "0123456789abcdef0123456789abcdef01234567"}}

func TestAPluginHookThatOptedInReceivesARunIdentityThatVerifies(t *testing.T) {
	issuer, apiURL := newFakeIdentityIssuer(t)
	plugin := reportingPlugin
	plugin.RunIdentity = &claudeplugins.RunIdentity{Audience: observerAudience}
	adapter, out := runReportingPlugin(t, plugin, apiURL)

	token, err := os.ReadFile(out)
	if err != nil || len(token) == 0 {
		t.Fatalf("the hook received no run identity: %v", err)
	}
	keySet, _ := json.Marshal(issuer.signer.KeySet())
	keys, err := runidentity.ParseKeySet(keySet)
	if err != nil {
		t.Fatal(err)
	}
	claims, err := runidentity.Verify(string(token), keys, runidentity.Expect{Issuer: observerIssuer, Audience: observerAudience})
	if err != nil {
		t.Fatalf("the hook's run identity does not verify: %v", err)
	}
	if claims.Run.ID != "42" || claims.Run.Trace != "ploeg-0123456789ab" || claims.Run.Branch != "agent/vik-7" {
		t.Errorf("claims = %+v", claims.Run)
	}
	if asked, _ := issuer.asked.Load().([]string); len(asked) != 1 || asked[0] != observerAudience {
		t.Errorf("the worker asked for audiences %v", asked)
	}
	env, _ := os.ReadFile(out + ".env")
	for _, leaked := range []string{"sk-run-model-key", "PLOEG_", "AGENT_BUILDER_TOKEN"} {
		if strings.Contains(string(env), leaked) {
			t.Errorf("the hook saw %s:\n%s", leaked, env)
		}
	}
	left, _ := filepath.Glob(filepath.Join(filepath.Dir(adapter.pluginDirs[0]), ".guard", "*", claudeplugins.RunIdentityFileName))
	if len(left) != 0 {
		t.Errorf("run identity files outlived the Run: %v", left)
	}
}

func TestAPluginHookThatDidNotOptInReceivesNoRunIdentity(t *testing.T) {
	issuer, apiURL := newFakeIdentityIssuer(t)
	_, out := runReportingPlugin(t, reportingPlugin, apiURL)
	if token, _ := os.ReadFile(out); len(token) != 0 {
		t.Errorf("a hook without the opt-in received %q", token)
	}
	if env, _ := os.ReadFile(out + ".env"); strings.Contains(string(env), claudeplugins.RunIdentityEnv) {
		t.Errorf("a hook without the opt-in saw %s:\n%s", claudeplugins.RunIdentityEnv, env)
	}
	if n := issuer.requests.Load(); n != 0 {
		t.Errorf("the worker asked for %d run identities nobody opted into", n)
	}
}

func TestAPluginHookRunsWithoutARunIdentityWhenPloegdIssuesNone(t *testing.T) {
	plugin := reportingPlugin
	plugin.RunIdentity = &claudeplugins.RunIdentity{Audience: observerAudience}
	off := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/identity") {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(off.Close)
	_, out := runReportingPlugin(t, plugin, off.URL)
	if token, _ := os.ReadFile(out); len(token) != 0 {
		t.Errorf("the hook received %q from a ploegd that issues none", token)
	}
}

type scriptedIdentities struct {
	answers chan error
	calls   atomic.Int32
}

func (s *scriptedIdentities) RunIdentities(_ context.Context, _ string, audiences []string) ([]RunIdentity, error) {
	n := s.calls.Add(1)
	if err := <-s.answers; err != nil {
		return nil, err
	}
	return []RunIdentity{{Audience: audiences[0], Token: "token-" + string(rune('0'+n)), ExpiresAt: time.Now().Add(time.Minute)}}, nil
}

func TestRunIdentityIsRefreshedEachIntervalAndRemovedWhenTheRunEnds(t *testing.T) {
	file := filepath.Join(t.TempDir(), claudeplugins.RunIdentityFileName)
	results := []claudeplugins.Result{{Name: "p", RunIdentityFile: file, Audience: observerAudience}}
	src := &scriptedIdentities{answers: make(chan error, 3)}
	src.answers <- nil
	src.answers <- nil
	k := startRunIdentity(context.Background(), src, "rt", results, 20*time.Millisecond, discardLog())
	waitFor(t, func() bool { b, _ := os.ReadFile(file); return string(b) == "token-2" })
	if info, err := os.Stat(file); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("run identity file mode %v %v", info.Mode().Perm(), err)
	}
	src.answers <- ErrRunIdentityEnded
	waitFor(t, func() bool { _, err := os.Stat(file); return os.IsNotExist(err) })
	k.Close()
	if n := src.calls.Load(); n != 3 {
		t.Errorf("asked %d times after the Run ended, want it to stop at 3", n)
	}
}

func TestRunIdentityKeepsTheLastTokenThroughATransientFailure(t *testing.T) {
	file := filepath.Join(t.TempDir(), claudeplugins.RunIdentityFileName)
	results := []claudeplugins.Result{{Name: "p", RunIdentityFile: file, Audience: observerAudience}}
	src := &scriptedIdentities{answers: make(chan error, 2)}
	src.answers <- nil
	src.answers <- errFlaky
	k := startRunIdentity(context.Background(), src, "rt", results, 20*time.Millisecond, discardLog())
	waitFor(t, func() bool { return src.calls.Load() >= 2 })
	if b, _ := os.ReadFile(file); string(b) != "token-1" {
		t.Errorf("a transient failure replaced the token with %q", b)
	}
	k.Close()
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Error("the run identity file outlived Close")
	}
}

func TestRunIdentityStopsAskingOncePloegdRefusesTheAudience(t *testing.T) {
	file := filepath.Join(t.TempDir(), claudeplugins.RunIdentityFileName)
	results := []claudeplugins.Result{{Name: "p", RunIdentityFile: file, Audience: observerAudience}}
	src := &scriptedIdentities{answers: make(chan error, 3)}
	src.answers <- nil
	src.answers <- ErrRunIdentityRefused
	src.answers <- nil
	k := startRunIdentity(context.Background(), src, "rt", results, 20*time.Millisecond, discardLog())
	waitFor(t, func() bool { _, err := os.Stat(file); return src.calls.Load() >= 2 && os.IsNotExist(err) })
	time.Sleep(100 * time.Millisecond)
	k.Close()
	if n := src.calls.Load(); n != 2 {
		t.Errorf("asked %d times after ploegd refused the audience, want it to stop at 2", n)
	}
}

func TestPloegdsRefusalOfAnAudienceIsNotATransientFailure(t *testing.T) {
	refusing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "audience is not configured for this Run's Team and Role", http.StatusForbidden)
	}))
	t.Cleanup(refusing.Close)
	api := &APIClient{Base: refusing.URL, HC: refusing.Client()}
	if _, err := api.RunIdentities(context.Background(), "rt", []string{observerAudience}); !errors.Is(err, ErrRunIdentityRefused) {
		t.Errorf("a 403 reads as %v, want ErrRunIdentityRefused", err)
	}
}

var errFlaky = errors.New("run identity: HTTP 503")

func waitFor(t *testing.T, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met within 5s")
		}
		time.Sleep(5 * time.Millisecond)
	}
}
