package httpapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/ploeg-hq/ploeg/pkg/credentialgateway"
	"github.com/ploeg-hq/ploeg/pkg/kubeauth"
)

const (
	runnerSessionToken = "sk-ant-cc-fixture-session-token-0123456789"
	runnerWorkOrder    = "eyJ.fixture-work-order.sig"
	runnerPodUID       = "uid-runner-pod"
	runnerPodName      = "ploeg-claude-runner-0123456789abcdef"
	runnerSessionID    = "session_01ROUTINE00000001"
	runnerGatewayToken = "runs-gateway-token"
)

type runnerPods struct{}

func (runnerPods) PodsAt(_ context.Context, _, selector, ip string) ([]kubeauth.Pod, error) {
	if ip != "127.0.0.1" || selector != "app.kubernetes.io/name=ploeg-claude-runner-session" {
		return nil, nil
	}
	return []kubeauth.Pod{{Name: runnerPodName, UID: runnerPodUID, IP: ip, Phase: "Running", OwnerKind: "Job",
		Labels:      map[string]string{"app.kubernetes.io/name": "ploeg-claude-runner-session"},
		Annotations: map[string]string{"ploeg.webgrip.dev/claude-session": runnerSessionID}}}, nil
}

func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func runnerJobTemplate(t *testing.T, root string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(root, "ops/helm/ploeg/ci/golden/executor-credential-gateway-claude-environment.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	dec := yaml.NewDecoder(strings.NewReader(string(raw)))
	for {
		var doc struct {
			Kind     string                `yaml:"kind"`
			Metadata struct{ Name string } `yaml:"metadata"`
			Data     map[string]string     `yaml:"data"`
		}
		if err := dec.Decode(&doc); err != nil {
			t.Fatalf("the golden render holds no runner Job template: %v", err)
		}
		if doc.Kind == "ConfigMap" && strings.HasSuffix(doc.Metadata.Name, "-orchestrator") {
			return []byte(doc.Data["job.json"])
		}
	}
}

type runnerJob struct {
	Spec struct {
		Template struct {
			Spec struct {
				Containers []struct {
					Name string `json:"name"`
					Env  []struct {
						Name      string          `json:"name"`
						Value     string          `json:"value"`
						ValueFrom json.RawMessage `json:"valueFrom"`
					} `json:"env"`
					EnvFrom json.RawMessage `json:"envFrom"`
				} `json:"containers"`
				Volumes []map[string]any `json:"volumes"`
			} `json:"spec"`
		} `json:"template"`
	} `json:"spec"`
}

type runnerForge struct {
	mu    sync.Mutex
	auth  []string
	pulls int
}

func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.test",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.test")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
}

const fakeRunner = `#!/usr/bin/env bash
set -u
[ "$1 $2 $3" = "self-hosted-runner --environment-secret-file @ORDERDIR@/work-order" ] || { echo "runner started as $*" >&2; exit 1; }
export CLAUDE_CODE_SESSION_ACCESS_TOKEN='@TOKEN@' CLAUDE_RUNNER_SESSION_ID='@SESSION@' CLAUDE_CONFIG_DIR='@WORK@/claude-config'
export CLAUDE_RUNNER_CHECKOUT_PATH='@WORK@/webgrip-fixture' CLAUDE_RUNNER_REPO_URL='https://github.com/webgrip/fixture.git'
mkdir -p "$CLAUDE_CONFIG_DIR"
'@HOOKS@/checkout' || exit 1
'@PROBE@'
export CLAUDE_RUNNER_WORKSPACE_PATHS="$CLAUDE_RUNNER_CHECKOUT_PATH" CLAUDE_RUNNER_EXIT_REASON=completed
'@HOOKS@/post-session'
`

const runnerProbe = `#!/bin/sh
out='@OUT@'
{
  env
  for f in /proc/[0-9]*/environ /proc/[0-9]*/cmdline; do cat "$f" 2>/dev/null; echo; done
  for d in "$HOME" "$CLAUDE_CONFIG_DIR" '@WORK@' '@ORDERDIR@' /var/run/secrets /run/secrets; do
    [ -d "$d" ] && find "$d" -type f -size -1024k ! -path "$out*" 2>/dev/null | while read -r f; do cat "$f" 2>/dev/null; echo; done
  done
  cat "$CLAUDE_RUNNER_CHECKOUT_PATH/.git/config"
  git -C "$CLAUDE_RUNNER_CHECKOUT_PATH" config --list --show-origin
} > "$out" 2>/dev/null
cd "$CLAUDE_RUNNER_CHECKOUT_PATH" || exit 1
echo probe > probe.txt
git add probe.txt && git -c user.name=probe -c user.email=probe@example.test commit -q -m probe
git -c "http.extraHeader=Authorization: Bearer $CLAUDE_CODE_SESSION_ACCESS_TOKEN" push -q origin HEAD:refs/heads/development > "$out.push" 2>&1
echo "exit $?" >> "$out.push"
exit 0
`

func TestClaudeRunnerPodHoldsNoClaudeCredential(t *testing.T) {
	for _, tool := range []string{"bash", "curl", "jq", "git"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s is not available", tool)
		}
	}
	execPath, err := exec.Command("git", "--exec-path").Output()
	if err != nil {
		t.Skip("git is not available")
	}
	backend := filepath.Join(strings.TrimSpace(string(execPath)), "git-http-backend")
	if _, err := os.Stat(backend); err != nil {
		t.Skip("git-http-backend is not available")
	}
	root := repoRoot(t)
	rig, broker := sessionGatewayRig(t)
	rig.server.LeaseTTL = 10 * time.Minute
	work := t.TempDir()

	projects := filepath.Join(work, "forge")
	bare := filepath.Join(projects, "webgrip", "fixture.git")
	gitIn(t, "", "init", "-q", "--bare", bare)
	gitIn(t, bare, "config", "http.receivepack", "true")
	seed := filepath.Join(work, "seed")
	if err := os.MkdirAll(seed, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(seed, "README.md"), []byte("# fixture\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, seed, "init", "-q")
	gitIn(t, seed, "add", ".")
	gitIn(t, seed, "commit", "-q", "-m", "base")
	gitIn(t, seed, "push", "-q", bare, "HEAD:refs/heads/development")
	forgeState := &runnerForge{}
	git := &cgi.Handler{Path: backend, Env: []string{"GIT_PROJECT_ROOT=" + projects, "GIT_HTTP_EXPORT_ALL=1"}}
	forge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		forgeState.mu.Lock()
		forgeState.auth = append(forgeState.auth, r.Header.Get("Authorization"))
		forgeState.mu.Unlock()
		if strings.HasPrefix(r.URL.Path, "/api/") {
			w.Header().Set("Content-Type", "application/json")
			if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/pulls") {
				forgeState.mu.Lock()
				forgeState.pulls++
				forgeState.mu.Unlock()
				_, _ = io.WriteString(w, `{"html_url":"https://forge.example/webgrip/fixture/pulls/1"}`)
				return
			}
			_, _ = io.WriteString(w, "[]")
			return
		}
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Basic ") {
			w.Header().Set("WWW-Authenticate", `Basic realm="forge"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		git.ServeHTTP(w, r)
	}))
	defer forge.Close()
	rig.server.ClaudeSessions.ForgeURL = forge.URL
	ploegd := httptest.NewServer(rig.server.Handler())
	defer ploegd.Close()

	rig.register()
	id := rig.workFor("bronze", "2301")
	if code, out := rig.fire(id); code != 200 {
		t.Fatalf("fire = %d %v", code, out)
	}
	rig.server.ClaudeSessions.Verifier = fakeSessionVerifier{claims: routineSession(1, "user:owner")}

	tokenFile := filepath.Join(work, "gateway-token")
	if err := os.WriteFile(tokenFile, []byte(runnerGatewayToken), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(work, "scratch"), 0o700); err != nil {
		t.Fatal(err)
	}
	gateway, err := credentialgateway.New(credentialgateway.Config{
		Namespace: "runs", PloegdURL: ploegd.URL, TokenFile: tokenFile, Bootstraps: map[string]string{"bronze--builder": "unused-bootstrap"},
		Pods: runnerPods{}, SessionPodSelector: "app.kubernetes.io/name=ploeg-claude-runner-session", ScratchDir: filepath.Join(work, "scratch"),
		Log: slog.New(slog.DiscardHandler),
	})
	if err != nil {
		t.Fatal(err)
	}
	sessionPort := httptest.NewServer(gateway.SessionHandler())
	defer sessionPort.Close()

	var submitted []byte
	kube := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		submitted, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusCreated)
	}))
	defer kube.Close()
	orchestrator := filepath.Join(t.TempDir(), "orchestrator")
	if err := os.MkdirAll(filepath.Join(orchestrator, "sa"), 0o700); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{"job.json": string(runnerJobTemplate(t, root)), "sa/token": "orchestrator-sa-token",
		"environment-secret": routineEnvironmentSecret, "work-order": runnerWorkOrder} {
		if err := os.WriteFile(filepath.Join(orchestrator, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	spawn := exec.Command(filepath.Join(root, "ops/claude-runner/spawn-runner"))
	spawn.Env = []string{"PATH=" + os.Getenv("PATH"), "POD_NAMESPACE=runs",
		"PLOEG_CLAUDE_RUNNER_JOB=" + filepath.Join(orchestrator, "job.json"), "PLOEG_CLAUDE_RUNNER_JOB_PREFIX=ploeg-claude-runner",
		"PLOEG_KUBERNETES_SERVICEACCOUNT_DIR=" + filepath.Join(orchestrator, "sa"), "PLOEG_KUBERNETES_API_URL=" + kube.URL,
		"SELF_HOSTED_RUNNER_ENVIRONMENT_SECRET=" + routineEnvironmentSecret, "CLAUDE_CODE_OAUTH_TOKEN=" + routineClaudeLogin,
		"CLAUDE_RUNNER_WORK_ORDER_FILE=" + filepath.Join(orchestrator, "work-order"), "CLAUDE_RUNNER_ORDER_ID=order-1",
		"CLAUDE_RUNNER_SESSION_ID=" + runnerSessionID, "CLAUDE_RUNNER_CLIENT_PLATFORM=scheduled_trigger"}
	if out, err := spawn.CombinedOutput(); err != nil {
		t.Fatalf("the fake orchestrator's spawn-runner hook failed: %v: %s", err, out)
	}
	for _, secret := range []string{routineEnvironmentSecret, routineClaudeLogin, routineTriggerToken} {
		if strings.Contains(string(submitted), secret) {
			t.Fatal("the runner Job the orchestrator submitted carries a secret it holds")
		}
	}
	var job runnerJob
	if err := json.Unmarshal(submitted, &job); err != nil {
		t.Fatalf("the submitted runner Job does not parse: %v", err)
	}
	if len(job.Spec.Template.Spec.Containers) != 1 {
		t.Fatalf("runner Job containers = %d, want the runner", len(job.Spec.Template.Spec.Containers))
	}
	runner := job.Spec.Template.Spec.Containers[0]
	if len(runner.EnvFrom) > 0 && string(runner.EnvFrom) != "null" {
		t.Fatalf("the runner pod takes envFrom %s", runner.EnvFrom)
	}
	for _, v := range job.Spec.Template.Spec.Volumes {
		if _, ok := v["emptyDir"]; !ok {
			t.Fatalf("the runner pod mounts a volume that is not an emptyDir: %v", v)
		}
	}

	home := filepath.Join(work, "home")
	orderDir := filepath.Join(work, "work-order")
	bin := filepath.Join(work, "bin")
	probeOut := filepath.Join(t.TempDir(), "probe.out")
	for _, dir := range []string{filepath.Join(home, ".claude"), orderDir, bin} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	replace := strings.NewReplacer("@TOKEN@", runnerSessionToken, "@SESSION@", runnerSessionID, "@WORK@", work, "@ORDERDIR@", orderDir,
		"@HOOKS@", filepath.Join(root, "ops/claude-runner"), "@PROBE@", filepath.Join(bin, "probe"), "@OUT@", probeOut)
	for name, script := range map[string]string{"claude": fakeRunner, "probe": runnerProbe} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(replace.Replace(script)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	env := []string{"PATH=" + bin + string(os.PathListSeparator) + os.Getenv("PATH"), "HOME=" + home, "PLOEG_CLAUDE_RUNNER_WORK_ORDER_DIR=" + orderDir,
		"PLOEG_CHECKOUT_ATTEMPTS=2", "PLOEG_CHECKOUT_WAIT_SECONDS=1", "PLOEG_CLAUDE_RUNNER_REGISTRATION_SECONDS=1",
		"PLOEG_CLAUDE_RUNNER_HEALTH_URL=http://127.0.0.1:1/healthz"}
	for _, e := range runner.Env {
		if len(e.ValueFrom) > 0 && string(e.ValueFrom) != "null" {
			t.Fatalf("the runner pod reads %s from %s", e.Name, e.ValueFrom)
		}
		value := e.Value
		if e.Name == "PLOEG_URL" {
			value = sessionPort.URL
		}
		env = append(env, e.Name+"="+value)
	}
	start := exec.Command(filepath.Join(root, "ops/claude-runner/runner-from-work-order"))
	start.Env = env
	start.Dir = work
	if out, err := start.CombinedOutput(); err != nil {
		t.Fatalf("the runner pod's session failed: %v: %s", err, out)
	} else {
		t.Logf("runner: %s", out)
	}

	seen, err := os.ReadFile(probeOut)
	if err != nil {
		t.Fatalf("the probe did not run: %v", err)
	}
	if !strings.Contains(string(seen), runnerSessionToken) {
		t.Fatal("the probe did not even find the session token in its environment, so it proves nothing")
	}
	minted := []string{}
	broker.mu.Lock()
	for i := 1; i <= broker.minted; i++ {
		minted = append(minted, fmt.Sprintf("secret-tok-%d-webgrip/fixture", i))
	}
	broker.mu.Unlock()
	if len(minted) == 0 {
		t.Fatal("ploegd minted no forge credential for the session's Run")
	}
	for name, value := range map[string]string{"routine trigger token": routineTriggerToken, "environment secret": routineEnvironmentSecret,
		"claude.ai login": routineClaudeLogin, "Claude OAuth token": "sk-ant-oat01", "forge credential": minted[0]} {
		if strings.Contains(string(seen), value) {
			t.Errorf("the session found the %s in its runner pod", name)
		}
	}
	for _, value := range minted {
		if strings.Contains(string(seen), value) {
			t.Errorf("the session found a minted forge credential in its runner pod")
		}
	}

	head, err := exec.Command("git", "-C", bare, "rev-parse", "--verify", "refs/heads/agent/vik-2301").Output()
	if err != nil || len(head) == 0 {
		t.Fatalf("the session's push did not reach the Run branch through the gateway: %v", err)
	}
	pushed, _ := os.ReadFile(probeOut + ".push")
	if strings.HasSuffix(strings.TrimSpace(string(pushed)), "exit 0") {
		t.Fatalf("a push outside the Run branch passed the gateway: %s", pushed)
	}
	if out, _ := exec.Command("git", "-C", bare, "log", "--format=%s", "development").Output(); strings.Contains(string(out), "probe") {
		t.Fatal("the probe's commit reached development")
	}
	forgeState.mu.Lock()
	defer forgeState.mu.Unlock()
	if forgeState.pulls != 1 {
		t.Fatalf("pull requests opened = %d, want 1", forgeState.pulls)
	}
	allowed := map[string]bool{"": true}
	for _, token := range minted {
		allowed["Basic "+base64.StdEncoding.EncodeToString([]byte("agent-builder:"+token))] = true
		allowed["token "+token] = true
	}
	for _, auth := range forgeState.auth {
		if !allowed[auth] {
			t.Errorf("the forge saw a credential other than the minted one: %q", auth)
		}
	}
	if n := countRows(t, `SELECT count(*) FROM claude_sessions c JOIN agent_runs a ON a.id = c.run_id
		WHERE a.work_item_id = $1 AND c.reported_at IS NOT NULL AND c.pushed_commit <> ''`, id); n != 1 {
		t.Fatal("the runner's report did not reach ploegd through the gateway")
	}
}
