package credentialgateway

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/gatewaycontract"
	"github.com/ploeg-hq/ploeg/pkg/kubeauth"
)

const (
	runCapability    = "run-capability-0123456789abcdef"
	otherCapability  = "other-run-capability-0123456789"
	settledCapabilty = "settled-run-capability-012345678"
	realForgeToken   = "real-forge-token-0123456789abcdef"
	realModelKey     = "sk-real-model-key-0123456789abcdef"
	gatewaySAToken   = "projected-gateway-token"
	bootstrapToken   = "bootstrap-capability-0123456789abcdef0123"
	podUID           = "pod-uid-1"
	podName          = "writer-pod"
)

type seen struct {
	mu       sync.Mutex
	requests []*http.Request
	bodies   []string
}

func (s *seen) add(r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requests = append(s.requests, r.Clone(context.Background()))
	s.bodies = append(s.bodies, string(body))
}

func (s *seen) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.requests)
}

func (s *seen) last() *http.Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.requests[len(s.requests)-1]
}

type fakePods struct {
	mu   sync.Mutex
	pods []kubeauth.Pod
}

func (f *fakePods) PodsAt(_ context.Context, _, _, ip string) ([]kubeauth.Pod, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []kubeauth.Pod
	for _, p := range f.pods {
		if p.IP == ip {
			out = append(out, p)
		}
	}
	return out, nil
}

func (f *fakePods) set(pods ...kubeauth.Pod) {
	f.mu.Lock()
	f.pods = pods
	f.mu.Unlock()
}

type fixture struct {
	gateway *Gateway
	forge   *seen
	model   *seen
	ploegd  *seen
	audits  []gatewaycontract.AuditBatch
	pods    *fakePods
	pinIP   string
	down    bool
	mu      sync.Mutex
	control *httptest.Server
	forgeGW *httptest.Server
	modelGW *httptest.Server
	mcpGW   *httptest.Server
	mcp     []gatewaycontract.MCPServer
	writes  bool
}

func runPod() kubeauth.Pod {
	return kubeauth.Pod{Name: podName, UID: podUID, IP: "127.0.0.1", Phase: "Running", OwnerKind: "Job",
		Labels: map[string]string{teamLabel: "builders", roleLabel: "writer"}}
}

func newFixture(t *testing.T, limits Limits) *fixture {
	t.Helper()
	f := &fixture{forge: &seen{}, model: &seen{}, ploegd: &seen{}, pods: &fakePods{}, pinIP: "127.0.0.1", writes: true}
	f.pods.set(runPod())
	forge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.forge.add(r)
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "{}")
	}))
	t.Cleanup(forge.Close)
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.model.add(r)
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	t.Cleanup(model.Close)
	ploegd := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		down := f.down
		f.mu.Unlock()
		if down {
			http.Error(w, "down", http.StatusServiceUnavailable)
			return
		}
		if r.URL.Path != claimPath && !strings.HasPrefix(r.URL.Path, runRoutePrefix) && r.Header.Get(gatewaycontract.AuthorizationHeader) != "Bearer "+gatewaySAToken {
			http.Error(w, "unauthenticated", http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case gatewaycontract.GrantsPath:
			var req gatewaycontract.GrantRequest
			_ = json.NewDecoder(r.Body).Decode(&req)
			switch req.Capability {
			case runCapability:
				f.mu.Lock()
				pin, mcp, writes := f.pinIP, f.mcp, f.writes
				f.mu.Unlock()
				_ = json.NewEncoder(w).Encode(gatewaycontract.Grant{
					Run: "run-1", WorkItem: "7", Team: "builders", Role: "writer", Writes: writes, Branch: "agent/7", MCP: mcp,
					ForgeDialect: "forgejo", ForgeURL: forge.URL, Owner: "acme", Repo: "widgets", BaseBranch: "main",
					ForgeToken: realForgeToken, ModelKey: realModelKey, ModelAlias: "run-1-alias",
					Pin: gatewaycontract.Pin{Namespace: "runs", PodName: podName, PodUID: podUID, PodIP: pin}, Deadline: time.Now().Add(time.Hour),
				})
			case settledCapabilty:
				http.Error(w, "settled", http.StatusGone)
			default:
				http.Error(w, "refused", http.StatusForbidden)
			}
		case gatewaycontract.AuditPath:
			var batch gatewaycontract.AuditBatch
			_ = json.NewDecoder(r.Body).Decode(&batch)
			f.mu.Lock()
			f.audits = append(f.audits, batch)
			f.mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		default:
			f.ploegd.add(r)
			_, _ = io.WriteString(w, `{"runToken":"run-1"}`)
		}
	}))
	t.Cleanup(ploegd.Close)
	tokenFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenFile, []byte(gatewaySAToken+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	g, err := New(Config{
		Namespace: "runs", PloegdURL: ploegd.URL, TokenFile: tokenFile, Bootstraps: map[string]string{"builders--writer": bootstrapToken},
		ModelURL: model.URL + "/v1", Pods: f.pods, Limits: limits, ScratchDir: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	f.gateway = g
	f.control = httptest.NewServer(g.ControlHandler())
	f.forgeGW = httptest.NewServer(g.Handler(FamilyForge))
	f.modelGW = httptest.NewServer(g.Handler(FamilyModel))
	t.Cleanup(f.control.Close)
	t.Cleanup(f.forgeGW.Close)
	t.Cleanup(f.modelGW.Close)
	f.mcpGW = httptest.NewServer(g.Handler(FamilyMCP))
	t.Cleanup(f.mcpGW.Close)
	return f
}

func do(t *testing.T, method, url string, body io.Reader, headers map[string]string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, url, body)
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	return resp
}

func basic(password string) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte("agent-builder:"+password))
}

func TestForgeReceivesTheMintedCredentialAndNeverTheCapability(t *testing.T) {
	f := newFixture(t, Limits{})
	resp := do(t, http.MethodGet, f.forgeGW.URL+"/api/v1/repos/acme/widgets/pulls", nil, map[string]string{"Authorization": "token " + runCapability})
	if resp.StatusCode != http.StatusOK || f.forge.count() != 1 {
		t.Fatalf("status %d, forge saw %d requests", resp.StatusCode, f.forge.count())
	}
	if got := f.forge.last().Header.Get("Authorization"); got != "token "+realForgeToken {
		t.Fatalf("forge got Authorization %q, want the minted credential", got)
	}
	resp = do(t, http.MethodGet, f.forgeGW.URL+"/acme/widgets.git/info/refs?service=git-upload-pack", nil, map[string]string{"Authorization": basic(runCapability)})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("git fetch through the gateway: status %d", resp.StatusCode)
	}
	for _, r := range f.forge.requests {
		for _, values := range r.Header {
			for _, v := range values {
				if strings.Contains(v, runCapability) || strings.Contains(decodeBasic(v), runCapability) {
					t.Fatalf("the forge saw the Run's capability in %q", v)
				}
			}
		}
	}
}

func decodeBasic(v string) string {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(v, "Basic "))
	if err != nil {
		return ""
	}
	return string(raw)
}

func TestModelGatewayReceivesThePerRunKey(t *testing.T) {
	f := newFixture(t, Limits{})
	resp := do(t, http.MethodPost, f.modelGW.URL+"/v1/chat/completions", strings.NewReader(`{}`), map[string]string{"Authorization": "Bearer " + runCapability})
	if resp.StatusCode != http.StatusOK || f.model.count() != 1 {
		t.Fatalf("status %d, model saw %d", resp.StatusCode, f.model.count())
	}
	if got := f.model.last().Header.Get("Authorization"); got != "Bearer "+realModelKey {
		t.Fatalf("model gateway got %q", got)
	}
	resp = do(t, http.MethodPost, f.modelGW.URL+"/v1/messages", strings.NewReader(`{}`), map[string]string{"X-Api-Key": runCapability})
	if resp.StatusCode != http.StatusOK || f.model.last().Header.Get("X-Api-Key") != realModelKey {
		t.Fatalf("an Anthropic-shaped call got status %d and key %q", resp.StatusCode, f.model.last().Header.Get("X-Api-Key"))
	}
}

func TestRefusedRequestsNeverReachAnUpstream(t *testing.T) {
	cases := []struct {
		name       string
		headers    map[string]string
		path       string
		prepare    func(*fixture)
		wantStatus int
		wantReason string
	}{
		{"no capability", nil, "/api/v1/repos/acme/widgets/pulls", nil, http.StatusForbidden, gatewaycontract.RefusedNoCapability},
		{"another Run's capability", map[string]string{"Authorization": "token " + otherCapability}, "/api/v1/repos/acme/widgets/pulls", nil, http.StatusForbidden, gatewaycontract.RefusedCapability},
		{"a settled Run", map[string]string{"Authorization": "token " + settledCapabilty}, "/api/v1/repos/acme/widgets/pulls", nil, http.StatusForbidden, gatewaycontract.RefusedRunSettled},
		{"another source pod", map[string]string{"Authorization": "token " + runCapability}, "/api/v1/repos/acme/widgets/pulls",
			func(f *fixture) { f.pinIP = "10.0.0.9" }, http.StatusForbidden, gatewaycontract.RefusedPin},
		{"the claiming pod is gone", map[string]string{"Authorization": "token " + runCapability}, "/api/v1/repos/acme/widgets/pulls",
			func(f *fixture) { f.pods.set() }, http.StatusForbidden, gatewaycontract.RefusedPin},
		{"a pod reusing the claimant's IP", map[string]string{"Authorization": "token " + runCapability}, "/api/v1/repos/acme/widgets/pulls",
			func(f *fixture) { p := runPod(); p.UID = "pod-uid-2"; f.pods.set(p) }, http.StatusForbidden, gatewaycontract.RefusedPin},
		{"ploegd unreachable", map[string]string{"Authorization": "token " + runCapability}, "/api/v1/repos/acme/widgets/pulls",
			func(f *fixture) { f.down = true }, http.StatusServiceUnavailable, gatewaycontract.RefusedUnavailable},
		{"another repository", map[string]string{"Authorization": "token " + runCapability}, "/api/v1/repos/acme/secrets/pulls", nil, http.StatusForbidden, "outside_repository"},
		{"a merge", map[string]string{"Authorization": "token " + runCapability}, "/api/v1/repos/acme/widgets/pulls/3/merge", nil, http.StatusForbidden, "forbidden_endpoint"},
		{"a dot segment", map[string]string{"Authorization": "token " + runCapability}, "/api/v1/repos/acme/widgets/%2e%2e/secrets/pulls", nil, http.StatusForbidden, "unsafe_path"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t, Limits{})
			if tc.prepare != nil {
				tc.prepare(f)
			}
			method := http.MethodGet
			if strings.HasSuffix(tc.path, "/merge") {
				method = http.MethodPost
			}
			req, _ := http.NewRequest(method, f.forgeGW.URL+tc.path, nil)
			req.URL.RawPath = tc.path
			for k, v := range tc.headers {
				req.Header.Set(k, v)
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			if resp.StatusCode != tc.wantStatus || resp.Header.Get(gatewaycontract.RefusalHeader) != tc.wantReason {
				t.Fatalf("status %d reason %q, want %d %q", resp.StatusCode, resp.Header.Get(gatewaycontract.RefusalHeader), tc.wantStatus, tc.wantReason)
			}
			if f.forge.count() != 0 || f.model.count() != 0 {
				t.Fatalf("a refused request reached an upstream")
			}
		})
	}
}

func TestGitWithoutACredentialIsChallenged(t *testing.T) {
	f := newFixture(t, Limits{})
	resp := do(t, http.MethodGet, f.forgeGW.URL+"/acme/widgets.git/info/refs?service=git-upload-pack", nil, nil)
	if resp.StatusCode != http.StatusUnauthorized || !strings.HasPrefix(resp.Header.Get("WWW-Authenticate"), "Basic") {
		t.Fatalf("status %d, challenge %q", resp.StatusCode, resp.Header.Get("WWW-Authenticate"))
	}
	if f.forge.count() != 0 {
		t.Fatal("the challenge reached the forge")
	}
}

func pktLine(s string) string { return fmt.Sprintf("%04x%s", len(s)+4, s) }

func pushBody(ref string, deletes bool) string {
	newID := strings.Repeat("b", 40)
	if deletes {
		newID = strings.Repeat("0", 40)
	}
	return pktLine(strings.Repeat("a", 40)+" "+newID+" "+ref+"\x00report-status") + "0000PACK"
}

func TestPushesAreFencedToTheRunBranchThroughTheGateway(t *testing.T) {
	cases := []struct {
		name   string
		body   string
		status int
		reason string
	}{
		{"the Run's branch", pushBody("refs/heads/agent/7", false), http.StatusOK, ""},
		{"another branch", pushBody("refs/heads/main", false), http.StatusForbidden, "push_to_other_branch"},
		{"a tag", pushBody("refs/tags/v1", false), http.StatusForbidden, "push_to_other_branch"},
		{"deleting its branch", pushBody("refs/heads/agent/7", true), http.StatusForbidden, "branch_deletion"},
		{"an unparseable command list", "zzzz", http.StatusForbidden, "uninspectable_push"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t, Limits{})
			resp := do(t, http.MethodPost, f.forgeGW.URL+"/acme/widgets.git/git-receive-pack", strings.NewReader(tc.body),
				map[string]string{"Authorization": basic(runCapability), "Content-Type": "application/x-git-receive-pack-request"})
			if resp.StatusCode != tc.status || resp.Header.Get(gatewaycontract.RefusalHeader) != tc.reason {
				t.Fatalf("status %d reason %q", resp.StatusCode, resp.Header.Get(gatewaycontract.RefusalHeader))
			}
			if (tc.status == http.StatusOK) != (f.forge.count() == 1) {
				t.Fatalf("forge saw %d pushes", f.forge.count())
			}
			if tc.status == http.StatusOK && f.forge.bodies[0] != tc.body {
				t.Fatal("the forge did not receive the push unchanged")
			}
		})
	}
}

func TestAPIRequestCarryingTheCapabilityIsRefusedAsALeak(t *testing.T) {
	f := newFixture(t, Limits{})
	resp := do(t, http.MethodPost, f.forgeGW.URL+"/api/v1/repos/acme/widgets/issues/3/comments",
		strings.NewReader(`{"body":"`+runCapability+`"}`), map[string]string{"Authorization": "token " + runCapability})
	if resp.StatusCode != http.StatusForbidden || resp.Header.Get(gatewaycontract.RefusalHeader) != "credential_in_request" {
		t.Fatalf("status %d reason %q", resp.StatusCode, resp.Header.Get(gatewaycontract.RefusalHeader))
	}
	resp = do(t, http.MethodPost, f.forgeGW.URL+"/api/v1/repos/acme/widgets/pulls", strings.NewReader(`{"title":"x"}`),
		map[string]string{"Authorization": "token " + runCapability})
	if resp.StatusCode != http.StatusForbidden || resp.Header.Get(gatewaycontract.RefusalHeader) != "writes_stopped_by_leak" {
		t.Fatalf("after a leak a write got status %d reason %q", resp.StatusCode, resp.Header.Get(gatewaycontract.RefusalHeader))
	}
	if f.forge.count() != 0 {
		t.Fatal("a leaking request reached the forge")
	}
}

func TestPerRunRateLimitRefusesWith429(t *testing.T) {
	f := newFixture(t, Limits{Forge: FamilyLimit{RequestsPerSecond: 0.001, Burst: 2}})
	for i := range 3 {
		resp := do(t, http.MethodGet, f.forgeGW.URL+"/api/v1/repos/acme/widgets/pulls", nil, map[string]string{"Authorization": "token " + runCapability})
		want := http.StatusOK
		if i == 2 {
			want = http.StatusTooManyRequests
		}
		if resp.StatusCode != want {
			t.Fatalf("request %d: status %d, want %d", i, resp.StatusCode, want)
		}
	}
}

func TestClaimGetsTheBootstrapAndPinOnlyForTheClaimingPod(t *testing.T) {
	claim := `{"team":"builders","role":"writer"}`
	f := newFixture(t, Limits{})
	resp := do(t, http.MethodPost, f.control.URL+claimPath, strings.NewReader(claim), map[string]string{workerIDHeader: podName,
		gatewaycontract.PodIPHeader: "10.9.9.9", "Authorization": "Bearer pod-supplied"})
	if resp.StatusCode != http.StatusOK || f.ploegd.count() != 1 {
		t.Fatalf("status %d, ploegd saw %d claims", resp.StatusCode, f.ploegd.count())
	}
	got := f.ploegd.last()
	want := map[string]string{"Authorization": "Bearer " + bootstrapToken, gatewaycontract.AuthorizationHeader: "Bearer " + gatewaySAToken,
		gatewaycontract.PodNameHeader: podName, gatewaycontract.PodUIDHeader: podUID, gatewaycontract.PodIPHeader: "127.0.0.1"}
	for k, v := range want {
		if got.Header.Get(k) != v {
			t.Errorf("ploegd got %s %q, want %q", k, got.Header.Get(k), v)
		}
	}
	refused := []struct {
		name   string
		worker string
		body   string
		pods   []kubeauth.Pod
	}{
		{"another worker identity", "someone-else", claim, []kubeauth.Pod{runPod()}},
		{"another Team than the pod's", podName, `{"team":"admins","role":"writer"}`, []kubeauth.Pod{runPod()}},
		{"another Role than the pod's", podName, `{"team":"builders","role":"reviewer"}`, []kubeauth.Pod{runPod()}},
		{"no pod at the address", podName, claim, nil},
		{"a pod no Job owns", podName, claim, []kubeauth.Pod{func() kubeauth.Pod { p := runPod(); p.OwnerKind = "ReplicaSet"; return p }()}},
		{"a pod being deleted", podName, claim, []kubeauth.Pod{func() kubeauth.Pod { p := runPod(); p.Deleting = true; return p }()}},
	}
	for _, tc := range refused {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t, Limits{})
			f.pods.set(tc.pods...)
			resp := do(t, http.MethodPost, f.control.URL+claimPath, strings.NewReader(tc.body), map[string]string{workerIDHeader: tc.worker})
			if resp.StatusCode != http.StatusForbidden || f.ploegd.count() != 0 {
				t.Fatalf("status %d, ploegd saw %d claims", resp.StatusCode, f.ploegd.count())
			}
		})
	}
}

func TestRunRoutesPassThroughWithNothingAttached(t *testing.T) {
	f := newFixture(t, Limits{})
	resp := do(t, http.MethodPost, f.control.URL+"/api/v1/runs/run-1/renew", strings.NewReader(`{}`),
		map[string]string{"Authorization": "Bearer " + runCapability, gatewaycontract.AuthorizationHeader: "Bearer forged"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	got := f.ploegd.last()
	if got.Header.Get("Authorization") != "Bearer "+runCapability || got.Header.Get(gatewaycontract.AuthorizationHeader) != "" {
		t.Fatalf("pass-through attached or forwarded a gateway credential: %v", got.Header)
	}
	resp = do(t, http.MethodGet, f.control.URL+"/api/v1/operator/work-items", nil, nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("an operator route passed: %d", resp.StatusCode)
	}
}

func TestAuditReachesPloegdWithEveryUseAndRefusal(t *testing.T) {
	f := newFixture(t, Limits{})
	do(t, http.MethodGet, f.forgeGW.URL+"/api/v1/repos/acme/widgets/pulls", nil, map[string]string{"Authorization": "token " + runCapability})
	do(t, http.MethodGet, f.forgeGW.URL+"/api/v1/repos/acme/other/pulls", nil, map[string]string{"Authorization": "token " + runCapability})
	do(t, http.MethodPost, f.modelGW.URL+"/v1/chat/completions", strings.NewReader(`{}`), map[string]string{"Authorization": "Bearer " + runCapability})
	if err := f.gateway.FlushAudit(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(f.audits) != 1 {
		t.Fatalf("ploegd got %d audit batches", len(f.audits))
	}
	batch := f.audits[0]
	totals := map[string]gatewaycontract.Use{}
	for _, u := range batch.Uses {
		totals[u.Family] = u
	}
	if totals[FamilyForge].Requests != 1 || totals[FamilyForge].Refused != 1 || totals[FamilyModel].Requests != 1 {
		t.Fatalf("uses %+v", batch.Uses)
	}
	if len(batch.Refusals) != 1 || batch.Refusals[0].Reason != "outside_repository" || batch.Refusals[0].RunToken != "run-1" {
		t.Fatalf("refusals %+v", batch.Refusals)
	}
	for _, r := range batch.Refusals {
		if strings.Contains(r.Route, runCapability) {
			t.Fatal("the audit carries the capability")
		}
	}
}
