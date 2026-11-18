package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/gatewaycontract"
	"github.com/ploeg-hq/ploeg/pkg/kubeauth"
	"github.com/ploeg-hq/ploeg/pkg/store"
)

type fakeReviewer map[string]string

func (f fakeReviewer) Review(_ context.Context, token, audience string) (kubeauth.ReviewedToken, error) {
	user, ok := f[token]
	if !ok || audience != gatewaycontract.Audience {
		return kubeauth.ReviewedToken{}, kubeauth.ErrTokenRejected
	}
	return kubeauth.ReviewedToken{Username: user}, nil
}

const (
	runsGatewayToken  = "runs-gateway-token"
	otherGatewayToken = "other-gateway-token"
)

func gatewayFixture(t *testing.T) (*Server, string, *managedBrokerFixture) {
	t.Helper()
	b := &managedBrokerFixture{}
	s, bootstrap := secureWorkerFixture(t, b)
	g, err := NewCredentialGateways(`[{"namespace":"runs","serviceAccount":"gw","teams":["bronze"]},{"namespace":"other","serviceAccount":"gw","teams":["silver"]}]`,
		fakeReviewer{runsGatewayToken: "system:serviceaccount:runs:gw", otherGatewayToken: "system:serviceaccount:other:gw"}, []byte(strings.Repeat("s", 32)))
	if err != nil {
		t.Fatal(err)
	}
	s.CredentialGateways = g
	return s, bootstrap, b
}

type gatewayClaim struct {
	RunToken          string `json:"runToken"`
	ControlToken      string `json:"controlToken"`
	ForgeToken        string `json:"forgeToken"`
	CredentialGateway bool   `json:"credentialGateway"`
}

func claimThroughGateway(t *testing.T, s *Server, bootstrap string, headers map[string]string) (gatewayClaim, string) {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/api/v1/claim", strings.NewReader(`{"team":"bronze","role":"reviewer"}`))
	r.Header.Set("Authorization", "Bearer "+bootstrap)
	r.Header.Set("X-Ploeg-Worker-ID", "pod")
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	var claim gatewayClaim
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &claim) != nil || claim.ControlToken == "" {
		t.Fatalf("claim=%d %s", w.Code, w.Body.String())
	}
	return claim, w.Body.String()
}

func gatewayHeaders(token string) map[string]string {
	return map[string]string{gatewaycontract.AuthorizationHeader: "Bearer " + token, gatewaycontract.PodNameHeader: "pod",
		gatewaycontract.PodUIDHeader: "uid-1", gatewaycontract.PodIPHeader: "10.1.2.3"}
}

func askGrant(s *Server, gatewayToken, capability, sourceIP string) *httptest.ResponseRecorder {
	body, _ := json.Marshal(gatewaycontract.GrantRequest{Capability: capability, SourceIP: sourceIP})
	r := httptest.NewRequest(http.MethodPost, gatewaycontract.GrantsPath, bytes.NewReader(body))
	if gatewayToken != "" {
		r.Header.Set(gatewaycontract.AuthorizationHeader, "Bearer "+gatewayToken)
	}
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	return w
}

func TestAGatewayClaimCarriesNoCredentialAndItsGrantGoesOnlyToItsGateway(t *testing.T) {
	s, bootstrap, broker := gatewayFixture(t)
	claim, raw := claimThroughGateway(t, s, bootstrap, gatewayHeaders(runsGatewayToken))
	if !claim.CredentialGateway || claim.ForgeToken != "" || strings.Contains(raw, "fixture-inference") {
		t.Fatalf("a gateway claim carried a credential or no gateway mode: %s", raw)
	}
	if broker.calls != 1 {
		t.Fatalf("the model key was minted %d times at claim", broker.calls)
	}

	w := askGrant(s, runsGatewayToken, claim.ControlToken, "10.1.2.3")
	var grant gatewaycontract.Grant
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &grant) != nil {
		t.Fatalf("grant=%d %s", w.Code, w.Body.String())
	}
	if grant.ModelKey != "fixture-inference" || grant.Run != claim.RunToken || grant.Pin.PodUID != "uid-1" || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("grant %+v", grant)
	}

	for name, tc := range map[string]struct {
		token, capability, ip string
		want                  int
	}{
		"no gateway token":          {"", claim.ControlToken, "10.1.2.3", http.StatusUnauthorized},
		"an unknown gateway token":  {"forged", claim.ControlToken, "10.1.2.3", http.StatusUnauthorized},
		"the gateway of another ns": {otherGatewayToken, claim.ControlToken, "10.1.2.3", http.StatusForbidden},
		"another source pod":        {runsGatewayToken, claim.ControlToken, "10.9.9.9", http.StatusForbidden},
		"a forged capability":       {runsGatewayToken, strings.Repeat("x", 40), "10.1.2.3", http.StatusForbidden},
		"the bootstrap capability":  {runsGatewayToken, bootstrap, "10.1.2.3", http.StatusForbidden},
	} {
		t.Run(name, func(t *testing.T) {
			if got := askGrant(s, tc.token, tc.capability, tc.ip); got.Code != tc.want || strings.Contains(got.Body.String(), "fixture-inference") {
				t.Fatalf("grant=%d %s, want %d", got.Code, got.Body.String(), tc.want)
			}
		})
	}

	if got := workerRequest(s, "POST", "/api/v1/runs/"+claim.RunToken+"/llm/credential", claim.ControlToken, "pod", ""); got.Code != http.StatusForbidden ||
		got.Header().Get(gatewaycontract.RefusalHeader) != gatewaycontract.RefusedGatewayMode {
		t.Fatalf("a gateway Run's pod minted its own key: %d %s", got.Code, got.Body.String())
	}
	if got := workerRequest(s, "POST", "/api/v1/runs/"+claim.RunToken+"/forge-credential", claim.ControlToken, "pod", ""); got.Code != http.StatusForbidden {
		t.Fatalf("a gateway Run's pod refreshed its own forge credential: %d", got.Code)
	}

	if _, err := testPool.Exec(context.Background(), `UPDATE agent_runs SET state = 'finished', finished_at = now() WHERE run_token = $1`, claim.RunToken); err != nil {
		t.Fatal(err)
	}
	if got := askGrant(s, runsGatewayToken, claim.ControlToken, "10.1.2.3"); got.Code != http.StatusGone {
		t.Fatalf("a settled Run's grant answered %d", got.Code)
	}
	if n, err := testStore.DropSettledGatewayGrants(context.Background()); err != nil || n != 1 {
		t.Fatalf("settled grants dropped=%d %v", n, err)
	}
}

func TestAGatewayTeamsClaimWithoutItsGatewayGetsNoCredentialAndNoGrant(t *testing.T) {
	s, bootstrap, _ := gatewayFixture(t)
	for name, headers := range map[string]map[string]string{
		"no gateway":                {gatewaycontract.PodIPHeader: "10.1.2.3", gatewaycontract.PodNameHeader: "pod", gatewaycontract.PodUIDHeader: "uid-1"},
		"the gateway of another ns": gatewayHeaders(otherGatewayToken),
	} {
		t.Run(name, func(t *testing.T) {
			reset(t)
			shiftFixture(t, "auth-fixture", 5, []store.Role{{Name: "reviewer", Cap: 1}})
			claim, raw := claimThroughGateway(t, s, bootstrap, headers)
			if claim.CredentialGateway || claim.ForgeToken != "" {
				t.Fatalf("claim %s", raw)
			}
			if got := askGrant(s, runsGatewayToken, claim.ControlToken, "10.1.2.3"); got.Code != http.StatusForbidden {
				t.Fatalf("a Run no gateway claimed has a grant: %d", got.Code)
			}
		})
	}
}

func TestGatewayAuditRecordsRefusalsUnderTheRun(t *testing.T) {
	s, bootstrap, _ := gatewayFixture(t)
	claim, _ := claimThroughGateway(t, s, bootstrap, gatewayHeaders(runsGatewayToken))
	batch, _ := json.Marshal(gatewaycontract.AuditBatch{
		Uses:     []gatewaycontract.Use{{RunToken: claim.RunToken, Family: "forge", Credential: "forge_token", Requests: 3, Refused: 1, Bytes: 10}},
		Refusals: []gatewaycontract.Refusal{{RunToken: claim.RunToken, Family: "forge", Method: "POST", Route: "/x", Reason: "outside_repository", Status: 403}},
	})
	for _, token := range []string{otherGatewayToken, runsGatewayToken} {
		r := httptest.NewRequest(http.MethodPost, gatewaycontract.AuditPath, bytes.NewReader(batch))
		r.Header.Set(gatewaycontract.AuthorizationHeader, "Bearer "+token)
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		if w.Code != http.StatusNoContent {
			t.Fatalf("audit=%d %s", w.Code, w.Body.String())
		}
	}
	var refusals, requests int
	if err := testPool.QueryRow(context.Background(), `SELECT count(*) FROM audit_log WHERE action = 'credential_gateway.refused'`).Scan(&refusals); err != nil {
		t.Fatal(err)
	}
	if err := testPool.QueryRow(context.Background(), `SELECT COALESCE(sum(requests), 0) FROM credential_gateway_use WHERE run_token = $1`, claim.RunToken).Scan(&requests); err != nil {
		t.Fatal(err)
	}
	if refusals != 1 || requests != 3 {
		t.Fatalf("refusal rows=%d requests=%d: the gateway of another namespace must not write a Run's audit", refusals, requests)
	}
}

func TestAWritersMintedForgeCredentialGoesIntoTheGrantAndNotTheClaim(t *testing.T) {
	bootstrap := strings.Repeat("w", 32)
	registry, _ := json.Marshal([]WorkerBootstrap{{Token: bootstrap, Team: "bronze", Role: "builder"}})
	a, err := NewWorkerSecurity(string(registry), strings.Repeat("s", 32), false)
	if err != nil {
		t.Fatal(err)
	}
	c, err := NewLLMControl(testStore, &managedBrokerFixture{}, `[{"team":"bronze","role":"builder","budgetUsd":1,"models":["trusted-model"],"ttl":"1h"}]`)
	if err != nil {
		t.Fatal(err)
	}
	s, _, _ := gatewayFixture(t)
	reset(t)
	targetedWriterShift(t, "991")
	s.WorkerSecurity, s.LLMControl, s.ForgeCreds = a, c, mintingBroker{}
	r := httptest.NewRequest(http.MethodPost, "/api/v1/claim", strings.NewReader(`{"team":"bronze","role":"builder"}`))
	r.Header.Set("Authorization", "Bearer "+bootstrap)
	r.Header.Set("X-Ploeg-Worker-ID", "pod")
	for k, v := range gatewayHeaders(runsGatewayToken) {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	var claim gatewayClaim
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &claim) != nil {
		t.Fatalf("claim=%d %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "minted-for-this-run") || strings.Contains(w.Body.String(), "forgeTokenPerRun") || !claim.CredentialGateway {
		t.Fatalf("the claim carried the minted forge credential: %s", w.Body.String())
	}
	got := askGrant(s, runsGatewayToken, claim.ControlToken, "10.1.2.3")
	var grant gatewaycontract.Grant
	if got.Code != http.StatusOK || json.Unmarshal(got.Body.Bytes(), &grant) != nil {
		t.Fatalf("grant=%d %s", got.Code, got.Body.String())
	}
	if grant.ForgeToken != "minted-for-this-run" || !grant.Writes || grant.Owner != "webgrip" || grant.Repo != "fixture" || grant.Branch != "agent/vik-991" {
		t.Fatalf("grant %+v", grant)
	}
}
