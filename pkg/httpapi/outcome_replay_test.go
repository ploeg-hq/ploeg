package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/plan"
	"github.com/ploeg-hq/ploeg/pkg/store"
)

const outcomeWithCheckpoint = `{"outcome":"no_change_needed","summary":"nothing to change","checkpoint":{"phase":"reviewed","branch":"agent/vik-replay"}}`

func checkpointCount(t *testing.T, runToken string) int {
	t.Helper()
	var n int
	if err := testPool.QueryRow(context.Background(), `
		SELECT count(*) FROM checkpoints
		WHERE work_item_id = (SELECT work_item_id FROM agent_runs WHERE run_token = $1)`, runToken).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func assertLostResponseRetryReplays(t *testing.T, runToken string, post func(body string) *httptest.ResponseRecorder) {
	t.Helper()
	if w := post(outcomeWithCheckpoint); w.Code != http.StatusNoContent {
		t.Fatalf("first outcome=%d %s", w.Code, w.Body)
	}
	if w := post(outcomeWithCheckpoint); w.Code != http.StatusNoContent {
		t.Fatalf("lost-response retry with checkpoint=%d %s, want the original success", w.Code, w.Body)
	}
	if got := checkpointCount(t, runToken); got != 1 {
		t.Fatalf("checkpoints=%d after a replay, want 1", got)
	}
	changed := strings.Replace(outcomeWithCheckpoint, "nothing to change", "a different story", 1)
	if w := post(changed); w.Code < 400 {
		t.Fatalf("changed payload for a finished run=%d, want rejection", w.Code)
	}
	if got := checkpointCount(t, runToken); got != 1 {
		t.Fatalf("checkpoints=%d after a rejected report, want 1", got)
	}
}

func TestManagedOutcomeRetryWithCheckpointReplaysOriginalSuccess(t *testing.T) {
	s, bootstrap := secureWorkerFixture(t, &managedBrokerFixture{})
	w := workerRequest(s, "POST", "/api/v1/claim", bootstrap, "pod", `{"team":"bronze","role":"reviewer"}`)
	var claim struct {
		RunToken     string `json:"runToken"`
		ControlToken string `json:"controlToken"`
	}
	if w.Code != http.StatusOK || json.NewDecoder(w.Body).Decode(&claim) != nil || claim.ControlToken == "" {
		t.Fatalf("managed claim=%d %s", w.Code, w.Body)
	}
	assertLostResponseRetryReplays(t, claim.RunToken, func(body string) *httptest.ResponseRecorder {
		return workerRequest(s, "POST", "/api/v1/runs/"+claim.RunToken+"/outcome", claim.ControlToken, "pod", body)
	})
}

func TestLegacyOutcomeRetryWithCheckpointReplaysOriginalSuccess(t *testing.T) {
	ctx := context.Background()
	reset(t)
	shiftFixture(t, "legacy-replay", 5, []store.Role{{Name: "reviewer", Cap: 1}})
	run, err := testStore.ClaimRole(ctx, "bronze", "reviewer", time.Minute, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := testStore.ReserveLLMAccount(ctx, store.LLMAccount{RunToken: run.RunToken, Alias: "fixture", Authorized: 1, Models: []string{"model"}, TTLSeconds: 60}); err != nil {
		t.Fatal(err)
	}
	h := apiServer(t, plan.Plans{})
	assertLostResponseRetryReplays(t, run.RunToken, func(body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/runs/"+run.RunToken+"/outcome", strings.NewReader(body)))
		return rec
	})
}
