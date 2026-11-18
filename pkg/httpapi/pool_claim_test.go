package httpapi

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/work"
)

func TestAPoolBootstrapNamesATenantAndHarnessAndNoTeam(t *testing.T) {
	token := strings.Repeat("p", 40)
	for body, ok := range map[string]bool{
		`[{"token":"` + token + `","pool":{"tenant":"default","harness":"openhands"}}]`:                 true,
		`[{"token":"` + token + `","team":"bronze","pool":{"tenant":"default","harness":"openhands"}}]`: false,
		`[{"token":"` + token + `","pool":{"tenant":"default"}}]`:                                       false,
		`[{"token":"` + token + `"}]`: false,
	} {
		if _, err := NewTransactionTokenWorkerSecurity(body); (err == nil) != ok {
			t.Errorf("%s: %v", body, err)
		}
	}
}

func TestAPoolClaimNamesTheRunsRuntimeTeamAndRoleAndCarriesThePoolMarker(t *testing.T) {
	rig := newRuntimeTeamsRig(t, nil, AttributionAsserted)
	_, out, _ := rig.call("POST", teamsPath, rig.me(), teamBody("docs", 10, "openhands"))
	teamID := out["team"].(map[string]any)["teamId"].(string)
	ctx := context.Background()
	item, _, err := testStore.IngestAssigned(ctx, work.WorkItem{Provider: "vikunja", ExternalID: "pool-1", Team: "docs", Title: "t"})
	if err != nil {
		t.Fatal(err)
	}
	shift, err := testStore.OpenShift(ctx, item, "docs", "agent/pool-1", 5)
	if err != nil {
		t.Fatal(err)
	}
	b := WorkerBootstrap{Pool: &BootstrapPool{Tenant: "default", Harness: "openhands"}}
	w := httptest.NewRecorder()
	if _, _, _, ok := rig.server.poolClaim(w, httptest.NewRequest("POST", "/api/v1/claim", nil), []byte(`{"team":""}`), b); ok || w.Code != 204 {
		t.Fatalf("no pending Run: ok %v code %d", ok, w.Code)
	}
	if _, err := testPool.Exec(ctx, `INSERT INTO agent_runs (work_item_id, shift_id, team, run_token, role, harness_requested, state)
		VALUES ($1, $2, 'docs', 'pool-claim', 'reviewer', 'openhands', 'pending')`, item, shift); err != nil {
		t.Fatal(err)
	}
	w = httptest.NewRecorder()
	r, body, bound, ok := rig.server.poolClaim(w, httptest.NewRequest("POST", "/api/v1/claim", nil), []byte(`{"team":"","harnessVersion":"1.2"}`), b)
	if !ok {
		t.Fatalf("pool claim refused: %d %s", w.Code, w.Body)
	}
	var req map[string]any
	if err := json.Unmarshal(body, &req); err != nil || req["team"] != "docs" || req["role"] != "reviewer" || req["harnessVersion"] != "1.2" {
		t.Fatalf("rewritten claim %s", body)
	}
	if bound.Team != "docs" || bound.Role != "reviewer" {
		t.Fatalf("capability bound to %+v", bound)
	}
	if claimed, err := testStore.ClaimRoleFor(r.Context(), "docs", "reviewer", "openhands", time.Minute, 0, 0); err != nil || claimed.RunToken != "pool-claim" {
		t.Fatalf("the marked claim got %+v %v", claimed, err)
	}
	if _, err := testStore.RuntimeTeam(ctx, teamID); err != nil {
		t.Fatal(err)
	}
}
