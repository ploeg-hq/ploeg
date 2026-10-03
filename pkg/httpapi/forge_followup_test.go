package httpapi

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/plan"
	"github.com/ploeg-hq/ploeg/pkg/provider"
	"github.com/ploeg-hq/ploeg/pkg/provider/forgejo"
	"github.com/ploeg-hq/ploeg/pkg/shiftengine"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

const followUpSecret = "shh"

func followUpServer(t *testing.T, policies map[string]work.ForgeFollowUps) http.Handler {
	t.Helper()
	reset(t)
	if _, err := testPool.Exec(context.Background(), "DELETE FROM forge_deliveries"); err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.DiscardHandler)
	return (&Server{
		Store:          testStore,
		Log:            log,
		LeaseTTL:       time.Minute,
		WorkerSecurity: &WorkerSecurity{AllowLegacy: true},
		Forges: map[string]provider.ForgeProvider{
			"forgejo": &forgejo.Provider{Secret: followUpSecret, Log: log},
		},
		Engine:    &shiftengine.Engine{Store: testStore, Plans: plan.Plans{}, Uniform: true, Log: log},
		FollowUps: policies,
		ForgeBots: []string{"agent-builder"},
	}).Handler()
}

func forgeEvent(t *testing.T, h http.Handler, delivery string, body map[string]any) int {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	event := "pull_request_review_rejected"
	if _, ok := body["state"]; ok {
		event = "status"
	}
	req := httptest.NewRequest(http.MethodPost, "/webhooks/forge/forgejo", bytes.NewReader(b))
	req.Header.Set("X-Forgejo-Event", event)
	req.Header.Set("X-Forgejo-Delivery", delivery)
	mac := hmac.New(sha256.New, []byte(followUpSecret))
	mac.Write(b)
	req.Header.Set("X-Forgejo-Signature", hex.EncodeToString(mac.Sum(nil)))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code
}

func enabled(maxRepairs int) map[string]work.ForgeFollowUps {
	return map[string]work.ForgeFollowUps{"bronze": {
		RepairFailedChecks: true, MaxRepairs: maxRepairs, ReworkOnChangesRequested: true,
	}}
}

func awaitingReviewItem(t *testing.T, externalID string) int64 {
	t.Helper()
	ctx := context.Background()
	id, _, err := testStore.IngestAssigned(ctx, work.WorkItem{
		Provider: "vikunja", ExternalID: externalID, Team: "bronze", Title: "add retries",
		Description: "retry the tracker call", Target: &work.Target{Owner: "webgrip", Repo: "ploeg", BaseBranch: "development"},
	})
	if err != nil {
		t.Fatal(err)
	}
	shiftID, err := testStore.OpenShift(ctx, id, "bronze", "agent/vik-"+externalID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := testStore.CloseShift(ctx, shiftID, "plan_exhausted"); err != nil {
		t.Fatal(err)
	}
	if _, err := testStore.SettleItem(ctx, id, work.StateAwaitingReview, "pull request opened"); err != nil {
		t.Fatal(err)
	}
	return id
}

func checkFailed(branch string) map[string]any {
	return map[string]any{
		"repository":  map[string]any{"full_name": "webgrip/ploeg"},
		"state":       "failure",
		"context":     "ci/test",
		"description": "go test failed",
		"branches":    []any{map[string]any{"name": branch}},
		"sender":      map[string]any{"login": "forgejo-actions"},
	}
}

func changesRequested(branch, reviewer, body string) map[string]any {
	return map[string]any{
		"repository":   map[string]any{"full_name": "webgrip/ploeg"},
		"pull_request": map[string]any{"number": 7, "head": map[string]any{"ref": branch}},
		"review":       map[string]any{"type": "pull_request_review_rejected", "content": body},
		"sender":       map[string]any{"login": reviewer},
	}
}

type followUpRow struct {
	ID     int64
	Team   string
	State  string
	Branch string
	Source int64
	Repo   string
}

func followUps(t *testing.T, source int64) []followUpRow {
	t.Helper()
	rows, err := testPool.Query(context.Background(), `
		SELECT id, team, state, source_branch, source_work_item_id, target_owner || '/' || target_repo
		FROM work_items WHERE origin = 'follow_up' AND source_work_item_id = $1 ORDER BY id`, source)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []followUpRow
	for rows.Next() {
		var r followUpRow
		if err := rows.Scan(&r.ID, &r.Team, &r.State, &r.Branch, &r.Source, &r.Repo); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}

func workItemState(t *testing.T, id int64) string {
	t.Helper()
	var state string
	if err := testPool.QueryRow(context.Background(), `SELECT state FROM work_items WHERE id = $1`, id).Scan(&state); err != nil {
		t.Fatal(err)
	}
	return state
}

func settleFollowUp(t *testing.T, id int64) {
	t.Helper()
	ctx := context.Background()
	if _, err := testPool.Exec(ctx, `UPDATE shifts SET closed_at = now(), close_reason = 'test' WHERE work_item_id = $1 AND closed_at IS NULL`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := testPool.Exec(ctx, `UPDATE agent_runs SET state = 'finished', finished_at = now() WHERE work_item_id = $1 AND state <> 'finished'`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := testStore.SettleItem(ctx, id, work.StateAwaitingReview, "repair pushed"); err != nil {
		t.Fatal(err)
	}
}

func TestFailedCheckCreatesOneQueuedFollowUp(t *testing.T) {
	h := followUpServer(t, enabled(2))
	source := awaitingReviewItem(t, "700")

	if code := forgeEvent(t, h, "c-1", checkFailed("agent/vik-700")); code != http.StatusAccepted {
		t.Fatalf("check webhook returned %d", code)
	}
	got := followUps(t, source)
	if len(got) != 1 {
		t.Fatalf("follow-ups = %d, want exactly 1", len(got))
	}
	f := got[0]
	if f.Team != "bronze" || f.Branch != "agent/vik-700" || f.Repo != "webgrip/ploeg" {
		t.Errorf("follow-up = %+v, want team bronze on agent/vik-700 in webgrip/ploeg", f)
	}
	if f.State != string(work.StateQueued) {
		t.Errorf("follow-up state = %q, want queued", f.State)
	}
	si, err := testStore.LiveShiftForItem(context.Background(), f.ID)
	if err != nil || si == nil {
		t.Fatalf("the follow-up has no shift: %v", err)
	}
	if si.Branch != "agent/vik-700" {
		t.Errorf("follow-up shift branch = %q, want the source pull request branch", si.Branch)
	}
	item, err := testStore.WorkItem(context.Background(), f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if item.Origin != work.OriginFollowUp || !strings.Contains(item.Description, "go test failed") ||
		!strings.Contains(item.Description, "VIK-700") {
		t.Errorf("follow-up item = %+v, want origin follow_up naming its source and the failure", item)
	}
	if state := workItemState(t, source); state != string(work.StateAwaitingReview) {
		t.Errorf("source state = %q, want it left awaiting review", state)
	}
}

func TestFailedCheckKeepsOneOpenFollowUpPerPullRequest(t *testing.T) {
	h := followUpServer(t, enabled(5))
	source := awaitingReviewItem(t, "701")

	for i, delivery := range []string{"c-1", "c-2", "c-3"} {
		if code := forgeEvent(t, h, delivery, checkFailed("agent/vik-701")); code != http.StatusAccepted {
			t.Fatalf("delivery %d returned %d", i, code)
		}
	}
	if n := len(followUps(t, source)); n != 1 {
		t.Errorf("three failed checks created %d follow-ups, want 1 while it is open", n)
	}
}

func TestFailedCheckStopsAtTheRepairCap(t *testing.T) {
	h := followUpServer(t, enabled(2))
	source := awaitingReviewItem(t, "702")

	for i, delivery := range []string{"c-1", "c-2", "c-3"} {
		if code := forgeEvent(t, h, delivery, checkFailed("agent/vik-702")); code != http.StatusAccepted {
			t.Fatalf("delivery %d returned %d", i, code)
		}
		for _, f := range followUps(t, source) {
			if f.State != string(work.StateAwaitingReview) {
				settleFollowUp(t, f.ID)
			}
		}
	}
	if n := len(followUps(t, source)); n != 2 {
		t.Errorf("follow-ups = %d after three failures, want the cap of 2", n)
	}
	var skipped int
	if err := testPool.QueryRow(context.Background(),
		`SELECT count(*) FROM audit_log WHERE action = 'follow_up.skipped' AND detail->>'reason' = 'capped'`).Scan(&skipped); err != nil {
		t.Fatal(err)
	}
	if skipped != 1 {
		t.Errorf("capped audit rows = %d, want 1", skipped)
	}
}

func TestChangesRequestedOnAwaitingReviewOpensAShiftWithTheReviewAsBriefing(t *testing.T) {
	h := followUpServer(t, enabled(0))
	source := awaitingReviewItem(t, "703")

	if code := forgeEvent(t, h, "r-1", changesRequested("agent/vik-703", "ryan", "the retry loop is unbounded")); code != http.StatusAccepted {
		t.Fatalf("review webhook returned %d", code)
	}
	si, err := testStore.LiveShiftForItem(context.Background(), source)
	if err != nil || si == nil {
		t.Fatalf("no new shift after changes were requested: %v", err)
	}
	code, resp := postClaim(t, h, `{"team":"bronze"}`)
	if code != http.StatusOK {
		t.Fatalf("claim returned %d", code)
	}
	if resp.Branch != "agent/vik-703" {
		t.Errorf("writer branch = %q, want the pull request branch", resp.Branch)
	}
	var found bool
	for _, f := range resp.Briefing {
		if strings.Contains(f.Findings, "the retry loop is unbounded") && strings.Contains(f.Role, "ryan") {
			found = true
		}
	}
	if !found {
		t.Errorf("briefing = %+v, want the human review", resp.Briefing)
	}
}

func TestChangesRequestedByTheBotDoesNothing(t *testing.T) {
	h := followUpServer(t, enabled(0))
	source := awaitingReviewItem(t, "704")

	forgeEvent(t, h, "r-1", changesRequested("agent/vik-704", "agent-builder", "self review"))
	if state := workItemState(t, source); state != string(work.StateAwaitingReview) {
		t.Errorf("a bot review moved the item to %q", state)
	}
	if n, _ := testStore.PendingReviews(context.Background(), source); n != 0 {
		t.Errorf("a bot review was recorded (%d pending)", n)
	}
}

func TestForgeFollowUpsOffByDefaultChangeNothing(t *testing.T) {
	h := followUpServer(t, nil)
	source := awaitingReviewItem(t, "705")

	forgeEvent(t, h, "c-1", checkFailed("agent/vik-705"))
	forgeEvent(t, h, "r-1", changesRequested("agent/vik-705", "ryan", "please fix"))

	if n := len(followUps(t, source)); n != 0 {
		t.Errorf("switch off created %d follow-ups", n)
	}
	if state := workItemState(t, source); state != string(work.StateAwaitingReview) {
		t.Errorf("switch off moved the item to %q", state)
	}
	if n, _ := testStore.PendingReviews(context.Background(), source); n != 0 {
		t.Errorf("switch off recorded %d reviews", n)
	}
	if n := forgeAuditCount(t); n != 2 {
		t.Errorf("forge audit rows = %d, want both events still recorded", n)
	}
}

func TestFailedCheckAfterTheSourceLeftReviewDoesNothing(t *testing.T) {
	h := followUpServer(t, enabled(2))
	source := awaitingReviewItem(t, "707")
	if _, err := testStore.SettleItem(context.Background(), source, work.StateDone, "merged"); err != nil {
		t.Fatal(err)
	}

	forgeEvent(t, h, "c-1", checkFailed("agent/vik-707"))
	if n := len(followUps(t, source)); n != 0 {
		t.Errorf("a failure after merge created %d follow-ups", n)
	}
}

func TestFailedCheckOnAnUnknownBranchDoesNothing(t *testing.T) {
	h := followUpServer(t, enabled(2))
	source := awaitingReviewItem(t, "706")

	forgeEvent(t, h, "c-1", checkFailed("feature/human-work"))
	if n := len(followUps(t, source)); n != 0 {
		t.Errorf("a failure on a human branch created %d follow-ups", n)
	}
}
