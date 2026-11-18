package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/claudesession"
	"github.com/ploeg-hq/ploeg/pkg/gatewaycontract"
)

const runnerPodIP = "10.4.0.7"

func sessionGatewayRig(t *testing.T) (*routineRig, *countingBroker) {
	t.Helper()
	rig := newRoutineRig(t)
	broker := &countingBroker{}
	rig.server.ForgeCreds = broker
	return rig, broker
}

func (rig *routineRig) askSessionGrant(gatewayToken string, claims claudesession.Claims, podUID, podIP string) *httptest.ResponseRecorder {
	return rig.askSessionGrantFor(gatewayToken, claims, podUID, podIP, "session_"+claims.SessionID)
}

func (rig *routineRig) askSessionGrantFor(gatewayToken string, claims claudesession.Claims, podUID, podIP, annotation string) *httptest.ResponseRecorder {
	rig.server.ClaudeSessions.Verifier = fakeSessionVerifier{claims: claims}
	body, _ := json.Marshal(gatewaycontract.SessionGrantRequest{SessionToken: "sk-ant-cc-session", SourceIP: podIP, SourceUID: podUID, PodName: "runner-" + podUID,
		SessionAnnotation: annotation})
	r := httptest.NewRequest(http.MethodPost, gatewaycontract.SessionGrantsPath, bytes.NewReader(body))
	if gatewayToken != "" {
		r.Header.Set(gatewaycontract.AuthorizationHeader, "Bearer "+gatewayToken)
	}
	w := httptest.NewRecorder()
	rig.handler.ServeHTTP(w, r)
	return w
}

func TestARoutineSessionsGrantGoesOnlyToItsGatewayPinnedToItsRunnerPod(t *testing.T) {
	rig, broker := sessionGatewayRig(t)
	rig.register()
	id := rig.workFor("bronze", "2201")
	if code, out := rig.fire(id); code != 200 {
		t.Fatalf("fire = %d %v", code, out)
	}
	session := routineSession(1, "user:owner")

	w := rig.askSessionGrant(runsGatewayToken, session, "uid-runner", runnerPodIP)
	var grant gatewaycontract.Grant
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &grant) != nil {
		t.Fatalf("session grant = %d %s", w.Code, w.Body)
	}
	if grant.ForgeToken == "" || !strings.HasPrefix(grant.ForgeToken, "secret-tok-") || grant.Branch != "agent/vik-2201" || grant.Owner != "webgrip" || grant.Repo != "fixture" ||
		!grant.Writes || grant.ClaudeSession != session.SessionID || grant.Pin.PodUID != "uid-runner" || grant.Pin.Namespace != "runs" || grant.ModelKey != "" ||
		w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("grant %+v", grant)
	}
	if again := rig.askSessionGrant(runsGatewayToken, session, "uid-runner", runnerPodIP); again.Code != http.StatusOK || broker.minted != 1 {
		t.Fatalf("a second ask = %d, minted %d: want the pinned grant again and one mint", again.Code, broker.minted)
	}
	if n := countRows(t, `SELECT count(*) FROM audit_log WHERE action = 'credential_gateway.session_pinned' AND actor = 'team:bronze/role:builder'
		AND detail->>'sub' = $1 AND detail->'act'->>'sub' = 'team:bronze/role:builder' AND work_item_id = $2`, rig.issuer.personID(), id); n != 1 {
		t.Fatalf("audit rows naming the person as subject and the Team and Role as actor = %d, want 1", n)
	}

	for name, tc := range map[string]struct {
		token  string
		claims claudesession.Claims
		uid    string
		ip     string
		want   int
	}{
		"no gateway token":           {"", session, "uid-runner", runnerPodIP, http.StatusUnauthorized},
		"the gateway of another ns":  {otherGatewayToken, session, "uid-runner", runnerPodIP, http.StatusForbidden},
		"another runner pod":         {runsGatewayToken, session, "uid-other", runnerPodIP, http.StatusForbidden},
		"another address":            {runsGatewayToken, session, "uid-runner", "10.4.0.8", http.StatusForbidden},
		"another creator":            {runsGatewayToken, routineSession(1, "user:intruder"), "uid-runner", runnerPodIP, http.StatusForbidden},
		"a session no routine fired": {runsGatewayToken, routineSession(9, "user:owner"), "uid-runner", runnerPodIP, http.StatusNotFound},
	} {
		t.Run(name, func(t *testing.T) {
			got := rig.askSessionGrant(tc.token, tc.claims, tc.uid, tc.ip)
			if got.Code != tc.want || strings.Contains(got.Body.String(), "secret-tok-") {
				t.Fatalf("session grant = %d %s, want %d and no credential", got.Code, got.Body, tc.want)
			}
		})
	}
	if strings.Contains(rig.logs.String(), "secret-tok-") {
		t.Fatal("a log line carries the minted forge credential")
	}

	if _, err := testPool.Exec(context.Background(), `UPDATE agent_runs SET state = 'finished', finished_at = now() WHERE work_item_id = $1`, id); err != nil {
		t.Fatal(err)
	}
	if got := rig.askSessionGrant(runsGatewayToken, session, "uid-runner", runnerPodIP); got.Code != http.StatusGone {
		t.Fatalf("a settled Run's session grant = %d %s, want 410", got.Code, got.Body)
	}
}

func TestAPodStartedForAnotherSessionCannotPinARoutineRunFirst(t *testing.T) {
	rig, broker := sessionGatewayRig(t)
	rig.register()
	id := rig.workFor("bronze", "2202")
	if code, out := rig.fire(id); code != 200 {
		t.Fatalf("fire = %d %v", code, out)
	}
	session := routineSession(1, "user:owner")
	if got := rig.askSessionGrantFor(runsGatewayToken, session, "uid-squatter", "10.4.0.9", "session_01ANOTHER"); got.Code != http.StatusForbidden {
		t.Fatalf("a pod started for another session = %d %s, want 403 before any pin", got.Code, got.Body)
	}
	if n := countRows(t, `SELECT count(*) FROM credential_gateway_grants g JOIN agent_runs a ON a.run_token = g.run_token WHERE a.work_item_id = $1`, id); n != 0 || broker.minted != 0 {
		t.Fatalf("the squatting pod left %d pins and %d mints", n, broker.minted)
	}
	if got := rig.askSessionGrant(runsGatewayToken, session, "uid-runner", runnerPodIP); got.Code != http.StatusOK {
		t.Fatalf("the session's own runner pod = %d %s, want its grant", got.Code, got.Body)
	}
}
