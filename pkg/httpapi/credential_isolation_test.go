package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/store"
)

func TestClaim_EveryRoleNeedsItsCredentialsIsolatedFromItsHarness(t *testing.T) {
	cases := map[string]struct {
		role, body, missing string
		writes              bool
	}{
		"a writer with the forge proxy off": {role: "builder", writes: true,
			body:    `{"team":"bronze","role":"builder","harness":"acp","harnessProfile":"openhands","llmKeyIsolation":"proxy"}`,
			missing: "forge token isolation is off"},
		"a reader with the model key proxy off": {role: "reviewer",
			body:    `{"team":"bronze","role":"reviewer","harness":"openhands","forgeTokenIsolation":"proxy"}`,
			missing: "model key isolation is off"},
		"an unqualified harness behind both proxies": {role: "reviewer",
			body:    `{"team":"bronze","role":"reviewer","harness":"acp","harnessProfile":"opencode","forgeTokenIsolation":"proxy","llmKeyIsolation":"proxy"}`,
			missing: "harness acp/opencode is not qualified"},
		"a Claude environment": {role: "builder", writes: true,
			body:    `{"team":"bronze","role":"builder","harness":"claude-environment","forgeTokenIsolation":"proxy","llmKeyIsolation":"proxy"}`,
			missing: "harness claude-environment is not qualified"},
		"a worker that names no harness": {role: "reviewer",
			body:    `{"team":"bronze","role":"reviewer","forgeTokenIsolation":"proxy","llmKeyIsolation":"proxy"}`,
			missing: "names no harness"},
		"an isolation value that is not a known mode": {role: "reviewer",
			body:    `{"team":"bronze","role":"reviewer","harness":"exec","forgeTokenIsolation":"proxy","llmKeyIsolation":"off"}`,
			missing: `model key isolation is "off", not proxy`},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			reset(t)
			shiftFixture(t, "4100", 10, []store.Role{{Name: tc.role, Writes: tc.writes, Cap: 1}})
			h := apiServer(t, nil)

			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/claim", strings.NewReader(tc.body)))

			var refusal struct {
				Error struct{ Code, Message string } `json:"error"`
			}
			if rec.Code != http.StatusForbidden || json.Unmarshal(rec.Body.Bytes(), &refusal) != nil ||
				refusal.Error.Code != credentialIsolationRequired || !strings.Contains(refusal.Error.Message, tc.missing) ||
				!strings.Contains(refusal.Error.Message, "bronze/"+tc.role) {
				t.Fatalf("claim = %d %s, want 403 %s naming bronze/%s and %q", rec.Code, rec.Body, credentialIsolationRequired, tc.role, tc.missing)
			}
			if n, _ := testStore.PendingRuns(context.Background(), "bronze", tc.role); n != 1 {
				t.Errorf("a refused claim left %d pending %s Runs, want the Run still pending", n, tc.role)
			}
		})
	}
}

func TestClaim_AQualifiedHarnessBehindBothProxiesClaims(t *testing.T) {
	for _, harness := range []string{`"harness":"openhands"`, `"harness":"exec"`, `"harness":"acp","harnessProfile":"openhands"`} {
		t.Run(harness, func(t *testing.T) {
			reset(t)
			shiftFixture(t, "4101", 10, []store.Role{{Name: "builder", Writes: true, Cap: 1}})

			code, resp := postClaim(t, apiServer(t, nil), `{"team":"bronze","role":"builder",`+harness+`,"forgeTokenIsolation":"proxy","llmKeyIsolation":"proxy"}`)
			if code != http.StatusOK || resp.Role != "builder" {
				t.Fatalf("claim = %d %+v, want the builder Run", code, resp)
			}
		})
	}
}
