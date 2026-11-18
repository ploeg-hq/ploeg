package credentialgateway

import (
	"encoding/json"
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
	routineToken      = "sk-ant-cc-session-token-0123456789"
	otherEnvToken     = "sk-ant-cc-other-environment-token-01"
	settledToken      = "sk-ant-cc-settled-run-token-0123456"
	unfiredToken      = "sk-ant-cc-unfired-session-token-0123"
	runnerUID         = "runner-uid-1"
	runnerName        = "ploeg-claude-runner-abc"
	sessionPodsSelect = "app.kubernetes.io/name=ploeg-claude-runner-session"
)

func runnerPod(session string) kubeauth.Pod {
	return kubeauth.Pod{Name: runnerName, UID: runnerUID, IP: "127.0.0.1", Phase: "Running", OwnerKind: "Job",
		Annotations: map[string]string{sessionAnnotation: session}}
}

type sessionFixture struct {
	forge   *seen
	reports *seen
	pods    *fakePods
	asked   []gatewaycontract.SessionGrantRequest
	mu      sync.Mutex
	url     string
}

func newSessionFixture(t *testing.T) *sessionFixture {
	t.Helper()
	f := &sessionFixture{forge: &seen{}, reports: &seen{}, pods: &fakePods{}}
	f.pods.set(runnerPod("session_01ROUTINE"))
	forge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.forge.add(r)
		_, _ = io.WriteString(w, "[]")
	}))
	t.Cleanup(forge.Close)
	ploegd := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == sessionReportPath {
			f.reports.add(r)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Header.Get(gatewaycontract.AuthorizationHeader) != "Bearer "+gatewaySAToken {
			http.Error(w, "unauthenticated", http.StatusUnauthorized)
			return
		}
		if r.URL.Path != gatewaycontract.SessionGrantsPath {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		var req gatewaycontract.SessionGrantRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		f.mu.Lock()
		f.asked = append(f.asked, req)
		f.mu.Unlock()
		switch req.SessionToken {
		case routineToken:
			_ = json.NewEncoder(w).Encode(gatewaycontract.Grant{
				Run: "run-9", WorkItem: "9", Team: "pewter", Role: "builder", Writes: true, Branch: "agent/vik-9",
				ForgeDialect: "forgejo", ForgeURL: forge.URL, Owner: "acme", Repo: "widgets", BaseBranch: "main", ForgeToken: realForgeToken,
				Pin:           gatewaycontract.Pin{Namespace: "runs", PodName: runnerName, PodUID: runnerUID, PodIP: req.SourceIP},
				ClaudeSession: "01ROUTINE", Title: "Add a health check", Reference: "VIK-9", Deadline: time.Now().Add(time.Hour),
			})
		case settledToken:
			http.Error(w, "settled", http.StatusGone)
		case unfiredToken:
			http.Error(w, "session_unknown", http.StatusNotFound)
		default:
			http.Error(w, "session_refused", http.StatusForbidden)
		}
	}))
	t.Cleanup(ploegd.Close)
	tokenFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenFile, []byte(gatewaySAToken), 0o600); err != nil {
		t.Fatal(err)
	}
	g, err := New(Config{Namespace: "runs", PloegdURL: ploegd.URL, TokenFile: tokenFile, Bootstraps: map[string]string{"pewter--builder": bootstrapToken},
		Pods: f.pods, SessionPodSelector: sessionPodsSelect, ScratchDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	session := httptest.NewServer(g.SessionHandler())
	t.Cleanup(session.Close)
	f.url = session.URL
	return f
}

func bearer(token string) map[string]string {
	return map[string]string{"Authorization": "Bearer " + token}
}

func TestARoutineSessionReachesTheForgeOnlyWithTheMintedCredential(t *testing.T) {
	f := newSessionFixture(t)
	resp, err := http.DefaultClient.Do(func() *http.Request {
		r, _ := http.NewRequest(http.MethodGet, f.url+sessionRunPath, nil)
		r.Header.Set("Authorization", "Bearer "+routineToken)
		return r
	}())
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	var run SessionRun
	if resp.StatusCode != http.StatusOK || json.Unmarshal(body, &run) != nil || run.Repository != "acme/widgets" || run.Branch != "agent/vik-9" ||
		run.BaseBranch != "main" || strings.Contains(string(body), realForgeToken) {
		t.Fatalf("run = %d %s", resp.StatusCode, body)
	}
	if got := do(t, http.MethodGet, f.url+"/api/v1/repos/acme/widgets/pulls", nil, bearer(routineToken)); got.StatusCode != http.StatusOK {
		t.Fatalf("pulls = %d", got.StatusCode)
	}
	last := f.forge.last()
	if last == nil || last.Header.Get("Authorization") != "token "+realForgeToken {
		t.Fatalf("the forge saw %v, want the minted credential", last)
	}
	if f.asked[0].PodName != runnerName || f.asked[0].SourceUID != runnerUID {
		t.Fatalf("ploegd was asked for pod %+v, want the runner pod", f.asked[0])
	}
	if got := do(t, http.MethodPost, f.url+sessionReportPath, strings.NewReader(`{"exitReason":"completed"}`), bearer(routineToken)); got.StatusCode != http.StatusNoContent ||
		f.reports.last().Header.Get("Authorization") != "Bearer "+routineToken {
		t.Fatalf("report = %d, want it passed to ploegd with the session token alone", got.StatusCode)
	}
}

func TestARoutineSessionIsRefusedBeforeTheForgeUnlessItIsItsRunnersOwn(t *testing.T) {
	for name, tc := range map[string]struct {
		token  string
		pod    *kubeauth.Pod
		want   int
		reason string
	}{
		"no session token":           {"", nil, http.StatusForbidden, gatewaycontract.RefusedNoSession},
		"the Run's capability":       {runCapability, nil, http.StatusForbidden, gatewaycontract.RefusedNoSession},
		"another environment":        {otherEnvToken, nil, http.StatusForbidden, gatewaycontract.RefusedSession},
		"a settled Run":              {settledToken, nil, http.StatusForbidden, gatewaycontract.RefusedRunSettled},
		"a session no routine fired": {unfiredToken, nil, http.StatusNotFound, gatewaycontract.RefusedSessionNone},
		"a pod started for another session": {routineToken, func() *kubeauth.Pod { p := runnerPod("session_01OTHER"); return &p }(),
			http.StatusForbidden, gatewaycontract.RefusedPin},
		"a pod no Job owns": {routineToken, func() *kubeauth.Pod { p := runnerPod("session_01ROUTINE"); p.OwnerKind = ""; return &p }(),
			http.StatusForbidden, gatewaycontract.RefusedPin},
		"another runner pod": {routineToken, func() *kubeauth.Pod { p := runnerPod("session_01ROUTINE"); p.UID = "runner-uid-2"; return &p }(),
			http.StatusForbidden, gatewaycontract.RefusedPin},
	} {
		t.Run(name, func(t *testing.T) {
			f := newSessionFixture(t)
			if tc.pod != nil {
				f.pods.set(*tc.pod)
			}
			headers := map[string]string{}
			if tc.token != "" {
				headers = bearer(tc.token)
			}
			got := do(t, http.MethodPost, f.url+"/acme/widgets.git/git-receive-pack", strings.NewReader(pushBody("refs/heads/agent/vik-9", false)), headers)
			if got.StatusCode != tc.want || got.Header.Get(gatewaycontract.RefusalHeader) != tc.reason {
				t.Fatalf("push = %d %q, want %d %q", got.StatusCode, got.Header.Get(gatewaycontract.RefusalHeader), tc.want, tc.reason)
			}
			if f.forge.count() != 0 {
				t.Fatal("a refused session request reached the forge")
			}
		})
	}
}

func TestARoutineSessionsPushIsFencedToItsRunBranch(t *testing.T) {
	f := newSessionFixture(t)
	got := do(t, http.MethodPost, f.url+"/acme/widgets.git/git-receive-pack", strings.NewReader(pushBody("refs/heads/main", false)), bearer(routineToken))
	if got.StatusCode != http.StatusForbidden || f.forge.count() != 0 {
		t.Fatalf("push to main = %d, forge saw %d", got.StatusCode, f.forge.count())
	}
	if got := do(t, http.MethodGet, f.url+"/api/v1/repos/acme/other/pulls", nil, bearer(routineToken)); got.StatusCode != http.StatusForbidden || f.forge.count() != 0 {
		t.Fatalf("another repository = %d", got.StatusCode)
	}
}
