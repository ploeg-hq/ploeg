package worker_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/credentialgateway"
	"github.com/ploeg-hq/ploeg/pkg/gatewaycontract"
	"github.com/ploeg-hq/ploeg/pkg/harness"
	"github.com/ploeg-hq/ploeg/pkg/harness/adapters/openhands"
	"github.com/ploeg-hq/ploeg/pkg/kubeauth"
	"github.com/ploeg-hq/ploeg/pkg/mcpstdio"
	"github.com/ploeg-hq/ploeg/pkg/work"
	"github.com/ploeg-hq/ploeg/pkg/worker"
)

const (
	fixtureCapability = "fixture-run-capability-0123456789abcdef"
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

const gatewayProcessEnv = "PLOEG_TEST_GATEWAY_PROCESS_DIR"

type gatewayProcessAddresses struct {
	Control, Forge, Model string
}

type gatewayProcessReport struct {
	Secrets              map[string]string
	PloegdFamilyRequests int64
	RunRouteAuth         []string
	ClaimAuth            string
	Outcome              bool
	BranchPushed         bool
	ForgeAuth            []string
	WantForgeAuth        []string
}

func randomSecret(prefix string) string {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return prefix + hex.EncodeToString(b)
}

func TestGatewayProcess(t *testing.T) {
	dir := os.Getenv(gatewayProcessEnv)
	if dir == "" {
		t.Skip("runs only as TestRunPodHoldsNoCredential's credential gateway process")
	}
	if err := mcpstdio.Undumpable(); err != nil {
		t.Fatal(err)
	}
	secrets := map[string]string{
		"bootstrap capability": randomSecret("bootstrap-"), "forge credential": randomSecret("forge-"),
		"model key": randomSecret("sk-model-"), "gateway token": randomSecret("gateway-sa-"), "transaction token": randomSecret("txn-"),
	}
	execPath, err := exec.Command("git", "--exec-path").Output()
	if err != nil {
		t.Fatal(err)
	}
	projects := filepath.Join(dir, "forge")
	bare := filepath.Join(projects, "acme", "widgets.git")
	gitRun(t, "", "init", "--bare", bare)
	gitRun(t, bare, "config", "http.receivepack", "true")
	seed := filepath.Join(dir, "seed")
	if err := os.MkdirAll(seed, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(seed, "README.md"), []byte("# widgets\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, seed, "init")
	gitRun(t, seed, "add", ".")
	gitRun(t, seed, "commit", "-m", "base")
	gitRun(t, seed, "push", bare, "HEAD:refs/heads/main")

	var mu sync.Mutex
	report := gatewayProcessReport{Secrets: secrets}
	wantForgeAuth := []string{"Basic " + base64.StdEncoding.EncodeToString([]byte("agent-builder:"+secrets["forge credential"])), "token " + secrets["forge credential"]}
	gitBackend := &cgi.Handler{Path: filepath.Join(strings.TrimSpace(string(execPath)), "git-http-backend"), Env: []string{"GIT_PROJECT_ROOT=" + projects, "GIT_HTTP_EXPORT_ALL=1"}}
	forge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		report.ForgeAuth = append(report.ForgeAuth, r.Header.Get("Authorization"))
		mu.Unlock()
		if strings.HasPrefix(r.URL.Path, "/api/") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, "[]")
			return
		}
		if r.Header.Get("Authorization") != wantForgeAuth[0] {
			w.Header().Set("WWW-Authenticate", `Basic realm="forge"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		gitBackend.ServeHTTP(w, r)
	}))
	defer forge.Close()
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, `{}`) }))
	defer model.Close()
	ploegd := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gatewayAuth := r.Header.Get(gatewaycontract.AuthorizationHeader) == "Bearer "+secrets["gateway token"]
		switch {
		case r.URL.Path == "/api/v1/claim":
			mu.Lock()
			report.ClaimAuth = r.Header.Get("Authorization")
			mu.Unlock()
			if !gatewayAuth || r.Header.Get(gatewaycontract.PodUIDHeader) != runPodUID {
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
			var req gatewaycontract.GrantRequest
			_ = json.NewDecoder(r.Body).Decode(&req)
			if !gatewayAuth || req.Capability != fixtureCapability || req.SourceUID != runPodUID {
				http.Error(w, "refused", http.StatusForbidden)
				return
			}
			_ = json.NewEncoder(w).Encode(gatewaycontract.Grant{
				Run: "run-token-42", WorkItem: "42", Team: "builders", Role: "builder", Writes: true, Branch: runBranch,
				ForgeDialect: harness.ForgeForgejo, ForgeURL: forge.URL, Owner: "acme", Repo: "widgets", BaseBranch: "main",
				ForgeToken: secrets["forge credential"], ModelKey: secrets["model key"], ModelAlias: "gateway-alias",
				Pin:      gatewaycontract.Pin{Namespace: "runs", PodName: runPodName, PodUID: runPodUID, PodIP: req.SourceIP},
				Deadline: time.Now().Add(10 * time.Minute), TransactionToken: secrets["transaction token"],
			})
		case r.URL.Path == gatewaycontract.AuditPath:
			var batch gatewaycontract.AuditBatch
			_ = json.NewDecoder(r.Body).Decode(&batch)
			mu.Lock()
			for _, u := range batch.Uses {
				if u.Family == credentialgateway.FamilyPloegd {
					report.PloegdFamilyRequests += u.Requests
				}
			}
			mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		case strings.HasPrefix(r.URL.Path, "/api/v1/runs/"):
			mu.Lock()
			report.RunRouteAuth = append(report.RunRouteAuth, r.Header.Get("Authorization"))
			mu.Unlock()
			if !gatewayAuth || r.Header.Get("Authorization") != "Bearer "+secrets["transaction token"] {
				http.Error(w, "not through the gateway", http.StatusUnauthorized)
				return
			}
			switch {
			case strings.HasSuffix(r.URL.Path, "/outcome"):
				mu.Lock()
				report.Outcome = true
				mu.Unlock()
				w.WriteHeader(http.StatusOK)
			case strings.HasSuffix(r.URL.Path, "/llm/block"):
				w.WriteHeader(http.StatusNoContent)
			case strings.HasSuffix(r.URL.Path, "/llm/spend"):
				_, _ = io.WriteString(w, `{"costUsd":0}`)
			default:
				_, _ = io.WriteString(w, `{}`)
			}
		default:
			_, _ = io.WriteString(w, `{}`)
		}
	}))
	defer ploegd.Close()

	secretDir := filepath.Join(dir, "secrets")
	if err := os.MkdirAll(secretDir, 0o700); err != nil {
		t.Fatal(err)
	}
	tokenFile := filepath.Join(secretDir, "token")
	if err := os.WriteFile(tokenFile, []byte(secrets["gateway token"]), 0o600); err != nil {
		t.Fatal(err)
	}
	gateway, err := credentialgateway.New(credentialgateway.Config{
		Namespace: "runs", PloegdURL: ploegd.URL, TokenFile: tokenFile, Bootstraps: map[string]string{"builders--builder": secrets["bootstrap capability"]},
		ModelURL: model.URL, Pods: gatewayPods{}, ScratchDir: filepath.Join(dir, "scratch"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "scratch"), 0o700); err != nil {
		t.Fatal(err)
	}
	control := httptest.NewServer(gateway.ControlHandler())
	defer control.Close()
	forgeGW := httptest.NewServer(gateway.Handler(credentialgateway.FamilyForge))
	defer forgeGW.Close()
	modelGW := httptest.NewServer(gateway.Handler(credentialgateway.FamilyModel))
	defer modelGW.Close()
	if err := json.NewEncoder(os.Stdout).Encode(gatewayProcessAddresses{Control: control.URL, Forge: forgeGW.URL, Model: modelGW.URL}); err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, os.Stdin)
	if err := gateway.FlushAudit(context.Background()); err != nil {
		t.Fatal(err)
	}
	head, err := exec.Command("git", "-C", bare, "rev-parse", "--verify", "refs/heads/"+runBranch).Output()
	mu.Lock()
	report.BranchPushed = err == nil && len(head) > 0
	report.WantForgeAuth = wantForgeAuth
	mu.Unlock()
	_ = json.NewEncoder(os.Stdout).Encode(report)
}

func startGatewayProcess(t *testing.T) (gatewayProcessAddresses, func() gatewayProcessReport) {
	t.Helper()
	dir, err := os.MkdirTemp(".", ".gateway-process-")
	if err != nil {
		t.Fatal(err)
	}
	dir, _ = filepath.Abs(dir)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	cmd := exec.Command(os.Args[0], "-test.run=^TestGatewayProcess$", "-test.v=false")
	cmd.Env = append(os.Environ(), gatewayProcessEnv+"="+dir)
	cmd.Stderr = os.Stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	dec := json.NewDecoder(stdout)
	var addrs gatewayProcessAddresses
	if err := dec.Decode(&addrs); err != nil {
		t.Fatalf("the gateway process did not start: %v", err)
	}
	return addrs, func() gatewayProcessReport {
		_ = stdin.Close()
		var report gatewayProcessReport
		if err := dec.Decode(&report); err != nil {
			t.Fatalf("the gateway process reported nothing: %v", err)
		}
		return report
	}
}

const runPodProbe = `#!/bin/sh
out='@OUT@'
{
  env
  for f in /proc/[0-9]*/environ /proc/[0-9]*/cmdline; do cat "$f" 2>/dev/null; echo; done
  for d in "$HOME" "$TMPDIR" '@WORK@' /var/run/secrets /run/secrets /var/run/secrets/kubernetes.io/serviceaccount; do
    [ -d "$d" ] && find "$d" -type f -size -1024k 2>/dev/null | while read -r f; do cat "$f" 2>/dev/null; echo; done
  done
  if [ -r /proc/self/mounts ]; then
    while read -r dev mnt fstype rest; do
      case "$fstype" in
        tmpfs|ramfs)
          case "$mnt" in /proc*|/sys*|/dev|/dev/*) continue ;; esac
          find "$mnt" -xdev -type f -size -1024k 2>/dev/null | head -n 5000 | while read -r f; do cat "$f" 2>/dev/null; echo; done ;;
      esac
    done < /proc/self/mounts
  fi
} > "$out" 2>/dev/null
mem="$out.mem"
: > "$mem"
readable=0
for p in /proc/[0-9]*; do
  [ "${p#/proc/}" = "$$" ] && continue
  [ -r "$p/maps" ] || continue
  while read -r range perms offset device inode path; do
    case "$perms" in r*) ;; *) continue ;; esac
    [ "$inode" = "0" ] || continue
    start=$((0x${range%-*}))
    end=$((0x${range#*-}))
    size=$((end - start))
    [ "$size" -le 67108864 ] || continue
    if dd if="$p/mem" bs=4096 skip=$((start / 4096)) count=$((size / 4096)) 2>/dev/null >> "$mem"; then readable=1; fi
    [ "$(wc -c < "$mem")" -lt 536870912 ] || break 2
  done < "$p/maps" 2>/dev/null
done
echo "$readable" > "$out.memread"
cd '@CLONE@' || exit 1
echo probe > probe.txt
git add probe.txt && git -c user.name=probe -c user.email=probe@example.test commit -q -m probe
git push -q origin HEAD:refs/heads/@BRANCH@ >> "$out.push" 2>&1
exit 0
`

func TestRunPodHoldsNoCredential(t *testing.T) {
	execPath, err := exec.Command("git", "--exec-path").Output()
	if err != nil {
		t.Skip("git is not available")
	}
	if _, err := os.Stat(filepath.Join(strings.TrimSpace(string(execPath)), "git-http-backend")); err != nil {
		t.Skip("git-http-backend is not available")
	}
	addrs, finish := startGatewayProcess(t)

	workDir := t.TempDir()
	probeOut := filepath.Join(t.TempDir(), "probe.out")
	clone := filepath.Join(workDir, "vik-42")
	probe := filepath.Join(t.TempDir(), "hostile-harness.sh")
	script := strings.NewReplacer("@OUT@", probeOut, "@WORK@", workDir, "@CLONE@", clone, "@BRANCH@", runBranch).Replace(runPodProbe)
	if err := os.WriteFile(probe, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	w := worker.New(worker.Config{
		APIURL: addrs.Control, WorkerID: runPodName, Team: "builders", Role: "builder",
		CredentialGateway: true, GatewayForgeURL: addrs.Forge, ForgeURL: addrs.Forge,
		LLMBaseURL: addrs.Model, WorkDir: workDir, KeyBudget: 1, KeyTTL: time.Hour,
	}, harness.RunCommand(openhands.New(probe)), nil, slog.New(slog.DiscardHandler))
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := w.RunContext(ctx); err != nil {
		t.Fatalf("RunContext: %v", err)
	}
	report := finish()

	seen, err := os.ReadFile(probeOut)
	if err != nil {
		t.Fatalf("the hostile harness did not run: %v", err)
	}
	if !strings.Contains(string(seen), fixtureCapability) {
		t.Fatal("the probe did not even find the Run's capability in its environment, so it proves nothing")
	}
	memory, _ := os.ReadFile(probeOut + ".mem")
	memRead, _ := os.ReadFile(probeOut + ".memread")
	t.Logf("the probe read %d bytes of other processes' memory (readable=%s)", len(memory), strings.TrimSpace(string(memRead)))
	for name, value := range report.Secrets {
		if strings.Contains(string(seen), value) {
			t.Errorf("the harness found the %s in its pod", name)
		}
		if bytes.Contains(memory, []byte(value)) {
			t.Errorf("the harness found the %s in a process's memory", name)
		}
	}
	if report.ClaimAuth != "Bearer "+report.Secrets["bootstrap capability"] {
		t.Fatal("the claim reached ploegd without the bootstrap only the gateway attaches")
	}
	if !report.BranchPushed {
		pushed, _ := os.ReadFile(probeOut + ".push")
		t.Fatalf("the harness's push through the gateway did not reach the forge: %s", pushed)
	}
	for _, v := range report.ForgeAuth {
		if v != "" && !slices.Contains(report.WantForgeAuth, v) {
			t.Errorf("the forge saw a credential other than the minted one: %q", v)
		}
	}
	if len(report.RunRouteAuth) == 0 || !report.Outcome {
		t.Fatalf("the worker's control calls did not reach ploegd through the gateway: %v outcome=%v", report.RunRouteAuth, report.Outcome)
	}
	for _, v := range report.RunRouteAuth {
		if v != "Bearer "+report.Secrets["transaction token"] {
			t.Errorf("ploegd's Run route got %q, want the transaction token the gateway attaches", v)
		}
	}
	if report.PloegdFamilyRequests == 0 {
		t.Error("the gateway's audit carries none of the Run's ploegd calls")
	}
}
