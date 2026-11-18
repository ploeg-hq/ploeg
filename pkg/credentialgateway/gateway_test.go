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
	runTxnToken      = "run-1-transaction-token-0123456789"
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
	settled bool
	grants  int
	mu      sync.Mutex
	control *httptest.Server
	forgeGW *httptest.Server
	modelGW *httptest.Server
}

func runPod() kubeauth.Pod {
	return kubeauth.Pod{Name: podName, UID: podUID, IP: "127.0.0.1", Phase: "Running", OwnerKind: "Job",
		Labels: map[string]string{teamLabel: "builders", roleLabel: "writer"}}
}

func newFixture(t *testing.T, limits Limits) *fixture {
	t.Helper()
	f := &fixture{forge: &seen{}, model: &seen{}, ploegd: &seen{}, pods: &fakePods{}, pinIP: "127.0.0.1"}
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
			f.mu.Lock()
			f.grants++
			settled := f.settled
			f.mu.Unlock()
			if settled {
				req.Capability = settledCapabilty
			}
			switch req.Capability {
			case runCapability:
				f.mu.Lock()
				pin := f.pinIP
				f.mu.Unlock()
				_ = json.NewEncoder(w).Encode(gatewaycontract.Grant{
					Run: "run-1", WorkItem: "7", Team: "builders", Role: "writer", Writes: true, Branch: "agent/7",
					ForgeDialect: "forgejo", ForgeURL: forge.URL, Owner: "acme", Repo: "widgets", BaseBranch: "main",
					ForgeToken: realForgeToken, ModelKey: realModelKey, ModelAlias: "run-1-alias",
					Pin: gatewaycontract.Pin{Namespace: "runs", PodName: podName, PodUID: podUID, PodIP: pin}, Deadline: time.Now().Add(time.Hour),
					TransactionToken: runTxnToken,
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

func TestRunRoutesCarryTheTransactionTokenOnlyFromThePinnedPod(t *testing.T) {
	f := newFixture(t, Limits{})
	resp := do(t, http.MethodPost, f.control.URL+"/api/v1/runs/run-1/renew", strings.NewReader(`{}`),
		map[string]string{"Authorization": "Bearer " + runCapability, workerIDHeader: podName, gatewaycontract.AuthorizationHeader: "Bearer forged",
			gatewaycontract.PodUIDHeader: "forged-uid"})
	if resp.StatusCode != http.StatusOK || f.ploegd.count() != 1 {
		t.Fatalf("status %d, ploegd saw %d", resp.StatusCode, f.ploegd.count())
	}
	got := f.ploegd.last()
	want := map[string]string{"Authorization": "Bearer " + runTxnToken, gatewaycontract.AuthorizationHeader: "Bearer " + gatewaySAToken,
		gatewaycontract.PodNameHeader: podName, gatewaycontract.PodUIDHeader: podUID, gatewaycontract.PodIPHeader: "127.0.0.1"}
	for k, v := range want {
		if got.Header.Get(k) != v {
			t.Errorf("ploegd got %s %q, want %q", k, got.Header.Get(k), v)
		}
	}
	for _, values := range got.Header {
		for _, v := range values {
			if strings.Contains(v, runCapability) {
				t.Fatalf("ploegd saw the capability in %q", v)
			}
		}
	}
	refused := []struct {
		name    string
		path    string
		headers map[string]string
		prepare func(*fixture)
		reason  string
	}{
		{"no capability", "/api/v1/runs/run-1/renew", map[string]string{workerIDHeader: podName}, nil, gatewaycontract.RefusedNoCapability},
		{"another worker identity", "/api/v1/runs/run-1/renew", map[string]string{"Authorization": "Bearer " + runCapability, workerIDHeader: "someone-else"}, nil, gatewaycontract.RefusedPin},
		{"another Run's routes", "/api/v1/runs/run-2/renew", map[string]string{"Authorization": "Bearer " + runCapability, workerIDHeader: podName}, nil, gatewaycontract.RefusedCapability},
		{"another source pod", "/api/v1/runs/run-1/renew", map[string]string{"Authorization": "Bearer " + runCapability, workerIDHeader: podName},
			func(f *fixture) { f.pinIP = "10.0.0.9" }, gatewaycontract.RefusedPin},
		{"a pod reusing the claimant's IP", "/api/v1/runs/run-1/renew", map[string]string{"Authorization": "Bearer " + runCapability, workerIDHeader: podName},
			func(f *fixture) { p := runPod(); p.UID = "pod-uid-2"; f.pods.set(p) }, gatewaycontract.RefusedPin},
		{"a forged capability", "/api/v1/runs/run-1/renew", map[string]string{"Authorization": "Bearer " + otherCapability, workerIDHeader: podName}, nil, gatewaycontract.RefusedCapability},
	}
	for _, tc := range refused {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t, Limits{})
			if tc.prepare != nil {
				tc.prepare(f)
			}
			resp := do(t, http.MethodPost, f.control.URL+tc.path, strings.NewReader(`{}`), tc.headers)
			if resp.StatusCode != http.StatusForbidden || resp.Header.Get(gatewaycontract.RefusalHeader) != tc.reason || f.ploegd.count() != 0 {
				t.Fatalf("status %d reason %q, ploegd saw %d", resp.StatusCode, resp.Header.Get(gatewaycontract.RefusalHeader), f.ploegd.count())
			}
		})
	}
	resp = do(t, http.MethodGet, f.control.URL+"/api/v1/operator/work-items", nil, nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("an operator route passed: %d", resp.StatusCode)
	}
}

func TestTheCapabilityStopsWhenItsRunSettles(t *testing.T) {
	f := newFixture(t, Limits{})
	forge := map[string]string{"Authorization": "token " + runCapability}
	if resp := do(t, http.MethodGet, f.forgeGW.URL+"/api/v1/repos/acme/widgets/pulls", nil, forge); resp.StatusCode != http.StatusOK {
		t.Fatalf("before the outcome: %d", resp.StatusCode)
	}
	resp := do(t, http.MethodPost, f.control.URL+"/api/v1/runs/run-1/outcome", strings.NewReader(`{}`),
		map[string]string{"Authorization": "Bearer " + runCapability, workerIDHeader: podName})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("outcome: %d", resp.StatusCode)
	}
	for _, url := range []string{f.forgeGW.URL + "/api/v1/repos/acme/widgets/pulls", f.modelGW.URL + "/v1/models"} {
		resp := do(t, http.MethodGet, url, nil, forge)
		if resp.StatusCode != http.StatusForbidden || resp.Header.Get(gatewaycontract.RefusalHeader) != gatewaycontract.RefusedRunSettled {
			t.Fatalf("%s after the outcome: %d %q", url, resp.StatusCode, resp.Header.Get(gatewaycontract.RefusalHeader))
		}
	}
	if f.forge.count() != 1 || f.model.count() != 0 {
		t.Fatalf("a settled Run reached an upstream: forge %d model %d", f.forge.count(), f.model.count())
	}
}

func TestACachedGrantLapsesSoonAfterItsRunSettlesElsewhere(t *testing.T) {
	f := newFixture(t, Limits{})
	now := time.Now()
	f.gateway.grants.now = func() time.Time { return now }
	forge := map[string]string{"Authorization": "token " + runCapability}
	if resp := do(t, http.MethodGet, f.forgeGW.URL+"/api/v1/repos/acme/widgets/pulls", nil, forge); resp.StatusCode != http.StatusOK {
		t.Fatalf("before settling: %d", resp.StatusCode)
	}
	f.mu.Lock()
	f.settled = true
	f.mu.Unlock()
	now = now.Add(15 * time.Second)
	resp := do(t, http.MethodGet, f.forgeGW.URL+"/api/v1/repos/acme/widgets/pulls", nil, forge)
	if resp.StatusCode != http.StatusForbidden || resp.Header.Get(gatewaycontract.RefusalHeader) != gatewaycontract.RefusedRunSettled {
		t.Fatalf("15s after its Run settled the capability got %d %q", resp.StatusCode, resp.Header.Get(gatewaycontract.RefusalHeader))
	}
}

func TestOnlyOneRunningPodAtTheAddressHoldsThePin(t *testing.T) {
	for name, pods := range map[string][]kubeauth.Pod{
		"a pending pod":                     {func() kubeauth.Pod { p := runPod(); p.Phase = "Pending"; return p }()},
		"a pod being deleted":               {func() kubeauth.Pod { p := runPod(); p.Deleting = true; return p }()},
		"two pods at the address":           {runPod(), func() kubeauth.Pod { p := runPod(); p.UID = "pod-uid-2"; p.Name = "other"; return p }()},
		"the pinned uid under another name": {func() kubeauth.Pod { p := runPod(); p.Name = "other"; return p }()},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t, Limits{})
			f.pods.set(pods...)
			resp := do(t, http.MethodGet, f.forgeGW.URL+"/api/v1/repos/acme/widgets/pulls", nil, map[string]string{"Authorization": "token " + runCapability})
			if resp.StatusCode != http.StatusForbidden || resp.Header.Get(gatewaycontract.RefusalHeader) != gatewaycontract.RefusedPin || f.forge.count() != 0 {
				t.Fatalf("status %d reason %q", resp.StatusCode, resp.Header.Get(gatewaycontract.RefusalHeader))
			}
		})
	}
}

func TestAClaimIsRefusedWhenTheGatewayHoldsABootstrapForTheClaimedTeamButNotThePods(t *testing.T) {
	for name, body := range map[string]string{
		"another Team than the pod's": `{"team":"admins","role":"writer"}`,
		"another Role than the pod's": `{"team":"builders","role":"reviewer"}`,
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t, Limits{})
			f.gateway.cfg.Bootstraps["admins--writer"] = bootstrapToken + "-admins"
			f.gateway.cfg.Bootstraps["builders--reviewer"] = bootstrapToken + "-reviewer"
			resp := do(t, http.MethodPost, f.control.URL+claimPath, strings.NewReader(body), map[string]string{workerIDHeader: podName})
			if resp.StatusCode != http.StatusForbidden || f.ploegd.count() != 0 {
				t.Fatalf("status %d, ploegd saw %d claims", resp.StatusCode, f.ploegd.count())
			}
		})
	}
}

func TestTheModelFamilyServesInferenceRoutesOnly(t *testing.T) {
	f := newFixture(t, Limits{})
	key := map[string]string{"Authorization": "Bearer " + runCapability}
	for _, tc := range []struct{ method, path string }{
		{http.MethodPost, "/key/generate"}, {http.MethodPost, "/v1/key/generate"}, {http.MethodGet, "/key/info"},
		{http.MethodPost, "/user/new"}, {http.MethodGet, "/spend/logs"}, {http.MethodGet, "/global/spend"},
		{http.MethodPost, "/v1/chat/completions/../../key/generate"}, {http.MethodPost, "/v1/%6bey/generate"},
		{http.MethodGet, "/v1/chat/completions"}, {http.MethodDelete, "/v1/models"}, {http.MethodPost, "/team/new"},
	} {
		req, _ := http.NewRequest(tc.method, f.modelGW.URL+tc.path, strings.NewReader(`{}`))
		req.URL.RawPath = tc.path
		req.Header.Set("Authorization", key["Authorization"])
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden || resp.Header.Get(gatewaycontract.RefusalHeader) != gatewaycontract.RefusedRoute {
			t.Fatalf("%s %s: %d %q", tc.method, tc.path, resp.StatusCode, resp.Header.Get(gatewaycontract.RefusalHeader))
		}
	}
	if f.model.count() != 0 {
		t.Fatalf("a management route reached the model gateway %d times", f.model.count())
	}
	for _, tc := range []struct{ method, path string }{
		{http.MethodPost, "/v1/chat/completions"}, {http.MethodPost, "/chat/completions"}, {http.MethodPost, "/v1/messages"},
		{http.MethodPost, "/v1/messages/count_tokens"}, {http.MethodGet, "/v1/models"}, {http.MethodPost, "/v1/responses"}, {http.MethodPost, "/mcp/tools/list"},
	} {
		if resp := do(t, tc.method, f.modelGW.URL+tc.path, strings.NewReader(`{}`), key); resp.StatusCode != http.StatusOK {
			t.Fatalf("%s %s: %d", tc.method, tc.path, resp.StatusCode)
		}
	}
}

func TestTheModelGatewayGetsNoOtherCredentialHeaderThanTheKey(t *testing.T) {
	f := newFixture(t, Limits{})
	resp := do(t, http.MethodPost, f.modelGW.URL+"/v1/chat/completions", strings.NewReader(`{}`), map[string]string{"Private-Token": runCapability})
	if resp.StatusCode != http.StatusOK || f.model.count() != 1 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	got := f.model.last().Header
	if got.Get("Private-Token") != "" || got.Get("Authorization") != "Bearer "+realModelKey {
		t.Fatalf("the model gateway got %v", got)
	}
}

func TestAuditReachesPloegdWithEveryUseAndRefusal(t *testing.T) {
	f := newFixture(t, Limits{})
	do(t, http.MethodGet, f.forgeGW.URL+"/api/v1/repos/acme/widgets/pulls", nil, map[string]string{"Authorization": "token " + runCapability})
	do(t, http.MethodGet, f.forgeGW.URL+"/api/v1/repos/acme/other/pulls", nil, map[string]string{"Authorization": "token " + runCapability})
	do(t, http.MethodPost, f.modelGW.URL+"/v1/chat/completions", strings.NewReader(`{}`), map[string]string{"Authorization": "Bearer " + runCapability})
	do(t, http.MethodPost, f.control.URL+"/api/v1/runs/run-1/checkpoint", strings.NewReader(`{}`), map[string]string{"Authorization": "Bearer " + runCapability, workerIDHeader: podName})
	totals := map[string]gatewaycontract.Use{}
	var batch gatewaycontract.AuditBatch
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if err := f.gateway.FlushAudit(context.Background()); err != nil {
			t.Fatal(err)
		}
		f.mu.Lock()
		for _, b := range f.audits {
			for _, u := range b.Uses {
				sum := totals[u.Family]
				sum.Requests, sum.Refused = sum.Requests+u.Requests, sum.Refused+u.Refused
				totals[u.Family] = sum
			}
			batch.Refusals = append(batch.Refusals, b.Refusals...)
		}
		f.audits = nil
		f.mu.Unlock()
		if totals[FamilyModel].Requests == 1 && totals[FamilyPloegd].Requests == 1 {
			break
		}
	}
	if totals[FamilyForge].Requests != 1 || totals[FamilyForge].Refused != 1 || totals[FamilyModel].Requests != 1 || totals[FamilyPloegd].Requests != 1 {
		t.Fatalf("uses %+v", totals)
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
