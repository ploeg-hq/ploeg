package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/harness"
	"github.com/ploeg-hq/ploeg/pkg/plan"
	"github.com/ploeg-hq/ploeg/pkg/store"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

// ADR-0096: a shadow's findings are recorded and never brief a later Run, so
// the writer of a fix round reads only the reviewer that acted.
func TestBriefingLeavesOutShadowFindings(t *testing.T) {
	reset(t)
	ctx := context.Background()
	shiftID := shiftFixture(t, "2033", 10,
		[]store.Role{{Name: "reviewer", Cap: 1}, {Name: "shadow-reviewer", Cap: 1, Shadow: true}})
	plans, err := plan.Parse(`{"bronze": {"pool": "10", "rounds": [
		{"roles": [{"name": "reviewer", "cap": "1"}, {"name": "shadow-reviewer", "shadow": true, "cap": "1"}]},
		{"roles": [{"name": "builder", "writes": true, "cap": "3"}]}]}}`)
	if err != nil {
		t.Fatal(err)
	}
	h := apiServer(t, plans)

	post := func(token string, rep harness.OutcomeReport) {
		t.Helper()
		b, _ := json.Marshal(rep)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/runs/"+token+"/outcome", bytes.NewReader(b)))
		if rec.Code != http.StatusNoContent {
			t.Fatalf("outcome returned %d: %s", rec.Code, rec.Body.String())
		}
	}
	for role, findings := range map[string]string{"reviewer": "- reviewer: add a test", "shadow-reviewer": "- shadow: rename it"} {
		code, claim := postClaim(t, h, `{"team":"bronze","role":"`+role+`"}`)
		if code != http.StatusOK {
			t.Fatalf("claim %s returned %d", role, code)
		}
		post(claim.RunToken, harness.OutcomeReport{Outcome: work.OutcomeNoChangeNeeded, Summary: "read it",
			Verdict: harness.VerdictRequestChanges, Findings: findings})
	}

	if _, err := testStore.OpenRound(ctx, shiftID, 1, []store.Role{{Name: "builder", Writes: true, Cap: 3}}); err != nil {
		t.Fatal(err)
	}
	_, writer := postClaim(t, h, `{"team":"bronze","role":"builder"}`)
	if len(writer.Briefing) != 1 || writer.Briefing[0].Role != "reviewer" {
		t.Fatalf("briefing = %+v, want the reviewer's findings only", writer.Briefing)
	}
}

func TestShadowReviewSeries(t *testing.T) {
	body := string(renderMetrics(store.OperationalMetrics{
		ShadowReviews: map[store.ShadowReviewKey]int{
			{Team: "bronze", Role: "shadow-reviewer", Primary: "request_changes", Shadow: "approve"}: 1,
			{Team: "bronze", Role: "shadow-reviewer", Primary: "approve", Shadow: "approve"}:         4,
		},
	}, nil))
	want := `# TYPE ploeg_shadow_reviews_total counter
ploeg_shadow_reviews_total{team="bronze",role="shadow-reviewer",primary="approve",shadow="approve"} 4
ploeg_shadow_reviews_total{team="bronze",role="shadow-reviewer",primary="request_changes",shadow="approve"} 1
`
	if !strings.Contains(body, want) {
		t.Fatalf("shadow review series missing or out of order:\n%s", body)
	}
}
