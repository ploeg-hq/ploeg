package worker_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
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

	"github.com/ploeg-hq/ploeg/pkg/credentialgateway"
	"github.com/ploeg-hq/ploeg/pkg/gatewaycontract"
	"github.com/ploeg-hq/ploeg/pkg/harness"
	"github.com/ploeg-hq/ploeg/pkg/harness/adapters/openhands"
	"github.com/ploeg-hq/ploeg/pkg/kubeauth"
	"github.com/ploeg-hq/ploeg/pkg/work"
	"github.com/ploeg-hq/ploeg/pkg/worker"
)

const (
	fixtureBootstrap  = "fixture-bootstrap-capability-0123456789abcdef"
	fixtureForgeToken = "fixture-minted-forge-token-0123456789abcdef"
	fixtureModelKey   = "sk-fixture-per-run-model-key-0123456789abcdef"
	fixtureCapability = "fixture-run-capability-0123456789abcdef"
	fixtureGatewaySA  = "fixture-gateway-projected-token"
	runPodName        = "builders-builder-run-pod"
	runPodUID         = "run-pod-uid"
	runBranch         = "agent/vik-42"
)

type gatewayPods struct{}

func (gatewayPods) PodsAt(_ context.Context, _, _, ip string) ([]kubeauth.Pod, error) {
	if ip != "127.0.0.1" {
		return nil, nil
	}
	return []kubeauth.Pod{{Name: runPodName, UID: runPodUID, IP: ip, Phase: "Running", OwnerKind: "Job",
		Labels: map[string]string{"ploeg.webgrip.dev/team": "builders", "ploeg.webgrip.dev/role": "builder"}}}, nil
}

func gitRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.test",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.test")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestRunPodHoldsNoCredential(t *testing.T) {
	execPath, err := exec.Command("git", "--exec-path").Output()
	if err != nil {
		t.Skip("git is not available")
	}
	backend := filepath.Join(strings.TrimSpace(string(execPath)), "git-http-backend")
	if _, err := os.Stat(backend); err != nil {
		t.Skip("git-http-backend is not available")
	}

	projects := t.TempDir()
	bare := filepath.Join(projects, "acme", "widgets.git")
	gitRun(t, "", "init", "--bare", bare)
	gitRun(t, bare, "config", "http.receivepack", "true")
	seed := t.TempDir()
	if err := os.WriteFile(filepath.Join(seed, "README.md"), []byte("# widgets\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, seed, "init")
	gitRun(t, seed, "add", ".")
	gitRun(t, seed, "commit", "-m", "base")
	gitRun(t, seed, "push", bare, "HEAD:refs/heads/main")

	var forgeAuth sync.Map
	gitBackend := &cgi.Handler{Path: backend, Env: []string{"GIT_PROJECT_ROOT=" + projects, "GIT_HTTP_EXPORT_ALL=1"}}
	wantForgeAuth := "Basic " + base64.StdEncoding.EncodeToString([]byte("agent-builder:"+fixtureForgeToken))
	forge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		forgeAuth.Store(r.Header.Get("Authorization"), true)
		if strings.HasPrefix(r.URL.Path, "/api/") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, "[]")
			return
		}
		if r.Header.Get("Authorization") != wantForgeAuth {
			w.Header().Set("WWW-Authenticate", `Basic realm="forge"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		gitBackend.ServeHTTP(w, r)
	}))
	defer forge.Close()

	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{}`)
	}))
	defer model.Close()

	var claimAuth string
	var outcome sync.Map
	ploegd := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v1/claim":
			claimAuth = r.Header.Get("Authorization")
			if r.Header.Get(gatewaycontract.AuthorizationHeader) != "Bearer "+fixtureGatewaySA || r.Header.Get(gatewaycontract.PodUIDHeader) != runPodUID {
				http.Error(w, "not through the gateway", http.StatusUnauthorized)
				return
			}
			_ = json.NewEncoder(w).Encode(worker.ClaimResponse{
				RunToken: "run-token-42", ControlToken: fixtureCapability, CredentialGateway: true,
				Deadline: time.Now().Add(10 * time.Minute), Role: "builder", Writes: true, Branch: runBranch,
				ForgeDialect: harness.ForgeForgejo, ForgeURL: forge.URL,
				WorkItem: work.WorkItem{ID: "42", ExternalID: "42", Title: "probe", Target: &work.Target{Owner: "acme", Repo: "widgets", BaseBranch: "main"}},
			})
		case r.URL.Path == gatewaycontract.GrantsPath:
			if r.Header.Get(gatewaycontract.AuthorizationHeader) != "Bearer "+fixtureGatewaySA {
				http.Error(w, "unauthenticated", http.StatusUnauthorized)
				return
			}
			var req gatewaycontract.GrantRequest
			_ = json.NewDecoder(r.Body).Decode(&req)
			if req.Capability != fixtureCapability {
				http.Error(w, "refused", http.StatusForbidden)
				return
			}
			_ = json.NewEncoder(w).Encode(gatewaycontract.Grant{
				Run: "run-token-42", WorkItem: "42", Team: "builders", Role: "builder", Writes: true, Branch: runBranch,
				ForgeDialect: harness.ForgeForgejo, ForgeURL: forge.URL, Owner: "acme", Repo: "widgets", BaseBranch: "main",
				ForgeToken: fixtureForgeToken, ModelKey: fixtureModelKey, ModelAlias: "gateway-alias",
				Pin:      gatewaycontract.Pin{Namespace: "runs", PodName: runPodName, PodUID: runPodUID, PodIP: req.SourceIP},
				Deadline: time.Now().Add(10 * time.Minute),
			})
		case strings.HasSuffix(r.URL.Path, "/outcome"):
			body, _ := io.ReadAll(r.Body)
			outcome.Store("body", string(body))
			w.WriteHeader(http.StatusOK)
		case strings.HasSuffix(r.URL.Path, "/llm/block"):
			w.WriteHeader(http.StatusNoContent)
		case strings.HasSuffix(r.URL.Path, "/llm/spend"):
			_, _ = io.WriteString(w, `{"costUsd":0}`)
		default:
			_, _ = io.WriteString(w, `{}`)
		}
	}))
	defer ploegd.Close()

	tokenFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenFile, []byte(fixtureGatewaySA), 0o600); err != nil {
		t.Fatal(err)
	}
	gateway, err := credentialgateway.New(credentialgateway.Config{
		Namespace: "runs", PloegdURL: ploegd.URL, TokenFile: tokenFile, Bootstraps: map[string]string{"builders--builder": fixtureBootstrap},
		ModelURL: model.URL, Pods: gatewayPods{}, ScratchDir: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	control := httptest.NewServer(gateway.ControlHandler())
	defer control.Close()
	forgeGW := httptest.NewServer(gateway.Handler(credentialgateway.FamilyForge))
	defer forgeGW.Close()
	modelGW := httptest.NewServer(gateway.Handler(credentialgateway.FamilyModel))
	defer modelGW.Close()

	workDir := t.TempDir()
	probeOut := filepath.Join(t.TempDir(), "probe.out")
	clone := filepath.Join(workDir, "vik-42")
	probe := filepath.Join(t.TempDir(), "hostile-harness.sh")
	script := `#!/bin/sh
out='` + probeOut + `'
{
  env
  for f in /proc/[0-9]*/environ /proc/[0-9]*/cmdline; do cat "$f" 2>/dev/null; echo; done
  for d in "$HOME" "$TMPDIR" '` + workDir + `' /var/run/secrets /run/secrets; do
    [ -d "$d" ] && find "$d" -type f 2>/dev/null | while read -r f; do cat "$f" 2>/dev/null; echo; done
  done
} > "$out" 2>/dev/null
cd '` + clone + `' || exit 1
echo probe > probe.txt
git add probe.txt && git -c user.name=probe -c user.email=probe@example.test commit -q -m probe
git push -q origin HEAD:refs/heads/` + runBranch + ` >> "$out.push" 2>&1
exit 0
`
	if err := os.WriteFile(probe, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	w := worker.New(worker.Config{
		APIURL: control.URL, WorkerID: runPodName, Team: "builders", Role: "builder",
		CredentialGateway: true, GatewayForgeURL: forgeGW.URL, ForgeURL: forgeGW.URL,
		LLMBaseURL: modelGW.URL, WorkDir: workDir, KeyBudget: 1, KeyTTL: time.Hour,
	}, harness.RunCommand(openhands.New(probe)), nil, slog.New(slog.DiscardHandler))
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := w.RunContext(ctx); err != nil {
		t.Fatalf("RunContext: %v", err)
	}

	if claimAuth != "Bearer "+fixtureBootstrap {
		t.Fatalf("the claim reached ploegd with %q; only the gateway attaches the bootstrap", claimAuth)
	}
	seen, err := os.ReadFile(probeOut)
	if err != nil {
		t.Fatalf("the hostile harness did not run: %v", err)
	}
	if !strings.Contains(string(seen), fixtureCapability) {
		t.Fatal("the probe did not even find the Run's capability in its environment, so it proves nothing")
	}
	for name, value := range map[string]string{"bootstrap capability": fixtureBootstrap, "forge credential": fixtureForgeToken, "model key": fixtureModelKey, "gateway token": fixtureGatewaySA} {
		if strings.Contains(string(seen), value) {
			t.Errorf("the harness found the %s in its pod", name)
		}
	}
	if head, err := exec.Command("git", "-C", bare, "rev-parse", "--verify", "refs/heads/"+runBranch).Output(); err != nil || len(head) == 0 {
		pushed, _ := os.ReadFile(probeOut + ".push")
		t.Fatalf("the harness's push through the gateway did not reach the forge: %v %s", err, pushed)
	}
	forgeAuth.Range(func(k, _ any) bool {
		if v := k.(string); v != "" && v != wantForgeAuth && v != "token "+fixtureForgeToken {
			t.Errorf("the forge saw a credential other than the minted one: %q", v)
		}
		return true
	})
	if _, ok := outcome.Load("body"); !ok {
		t.Error("the worker reported no outcome")
	}
}
