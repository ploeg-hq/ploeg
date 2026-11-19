package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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
	return claimWithBody(t, s, bootstrap, headers, `{"team":"bronze","role":"reviewer","harness":"openhands"}`)
}

func claimWithBody(t *testing.T, s *Server, bootstrap string, headers map[string]string, body string) (gatewayClaim, string) {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/api/v1/claim", strings.NewReader(body))
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
	return askGrantFrom(s, gatewayToken, capability, sourceIP, "uid-1")
}

func askGrantFrom(s *Server, gatewayToken, capability, sourceIP, sourceUID string) *httptest.ResponseRecorder {
	body, _ := json.Marshal(gatewaycontract.GrantRequest{Capability: capability, SourceIP: sourceIP, SourceUID: sourceUID})
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

	for _, route := range []string{"llm/credential", "forge-credential", "identity"} {
		if got := gatewayRunRequest(s, claim.RunToken, route, claim.ControlToken, gatewayHeaders(runsGatewayToken)); got.Code != http.StatusForbidden ||
			got.Header().Get(gatewaycontract.RefusalHeader) != gatewaycontract.RefusedGatewayMode {
			t.Fatalf("a gateway Run's %s answered its pod: %d %s", route, got.Code, got.Body.String())
		}
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

func TestAGatewayTeamsClaimWithoutItsGatewayIsRefusedBeforeAnythingIsClaimed(t *testing.T) {
	s, bootstrap, broker := gatewayFixture(t)
	for name, tc := range map[string]struct {
		headers map[string]string
		worker  string
	}{
		"no gateway":                {map[string]string{gatewaycontract.PodIPHeader: "10.1.2.3", gatewaycontract.PodNameHeader: "pod", gatewaycontract.PodUIDHeader: "uid-1"}, "pod"},
		"a forged gateway token":    {gatewayHeaders("forged"), "pod"},
		"the gateway of another ns": {gatewayHeaders(otherGatewayToken), "pod"},
		"another worker than the pod the gateway identified": {gatewayHeaders(runsGatewayToken), "someone-else"},
	} {
		t.Run(name, func(t *testing.T) {
			reset(t)
			shiftFixture(t, "auth-fixture", 5, []store.Role{{Name: "reviewer", Cap: 1}})
			minted := broker.calls
			r := httptest.NewRequest(http.MethodPost, "/api/v1/claim", strings.NewReader(`{"team":"bronze","role":"reviewer"}`))
			r.Header.Set("Authorization", "Bearer "+bootstrap)
			r.Header.Set("X-Ploeg-Worker-ID", tc.worker)
			for k, v := range tc.headers {
				r.Header.Set(k, v)
			}
			w := httptest.NewRecorder()
			s.Handler().ServeHTTP(w, r)
			if w.Code != http.StatusForbidden || w.Header().Get(gatewaycontract.RefusalHeader) != gatewaycontract.RefusedClaim ||
				strings.Contains(w.Body.String(), "fixture-inference") || strings.Contains(w.Body.String(), "controlToken") {
				t.Fatalf("a gated Team's claim outside its gateway answered %d %s", w.Code, w.Body.String())
			}
			var runs int
			if err := testPool.QueryRow(context.Background(), `SELECT count(*) FROM agent_runs WHERE state = 'running'`).Scan(&runs); err != nil {
				t.Fatal(err)
			}
			if runs != 0 || broker.calls != minted {
				t.Fatalf("the refused claim still claimed %d Runs and minted %d keys", runs, broker.calls-minted)
			}
		})
	}
}

func gatewayRunRequest(s *Server, runToken, route, token string, headers map[string]string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, "/api/v1/runs/"+runToken+"/"+route, strings.NewReader(`{}`))
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("X-Ploeg-Worker-ID", "pod")
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	return w
}

func withHeader(headers map[string]string, key, value string) map[string]string {
	out := map[string]string{}
	for k, v := range headers {
		out[k] = v
	}
	out[key] = value
	return out
}

func TestAGatewayRunIsControlledOnlyThroughItsGatewayFromItsPinnedPod(t *testing.T) {
	s, bootstrap, _ := gatewayFixture(t)
	claim, _ := claimThroughGateway(t, s, bootstrap, gatewayHeaders(runsGatewayToken))
	pinned := gatewayHeaders(runsGatewayToken)
	for name, headers := range map[string]map[string]string{
		"the bare capability":                 nil,
		"pin headers without a gateway token": withHeader(pinned, gatewaycontract.AuthorizationHeader, ""),
		"a forged gateway token":              gatewayHeaders("forged"),
		"the gateway of another ns":           gatewayHeaders(otherGatewayToken),
		"another pod name":                    withHeader(pinned, gatewaycontract.PodNameHeader, "other-pod"),
		"another pod uid":                     withHeader(pinned, gatewaycontract.PodUIDHeader, "uid-2"),
		"another pod address":                 withHeader(pinned, gatewaycontract.PodIPHeader, "10.9.9.9"),
	} {
		t.Run(name, func(t *testing.T) {
			got := gatewayRunRequest(s, claim.RunToken, "renew", claim.ControlToken, headers)
			if got.Code != http.StatusForbidden || got.Header().Get(gatewaycontract.RefusalHeader) != gatewaycontract.RefusedPin {
				t.Fatalf("renew=%d %s", got.Code, got.Body.String())
			}
		})
	}
	if got := gatewayRunRequest(s, claim.RunToken, "renew", claim.ControlToken, pinned); got.Code != http.StatusOK {
		t.Fatalf("the gateway's renew from the pinned pod answered %d %s", got.Code, got.Body.String())
	}
}

func TestAGatewayRunsTransactionTokenNeverReachesItsPod(t *testing.T) {
	s, bootstrap, _ := gatewayFixture(t)
	s.TransactionTokens = txnSigner(t)
	s.TeamSponsors = map[string]string{"bronze": txnSponsor}
	claim, raw := claimThroughGateway(t, s, bootstrap, gatewayHeaders(runsGatewayToken))
	if strings.Contains(raw, "transactionToken") {
		t.Fatalf("a gateway claim handed the pod a transaction token: %s", raw)
	}
	w := askGrant(s, runsGatewayToken, claim.ControlToken, "10.1.2.3")
	var grant gatewaycontract.Grant
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &grant) != nil || grant.TransactionToken == "" {
		t.Fatalf("the gateway's grant carries no transaction token: %d %s", w.Code, w.Body.String())
	}
	renewed := gatewayRunRequest(s, claim.RunToken, "renew", grant.TransactionToken, gatewayHeaders(runsGatewayToken))
	if renewed.Code != http.StatusOK || strings.Contains(renewed.Body.String(), "transactionToken") {
		t.Fatalf("renew through the gateway answered %d %s", renewed.Code, renewed.Body.String())
	}
	if got := gatewayRunRequest(s, claim.RunToken, "renew", grant.TransactionToken, nil); got.Code != http.StatusForbidden {
		t.Fatalf("the transaction token alone controlled a gateway Run: %d", got.Code)
	}
}

func TestAGrantIsServedOnlyForTheCapabilitysOwnRunTeamRoleAndPod(t *testing.T) {
	s, bootstrap, _ := gatewayFixture(t)
	claim, _ := claimThroughGateway(t, s, bootstrap, gatewayHeaders(runsGatewayToken))
	sign := func(c workerCapability) string {
		c.Audience, c.RunToken, c.Expires = "ploeg-worker-run", claim.RunToken, time.Now().Add(time.Hour).Unix()
		return s.WorkerSecurity.sign(c)
	}
	for name, capability := range map[string]string{
		"another worker than the pinned pod": sign(workerCapability{WorkerID: "someone-else", Team: "bronze", Role: "reviewer"}),
		"another Team than the Run's":        sign(workerCapability{WorkerID: "pod", Team: "silver", Role: "reviewer"}),
		"another Role than the Run's":        sign(workerCapability{WorkerID: "pod", Team: "bronze", Role: "builder"}),
	} {
		t.Run(name, func(t *testing.T) {
			if got := askGrant(s, runsGatewayToken, capability, "10.1.2.3"); got.Code != http.StatusForbidden || strings.Contains(got.Body.String(), "fixture-inference") {
				t.Fatalf("grant=%d %s", got.Code, got.Body.String())
			}
		})
	}
	if got := askGrantFrom(s, runsGatewayToken, claim.ControlToken, "10.1.2.3", "uid-2"); got.Code != http.StatusForbidden {
		t.Fatalf("a pod reusing the pinned address got the grant: %d", got.Code)
	}
	if got := askGrant(s, runsGatewayToken, sign(workerCapability{WorkerID: "uid-1", Team: "bronze", Role: "reviewer"}), "10.1.2.3"); got.Code != http.StatusOK {
		t.Fatalf("the pinned pod named by its uid was refused: %d %s", got.Code, got.Body.String())
	}
}

func TestGatewayAuditRecordsRefusalsUnderTheRun(t *testing.T) {
	s, bootstrap, _ := gatewayFixture(t)
	claim, _ := claimThroughGateway(t, s, bootstrap, gatewayHeaders(runsGatewayToken))
	batch, _ := json.Marshal(gatewaycontract.AuditBatch{
		Uses:     []gatewaycontract.Use{{RunToken: claim.RunToken, Family: "forge", Credential: "forge_token", Requests: 3, Refused: 1, Bytes: 10}},
		Refusals: []gatewaycontract.Refusal{{RunToken: claim.RunToken, Family: "forge", Method: "POST", Route: "/x", Reason: "outside_repository", Status: 403}},
		MCPCalls: []gatewaycontract.MCPCall{{RunToken: claim.RunToken, ServerID: "mcp_01", Server: "obs", Method: "tools/call", Tool: "search_logs", Access: "read", Outcome: "allowed", Status: 200}},
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
	var calls int
	if err := testPool.QueryRow(context.Background(), `SELECT count(*) FROM audit_log WHERE action = 'mcp.call' AND detail->>'tool' = 'search_logs' AND detail->>'outcome' = 'allowed'`).Scan(&calls); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("mcp.call rows=%d: ploegd records each MCP call once, and only from the Run's gateway", calls)
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
	r := httptest.NewRequest(http.MethodPost, "/api/v1/claim", strings.NewReader(`{"team":"bronze","role":"builder","harness":"openhands"}`))
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

func TestAGatewayClaimsOnlyForItsOwnServiceAccountsTeams(t *testing.T) {
	s, bootstrap, _ := gatewayFixture(t)
	g, err := NewCredentialGateways(`[{"namespace":"runs","serviceAccount":"gw","teams":["bronze"]},{"namespace":"runs","serviceAccount":"gw-copper","teams":["copper"]}]`,
		fakeReviewer{runsGatewayToken: "system:serviceaccount:runs:gw", "copper-gateway-token": "system:serviceaccount:runs:gw-copper"}, []byte(strings.Repeat("s", 32)))
	if err != nil {
		t.Fatal(err)
	}
	s.CredentialGateways = g
	r := httptest.NewRequest(http.MethodPost, "/api/v1/claim", strings.NewReader(`{"team":"bronze","role":"reviewer"}`))
	r.Header.Set("Authorization", "Bearer "+bootstrap)
	r.Header.Set("X-Ploeg-Worker-ID", "pod")
	for k, v := range gatewayHeaders("copper-gateway-token") {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusForbidden || w.Header().Get(gatewaycontract.RefusalHeader) != gatewaycontract.RefusedClaim {
		t.Fatalf("another ServiceAccount's gateway in the same namespace claimed a bronze Run: %d %s", w.Code, w.Body.String())
	}
	if claim, _ := claimThroughGateway(t, s, bootstrap, gatewayHeaders(runsGatewayToken)); !claim.CredentialGateway {
		t.Fatal("bronze's own gateway could not claim")
	}
}

func TestAGatewayTeamsWorkIsNeverClaimedByAClient(t *testing.T) {
	reset(t)
	s, token := clientCarriedServer(t)
	g, err := NewCredentialGateways(`[{"namespace":"runs","serviceAccount":"gw","teams":["bronze"]}]`, fakeReviewer{}, []byte(strings.Repeat("s", 32)))
	if err != nil {
		t.Fatal(err)
	}
	s.CredentialGateways = g
	id := clientCarriedWork(t, s, "1450", "ryan", clientPerson)
	w := clientClaim(s, token, clientPerson, "", id)
	if w.Code != http.StatusConflict || operatorErrorCode(t, w) != "credential_gateway_team" {
		t.Fatalf("a client's claim of a gateway Team's Run answered %d %s", w.Code, w.Body)
	}
	if got := runStates(t, id); got != "pending" {
		t.Fatalf("after the refused claim runs = %q, want pending", got)
	}
}

func TestTheAuditOfASweptGrantsRunIsStillKeptForItsOwnGatewayOnly(t *testing.T) {
	s, bootstrap, _ := gatewayFixture(t)
	claim, _ := claimThroughGateway(t, s, bootstrap, gatewayHeaders(runsGatewayToken))
	ctx := context.Background()
	if _, err := testPool.Exec(ctx, `UPDATE agent_runs SET state = 'finished', finished_at = now() WHERE run_token = $1`, claim.RunToken); err != nil {
		t.Fatal(err)
	}
	if n, err := testStore.DropSettledGatewayGrants(ctx); err != nil || n != 1 {
		t.Fatalf("settled grants dropped=%d %v", n, err)
	}
	batch, _ := json.Marshal(gatewaycontract.AuditBatch{
		Uses:     []gatewaycontract.Use{{RunToken: claim.RunToken, Family: "forge", Credential: "forge_token", Requests: 2}},
		Refusals: []gatewaycontract.Refusal{{RunToken: claim.RunToken, Family: "forge", Method: "POST", Route: "/x", Reason: "run_settled", Status: 403}},
		MCPCalls: []gatewaycontract.MCPCall{{RunToken: claim.RunToken, ServerID: "mcp_01", Server: "obs", Method: "tools/call", Tool: "after_settle", Outcome: "allowed", Status: 200}},
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
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE action = 'credential_gateway.refused' AND detail->>'reason' = 'run_settled'`).Scan(&refusals); err != nil {
		t.Fatal(err)
	}
	if err := testPool.QueryRow(ctx, `SELECT COALESCE(sum(requests), 0) FROM credential_gateway_use WHERE run_token = $1`, claim.RunToken).Scan(&requests); err != nil {
		t.Fatal(err)
	}
	var calls int
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE action = 'mcp.call' AND detail->>'tool' = 'after_settle'`).Scan(&calls); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("after the sweep: mcp.call rows=%d, want the Run's own gateway's 1", calls)
	}
	if refusals != 1 || requests != 2 {
		t.Fatalf("after the sweep: refusal rows=%d requests=%d, want the Run's own gateway's 1 and 2", refusals, requests)
	}
}
